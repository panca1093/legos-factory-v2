package usecase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

const (
	oauthPendingTTL   = 10 * time.Minute
	oauthRefreshSkew  = 60 * time.Second
	oauthMaxBody      = 1 << 20
	oauthNoDCRMessage = "this server's authorization server does not support dynamic client registration; enter a client ID under Advanced"
)

// oauthState is the sealed secret of an oauth server.
type oauthState struct {
	ClientID      string    `json:"client_id,omitempty"`
	ClientSecret  string    `json:"client_secret,omitempty"`
	Dynamic       bool      `json:"dynamic,omitempty"`
	Scopes        string    `json:"scopes,omitempty"`
	AuthEndpoint  string    `json:"auth_endpoint,omitempty"`
	TokenEndpoint string    `json:"token_endpoint,omitempty"`
	Access        string    `json:"access,omitempty"`
	Refresh       string    `json:"refresh,omitempty"`
	Expiry        time.Time `json:"expiry"`
}

type oauthPending struct {
	serverID, userID, verifier, serverURL, tokenEndpoint string
	expires                                              time.Time
}

type oauthMeta struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

func (m *MCPServers) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *MCPServers) httpClient() *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (m *MCPServers) loadOAuth(ctx context.Context, id string) (oauthState, error) {
	var st oauthState
	sealed, err := m.Store.Secret(ctx, id)
	if err != nil || len(sealed) == 0 || m.Secrets == nil {
		return st, err
	}
	plain, err := m.Secrets.Open(sealed)
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(plain, &st)
}

func (m *MCPServers) sealOAuth(st oauthState) ([]byte, error) {
	if m.Secrets == nil {
		return nil, model.ErrSecretsUnavailable
	}
	b, _ := json.Marshal(st)
	return m.Secrets.Seal(b)
}

func (m *MCPServers) oauthView(ctx context.Context, rec *model.MCPServerRec, v *model.MCPServerView) {
	st, err := m.loadOAuth(ctx, rec.ID)
	if err != nil {
		return
	}
	v.SignedIn = st.Access != "" && rec.Status != model.MCPNeedsSignIn
	if !st.Expiry.IsZero() && st.Access != "" {
		t := st.Expiry
		v.ExpiresAt = &t
	}
	switch {
	case st.ClientID == "":
	case st.Dynamic:
		v.OAuthClient = "dynamic"
	default:
		v.OAuthClient = "manual"
	}
}

func (m *MCPServers) createOAuth(in model.CreateMCPServer) ([]byte, error) {
	if in.Transport != model.MCPHTTP {
		return nil, mcpInvalid("OAuth sign-in needs an http server")
	}
	if len(in.Secret) > 0 {
		return nil, mcpInvalid("an OAuth server takes no token")
	}
	if in.ClientSecret != "" && in.ClientID == "" {
		return nil, mcpInvalid("a client secret needs a client ID")
	}
	return m.sealOAuth(oauthState{ClientID: in.ClientID, ClientSecret: in.ClientSecret, Scopes: strings.Join(strings.Fields(in.Scopes), " ")})
}

