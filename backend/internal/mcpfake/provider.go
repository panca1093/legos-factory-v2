// Package mcpfake is a local OAuth-protected MCP server and authorization server for tests; it checks what a real provider would.
package mcpfake

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type grant struct{ challenge, redirect, resource, client string }

// Provider serves /mcp (401 without a valid bearer), the discovery documents, /register, /authorize and /token.
type Provider struct {
	Server *httptest.Server
	// switches, set before use
	OIDCOnly, NoDCR, NoHint, RefreshFail, RefreshDown, NoRotate bool
	ExpiresIn                                                   int
	// ExpiresInRaw, when set, is sent as expires_in instead of ExpiresIn.
	ExpiresInRaw any
	// TokenStatus, when set, is the status every token request answers with, with an empty body.
	TokenStatus int
	// MetaOverride and PRMOverride replace fields of the authorization server metadata and the protected resource metadata.
	MetaOverride, PRMOverride map[string]any
	// HintOverride replaces the resource_metadata URL of the 401 challenge.
	HintOverride string

	mu        sync.Mutex
	grants    map[string]grant
	access    map[string]bool
	refresh   map[string]bool
	seq       int
	dcr       int
	refreshes int
	forms     []url.Values
	gates     map[string]*gate
}

type gate struct {
	open chan struct{}
	hit  chan struct{}
}

func New(t testing.TB) *Provider {
	p := &Provider{grants: map[string]grant{}, access: map[string]bool{}, refresh: map[string]bool{}, ExpiresIn: 3600}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Server.Close)
	return p
}

// Hold makes the endpoint ("metadata" or "token") block every request until release is called; hit receives one value per blocked request.
func (p *Provider) Hold(endpoint string) (hit <-chan struct{}, release func()) {
	g := &gate{open: make(chan struct{}), hit: make(chan struct{}, 16)}
	p.mu.Lock()
	if p.gates == nil {
		p.gates = map[string]*gate{}
	}
	p.gates[endpoint] = g
	p.mu.Unlock()
	var once sync.Once
	return g.hit, func() { once.Do(func() { close(g.open) }) }
}

func (p *Provider) wait(endpoint string) {
	p.mu.Lock()
	g := p.gates[endpoint]
	p.mu.Unlock()
	if g != nil {
		g.hit <- struct{}{}
		<-g.open
	}
}

func (p *Provider) MCPURL() string { return p.Server.URL + "/mcp" }

func (p *Provider) DCRCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dcr
}

func (p *Provider) Refreshes() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refreshes
}

// Forms returns the form of every token request so far.
func (p *Provider) Forms() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]url.Values(nil), p.forms...)
}

// Browser plays the user's browser: it opens authorizeURL and returns the code and state the provider redirects back with.
func (p *Provider) Browser(authorizeURL string) (code, state string, err error) {
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(authorizeURL)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound {
		return "", "", fmt.Errorf("authorize answered %d", resp.StatusCode)
	}
	return loc.Query().Get("code"), loc.Query().Get("state"), nil
}

func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/mcp":
		p.mcp(w, r)
	case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp":
		prm := map[string]any{"resource": p.MCPURL(), "authorization_servers": []string{p.Server.URL}}
		for k, v := range p.PRMOverride {
			prm[k] = v
		}
		writeJSON(w, prm)
	case "/.well-known/oauth-authorization-server":
		if p.OIDCOnly {
			http.NotFound(w, r)
			return
		}
		p.meta(w)
	case "/.well-known/openid-configuration":
		p.meta(w)
	case "/register":
		p.register(w, r)
	case "/authorize":
		p.authorize(w, r)
	case "/token":
		p.token(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (p *Provider) meta(w http.ResponseWriter) {
	p.wait("metadata")
	m := map[string]any{"issuer": p.Server.URL, "authorization_endpoint": p.Server.URL + "/authorize", "token_endpoint": p.Server.URL + "/token",
		"code_challenge_methods_supported": []string{"S256"}}
	if !p.NoDCR {
		m["registration_endpoint"] = p.Server.URL + "/register"
	}
	for k, v := range p.MetaOverride {
		m[k] = v
	}
	writeJSON(w, m)
}

func (p *Provider) mcp(w http.ResponseWriter, r *http.Request) {
	var m struct {
		ID     any    `json:"id"`
		Method string `json:"method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&m)
	p.mu.Lock()
	ok := p.access[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	p.mu.Unlock()
	if !ok {
		if !p.NoHint {
			hint := p.Server.URL + "/.well-known/oauth-protected-resource/mcp"
			if p.HintOverride != "" {
				hint = p.HintOverride
			}
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s"`, hint))
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if m.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	res := map[string]any{"protocolVersion": "2025-03-26"}
	if m.Method == "tools/list" {
		res = map[string]any{"tools": []any{
			map[string]any{"name": "search", "description": "find", "inputSchema": map[string]any{}, "annotations": map[string]any{"readOnlyHint": true}},
		}}
	}
	writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": res})
}

func (p *Provider) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RedirectURIs []string `json:"redirect_uris"`
		GrantTypes   []string `json:"grant_types"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if len(in.RedirectURIs) != 1 || !strings.HasSuffix(in.RedirectURIs[0], "/api/v1/mcp/oauth/callback") || in.AuthMethod != "none" ||
		strings.Join(in.GrantTypes, ",") != "authorization_code,refresh_token" {
		http.Error(w, `{"error":"invalid_client_metadata"}`, http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.dcr++
	id := fmt.Sprintf("dyn-%d", p.dcr)
	p.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{"client_id": id})
}

func (p *Provider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("state") == "" || q.Get("client_id") == "" || q.Get("redirect_uri") == "" || q.Get("resource") != p.MCPURL() {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.seq++
	code := fmt.Sprintf("code-%d", p.seq)
	p.grants[code] = grant{q.Get("code_challenge"), q.Get("redirect_uri"), q.Get("resource"), q.Get("client_id")}
	p.mu.Unlock()
	http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	p.wait("token")
	_ = r.ParseForm()
	f := r.PostForm
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forms = append(p.forms, f)
	if p.TokenStatus != 0 {
		w.WriteHeader(p.TokenStatus)
		return
	}
	expires := any(p.ExpiresIn)
	if p.ExpiresInRaw != nil {
		expires = p.ExpiresInRaw
	}
	deny := func() {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "invalid_grant"})
	}
	switch f.Get("grant_type") {
	case "authorization_code":
		g, ok := p.grants[f.Get("code")]
		delete(p.grants, f.Get("code"))
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		if !ok || f.Get("code_verifier") == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge ||
			f.Get("redirect_uri") != g.redirect || f.Get("resource") != g.resource || f.Get("client_id") != g.client {
			deny()
			return
		}
	case "refresh_token":
		if !p.refresh[f.Get("refresh_token")] || p.RefreshFail || f.Get("resource") != p.MCPURL() {
			deny()
			return
		}
		if p.RefreshDown {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		p.refreshes++
		if p.NoRotate {
			p.seq++
			at := fmt.Sprintf("access-%d", p.seq)
			p.access[at] = true
			writeJSON(w, map[string]any{"access_token": at, "token_type": "Bearer", "expires_in": expires})
			return
		}
		delete(p.refresh, f.Get("refresh_token"))
	default:
		deny()
		return
	}
	p.seq++
	at, rt := fmt.Sprintf("access-%d", p.seq), fmt.Sprintf("refresh-%d", p.seq)
	p.access[at], p.refresh[rt] = true, true
	writeJSON(w, map[string]any{"access_token": at, "refresh_token": rt, "token_type": "Bearer", "expires_in": expires})
}
