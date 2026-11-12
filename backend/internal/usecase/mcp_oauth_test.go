package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/agent"
	"legos-factory/backend/internal/mcpfake"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

const oauthCallback = "http://app.test/api/v1/mcp/oauth/callback"

type oauthRig struct {
	m     *MCPServers
	fp    *mcpfake.Provider
	clock *time.Time
	repo  *store.MCPRepo
	mu    sync.Mutex
}

func newOAuthRig(t *testing.T) *oauthRig {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, _ := store.NewSecretBox(bytes.Repeat([]byte{5}, 32))
	fp := mcpfake.New(t)
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	r := &oauthRig{fp: fp, clock: &now, repo: &store.MCPRepo{DB: db}}
	r.m = &MCPServers{Store: r.repo, Secrets: box, Lister: agent.ListMCPTools, CallbackURL: oauthCallback, HTTP: fp.Server.Client(),
		Now: func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return *r.clock }}
	return r
}

func (r *oauthRig) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.clock = r.clock.Add(d)
}

func (r *oauthRig) create(t *testing.T, in model.CreateMCPServer) model.MCPServerView {
	t.Helper()
	in.Name, in.Transport, in.URL, in.Auth = "docs", model.MCPHTTP, r.fp.MCPURL(), model.MCPAuthOAuth
	v, err := r.m.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (r *oauthRig) signIn(t *testing.T, id, user string) {
	t.Helper()
	ctx := context.Background()
	u, err := r.m.OAuthStart(ctx, id, user)
	if err != nil {
		t.Fatal(err)
	}
	code, state, err := r.fp.Browser(u)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.m.OAuthCallback(ctx, user, code, state); err != nil {
		t.Fatal(err)
	}
}

func (r *oauthRig) offered(t *testing.T, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := r.m.Discover(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.SetRoles(ctx, id, []string{"inspector"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.SetTools(ctx, id, []model.MCPToolToggle{{Name: "search", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
}

func viewJSON(t *testing.T, r *oauthRig, id string) string {
	t.Helper()
	v, err := r.m.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func assertNoSecrets(t *testing.T, where string, secrets []string, texts ...string) {
	t.Helper()
	for _, txt := range texts {
		for _, s := range secrets {
			if strings.Contains(txt, s) {
				t.Fatalf("%s leaks %q: %s", where, s, txt)
			}
		}
	}
}

func TestOAuthSignInStoresSealedTokensAndNeverShowsThem(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	v := r.create(t, model.CreateMCPServer{Scopes: " read  write "})
	if v.Auth != model.MCPAuthOAuth || v.Status != model.MCPNeedsSignIn || v.SignedIn {
		t.Fatalf("new oauth server: %+v", v)
	}
	u, err := r.m.OAuthStart(ctx, v.ID, "adm")
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	q := pu.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("resource") != r.fp.MCPURL() ||
		q.Get("redirect_uri") != oauthCallback || q.Get("scope") != "read write" || q.Get("client_id") != "dyn-1" || len(q.Get("state")) < 20 {
		t.Fatalf("authorize url: %s", u)
	}
	code, state, err := r.fp.Browser(u)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.m.OAuthCallback(ctx, "adm", code, state); err != nil {
		t.Fatal(err)
	}
	got, _ := r.m.Get(ctx, v.ID)
	if !got.SignedIn || got.Status != model.MCPActive || got.OAuthClient != "dynamic" || got.ExpiresAt == nil || !got.ExpiresAt.Equal(r.clock.Add(time.Hour)) {
		t.Fatalf("after sign-in: %+v", got)
	}
	sealed, _ := r.repo.Secret(ctx, v.ID)
	assertNoSecrets(t, "sealed column", []string{"access-", "refresh-", "dyn-1"}, string(sealed))
	assertNoSecrets(t, "view", []string{"access-", "refresh-", code, state, "verifier"}, viewJSON(t, r, v.ID))
	if f := r.fp.Forms(); len(f) != 1 || f[0].Get("code_verifier") == "" {
		t.Fatalf("token request: %v", f)
	}
}

func TestOAuthCallbackRejectsBadState(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	v := r.create(t, model.CreateMCPServer{})
	start := func() (string, string) {
		u, err := r.m.OAuthStart(ctx, v.ID, "adm")
		if err != nil {
			t.Fatal(err)
		}
		c, s, _ := r.fp.Browser(u)
		return c, s
	}
	code, state := start()
	if err := r.m.OAuthCallback(ctx, "other", code, state); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong user: %v", err)
	}
	if err := r.m.OAuthCallback(ctx, "adm", code, state); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a state a wrong user touched must be spent: %v", err)
	}
	code, state = start()
	if err := r.m.OAuthCallback(ctx, "adm", code, state); err != nil {
		t.Fatal(err)
	}
	if err := r.m.OAuthCallback(ctx, "adm", code, state); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reuse: %v", err)
	}
	if err := r.m.OAuthCallback(ctx, "adm", code, "never-issued"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown state: %v", err)
	}
	code, state = start()
	r.advance(10*time.Minute + time.Second)
	if err := r.m.OAuthCallback(ctx, "adm", code, state); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired: %v", err)
	}
	code, state = start()
	if err := r.m.OAuthCallback(ctx, "adm", "", state); !errors.Is(err, ErrInvalid) {
		t.Fatalf("denied (no code): %v", err)
	}
}

func TestOAuthProviderRejectsExchangeWithoutValidPKCE(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	v := r.create(t, model.CreateMCPServer{})
	u, _ := r.m.OAuthStart(ctx, v.ID, "adm")
	code, state, _ := r.fp.Browser(u)
	st, _ := r.m.loadOAuth(ctx, v.ID)
	st.ClientID = "dyn-1"
	for name, form := range map[string]url.Values{
		"no verifier":    {"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {oauthCallback}, "resource": {r.fp.MCPURL()}},
		"wrong verifier": {"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {oauthCallback}, "resource": {r.fp.MCPURL()}, "code_verifier": {"x"}},
	} {
		if _, rejected, err := r.m.tokenRequest(ctx, st, form); err == nil || !rejected {
			t.Fatalf("%s: err=%v rejected=%v", name, err, rejected)
		}
	}
	if err := r.m.OAuthCallback(ctx, "adm", code, state); err == nil {
		t.Fatal("the code was spent by the failed attempts")
	}
}

func TestOAuthManualClientSkipsRegistration(t *testing.T) {
	r := newOAuthRig(t)
	r.fp.NoDCR = true
	v := r.create(t, model.CreateMCPServer{ClientID: "my-client", ClientSecret: "my-client-secret"})
	r.signIn(t, v.ID, "adm")
	got, _ := r.m.Get(context.Background(), v.ID)
	if r.fp.DCRCalls() != 0 || got.OAuthClient != "manual" || !got.SignedIn {
		t.Fatalf("dcr=%d view=%+v", r.fp.DCRCalls(), got)
	}
	if f := r.fp.Forms()[0]; f.Get("client_id") != "my-client" || f.Get("client_secret") != "my-client-secret" {
		t.Fatalf("token request: %v", f)
	}
	assertNoSecrets(t, "view", []string{"my-client-secret"}, viewJSON(t, r, v.ID))
}

func TestOAuthWithoutRegistrationOrClientIDExplainsWhat(t *testing.T) {
	r := newOAuthRig(t)
	r.fp.NoDCR = true
	v := r.create(t, model.CreateMCPServer{})
	_, err := r.m.OAuthStart(context.Background(), v.ID, "adm")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "enter a client ID") {
		t.Fatalf("got %v", err)
	}
}

func TestOAuthDiscoveryFallbacks(t *testing.T) {
	for name, set := range map[string]func(*mcpfake.Provider){
		"openid-configuration only": func(p *mcpfake.Provider) { p.OIDCOnly = true },
		"no resource_metadata hint": func(p *mcpfake.Provider) { p.NoHint = true },
	} {
		t.Run(name, func(t *testing.T) {
			r := newOAuthRig(t)
			set(r.fp)
			v := r.create(t, model.CreateMCPServer{})
			r.signIn(t, v.ID, "adm")
		})
	}
}

func TestOAuthCreateValidation(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	for name, in := range map[string]model.CreateMCPServer{
		"stdio":             {Name: "a", Transport: model.MCPStdio, Command: "/bin/x", Auth: "oauth"},
		"with a token":      {Name: "a", Transport: model.MCPHTTP, URL: "https://x", Auth: "oauth", Secret: map[string]string{"Authorization": "x"}},
		"secret without id": {Name: "a", Transport: model.MCPHTTP, URL: "https://x", Auth: "oauth", ClientSecret: "s"},
		"unknown auth":      {Name: "a", Transport: model.MCPHTTP, URL: "https://x", Auth: "kerberos"},
	} {
		if _, err := r.m.Create(ctx, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestOAuthBearerReachesDiscoverAndSelection(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	v := r.create(t, model.CreateMCPServer{})
	if got, _ := r.m.Discover(ctx, v.ID); got.DiscoveredAt != nil || !strings.Contains(got.LastError, "not signed in") {
		t.Fatalf("discover before sign-in: %+v", got)
	}
	r.signIn(t, v.ID, "adm")
	r.offered(t, v.ID)
	sel, err := r.m.Selection(ctx, model.AgentInspector)
	if err != nil || len(sel.Servers) != 1 || !strings.HasPrefix(sel.Servers[0].Secret["Authorization"], "Bearer access-") {
		t.Fatalf("selection: %+v %v", sel, err)
	}
}

func TestOAuthRefreshesOnceUnderConcurrencyAndRotates(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	v := r.create(t, model.CreateMCPServer{})
	r.signIn(t, v.ID, "adm")
	r.offered(t, v.ID)
	before, _ := r.m.Selection(ctx, model.AgentInspector)
	r.advance(time.Hour - 30*time.Second)
	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sel, err := r.m.Selection(ctx, model.AgentInspector)
			if err == nil && len(sel.Servers) == 1 {
				got[i] = sel.Servers[0].Secret["Authorization"]
			}
		}()
	}
	wg.Wait()
	if r.fp.Refreshes() != 1 {
		t.Fatalf("refreshes = %d, want 1", r.fp.Refreshes())
	}
	for _, g := range got {
		if g == "" || g == before.Servers[0].Secret["Authorization"] || g != got[0] {
			t.Fatalf("selections: %v", got)
		}
	}
	forms := r.fp.Forms()
	if last := forms[len(forms)-1]; last.Get("grant_type") != "refresh_token" || last.Get("refresh_token") == "" || last.Get("resource") != r.fp.MCPURL() {
		t.Fatalf("refresh form: %v", last)
	}
	r.advance(time.Hour - 30*time.Second)
	if _, err := r.m.Selection(ctx, model.AgentInspector); err != nil || r.fp.Refreshes() != 2 {
		t.Fatalf("the rotated refresh token must work: refreshes=%d err=%v", r.fp.Refreshes(), err)
	}
}

func TestOAuthRefreshFailureDropsServerAndAsksForSignIn(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	v := r.create(t, model.CreateMCPServer{})
	r.signIn(t, v.ID, "adm")
	r.offered(t, v.ID)
	r.advance(time.Hour)
	r.fp.RefreshFail = true
	sel, err := r.m.Selection(ctx, model.AgentInspector)
	if err != nil || len(sel.Servers) != 0 {
		t.Fatalf("selection must skip the server, not fail: %+v %v", sel, err)
	}
	got, _ := r.m.Get(ctx, v.ID)
	if got.Status != model.MCPNeedsSignIn || got.SignedIn || !strings.Contains(got.LastError, "sign in again") {
		t.Fatalf("view: %+v", got)
	}
	assertNoSecrets(t, "last_error", []string{"refresh-", "access-"}, got.LastError)
	r.signIn(t, v.ID, "adm")
	if sel, _ = r.m.Selection(ctx, model.AgentInspector); len(sel.Servers) != 1 {
		t.Fatalf("signing in again restores the server: %+v", sel)
	}
}

func TestOAuthTransientRefreshFailureKeepsSignIn(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	v := r.create(t, model.CreateMCPServer{})
	r.signIn(t, v.ID, "adm")
	r.offered(t, v.ID)
	r.advance(time.Hour)
	r.fp.RefreshDown = true
	if sel, _ := r.m.Selection(ctx, model.AgentInspector); len(sel.Servers) != 0 {
		t.Fatalf("the run goes on without the server: %+v", sel)
	}
	if got, _ := r.m.Get(ctx, v.ID); got.Status != model.MCPActive {
		t.Fatalf("a provider outage must not force a new sign-in: %+v", got)
	}
}

func TestOAuthURLChangeDropsTokens(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	v := r.create(t, model.CreateMCPServer{})
	r.signIn(t, v.ID, "adm")
	other := "https://elsewhere.example/mcp"
	got, err := r.m.Update(ctx, v.ID, model.UpdateMCPServer{URL: &other})
	if err != nil || got.SignedIn || got.OAuthClient != "" || got.Status != model.MCPNeedsSignIn {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestOAuthKeepsRefreshTokenWhenNotRotated(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	r.fp.NoRotate = true
	v := r.create(t, model.CreateMCPServer{})
	r.signIn(t, v.ID, "adm")
	r.offered(t, v.ID)
	for i := 1; i <= 2; i++ {
		r.advance(time.Hour)
		if sel, err := r.m.Selection(ctx, model.AgentInspector); err != nil || len(sel.Servers) != 1 || r.fp.Refreshes() != i {
			t.Fatalf("refresh %d: %+v %v refreshes=%d", i, sel, err, r.fp.Refreshes())
		}
	}
}

func recv(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func signedOut(t *testing.T, r *oauthRig, id string) {
	t.Helper()
	st, err := r.m.loadOAuth(context.Background(), id)
	if err != nil || st.Access != "" || st.Refresh != "" {
		t.Fatalf("tokens survived: %+v %v", st, err)
	}
	if got, _ := r.m.Get(context.Background(), id); got.SignedIn {
		t.Fatalf("still signed in: %+v", got)
	}
}

func TestOAuthURLChangeDuringRefreshLeavesNoTokensOnTheNewHost(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	v := r.create(t, model.CreateMCPServer{})
	r.signIn(t, v.ID, "adm")
	r.offered(t, v.ID)
	hit, release := r.fp.Hold("token")
	r.advance(time.Hour)
	done, patched := make(chan struct{}), make(chan error, 1)
	go func() { _, _ = r.m.Selection(ctx, model.AgentInspector); close(done) }()
	recv(t, hit, "the refresh request")
	other := "https://elsewhere.example/mcp"
	go func() { _, err := r.m.Update(ctx, v.ID, model.UpdateMCPServer{URL: &other}); patched <- err }()
	time.Sleep(50 * time.Millisecond)
	release()
	recv(t, done, "the refresh")
	if err := <-patched; err != nil {
		t.Fatal(err)
	}
	signedOut(t, r, v.ID)
}

func TestOAuthRefreshDoesNotWriteWhenTheURLChangedUnderIt(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	v := r.create(t, model.CreateMCPServer{})
	r.signIn(t, v.ID, "adm")
	r.offered(t, v.ID)
	before, _ := r.m.loadOAuth(ctx, v.ID)
	hit, release := r.fp.Hold("token")
	r.advance(time.Hour)
	done := make(chan struct{})
	go func() { _, _ = r.m.Selection(ctx, model.AgentInspector); close(done) }()
	recv(t, hit, "the refresh request")
	rec, _ := r.repo.Get(ctx, v.ID)
	rec.URL = "https://elsewhere.example/mcp"
	if err := r.repo.Update(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	release()
	recv(t, done, "the refresh")
	if after, _ := r.m.loadOAuth(ctx, v.ID); after.Access != before.Access {
		t.Fatal("the refreshed token was stored for a server that moved")
	}
}

func TestOAuthURLChangeDuringCallbackLeavesNoTokensOnTheNewHost(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	v := r.create(t, model.CreateMCPServer{})
	u, err := r.m.OAuthStart(ctx, v.ID, "adm")
	if err != nil {
		t.Fatal(err)
	}
	code, state, _ := r.fp.Browser(u)
	hit, release := r.fp.Hold("token")
	called, patched := make(chan struct{}), make(chan error, 1)
	go func() { _ = r.m.OAuthCallback(ctx, "adm", code, state); close(called) }()
	recv(t, hit, "the token request")
	other := "https://elsewhere.example/mcp"
	go func() { _, err := r.m.Update(ctx, v.ID, model.UpdateMCPServer{URL: &other}); patched <- err }()
	time.Sleep(50 * time.Millisecond)
	release()
	recv(t, called, "the callback")
	if err := <-patched; err != nil {
		t.Fatal(err)
	}
	signedOut(t, r, v.ID)
}

func TestOAuthPendingSignInIsBoundToTheServerItStartedFor(t *testing.T) {
	t.Run("url changed", func(t *testing.T) {
		r := newOAuthRig(t)
		ctx := context.Background()
		v := r.create(t, model.CreateMCPServer{})
		u, _ := r.m.OAuthStart(ctx, v.ID, "adm")
		code, state, _ := r.fp.Browser(u)
		other := "https://elsewhere.example/mcp"
		if _, err := r.m.Update(ctx, v.ID, model.UpdateMCPServer{URL: &other}); err != nil {
			t.Fatal(err)
		}
		if err := r.m.OAuthCallback(ctx, "adm", code, state); !errors.Is(err, ErrInvalid) || len(r.fp.Forms()) != 0 {
			t.Fatalf("err=%v token requests=%d", err, len(r.fp.Forms()))
		}
		signedOut(t, r, v.ID)
	})
	t.Run("token endpoint changed", func(t *testing.T) {
		r := newOAuthRig(t)
		ctx := context.Background()
		v := r.create(t, model.CreateMCPServer{})
		u, _ := r.m.OAuthStart(ctx, v.ID, "adm")
		code, state, _ := r.fp.Browser(u)
		r.fp.MetaOverride = map[string]any{"token_endpoint": r.fp.Server.URL + "/token?v=2"}
		if _, err := r.m.OAuthStart(ctx, v.ID, "adm"); err != nil {
			t.Fatal(err)
		}
		if err := r.m.OAuthCallback(ctx, "adm", code, state); !errors.Is(err, ErrInvalid) || len(r.fp.Forms()) != 0 {
			t.Fatalf("err=%v token requests=%d", err, len(r.fp.Forms()))
		}
	})
}

func TestOAuthStartDoesNotHoldTheServerLockWhileTalkingToTheNetwork(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	v := r.create(t, model.CreateMCPServer{})
	r.signIn(t, v.ID, "adm")
	r.offered(t, v.ID)
	hit, release := r.fp.Hold("metadata")
	defer release()
	started := make(chan struct{})
	go func() { _, _ = r.m.OAuthStart(ctx, v.ID, "adm"); close(started) }()
	recv(t, hit, "the metadata request")
	sel := make(chan int, 1)
	go func() { s, _ := r.m.Selection(ctx, model.AgentInspector); sel <- len(s.Servers) }()
	select {
	case n := <-sel:
		if n != 1 {
			t.Fatalf("selection lost the server: %d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Selection blocked behind a sign-in that was waiting on the network")
	}
	release()
	recv(t, started, "OAuthStart")
}

func TestOAuthStartAbortsWhenTheURLChangedDuringDiscovery(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	v := r.create(t, model.CreateMCPServer{})
	hit, release := r.fp.Hold("metadata")
	defer release()
	res := make(chan error, 1)
	go func() { _, err := r.m.OAuthStart(ctx, v.ID, "adm"); res <- err }()
	recv(t, hit, "the metadata request")
	other := "https://elsewhere.example/mcp"
	if _, err := r.m.Update(ctx, v.ID, model.UpdateMCPServer{URL: &other}); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-res; !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("got %v", err)
	}
	if st, _ := r.m.loadOAuth(ctx, v.ID); st.TokenEndpoint != "" || st.ClientID != "" {
		t.Fatalf("discovery of the old host was stored: %+v", st)
	}
}

func TestOAuthPlainHTTPOnlyForLoopback(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://example.com/x": true, "http://localhost:8080/x": true, "http://127.0.0.1/x": true, "http://127.9.9.9:1/x": true,
		"http://[::1]:9/x": true, "http://example.com/x": false, "http://localhost.evil.com/x": false, "http://127.0.0.1.evil.com/x": false,
		"http://10.0.0.1/x": false, "ftp://localhost/x": false, "http:///x": false,
	} {
		if got := isHTTPURL(raw); got != want {
			t.Errorf("isHTTPURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestOAuthServerURLMustBeHTTPSUnlessLoopback(t *testing.T) {
	r := newOAuthRig(t)
	ctx := context.Background()
	if _, err := r.m.Create(ctx, model.CreateMCPServer{Name: "a", Transport: model.MCPHTTP, URL: "http://example.com/mcp", Auth: model.MCPAuthOAuth}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create: %v", err)
	}
	v := r.create(t, model.CreateMCPServer{})
	plain := "http://example.com/mcp"
	if _, err := r.m.Update(ctx, v.ID, model.UpdateMCPServer{URL: &plain}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("update: %v", err)
	}
	if _, err := r.m.Create(ctx, model.CreateMCPServer{Name: "tok", Transport: model.MCPHTTP, URL: plain}); err != nil {
		t.Fatalf("a server without OAuth keeps accepting http: %v", err)
	}
}

func TestOAuthDiscoveryRejectsPlainHTTPAtEveryPoint(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*mcpfake.Provider)
		want string
	}{
		"resource metadata hint": {func(p *mcpfake.Provider) { p.HintOverride = "http://example.com/prm" }, "protected resource metadata"},
		"authorization server": {func(p *mcpfake.Provider) {
			p.PRMOverride = map[string]any{"authorization_servers": []string{"http://example.com"}}
		}, "invalid authorization server"},
		"authorization endpoint": {func(p *mcpfake.Provider) {
			p.MetaOverride = map[string]any{"authorization_endpoint": "http://example.com/authorize"}
		}, "metadata"},
		"token endpoint": {func(p *mcpfake.Provider) {
			p.MetaOverride = map[string]any{"token_endpoint": "http://example.com/token"}
		}, "metadata"},
		"registration endpoint": {func(p *mcpfake.Provider) {
			p.MetaOverride = map[string]any{"registration_endpoint": "http://example.com/register"}
		}, "enter a client ID"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newOAuthRig(t)
			tc.set(r.fp)
			v := r.create(t, model.CreateMCPServer{})
			_, err := r.m.OAuthStart(context.Background(), v.ID, "adm")
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestOAuthMetadataMustDescribeItself(t *testing.T) {
	start := func(t *testing.T, set func(*mcpfake.Provider)) error {
		r := newOAuthRig(t)
		set(r.fp)
		v := r.create(t, model.CreateMCPServer{})
		_, err := r.m.OAuthStart(context.Background(), v.ID, "adm")
		return err
	}
	t.Run("resource mismatch", func(t *testing.T) {
		err := start(t, func(p *mcpfake.Provider) { p.PRMOverride = map[string]any{"resource": "https://other.example/mcp"} })
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "different resource") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("missing resource", func(t *testing.T) {
		err := start(t, func(p *mcpfake.Provider) { p.PRMOverride = map[string]any{"resource": ""} })
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("issuer mismatch", func(t *testing.T) {
		err := start(t, func(p *mcpfake.Provider) { p.MetaOverride = map[string]any{"issuer": "https://other.example"} })
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "different issuer") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("trailing slashes match", func(t *testing.T) {
		err := start(t, func(p *mcpfake.Provider) {
			p.PRMOverride = map[string]any{"resource": p.MCPURL() + "/", "authorization_servers": []string{p.Server.URL + "/"}}
			p.MetaOverride = map[string]any{"issuer": p.Server.URL + "/"}
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("endpoints may live on another host", func(t *testing.T) {
		err := start(t, func(p *mcpfake.Provider) {
			other := strings.Replace(p.Server.URL, "127.0.0.1", "localhost", 1)
			p.MetaOverride = map[string]any{"token_endpoint": other + "/token", "authorization_endpoint": other + "/authorize", "registration_endpoint": other + "/register"}
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestWWWAuthParamMatchesWholeNames(t *testing.T) {
	for _, tc := range []struct{ header, name, want string }{
		{`Bearer resource_metadata="https://a/prm", scope="read write"`, "scope", "read write"},
		{`Bearer resource_metadata="https://a/prm", scope="read write"`, "resource_metadata", "https://a/prm"},
		{`Bearer error_scope="x"`, "scope", ""},
		{`Bearer error_scope="x", scope="read"`, "scope", "read"},
		{`Bearer myscope="x" scope=read`, "scope", "read"},
		{`Bearer realm="a, scope=\"evil\"", scope="ok"`, "scope", "ok"},
		{`Bearer Scope="read"`, "scope", "read"},
		{`Bearer`, "scope", ""},
		{``, "scope", ""},
		{`Bearer scope="unterminated`, "scope", ""},
	} {
		if got := wwwAuthParam(tc.header, tc.name); got != tc.want {
			t.Errorf("wwwAuthParam(%q, %q) = %q, want %q", tc.header, tc.name, got, tc.want)
		}
	}
}