// resetOAuthTarget drops tokens and the dynamic client when the server's URL changes, so they never reach another host.
func (m *MCPServers) resetOAuthTarget(ctx context.Context, rec *model.MCPServerRec) ([]byte, error) {
	st, err := m.loadOAuth(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	keep := oauthState{Scopes: st.Scopes}
	if !st.Dynamic {
		keep.ClientID, keep.ClientSecret = st.ClientID, st.ClientSecret
	}
	if rec.Status == model.MCPActive {
		rec.Status = model.MCPNeedsSignIn
	}
	return m.sealOAuth(keep)
}

func (m *MCPServers) markNeedsSignIn(ctx context.Context, id, msg string) {
	rec, err := m.Store.Get(ctx, id)
	if err != nil {
		return
	}
	if rec.Status == model.MCPActive {
		rec.Status = model.MCPNeedsSignIn
	}
	rec.LastError = truncateRunes(msg, 500)
	_ = m.Store.Update(ctx, rec, nil)
}

func (m *MCPServers) lockServer(id string) func() {
	l := m.serverLock(id)
	l.Lock()
	return l.Unlock
}

func (m *MCPServers) serverLock(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks == nil {
		m.locks = map[string]*sync.Mutex{}
	}
	if m.locks[id] == nil {
		m.locks[id] = &sync.Mutex{}
	}
	return m.locks[id]
}

// oauthHeaders returns the bearer header for rec, refreshing the access token first when it is expired or about to be.
func (m *MCPServers) oauthHeaders(ctx context.Context, rec *model.MCPServerRec) (map[string]string, error) {
	defer m.lockServer(rec.ID)()
	cur, err := m.Store.Get(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	if cur.URL != rec.URL {
		return nil, stateConflict("the server changed; try again")
	}
	st, err := m.loadOAuth(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	if st.Access == "" {
		return nil, errors.New("not signed in")
	}
	if !st.Expiry.IsZero() && m.now().Add(oauthRefreshSkew).After(st.Expiry) {
		if st, err = m.refresh(ctx, rec, st); err != nil {
			return nil, err
		}
	}
	return map[string]string{"Authorization": "Bearer " + st.Access}, nil
}

func (m *MCPServers) refresh(ctx context.Context, rec *model.MCPServerRec, st oauthState) (oauthState, error) {
	if st.Refresh == "" {
		err := errors.New("the sign-in expired; sign in again")
		m.markNeedsSignIn(ctx, rec.ID, err.Error())
		return st, err
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {st.Refresh}, "resource": {rec.URL}}
	tok, rejected, err := m.tokenRequest(ctx, st, form)
	if err != nil {
		err = fmt.Errorf("could not refresh the sign-in: %w", err)
		if rejected {
			m.markNeedsSignIn(ctx, rec.ID, err.Error()+"; sign in again")
		}
		return st, err
	}
	st.applyToken(tok, m.now())
	cur, err := m.Store.Get(ctx, rec.ID)
	if err != nil {
		return st, err
	}
	if cur.URL != rec.URL {
		return st, stateConflict("the server changed while its sign-in was refreshed")
	}
	sealed, err := m.sealOAuth(st)
	if err != nil {
		return st, err
	}
	return st, m.Store.Update(ctx, cur, sealed)
}

type oauthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
}

func (st *oauthState) applyToken(t oauthToken, now time.Time) {
	st.Access = t.AccessToken
	if t.RefreshToken != "" {
		st.Refresh = t.RefreshToken
	}
	st.Expiry = time.Time{}
	if t.ExpiresIn > 0 {
		st.Expiry = now.Add(time.Duration(t.ExpiresIn) * time.Second)
	}
}

// tokenRequest posts to the token endpoint; rejected reports an answer from the provider (not a transport failure).
// Errors carry only the provider's error code, never its body.
func (m *MCPServers) tokenRequest(ctx context.Context, st oauthState, form url.Values) (oauthToken, bool, error) {
	form.Set("client_id", st.ClientID)
	if st.ClientSecret != "" {
		form.Set("client_secret", st.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, st.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthToken{}, false, errors.New("the token endpoint address is not valid")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return oauthToken{}, false, errors.New("the token endpoint is unreachable")
	}
	defer resp.Body.Close()
	var t oauthToken
	_ = json.NewDecoder(io.LimitReader(resp.Body, oauthMaxBody)).Decode(&t)
	if resp.StatusCode >= 500 {
		return t, false, fmt.Errorf("the token endpoint answered %d", resp.StatusCode)
	}
	if resp.StatusCode/100 != 2 || t.AccessToken == "" {
		code := safeToken(t.Error)
		if code == "" {
			code = fmt.Sprint(resp.StatusCode)
		}
		return t, true, fmt.Errorf("the token endpoint refused the request (%s)", code)
	}
	if t.TokenType != "" && !strings.EqualFold(t.TokenType, "bearer") {
		return t, true, errors.New("the token endpoint issued a token that is not a bearer token")
	}
	return t, false, nil
}

func safeToken(s string) string {
	if len(s) > 40 {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return ""
		}
	}
	return s
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && isLoopbackHost(u.Hostname()))
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func (m *MCPServers) getJSON(ctx context.Context, u string, out any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, oauthMaxBody)).Decode(out) == nil
}

// wwwAuthParam reads the parameter called name from a WWW-Authenticate value, matching whole names only.
func wwwAuthParam(header, name string) string {
	s := header
	for {
		s = strings.TrimLeft(s, " \t,")
		i := strings.IndexAny(s, "= \t,")
		if i < 0 {
			return ""
		}
		key := s[:i]
		s = strings.TrimLeft(s[i:], " \t")
		if !strings.HasPrefix(s, "=") {
			continue
		}
		s = strings.TrimLeft(s[1:], " \t")
		var val strings.Builder
		if strings.HasPrefix(s, `"`) {
			s = s[1:]
			closed := false
			for len(s) > 0 && !closed {
				switch c := s[0]; {
				case c == '\\' && len(s) > 1:
					val.WriteByte(s[1])
					s = s[2:]
				case c == '"':
					closed, s = true, s[1:]
				default:
					val.WriteByte(c)
					s = s[1:]
				}
			}
			if !closed {
				return ""
			}
		} else {
			j := strings.IndexAny(s, ", \t")
			if j < 0 {
				j = len(s)
			}
			val.WriteString(s[:j])
			s = s[j:]
		}
		if strings.EqualFold(key, name) {
			return val.String()
		}
	}
}

