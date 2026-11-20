package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

type wbStore struct {
	MCPStore
	recs []model.MCPServerRec
}

func (s wbStore) List(context.Context) ([]model.MCPServerRec, error) { return s.recs, nil }
func (s wbStore) Get(_ context.Context, id string) (*model.MCPServerRec, error) {
	for i := range s.recs {
		if s.recs[i].ID == id {
			return &s.recs[i], nil
		}
	}
	return nil, model.ErrMCPNotFound
}

func wbTools(names ...string) []model.MCPTool {
	var out []model.MCPTool
	for _, n := range names {
		out = append(out, model.MCPTool{Name: n})
	}
	return out
}

const cloneAnswer = `{"results":[{"tool":"bitbucket_get_clone_url","result":{"url":"https://x-token-auth:clone-secret@bitbucket.org/acme/app.git"}}]}`

type wbRig struct {
	*rvRig
	wb    *Workbench
	now   time.Time
	mints atomic.Int32
	calls []string
	clone string
}

func newWbRig(t *testing.T) *wbRig {
	t.Helper()
	g := &wbRig{rvRig: newRvRig(t), now: time.Unix(2_000_000, 0), clone: cloneAnswer}
	g.conns.conns = nil
	g.r.Integrations.Host = &fakeHost{repos: map[string]string{"/work/alpha": "git@bitbucket.org:acme/app.git"}}
	g.rv["wb"] = g.rv["c1"]
	g.rv["wb"].me = model.HostUser{Login: "ana", ID: "{ana}"}
	g.wb = &Workbench{
		MCP: &MCPServers{Store: wbStore{recs: []model.MCPServerRec{
			{ID: "m1", Name: "workbench", URL: "https://workbench.example/mcp", Transport: model.MCPHTTP, Status: model.MCPActive, Tools: wbTools("curl_session", "execute_tools", "whoami")},
			{ID: "m2", Name: "stdio", Transport: model.MCPStdio, Status: model.MCPActive, Tools: wbTools("curl_session", "execute_tools")},
			{ID: "m3", Name: "no-exec", Transport: model.MCPHTTP, Status: model.MCPActive, Tools: wbTools("curl_session")},
			{ID: "m4", Name: "signed-out", Transport: model.MCPHTTP, Status: model.MCPNeedsSignIn, Tools: wbTools("curl_session", "execute_tools")},
		}}},
		Now: func() time.Time { return g.now },
		Call: func(_ context.Context, _ model.MCPServer, name string, args any) (string, error) {
			g.calls = append(g.calls, fmt.Sprintf("%s %v", name, args))
			if name == "curl_session" {
				n := g.mints.Add(1)
				return fmt.Sprintf(`{"token":"jwt-%d","expiresIn":900,"proxyBaseUrl":"https://workbench.example/c","usage":"x"}`, n), nil
			}
			return g.clone, nil
		},
		NewReviewer: func(baseURL string, token func(context.Context, string) (string, error)) PRReviewer {
			g.built = append(g.built, "wb "+baseURL)
			return g.rv["wb"]
		},
	}
	g.r.Workbench = g.wb
	return g
}

func TestWorkbenchServersNeedHTTPActiveAndBothTools(t *testing.T) {
	g := newWbRig(t)
	got := g.wb.choices(context.Background())
	if len(got) != 1 || got[0].ID != "m1" || got[0].Name != "workbench" {
		t.Fatalf("%+v", got)
	}
}

func TestWorkbenchTokenIsCachedMintedOnceAndRemintedOnExpiryOrForce(t *testing.T) {
	g := newWbRig(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.wb.session(ctx, "m1", ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if g.mints.Load() != 1 {
		t.Fatalf("concurrent callers must mint once, minted %d", g.mints.Load())
	}
	if want := `curl_session map[expiresInSeconds:900 integrations:[atlassian-bitbucket]]`; g.calls[0] != want {
		t.Fatalf("mint call %q", g.calls[0])
	}
	g.now = g.now.Add(14*time.Minute - time.Second)
	if _, err := g.wb.session(ctx, "m1", ""); err != nil || g.mints.Load() != 1 {
		t.Fatalf("still valid: %v %d", err, g.mints.Load())
	}
	g.now = g.now.Add(2 * time.Second)
	if tok, err := g.wb.session(ctx, "m1", ""); err != nil || tok.token != "jwt-2" || g.mints.Load() != 2 {
		t.Fatalf("within 60 s of expiry must re-mint: %v %+v", err, tok)
	}
	if tok, err := g.wb.session(ctx, "m1", "jwt-2"); err != nil || tok.token != "jwt-3" {
		t.Fatalf("force must re-mint: %v %+v", err, tok)
	}
	if tok, _ := g.wb.session(ctx, "m1", ""); tok.base != "https://workbench.example/c/atlassian-bitbucket" {
		t.Fatalf("base %q", tok.base)
	}
}

func TestWorkbenchConcurrent401sOnTheSameTokenRemintOnce(t *testing.T) {
	g := newWbRig(t)
	ctx := context.Background()
	first, _ := g.wb.session(ctx, "m1", "")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := g.wb.session(ctx, "m1", first.token)
			if err != nil || tok.token == first.token {
				t.Errorf("%v %+v", err, tok)
			}
		}()
	}
	wg.Wait()
	if g.mints.Load() != 2 {
		t.Fatalf("one mint at start plus one re-mint, got %d", g.mints.Load())
	}
}

