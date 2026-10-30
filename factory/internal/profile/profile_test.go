package profile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/sandbox"
)

var bg = context.Background()

func newStore(t *testing.T, single bool) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(Config{DB: filepath.Join(dir, "c.db"), Key: bytes.Repeat([]byte{7}, 32), SingleOwner: single,
		PlansFile: filepath.Join(dir, "plans.json")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustCred(t *testing.T, s *Store, c Credential, secret string) Credential {
	t.Helper()
	c, err := s.CreateCredential(bg, c, secret)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCredentialsNeverExposeSecret(t *testing.T) {
	s := newStore(t, false)
	c := mustCred(t, s, Credential{Name: "a", Kind: "api_key", Provider: "anthropic"}, "sk-very-secret")
	list, err := s.ListCredentials(bg)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "sk-very-secret") {
		t.Fatalf("secret leaked: %s", raw)
	}
	if list[0].Concurrency != 1 {
		t.Fatalf("default concurrency = %d", list[0].Concurrency)
	}
	var stored []byte
	if err := s.db.QueryRow(`SELECT secret FROM credentials`).Scan(&stored); err != nil || bytes.Contains(stored, []byte("sk-very-secret")) {
		t.Fatalf("secret stored in the clear: %v", err)
	}
	if got, err := s.Secret(bg, c.ID); err != nil || got != "sk-very-secret" {
		t.Fatalf("Secret = %q, %v", got, err)
	}
	if err := s.DeleteCredential(bg, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCredential(bg, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestSubscriptionNeedsSingleOwner(t *testing.T) {
	sub := Credential{Name: "me", Kind: "subscription", Provider: "claude"}
	if _, err := newStore(t, false).CreateCredential(bg, sub, "tok"); !errors.Is(err, ErrSingleOwnerOnly) {
		t.Fatalf("multi-owner: %v", err)
	}
	if _, err := newStore(t, true).CreateCredential(bg, sub, "tok"); err != nil {
		t.Fatalf("single-owner: %v", err)
	}
}

func TestAgentsMatrix(t *testing.T) {
	cases := []struct {
		c    Credential
		want string
	}{
		{Credential{Kind: "subscription", Provider: "claude"}, "claude-agent-acp"},
		{Credential{Kind: "subscription", Provider: "chatgpt"}, "codex-acp,opencode"},
		{Credential{Kind: "subscription", Provider: "opencode-go"}, "opencode"},
		{Credential{Kind: "api_key", Provider: "anthropic"}, "claude-agent-acp,opencode"},
		{Credential{Kind: "api_key", Provider: "zai"}, "claude-agent-acp,opencode"},
		{Credential{Kind: "api_key", Provider: "moonshot"}, "claude-agent-acp,opencode"},
		{Credential{Kind: "api_key", Provider: "minimax"}, "claude-agent-acp,opencode"},
		{Credential{Kind: "api_key", Provider: "openai"}, "codex-acp,opencode"},
	}
	for _, tc := range cases {
		if got := strings.Join(Agents(tc.c), ","); got != tc.want {
			t.Errorf("%s/%s: %s, want %s", tc.c.Kind, tc.c.Provider, got, tc.want)
		}
	}
}

func modelServer(t *testing.T, wantHdr, wantVal, path string, ids ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path || r.Header.Get(wantHdr) != wantVal {
			http.Error(w, "bad request", http.StatusUnauthorized)
			return
		}
		var data []map[string]string
		for _, id := range ids {
			data = append(data, map[string]string{"id": id})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestModelsFetch(t *testing.T) {
	s := newStore(t, true)
	an := modelServer(t, "x-api-key", "k1", "/v1/models", "claude-a", "claude-b")
	c := mustCred(t, s, Credential{Name: "an", Kind: "api_key", Provider: "anthropic", BaseURL: an.URL}, "k1")
	ms, err := s.Models(bg, c)
	if err != nil || len(ms) != 2 || ms[0].ID != "claude-a" {
		t.Fatalf("anthropic models: %v %v", ms, err)
	}
	oa := modelServer(t, "Authorization", "Bearer k2", "/models", "glm-x")
	c = mustCred(t, s, Credential{Name: "z", Kind: "api_key", Provider: "zai", BaseURL: oa.URL}, "k2")
	if ms, err = s.Models(bg, c); err != nil || len(ms) != 1 || ms[0].ID != "glm-x" {
		t.Fatalf("zai models: %v %v", ms, err)
	}
	bad := mustCred(t, s, Credential{Name: "bad", Kind: "api_key", Provider: "zai", BaseURL: oa.URL}, "wrong")
	if _, err = s.Models(bg, bad); err == nil {
		t.Fatal("expected auth failure")
	}
}

func TestAnthropicModelsPaginate(t *testing.T) {
	s := newStore(t, true)
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("after_id") == "" {
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}, "has_more": true, "last_id": "m1"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m2"}}, "has_more": false})
	}))
	t.Cleanup(srv.Close)
	c := mustCred(t, s, Credential{Name: "an", Kind: "api_key", Provider: "anthropic", BaseURL: srv.URL}, "k")
	ms, err := s.Models(bg, c)
	if err != nil || len(ms) != 2 || ms[1].ID != "m2" {
		t.Fatalf("models = %v, %v", ms, err)
	}
	if len(queries) != 2 || queries[0] != "limit=1000" || queries[1] != "limit=1000&after_id=m1" {
		t.Fatalf("queries = %v", queries)
	}
}

func TestOpencodeKnownProviderBaseURL(t *testing.T) {
	s := newStore(t, true)
	c := mustCred(t, s, Credential{Name: "proxy", Kind: "api_key", Provider: "anthropic", BaseURL: "http://proxy.local/v1"}, "S")
	rs, err := s.RunEnv(bg, Profile{Agent: "opencode", CredentialID: c.ID, Model: "M"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Provider map[string]struct {
			Options map[string]string `json:"options"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(rs.Env["OPENCODE_CONFIG_CONTENT"]), &cfg); err != nil || cfg.Provider["anthropic"].Options["baseURL"] != "http://proxy.local/v1" || rs.Env["ANTHROPIC_API_KEY"] != "S" {
		t.Fatalf("env = %v err = %v", rs.Env, err)
	}
}

func TestSubscriptionModelsFromPlansFile(t *testing.T) {
	s := newStore(t, true)
	if _, err := os.Stat(s.plansFile); err != nil {
		t.Fatalf("plans file not written by Open: %v", err)
	}
	c := mustCred(t, s, Credential{Name: "me", Kind: "subscription", Provider: "claude"}, "tok")
	ms, err := s.Models(bg, c)
	if err != nil || len(ms) == 0 {
		t.Fatalf("defaults: %v %v", ms, err)
	}
	if err := os.WriteFile(s.plansFile, []byte(`{"claude":[{"id":"edited"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if ms, err = s.Models(bg, c); err != nil || len(ms) != 1 || ms[0].ID != "edited" {
		t.Fatalf("edited plans: %v %v", ms, err)
	}
}

func TestCreateProfileValidation(t *testing.T) {
	s := newStore(t, true)
	srv := modelServer(t, "x-api-key", "k", "/v1/models", "m1")
	c := mustCred(t, s, Credential{Name: "an", Kind: "api_key", Provider: "anthropic", BaseURL: srv.URL}, "k")

	if _, err := s.CreateProfile(bg, Profile{Name: "p", Agent: "codex-acp", CredentialID: c.ID, Model: "m1"}); err == nil {
		t.Error("incompatible agent accepted")
	}
	if _, err := s.CreateProfile(bg, Profile{Name: "p", Agent: "opencode", CredentialID: c.ID, Model: "nope"}); err == nil {
		t.Error("unknown model accepted")
	}
	p, err := s.CreateProfile(bg, Profile{Name: "p", Agent: "claude-agent-acp", CredentialID: c.ID, Model: "m1"})
	if err != nil || p.VerifiedAt != nil || p.ModelUnchecked {
		t.Fatalf("create: %+v %v", p, err)
	}

	srv.Close()
	if _, err := s.CreateProfile(bg, Profile{Name: "p2", Agent: "claude-agent-acp", CredentialID: c.ID, Model: "m1"}); err == nil {
		t.Error("catalog failure tolerated for a known provider")
	}
	cu := mustCred(t, s, Credential{Name: "cu", Kind: "api_key", Provider: "acme", BaseURL: srv.URL}, "k")
	p, err = s.CreateProfile(bg, Profile{Name: "p3", Agent: "opencode", CredentialID: cu.ID, Model: "m1"})
	if err != nil || !p.ModelUnchecked {
		t.Fatalf("custom provider with dead catalog: %+v %v", p, err)
	}
	got, err := s.GetProfile(bg, p.ID)
	if err != nil || !got.ModelUnchecked {
		t.Fatalf("persisted flag: %+v %v", got, err)
	}
}

func TestRunEnv(t *testing.T) {
	s := newStore(t, true)
	cases := []struct {
		name  string
		cred  Credential
		agent string
		want  map[string]string
		err   error
	}{
		{"anthropic", Credential{Kind: "api_key", Provider: "anthropic"}, "claude-agent-acp",
			map[string]string{"ANTHROPIC_API_KEY": "S", "ANTHROPIC_MODEL": "M", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "M"}, nil},
		{"claude sub", Credential{Kind: "subscription", Provider: "claude"}, "claude-agent-acp",
			map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "S", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "M"}, nil},
		{"zai", Credential{Kind: "api_key", Provider: "zai"}, "claude-agent-acp",
			map[string]string{"ANTHROPIC_BASE_URL": "https://api.z.ai/api/anthropic", "ANTHROPIC_AUTH_TOKEN": "S",
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "M", "ANTHROPIC_DEFAULT_OPUS_MODEL": "M"}, nil},
		{"moonshot override", Credential{Kind: "api_key", Provider: "moonshot", BaseURL: "http://x"}, "claude-agent-acp",
			map[string]string{"ANTHROPIC_BASE_URL": "http://x", "ANTHROPIC_AUTH_TOKEN": "S"}, nil},
		{"custom needs base", Credential{Kind: "api_key", Provider: "acme"}, "claude-agent-acp", nil, errors.New("base_url")},
		{"openai codex", Credential{Kind: "api_key", Provider: "openai"}, "codex-acp", map[string]string{"OPENAI_API_KEY": "S"}, nil},
		{"chatgpt codex bad secret", Credential{Kind: "subscription", Provider: "chatgpt"}, "codex-acp", nil, errors.New("JSON object")},
		{"opencode anthropic", Credential{Kind: "api_key", Provider: "anthropic"}, "opencode", map[string]string{"ANTHROPIC_API_KEY": "S"}, nil},
		{"opencode go bad secret", Credential{Kind: "subscription", Provider: "opencode-go"}, "opencode", nil, errors.New("JSON object")},
		{"opencode custom needs base", Credential{Kind: "api_key", Provider: "acme"}, "opencode", nil, errors.New("base_url")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.cred.Name = tc.name
			c := mustCred(t, s, tc.cred, "S")
			rs, err := s.RunEnv(bg, Profile{Agent: tc.agent, CredentialID: c.ID, Model: "M"})
			hp, env := rs.Profile, rs.Env
			if tc.err != nil {
				if err == nil || (!errors.Is(err, tc.err) && !strings.Contains(err.Error(), tc.err.Error())) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.want {
				if env[k] != v {
					t.Errorf("env[%s] = %q, want %q", k, env[k], v)
				}
			}
			if hp.Agent != tc.agent || hp.Provider != tc.cred.Provider || hp.BillingKind != tc.cred.Kind || hp.Model != "M" {
				t.Errorf("harness profile = %+v", hp)
			}
		})
	}
}

func TestRunEnvAuthFiles(t *testing.T) {
	s := newStore(t, true)
	const authJSON = `{"opencode-go":{"type":"api","key":"k"}}`
	cases := []struct {
		name         string
		cred         Credential
		agent        string
		secret, file string
	}{
		{"opencode go", Credential{Kind: "subscription", Provider: "opencode-go"}, "opencode", authJSON, opencodeAuthFile},
		{"chatgpt codex", Credential{Kind: "subscription", Provider: "chatgpt"}, "codex-acp", `{"tokens":{"access_token":"a"}}`, codexAuthFile},
		{"chatgpt opencode", Credential{Kind: "subscription", Provider: "chatgpt"}, "opencode", `{"openai":{"type":"oauth"}}`, opencodeAuthFile},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.cred.Name = tc.name
			c := mustCred(t, s, tc.cred, tc.secret)
			rs, err := s.RunEnv(bg, Profile{Agent: tc.agent, CredentialID: c.ID, Model: "M"})
			if err != nil {
				t.Fatal(err)
			}
			if string(rs.Files[tc.file]) != tc.secret || len(rs.Files) != 1 || len(rs.Env) != 0 {
				t.Fatalf("files=%v env=%v", rs.Files, rs.Env)
			}
		})
	}

	c := mustCred(t, s, Credential{Name: "acme", Kind: "api_key", Provider: "acme", BaseURL: "http://acme/v1"}, "KEY")
	rs, err := s.RunEnv(bg, Profile{Agent: "opencode", CredentialID: c.ID, Model: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]map[string]string
	if err := json.Unmarshal(rs.Files[opencodeAuthFile], &auth); err != nil || auth["acme"]["key"] != "KEY" || auth["acme"]["type"] != "api" {
		t.Fatalf("auth = %s %v", rs.Files[opencodeAuthFile], err)
	}
	var cfg struct {
		Provider map[string]struct {
			Options map[string]string          `json:"options"`
			Models  map[string]json.RawMessage `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal([]byte(rs.Env["OPENCODE_CONFIG_CONTENT"]), &cfg); err != nil ||
		cfg.Provider["acme"].Options["baseURL"] != "http://acme/v1" || cfg.Provider["acme"].Models["m1"] == nil {
		t.Fatalf("config = %s %v", rs.Env["OPENCODE_CONFIG_CONTENT"], err)
	}
	if strings.Contains(rs.Env["OPENCODE_CONFIG_CONTENT"], "KEY") {
		t.Fatal("key leaked into config env")
	}
}

func verifiedProfile(t *testing.T, s *Store, name string, verified bool) Profile {
	t.Helper()
	c := mustCred(t, s, Credential{Name: name, Kind: "subscription", Provider: "claude"}, "tok")
	p, err := s.CreateProfile(bg, Profile{Name: name, Agent: "claude-agent-acp", CredentialID: c.ID, Model: "claude-sonnet-4-5"})
	if err != nil {
		t.Fatal(err)
	}
	if verified {
		if err := s.markVerified(bg, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestBindAndResolve(t *testing.T) {
	s := newStore(t, true)
	unv := verifiedProfile(t, s, "unv", false)
	ver := verifiedProfile(t, s, "ver", true)

	if err := s.Bind(bg, "", harness.RoleBuilder, unv.ID); !errors.Is(err, ErrUnverified) {
		t.Fatalf("unverified builder: %v", err)
	}
	if err := s.Bind(bg, "", harness.RoleInspector, unv.ID); err != nil {
		t.Fatalf("unverified read-only role: %v", err)
	}
	if _, err := s.Resolve(bg, "p1", harness.RoleBuilder); !errors.Is(err, ErrNoBinding) {
		t.Fatalf("empty resolve: %v", err)
	}
	if err := s.Bind(bg, "", harness.RoleBuilder, ver.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Resolve(bg, "p1", harness.RoleBuilder); err != nil || got.ID != ver.ID || got.VerifiedAt == nil {
		t.Fatalf("global fallback: %+v %v", got, err)
	}
	ver2 := verifiedProfile(t, s, "ver2", true)
	if err := s.Bind(bg, "p1", harness.RoleBuilder, ver2.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Resolve(bg, "p1", harness.RoleBuilder); got.ID != ver2.ID {
		t.Fatalf("project binding lost: %+v", got)
	}
	if got, _ := s.Resolve(bg, "p2", harness.RoleBuilder); got.ID != ver.ID {
		t.Fatalf("other project should use global: %+v", got)
	}
	if err := s.Bind(bg, "p1", harness.RoleBuilder, ver.ID); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if got, _ := s.Resolve(bg, "p1", harness.RoleBuilder); got.ID != ver.ID {
		t.Fatalf("rebind not applied: %+v", got)
	}
}

func TestDeleteCredentialDropsSemaphore(t *testing.T) {
	s := newStore(t, true)
	c := mustCred(t, s, Credential{Name: "sem", Kind: "api_key", Provider: "anthropic"}, "k")
	rel, err := s.Acquire(bg, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if err := s.DeleteCredential(bg, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.sems[c.ID]; ok {
		t.Fatal("semaphore kept after delete")
	}
}

func TestAcquireConcurrency(t *testing.T) {
	s := newStore(t, false)
	c := mustCred(t, s, Credential{Name: "a", Kind: "api_key", Provider: "anthropic", Concurrency: 2}, "k")

	r1, err := s.Acquire(bg, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.Acquire(bg, c.ID)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(bg, 50*time.Millisecond)
	defer cancel()
	if _, err := s.Acquire(ctx, c.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third acquire should time out, got %v", err)
	}

	got := make(chan func(), 1)
	go func() {
		r, err := s.Acquire(bg, c.ID)
		if err != nil {
			t.Error(err)
		}
		got <- r
	}()
	select {
	case <-got:
		t.Fatal("acquired before release")
	case <-time.After(50 * time.Millisecond):
	}
	r1()
	r1() // double release must not free a second slot
	var r3 func()
	select {
	case r3 = <-got:
	case <-time.After(time.Second):
		t.Fatal("waiter not woken by release")
	}
	ctx2, cancel2 := context.WithTimeout(bg, 50*time.Millisecond)
	defer cancel2()
	if _, err := s.Acquire(ctx2, c.ID); err == nil {
		t.Fatal("double release freed an extra slot")
	}
	r2()
	r3()

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Acquire(bg, c.ID)
			if err == nil {
				time.Sleep(time.Millisecond)
				r()
			}
		}()
	}
	wg.Wait()
}

func TestCost(t *testing.T) {
	price := Price{InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75}
	u := harness.Usage{InputTokens: 1_000_000, OutputTokens: 200_000, CacheReadTokens: 5_000_000, CacheCreateTokens: 400_000, CostUSD: 99}
	want := 3.0 + 3.0 + 1.5 + 1.5
	if got := Cost(price, u); math.Abs(got-want) > 1e-9 {
		t.Fatalf("Cost = %v, want %v", got, want)
	}
	if Cost(Price{}, u) != 0 {
		t.Fatal("zero price must cost zero regardless of harness estimate")
	}
}

func TestCostPrefersPerModelUsage(t *testing.T) {
	price := Price{InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3, CacheWritePerMTok: 3.75}
	u := harness.Usage{InputTokens: 10, Models: []harness.ModelUsage{
		{Model: "a", InputTokens: 1_000_000, OutputTokens: 100_000},
		{Model: "b", InputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000},
	}}
	want := 3.0 + 1.5 + 3.0 + 0.3 + 3.75
	if got := Cost(price, u); math.Abs(got-want) > 1e-9 {
		t.Fatalf("Cost = %v, want %v", got, want)
	}
}

type smokeHarness struct {
	write   bool
	content string // defaults to "smoke ok\n"
	extra   string // additional file to write
	stop    string // defaults to end_turn
	noUsage bool
	gotEnv  map[string]string
	gotSpec harness.SessionSpec
}

func (h *smokeHarness) Start(_ context.Context, s harness.SessionSpec) (harness.Session, error) {
	h.gotSpec, h.gotEnv = s, s.Env
	return &smokeSession{h: h, dir: s.Dir, ch: make(chan harness.Event, 4)}, nil
}

type smokeSession struct {
	h   *smokeHarness
	dir string
	ch  chan harness.Event
}

func (s *smokeSession) ID() string                   { return "s" }
func (s *smokeSession) Events() <-chan harness.Event { return s.ch }
func (s *smokeSession) Close() error                 { close(s.ch); return nil }
func (s *smokeSession) Prompt(context.Context, string) (string, error) {
	if !s.h.noUsage {
		s.ch <- harness.Event{Kind: harness.EventUsage, Usage: harness.Usage{InputTokens: 1, OutputTokens: 1}}
	}
	stop := orDefault(s.h.stop, "end_turn")
	if s.h.extra != "" {
		if err := os.WriteFile(filepath.Join(s.dir, s.h.extra), []byte("x"), 0o644); err != nil {
			return "", err
		}
	}
	if s.h.write {
		return stop, os.WriteFile(filepath.Join(s.dir, smokeFile), []byte(orDefault(s.h.content, "smoke ok\n")), 0o644)
	}
	return stop, nil
}

func TestSmoke(t *testing.T) {
	s := newStore(t, true)
	p := verifiedProfile(t, s, "sm", false)
	rt := sandbox.Local{Root: t.TempDir()}

	h := &smokeHarness{}
	if err := s.Smoke(bg, h, rt, p); err == nil || !strings.Contains(err.Error(), smokeFile) {
		t.Fatalf("agent that wrote nothing should fail, got %v", err)
	}
	if got, _ := s.GetProfile(bg, p.ID); got.VerifiedAt != nil {
		t.Fatal("failed smoke marked verified")
	}

	h = &smokeHarness{write: true}
	if err := s.Smoke(bg, h, rt, p); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetProfile(bg, p.ID)
	if got.VerifiedAt == nil {
		t.Fatal("successful smoke did not set VerifiedAt")
	}
	if h.gotEnv["CLAUDE_CODE_OAUTH_TOKEN"] != "tok" || h.gotSpec.Role != harness.RoleBuilder || h.gotSpec.Profile.Model != "claude-sonnet-4-5" {
		t.Fatalf("spec = %+v", h.gotSpec)
	}
	if err := s.Bind(bg, "", harness.RoleBuilder, p.ID); err != nil {
		t.Fatalf("bind after smoke: %v", err)
	}
}

func TestSmokeNegatives(t *testing.T) {
	cases := []struct {
		name string
		h    smokeHarness
		want string
	}{
		{"wrong content", smokeHarness{write: true, content: "hello\n"}, "contains"},
		{"extra file", smokeHarness{write: true, extra: "other.txt"}, "changed more than"},
		{"max_tokens stop", smokeHarness{write: true, stop: "max_tokens"}, "stop reason"},
		{"no usage", smokeHarness{write: true, noUsage: true}, "no token usage"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t, true)
			p := verifiedProfile(t, s, "sm", false)
			h := c.h
			if err := s.Smoke(bg, &h, sandbox.Local{Root: t.TempDir()}, p); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if got, _ := s.GetProfile(bg, p.ID); got.VerifiedAt != nil {
				t.Fatal("failed smoke marked verified")
			}
		})
	}
}

func TestRecordSmoke(t *testing.T) {
	s := newStore(t, true)
	p := verifiedProfile(t, s, "rs", false)
	if err := s.RecordSmoke(bg, p.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetProfile(bg, p.ID); got.SmokeError != "boom" || got.SmokeAt == nil {
		t.Fatalf("got %+v", got)
	}
	if err := s.RecordSmoke(bg, p.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetProfile(bg, p.ID); got.SmokeError != "" || got.SmokeAt == nil {
		t.Fatalf("got %+v", got)
	}
}

func TestSmokeAuthFile(t *testing.T) {
	s := newStore(t, true)
	const secret = `{"opencode-go":{"type":"api","key":"k"}}`
	c := mustCred(t, s, Credential{Name: "go", Kind: "subscription", Provider: "opencode-go"}, secret)
	p, err := s.CreateProfile(bg, Profile{Name: "gop", Agent: "opencode", CredentialID: c.ID, Model: "kimi-k2"})
	if err != nil {
		t.Fatal(err)
	}
	h := &smokeHarness{write: true}
	if err := s.Smoke(bg, h, sandbox.Local{Root: t.TempDir()}, p); err != nil {
		t.Fatal(err)
	}
	if string(h.gotSpec.Files[opencodeAuthFile]) != secret {
		t.Fatalf("files = %v", h.gotSpec.Files)
	}
}