func sameURL(a, b string) bool { return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/") }

// discoverOAuth follows the MCP authorization spec: the server's 401 hint or well-known path leads to the
// protected resource metadata, whose first authorization server leads to its metadata.
func (m *MCPServers) discoverOAuth(ctx context.Context, serverURL string) (oauthMeta, string, error) {
	var none oauthMeta
	su, err := url.Parse(serverURL)
	if err != nil {
		return none, "", mcpInvalid("the server url is not valid")
	}
	probe, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"legos-factory","version":"1"}}}`))
	if err != nil {
		return none, "", mcpInvalid("the server url is not valid")
	}
	probe.Header.Set("Content-Type", "application/json")
	probe.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := m.httpClient().Do(probe)
	if err != nil {
		return none, "", errors.New("could not reach the MCP server")
	}
	_ = resp.Body.Close()
	var hint, scope string
	if resp.StatusCode == http.StatusUnauthorized {
		h := resp.Header.Get("WWW-Authenticate")
		hint, scope = wwwAuthParam(h, "resource_metadata"), wwwAuthParam(h, "scope")
	}
	if hint == "" {
		hint = su.Scheme + "://" + su.Host + "/.well-known/oauth-protected-resource"
	}
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if !isHTTPURL(hint) || !m.getJSON(ctx, hint, &prm) || len(prm.AuthorizationServers) == 0 {
		return none, "", mcpInvalid("the server does not publish OAuth protected resource metadata, so it cannot be signed in to")
	}
	if !sameURL(prm.Resource, serverURL) {
		return none, "", mcpInvalid("the server's metadata describes a different resource (%q), so it cannot be signed in to", truncateRunes(prm.Resource, 100))
	}
	issuer, err := url.Parse(prm.AuthorizationServers[0])
	if err != nil || !isHTTPURL(prm.AuthorizationServers[0]) {
		return none, "", mcpInvalid("the server names an invalid authorization server")
	}
	host, path := issuer.Scheme+"://"+issuer.Host, strings.TrimRight(issuer.Path, "/")
	var wrongIssuer string
	for _, u := range []string{host + "/.well-known/oauth-authorization-server" + path, host + "/.well-known/openid-configuration" + path, host + path + "/.well-known/openid-configuration"} {
		var cand oauthMeta
		if m.getJSON(ctx, u, &cand) && isHTTPURL(cand.AuthorizationEndpoint) && isHTTPURL(cand.TokenEndpoint) {
			if !sameURL(cand.Issuer, prm.AuthorizationServers[0]) {
				wrongIssuer = cand.Issuer
				continue
			}
			return cand, scope, nil
		}
	}
	if wrongIssuer != "" {
		return none, "", mcpInvalid("the authorization server's metadata names a different issuer (%q)", truncateRunes(wrongIssuer, 100))
	}
	return none, "", mcpInvalid("could not read the authorization server's metadata")
}

func (m *MCPServers) register(ctx context.Context, endpoint string) (string, error) {
	if !isHTTPURL(endpoint) {
		return "", mcpInvalid(oauthNoDCRMessage)
	}
	body, _ := json.Marshal(map[string]any{
		"client_name": "Legos Factory", "redirect_uris": []string{m.CallbackURL},
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", mcpInvalid(oauthNoDCRMessage)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return "", errors.New("the client registration endpoint is unreachable")
	}
	defer resp.Body.Close()
	var out struct {
		ClientID string `json:"client_id"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, oauthMaxBody)).Decode(&out)
	if resp.StatusCode/100 != 2 || out.ClientID == "" {
		return "", mcpInvalid("client registration was refused; enter a client ID under Advanced")
	}
	return out.ClientID, nil
}