func TestWorkbenchProxyHostMustMatchTheMCPServer(t *testing.T) {
	g := newWbRig(t)
	g.wb.Call = func(context.Context, model.MCPServer, string, any) (string, error) {
		return `{"token":"jwt-x","expiresIn":900,"proxyBaseUrl":"https://other.example/c"}`, nil
	}
	if _, err := g.wb.session(context.Background(), "m1", ""); err == nil || strings.Contains(err.Error(), "jwt-x") {
		t.Fatalf("%v", err)
	}
}

func TestWorkbenchMintErrorsNeverCarryTheToken(t *testing.T) {
	g := newWbRig(t)
	g.wb.Call = func(context.Context, model.MCPServer, string, any) (string, error) {
		return `{"token":"jwt-secret","expiresIn":900,"proxyBaseUrl":"http://evil.example/c"}`, nil
	}
	_, err := g.wb.session(context.Background(), "m1", "")
	if err == nil || strings.Contains(err.Error(), "jwt-secret") {
		t.Fatalf("a non-https proxy URL must be refused without echoing the token: %v", err)
	}
	g.wb.Call = func(context.Context, model.MCPServer, string, any) (string, error) { return "not json jwt-secret", nil }
	if _, err = g.wb.session(context.Background(), "m1", ""); err == nil || strings.Contains(err.Error(), "jwt-secret") {
		t.Fatalf("%v", err)
	}
}

func TestWorkbenchCloneAuth(t *testing.T) {
	g := newWbRig(t)
	ctx := context.Background()
	auth, err := g.wb.CloneAuth(ctx, "m1", "acme/app")
	if err != nil || auth.Username != "x-token-auth" || auth.Password != "clone-secret" {
		t.Fatalf("%+v %v", auth, err)
	}
	if want := `execute_tools map[executions:[map[args:map[repoSlug:app workspace:acme] tool:bitbucket_get_clone_url]]]`; g.calls[len(g.calls)-1] != want {
		t.Fatalf("call %q", g.calls[len(g.calls)-1])
	}
	g.clone = `{"results":[{"result":"{\"url\":\"https://x-token-auth:text-secret@bitbucket.org/acme/app.git\"}"}]}`
	if auth, err = g.wb.CloneAuth(ctx, "m1", "acme/app"); err != nil || auth.Password != "text-secret" {
		t.Fatalf("a JSON-string result must parse too: %+v %v", auth, err)
	}
	g.clone = `{"results":[{"tool":"bitbucket_get_clone_url","error":"repository not found"}]}`
	if _, err = g.wb.CloneAuth(ctx, "m1", "acme/app"); err == nil || !strings.Contains(err.Error(), "repository not found") {
		t.Fatalf("an error entry must surface: %v", err)
	}
	g.clone = `{"results":[{"result":"https://bitbucket.org/acme/app.git"}]}`
	if _, err = g.wb.CloneAuth(ctx, "m1", "acme/app"); err == nil {
		t.Fatal("a URL without credentials is no clone auth")
	}
	g.clone = `{"results":[]}`
	if _, err = g.wb.CloneAuth(ctx, "m1", "acme/app"); err == nil {
		t.Fatal("no results")
	}
	if _, err = g.wb.CloneAuth(ctx, "m1", "noslash"); err == nil {
		t.Fatal("repo must be workspace/slug")
	}
}

