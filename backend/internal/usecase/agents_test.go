package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type fakeTools struct {
	runs     atomic.Int32
	outputs  map[string]string
	keychain string
}

func newAgents(t *testing.T) (*Agents, *fakeTools) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	users := &store.UserRepo{DB: db}
	for _, id := range []string{"u1", "u2"} {
		if err := users.Create(context.Background(), model.User{ID: id, Email: id + "@t", Name: id, Role: model.RoleEngineer}); err != nil {
			t.Fatal(err)
		}
	}
	ft := &fakeTools{outputs: map[string]string{
		"claude": `{"loggedIn":true,"email":"me@x.com","subscriptionType":"max","planTier":"max_5x","loginExpiresAt":"2026-12-01T00:00:00Z"}`,
		"codex":  "Logged in using ChatGPT",
	}}
	a := NewAgents(&store.AgentAccountRepo{DB: db}, t.TempDir())
	a.Keychain = func(context.Context, string) (string, error) { return "", errors.New("no keychain in tests") }
	a.LookPath = func(string) (string, error) { return "/bin/x", nil }
	a.Run = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		ft.runs.Add(1)
		return []byte(ft.outputs[name]), nil
	}
	a.CodexLimits = func(context.Context, string) (CodexLimits, error) {
		return CodexLimits{Primary: &UsageWindow{UsedPct: 12, ResetsAt: time.Unix(1794809006, 0).UTC()}}, nil
	}
	return a, ft
}

func TestAgentAccountsAreIsolatedPerUser(t *testing.T) {
	a, _ := newAgents(t)
	ctx := context.Background()
	u1, u2 := &model.User{ID: "u1"}, &model.User{ID: "u2"}
	n, err := a.CreateAccount(ctx, u1, "claude", "Work")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(n.LoginCommand, "CLAUDE_CONFIG_DIR=") || !strings.HasSuffix(n.LoginCommand, "claude auth login") {
		t.Fatalf("login command %q", n.LoginCommand)
	}
	dir := filepath.Join(a.DataDir, "accounts", "u1", "claude", n.Account.ID)
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("account dir: %v %v", fi, err)
	}
	if fi, _ := os.Stat(filepath.Join(a.DataDir, "accounts")); fi.Mode().Perm() != 0o700 {
		t.Fatal("accounts root must be private")
	}
	list2, _ := a.List(ctx, u2)
	if got := len(list2.Agents[0].Accounts); got != 1 {
		t.Fatalf("user 2 sees %d accounts", got)
	}
	if err := a.Activate(ctx, u2, "claude", n.Account.ID); !errors.Is(err, model.ErrAgentAccountNotFound) {
		t.Fatalf("activating another user's account: %v", err)
	}
	if err := a.DeleteAccount(ctx, u2, "claude", n.Account.ID); !errors.Is(err, model.ErrAgentAccountNotFound) {
		t.Fatalf("deleting another user's account: %v", err)
	}
	if _, err := a.Refresh(ctx, u2, "claude", n.Account.ID); !errors.Is(err, model.ErrAgentAccountNotFound) {
		t.Fatalf("refreshing another user's account: %v", err)
	}

	env, _, _ := a.RunEnv(ctx, "claude", "u1")
	if len(env) != 0 {
		t.Fatalf("system default must add no env: %v", env)
	}
	if err := a.Activate(ctx, u1, "claude", n.Account.ID); err != nil {
		t.Fatal(err)
	}
	if env, _, _ = a.RunEnv(ctx, "claude", "u1"); len(env) != 1 || env[0] != "CLAUDE_CONFIG_DIR="+dir {
		t.Fatalf("env %v", env)
	}
	if env, _, _ = a.RunEnv(ctx, "claude", "u2"); len(env) != 0 {
		t.Fatalf("user 2 inherited user 1's account: %v", env)
	}
	list1, _ := a.List(ctx, u1)
	accs := list1.Agents[0].Accounts
	if len(accs) != 2 || !accs[1].Active || accs[0].Active || accs[0].ID != SystemAccountID || accs[0].LoginCommand != "claude auth login" {
		t.Fatalf("accounts %+v", accs)
	}

	if err := a.DeleteAccount(ctx, u1, "claude", SystemAccountID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("system account delete: %v", err)
	}
	if err := a.DeleteAccount(ctx, u1, "claude", n.Account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("account dir not removed")
	}
	list1, _ = a.List(ctx, u1)
	if accs = list1.Agents[0].Accounts; len(accs) != 1 || !accs[0].Active {
		t.Fatalf("deleting the active account must fall back to the system default: %+v", accs)
	}
}