func randomURLString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// OAuthStart begins a sign-in for userID and returns the URL the browser must open.
func (m *MCPServers) OAuthStart(ctx context.Context, id, userID string) (string, error) {
	rec, err := m.Store.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if rec.Auth != model.MCPAuthOAuth {
		return "", mcpInvalid("this server does not use OAuth sign-in")
	}
	serverURL := rec.URL
	meta, scope, err := m.discoverOAuth(ctx, serverURL)
	if err != nil {
		return "", err
	}
	st, err := m.loadOAuth(ctx, id)
	if err != nil {
		return "", err
	}
	needClient := func(st oauthState) bool {
		return st.ClientID == "" || (st.Dynamic && st.TokenEndpoint != meta.TokenEndpoint)
	}
	var cid string
	if needClient(st) {
		if cid, err = m.register(ctx, meta.RegistrationEndpoint); err != nil {
			return "", err
		}
	}
	defer m.lockServer(id)()
	if rec, err = m.Store.Get(ctx, id); err != nil {
		return "", err
	}
	if rec.URL != serverURL {
		return "", stateConflict("the server changed while the sign-in was starting; start again")
	}
	if st, err = m.loadOAuth(ctx, id); err != nil {
		return "", err
	}
	if st.TokenEndpoint != "" && st.TokenEndpoint != meta.TokenEndpoint {
		if !st.Dynamic && st.ClientID != "" {
			return "", mcpInvalid("the authorization server changed; enter the client again")
		}
		st.Access, st.Refresh, st.Expiry = "", "", time.Time{}
		if rec.Status == model.MCPActive {
			rec.Status = model.MCPNeedsSignIn
		}
	}
	if needClient(st) {
		if cid == "" {
			return "", stateConflict("the sign-in changed while it was starting; start again")
		}
		st.ClientID, st.ClientSecret, st.Dynamic = cid, "", true
	}
	st.AuthEndpoint, st.TokenEndpoint = meta.AuthorizationEndpoint, meta.TokenEndpoint
	sealed, err := m.sealOAuth(st)
	if err != nil {
		return "", err
	}
	if err := m.Store.Update(ctx, rec, sealed); err != nil {
		return "", err
	}
	verifier, state := randomURLString(32), randomURLString(24)
	sum := sha256.Sum256([]byte(verifier))
	u, _ := url.Parse(meta.AuthorizationEndpoint)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", st.ClientID)
	q.Set("redirect_uri", m.CallbackURL)
	q.Set("state", state)
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	q.Set("code_challenge_method", "S256")
	q.Set("resource", rec.URL)
	if st.Scopes != "" {
		scope = st.Scopes
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil {
		m.pending = map[string]oauthPending{}
	}
	for k, p := range m.pending {
		if m.now().After(p.expires) {
			delete(m.pending, k)
		}
	}
	m.pending[state] = oauthPending{serverID: id, userID: userID, verifier: verifier, serverURL: rec.URL, tokenEndpoint: meta.TokenEndpoint, expires: m.now().Add(oauthPendingTTL)}
	return u.String(), nil
}

// OAuthCallback finishes the sign-in that userID started with state; the state is spent whatever the outcome.
func (m *MCPServers) OAuthCallback(ctx context.Context, userID, code, state string) error {
	m.mu.Lock()
	p, ok := m.pending[state]
	delete(m.pending, state)
	m.mu.Unlock()
	if !ok || m.now().After(p.expires) || p.userID != userID {
		return mcpInvalid("the sign-in link is unknown, expired or already used; start again")
	}
	defer m.lockServer(p.serverID)()
	srv, err := m.Store.Get(ctx, p.serverID)
	if err != nil {
		return err
	}
	st, err := m.loadOAuth(ctx, p.serverID)
	if err != nil {
		return err
	}
	if srv.URL != p.serverURL || st.TokenEndpoint != p.tokenEndpoint {
		return mcpInvalid("the server changed since the sign-in started; start again")
	}
	fail := func(err error) error {
		if cur, gerr := m.Store.Get(ctx, p.serverID); gerr == nil {
			cur.LastError = truncateRunes(err.Error(), 500)
			_ = m.Store.Update(ctx, cur, nil)
		}
		return err
	}
	if code == "" {
		return fail(mcpInvalid("the provider did not grant access"))
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {m.CallbackURL},
		"code_verifier": {p.verifier}, "resource": {srv.URL}}
	tok, _, err := m.tokenRequest(ctx, st, form)
	if err != nil {
		return fail(fmt.Errorf("sign-in failed: %w", err))
	}
	st.applyToken(tok, m.now())
	sealed, err := m.sealOAuth(st)
	if err != nil {
		return err
	}
	cur, err := m.Store.Get(ctx, p.serverID)
	if err != nil {
		return err
	}
	if cur.URL != p.serverURL {
		return mcpInvalid("the server changed while signing in; start again")
	}
	if cur.Status == model.MCPNeedsSignIn {
		cur.Status = model.MCPActive
	}
	cur.LastError = ""
	return m.Store.Update(ctx, cur, sealed)
}