func TestWorkbenchPinBindAndIdentities(t *testing.T) {
	ctx := context.Background()
	g := newWbRig(t)
	if _, err := g.r.resolve(ctx, "ana", g.project(), false); !errors.Is(err, ErrNoReviewIdentity) {
		t.Fatalf("workbench is never auto-picked: %v", err)
	}
	opts, err := g.r.Identities(ctx, g.ana)
	if err != nil || len(opts) != 1 || opts[0].Host != "bitbucket.org" || len(opts[0].Workbench) != 1 || opts[0].Workbench[0].ID != "m1" {
		t.Fatalf("%v %+v", err, opts)
	}

	pin, err := g.r.Pin(ctx, g.ana, model.PinRequest{Provider: "bitbucket", Host: "bitbucket.org", Source: "workbench", ConnectionID: "m1"})
	if err != nil || pin.Source != "workbench" || pin.ConnectionID != "m1" || pin.Login != "ana" || pin.HostUserID != "{ana}" {
		t.Fatalf("%v %+v", err, pin)
	}
	if got := g.st.pins["ana|bitbucket|bitbucket.org"]; got.Source != "workbench" || got.ConnectionID != "m1" {
		t.Fatalf("%+v", got)
	}
	rc, err := g.r.resolve(ctx, "ana", g.project(), false)
	if err != nil || rc.source != "workbench" || rc.connectionID != "m1" || rc.token != "" || rc.gitAuth() != nil {
		t.Fatalf("%v %+v", err, rc)
	}
	if last := g.built[len(g.built)-1]; last != "wb https://workbench.example/c/atlassian-bitbucket" {
		t.Fatalf("built %q", last)
	}
	opts, _ = g.r.Identities(ctx, g.ana)
	if opts[0].Pinned == nil || opts[0].Pinned.Source != "workbench" {
		t.Fatalf("%+v", opts[0].Pinned)
	}

	for _, bad := range []model.PinRequest{
		{Provider: "bitbucket", Host: "bitbucket.org", Source: "workbench", ConnectionID: "m2"},
		{Provider: "bitbucket", Host: "bitbucket.org", Source: "workbench", ConnectionID: "nope"},
		{Provider: "github", Host: "github.com", Source: "workbench", ConnectionID: "m1"},
		{Provider: "bitbucket", Host: "bitbucket.example.com", Source: "workbench", ConnectionID: "m1"},
	} {
		if _, err := g.r.Pin(ctx, g.ana, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v must be rejected: %v", bad, err)
		}
	}
}

func TestWorkbenchIsOffOutsideDevMode(t *testing.T) {
	ctx := context.Background()
	g := newWbRig(t)
	g.r.Workbench = nil
	opts, err := g.r.Identities(ctx, g.ana)
	if err != nil || len(opts) != 1 || opts[0].Workbench != nil {
		t.Fatalf("no workbench list without dev wiring: %v %+v", err, opts)
	}
	if _, err := g.r.Pin(ctx, g.ana, model.PinRequest{Provider: "bitbucket", Host: "bitbucket.org", Source: "workbench", ConnectionID: "m1"}); !errors.Is(err, ErrWorkbenchUnavailable) {
		t.Fatalf("%v", err)
	}
	g.st.pins["ana|bitbucket|bitbucket.org"] = model.ReviewIdentity{UserID: "ana", Provider: "bitbucket", Host: "bitbucket.org", Source: "workbench", ConnectionID: "m1", Login: "ana"}
	if _, err := g.r.resolve(ctx, "ana", g.project(), false); !errors.Is(err, ErrNoReviewIdentity) {
		t.Fatalf("a stored workbench pin must not bind without the dev wiring: %v", err)
	}
}

func TestWorkbenchRunFetchesWithCloneAuthAndNeverStoresIt(t *testing.T) {
	ctx := context.Background()
	g := newWbRig(t)
	g.rv["wb"].detail.SourceRepo = "acme/app"
	if _, err := g.r.Pin(ctx, g.ana, model.PinRequest{Provider: "bitbucket", Host: "bitbucket.org", Source: "workbench", ConnectionID: "m1"}); err != nil {
		t.Fatal(err)
	}
	g.ag.replies = []string{`{"summary":"fine","findings":[]}`}
	if _, err := g.r.StartRun(ctx, g.ana, "p1", 7); err != nil {
		t.Fatal(err)
	}
	g.r.Wait()
	if len(g.ws.auths) != 1 || g.ws.auths[0] == nil || g.ws.auths[0].Username != "x-token-auth" || g.ws.auths[0].Password != "clone-secret" {
		t.Fatalf("auths %+v", g.ws.auths)
	}
	g.ev.mu.Lock()
	for _, e := range g.ev.ev {
		if strings.Contains(fmt.Sprint(e.Payload), "clone-secret") || strings.Contains(fmt.Sprint(e.Payload), "jwt-") {
			t.Fatalf("a secret reached an event: %+v", e)
		}
	}
	g.ev.mu.Unlock()
	for _, p := range g.ag.prompts {
		if strings.Contains(p, "clone-secret") || strings.Contains(p, "jwt-") {
			t.Fatal("a secret reached the agent prompt")
		}
	}

	g.clone = `{"results":[{"error":"no access"}]}`
	g2, _ := g.st.Get(ctx, "ana", "bitbucket", "bitbucket.org", "acme/app", 7)
	if g2 == nil {
		t.Fatal("review row")
	}
	if _, err := g.r.StartRun(ctx, g.ana, "p1", 7); err != nil {
		t.Fatal(err)
	}
	g.r.Wait()
	last, _ := g.st.LatestRun(ctx, g2.ID)
	if last.State != "failed" || !strings.Contains(last.Error, "no access") {
		t.Fatalf("a clone-url error must fail the run: %+v", last)
	}
}