func TestAgentAccountEnvPerAgent(t *testing.T) {
	a, _ := newAgents(t)
	ctx := context.Background()
	u := &model.User{ID: "u1"}
	for agent, want := range map[string]string{"codex": "CODEX_HOME=", "opencode": "XDG_DATA_HOME="} {
		n, err := a.CreateAccount(ctx, u, agent, "x")
		if err != nil || !strings.Contains(n.EnvHint, want) {
			t.Fatalf("%s: %v %q", agent, err, n.EnvHint)
		}
		_ = a.Activate(ctx, u, agent, n.Account.ID)
		env, args, _ := a.RunEnv(ctx, agent, "u1")
		if agent == "opencode" && (len(env) != 4 || args != nil) {
			t.Fatalf("opencode env %v args %v", env, args)
		}
		if agent == "codex" && (len(env) != 1 || args != nil) {
			t.Fatalf("codex env %v args %v", env, args)
		}
	}
	if _, err := a.CreateAccount(ctx, u, "nope", "x"); !errors.Is(err, model.ErrAgentUnknown) {
		t.Fatal(err)
	}
	if _, err := a.CreateAccount(ctx, u, "claude", "  "); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestParseStatus(t *testing.T) {
	for _, c := range []struct {
		agent, out string
		err        error
		want       string
		who, plan  string
	}{
		{"claude", `{"loggedIn":true,"email":"a@b.c","planTier":"max_5x","subscriptionType":"max","loginExpiresAt":"2026-11-29T02:54:14.594Z"}`, nil, "signed_in", "", "max_5x"},
		{"claude", `{"loggedIn":false}`, errors.New("exit 1"), "signed_out", "", ""},
		{"claude", `garbage`, nil, "unknown", "", ""},
		{"claude", ``, errors.New("timeout"), "unknown", "", ""},
		{"codex", "Logged in using ChatGPT\n", nil, "signed_in", "", "ChatGPT"},
		{"codex", "Not logged in\n", errors.New("exit 1"), "signed_out", "", ""},
		{"codex", "boom", errors.New("exit 2"), "unknown", "", ""},
		{"opencode", "DeepSeek          DEEPSEEK_API_KEY            environment\nOpenAI            OpenAI                      stored\n", nil, "signed_in", "DeepSeek, OpenAI", ""},
		{"opencode", "\x1b[0m0 credentials\n", nil, "signed_out", "", ""},
	} {
		got := parseStatus(c.agent, []byte(c.out), c.err)
		if got.Status != c.want || got.Who != c.who || got.Plan != c.plan {
			t.Fatalf("%s %q: %+v", c.agent, c.out, got)
		}
	}
	if st := parseStatus("claude", []byte(`{"loggedIn":true,"loginExpiresAt":"2026-11-29T02:54:14.594Z"}`), nil); st.ExpiresAt == nil {
		t.Fatal("expiry not parsed")
	}
}

func TestStatusIsCachedAndEnvScopedToAccount(t *testing.T) {
	a, ft := newAgents(t)
	ctx := context.Background()
	u := &model.User{ID: "u1"}
	for i := 0; i < 3; i++ {
		if _, err := a.List(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	// claude, codex, opencode system accounts: one status command each, cached for the other two lists
	if got := ft.runs.Load(); got != 3 {
		t.Fatalf("status commands = %d, want 3", got)
	}
	n, _ := a.CreateAccount(ctx, u, "claude", "w")
	acc, err := a.Refresh(ctx, u, "claude", n.Account.ID)
	if err != nil || acc.Status != "signed_in" || acc.Who != "" || acc.Plan != "max_5x" {
		t.Fatalf("%+v %v", acc, err)
	}
}

func TestStatusTimeoutAndUninstalled(t *testing.T) {
	a, _ := newAgents(t)
	a.StatusTimeout = time.Second
	a.Run = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	a.LookPath = func(c string) (string, error) {
		if c == "codex-acp" {
			return "", errors.New("not found")
		}
		return "/bin/x", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	l, err := a.List(ctx, &model.User{ID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("a hung status command must be cut off at StatusTimeout")
	}
	if l.Agents[0].Accounts[0].Status != "unknown" || l.Agents[1].Installed || l.Agents[1].InstallHint == "" {
		t.Fatalf("%+v", l.Agents)
	}
	for _, r := range l.Agents[1].RolesAllowed {
		if r == "inspector" {
			t.Fatal("codex must not be allowed for inspector")
		}
	}
}

func TestCodexUsageAndFailure(t *testing.T) {
	a, _ := newAgents(t)
	ctx := context.Background()
	l, _ := a.List(ctx, &model.User{ID: "u1"})
	u := l.Agents[1].Accounts[0].Usage
	if u == nil || u.Session == nil || u.Session.UsedPct != 12 || u.Week != nil {
		t.Fatalf("%+v", l.Agents[1].Accounts[0])
	}
	a.CodexLimits = func(context.Context, string) (CodexLimits, error) { return CodexLimits{}, errors.New("boom") }
	acc, _ := a.Refresh(ctx, &model.User{ID: "u1"}, "codex", SystemAccountID)
	if acc.Usage != nil || acc.UsageError != "unavailable" {
		t.Fatalf("%+v", acc)
	}
}

func TestParseCodexLimits(t *testing.T) {
	l, err := parseCodexLimits([]byte(`{"rateLimits":{"primary":{"usedPercent":2,"windowDurationMins":300,"resetsAt":1794809006},"secondary":{"usedPercent":1,"windowDurationMins":10080,"resetsAt":1795330726},"planType":"plus"}}`))
	if err != nil || l.Primary.UsedPct != 2 || l.Secondary.UsedPct != 1 || l.Plan != "plus" || l.Primary.ResetsAt.Unix() != 1794809006 {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := parseCodexLimits([]byte(`{"x":1}`)); err == nil {
		t.Fatal("shape error expected")
	}
}

func TestClaudeUsageIsReadAndNeverLeaksToken(t *testing.T) {
	a, _ := newAgents(t)
	ctx := context.Background()
	u := &model.User{ID: "u1"}
	var hits atomic.Int32
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":31.5,"resets_at":"2026-11-16T12:00:00Z"},"seven_day":{"utilization":7,"resets_at":"2026-11-20T00:00:00Z"}}`))
	}))
	defer srv.Close()
	a.UsageURL, a.HTTP = srv.URL, srv.Client()
	var mu sync.Mutex
	services := map[string]bool{}
	a.Keychain = func(_ context.Context, svc string) (string, error) {
		mu.Lock()
		services[svc] = true
		mu.Unlock()
		return `{"claudeAiOauth":{"accessToken":"tok-secret","expiresAt":` + "9999999999999" + `}}`, nil
	}

	l, _ := a.List(ctx, u)
	if hits.Load() != 1 || l.Agents[0].Accounts[0].Usage == nil {
		t.Fatal("claude usage must be read by default")
	}
	acc, err := a.Refresh(ctx, u, "claude", SystemAccountID)
	if err != nil || acc.Usage == nil || acc.Usage.Session.UsedPct != 31.5 || acc.Usage.Week.UsedPct != 7 {
		t.Fatalf("%+v %v", acc, err)
	}
	if auth != "Bearer tok-secret" || !services["Claude Code-credentials"] {
		t.Fatalf("auth %q services %v", auth, services)
	}
	b, _ := json.Marshal(acc)
	if strings.Contains(string(b), "tok-secret") {
		t.Fatal("token leaked")
	}

	// a scoped account reads the scoped Keychain item
	n, _ := a.CreateAccount(ctx, u, "claude", "w")
	_, _ = a.Refresh(ctx, u, "claude", n.Account.ID)
	scoped := false
	for svc := range services {
		scoped = scoped || (strings.HasPrefix(svc, "Claude Code-credentials-") && len(svc) == len("Claude Code-credentials-")+8)
	}
	if !scoped {
		t.Fatalf("no scoped service in %v", services)
	}
}

func TestClaudeUsageFailuresReportUnavailable(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		expiry string
	}{
		"401":     {401, `{}`, "9999999999999"},
		"429":     {429, `{}`, "9999999999999"},
		"shape":   {200, `{"nope":1}`, "9999999999999"},
		"expired": {200, `{}`, "1"},
	} {
		a, _ := newAgents(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		a.UsageURL, a.HTTP = srv.URL, srv.Client()
		a.Keychain = func(context.Context, string) (string, error) {
			return `{"claudeAiOauth":{"accessToken":"t","expiresAt":` + tc.expiry + `}}`, nil
		}
		ctx := context.Background()
		u := &model.User{ID: "u1"}
		acc, _ := a.Refresh(ctx, u, "claude", SystemAccountID)
		srv.Close()
		if acc.Usage != nil || acc.UsageError != "unavailable" {
			t.Fatalf("%s: %+v", name, acc)
		}
	}
}

func TestReadyRefusesDisabledAndMissingAgents(t *testing.T) {
	a, _ := newAgents(t)
	ctx := context.Background()
	if err := a.Ready(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	_ = a.SetEnabled(ctx, "claude", false)
	if err := a.Ready(ctx, "claude"); !errors.Is(err, model.ErrPermanent) || !strings.Contains(err.Error(), "switched off") {
		t.Fatal(err)
	}
	a.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	if err := a.Ready(ctx, "codex"); !errors.Is(err, model.ErrPermanent) || !strings.Contains(err.Error(), "Codex agent is not installed") {
		t.Fatal(err)
	}
}

func TestClaudeServiceNameIsStableAndScoped(t *testing.T) {
	if claudeServiceName("") != "Claude Code-credentials" {
		t.Fatal("system default")
	}
	a, b := claudeServiceName("/data/accounts/u/claude/a1"), claudeServiceName("/data/accounts/u/claude/a2")
	if a == b || a != claudeServiceName("/data/accounts/u/claude/a1") {
		t.Fatal("scoped name must be a stable function of the path")
	}
}

const deepseekSettings = `{"env":{"ANTHROPIC_BASE_URL":"https://api.deepseek.com/anthropic","ANTHROPIC_AUTH_TOKEN":"sk-secret-1","ANTHROPIC_DEFAULT_OPUS_MODEL":"x","OTHER":"y"}}`

func apiAccount(t *testing.T, a *Agents, settings string) (*model.User, string) {
	t.Helper()
	ctx := context.Background()
	u := &model.User{ID: "u1"}
	n, err := a.CreateAccount(ctx, u, "claude", "DeepSeek")
	if err != nil {
		t.Fatal(err)
	}
	if settings != "" {
		if err := os.WriteFile(filepath.Join(filepath.Join(a.DataDir, "accounts", "u1", "claude", n.Account.ID), "settings.json"), []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Activate(ctx, u, "claude", n.Account.ID); err != nil {
		t.Fatal(err)
	}
	return u, n.Account.ID
}

func TestClaudeAPIKeyAccountLiftsEnvAndReportsSignedIn(t *testing.T) {
	a, ft := newAgents(t)
	ft.outputs["claude"] = `{"loggedIn":false}`
	ctx := context.Background()
	u, id := apiAccount(t, a, deepseekSettings)

	env, _, err := a.RunEnv(ctx, "claude", "u1")
	if err != nil || len(env) != 3 || env[1] != "ANTHROPIC_BASE_URL=https://api.deepseek.com/anthropic" || env[2] != "ANTHROPIC_AUTH_TOKEN=sk-secret-1" {
		t.Fatalf("env %v err %v", env, err)
	}
	list, _ := a.List(ctx, u)
	var got AccountView
	for _, acc := range list.Agents[0].Accounts {
		if acc.ID == id {
			got = acc
		}
	}
	if got.Status != "signed_in" || got.Plan != "API key · api.deepseek.com" || got.Usage != nil || got.UsageError != "" {
		t.Fatalf("view %+v", got)
	}

	home := filepath.Join(a.DataDir, "accounts", "u1", "claude", id)
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://api.z.ai/api/anthropic","ANTHROPIC_API_KEY":"k2"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := a.Refresh(ctx, u, "claude", id); err != nil || v.Plan != "API key · api.z.ai" {
		t.Fatalf("refresh %+v %v", v, err)
	}
	if env, _, _ = a.RunEnv(ctx, "claude", "u1"); env[2] != "ANTHROPIC_API_KEY=k2" {
		t.Fatalf("env %v", env)
	}
}

func TestClaudeEnvLiftOnlyWhenBaseURLSet(t *testing.T) {
	ctx := context.Background()
	for name, settings := range map[string]string{"no file": "", "no base url": `{"env":{"ANTHROPIC_AUTH_TOKEN":"k"}}`, "no env": `{}`} {
		a, ft := newAgents(t)
		ft.outputs["claude"] = `{"loggedIn":false}`
		u, id := apiAccount(t, a, settings)
		env, _, err := a.RunEnv(ctx, "claude", "u1")
		if err != nil || len(env) != 1 {
			t.Fatalf("%s: env %v err %v", name, env, err)
		}
		if v, _ := a.Refresh(ctx, u, "claude", id); v.Status != "signed_out" {
			t.Fatalf("%s: status %q", name, v.Status)
		}
	}
}

func TestClaudeMalformedSettingsFailsRunWithoutLeakingKey(t *testing.T) {
	a, _ := newAgents(t)
	apiAccount(t, a, `{"env":{"ANTHROPIC_BASE_URL":"https://x","ANTHROPIC_AUTH_TOKEN":"sk-secret-1"`)
	_, _, err := a.RunEnv(context.Background(), "claude", "u1")
	if err == nil || strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("err %v", err)
	}
}

func TestSystemDefaultNeverLiftsEnv(t *testing.T) {
	a, _ := newAgents(t)
	apiAccount(t, a, deepseekSettings)
	if err := a.Activate(context.Background(), &model.User{ID: "u1"}, "claude", SystemAccountID); err != nil {
		t.Fatal(err)
	}
	if env, _, err := a.RunEnv(context.Background(), "claude", "u1"); err != nil || len(env) != 0 {
		t.Fatalf("env %v err %v", env, err)
	}
}
