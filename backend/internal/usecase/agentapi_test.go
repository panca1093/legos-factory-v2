package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

const testKey = "sk-live-do-not-leak-123"

func sealedAgents(t *testing.T) *Agents {
	t.Helper()
	a, _ := newAgents(t)
	box, err := store.NewSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	a.Secrets = box
	return a
}

func deepseekInput() APIInput {
	return APIInput{Provider: "deepseek", BaseURL: "https://api.deepseek.com/anthropic", APIKey: testKey,
		Models: APIModels{Opus: "deepseek-flash[1m]", Sonnet: "deepseek-v4-pro", Haiku: "deepseek-chat"}}
}

func noKey(t *testing.T, what string, v any) {
	t.Helper()
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), testKey) {
		t.Fatalf("%s leaks the key: %s", what, raw)
	}
}

func TestAPIAccountRunEnvCarriesProviderAndBlanksBackendKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-backend")
	a := sealedAgents(t)
	ctx := context.Background()
	u := &model.User{ID: "u1"}
	in := deepseekInput()
	n, err := a.CreateAccount(ctx, u, "claude", "DeepSeek", &in)
	if err != nil {
		t.Fatal(err)
	}
	if n.LoginCommand != "" || n.EnvHint != "" || n.Account.Status != "signed_in" || n.Account.Plan != "API key · api.deepseek.com" {
		t.Fatalf("new account %+v", n)
	}
	noKey(t, "new account", n)
	if err := a.Activate(ctx, u, "claude", n.Account.ID); err != nil {
		t.Fatal(err)
	}
	env, _, err := a.RunEnv(ctx, "claude", "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ANTHROPIC_BASE_URL=https://api.deepseek.com/anthropic", "ANTHROPIC_AUTH_TOKEN=" + testKey, "ANTHROPIC_API_KEY=",
		"ANTHROPIC_DEFAULT_OPUS_MODEL=deepseek-flash[1m]", "ANTHROPIC_DEFAULT_SONNET_MODEL=deepseek-v4-pro", "ANTHROPIC_DEFAULT_HAIKU_MODEL=deepseek-chat"}
	for _, w := range want {
		found := false
		for _, e := range env {
			found = found || e == w
		}
		if !found {
			t.Fatalf("env lacks %q: %v", strings.Replace(w, testKey, "<key>", 1), len(env))
		}
	}
	child := exec.Command("sh", "-c", `printf "%s" "$ANTHROPIC_API_KEY"`)
	child.Env = append(os.Environ(), env...)
	if out, err := child.Output(); err != nil || len(out) != 0 {
		t.Fatalf("the child inherited a key: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(a.DataDir, "accounts", "u1", "claude", n.Account.ID, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("the key must never be written to settings.json")
	}

	list, _ := a.List(ctx, u)
	noKey(t, "agent list", list)
	for _, acc := range list.Agents[0].Accounts {
		if acc.ID == n.Account.ID {
			if acc.API == nil || !acc.API.HasKey || acc.API.Host != "api.deepseek.com" || acc.API.Provider != "deepseek" || acc.Usage != nil {
				t.Fatalf("view %+v", acc)
			}
		}
	}
}

func TestAPIAccountValidation(t *testing.T) {
	a := sealedAgents(t)
	ctx := context.Background()
	u := &model.User{ID: "u1"}
	cases := map[string]func(in *APIInput){
		"http remote":    func(in *APIInput) { in.BaseURL = "http://api.deepseek.com" },
		"relative url":   func(in *APIInput) { in.BaseURL = "api.deepseek.com" },
		"userinfo":       func(in *APIInput) { in.BaseURL = "https://u:p@api.deepseek.com" },
		"no key":         func(in *APIInput) { in.APIKey = " " },
		"key whitespace": func(in *APIInput) { in.APIKey = "sk a" },
		"model space":    func(in *APIInput) { in.Models.Opus = "deep seek" },
		"model long":     func(in *APIInput) { in.Models.Opus = strings.Repeat("m", 101) },
		"no opus/sonnet": func(in *APIInput) { in.Models.Opus, in.Models.Sonnet = "", "" },
		"bad provider":   func(in *APIInput) { in.Provider = "openai" },
	}
	for name, mut := range cases {
		in := deepseekInput()
		mut(&in)
		_, err := a.CreateAccount(ctx, u, "claude", name, &in)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), testKey) {
			t.Fatalf("%s: error leaks the key", name)
		}
	}
	in := deepseekInput()
	in.BaseURL = "http://localhost:8080/anthropic"
	if _, err := a.CreateAccount(ctx, u, "claude", "local", &in); err != nil {
		t.Fatalf("http is fine for localhost: %v", err)
	}
	in = deepseekInput()
	if _, err := a.CreateAccount(ctx, u, "codex", "x", &in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("api on codex: %v", err)
	}
	plain, _ := newAgents(t)
	if _, err := plain.CreateAccount(ctx, u, "claude", "x", &in); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "secret key") {
		t.Fatalf("no sealer: %v", err)
	}
}

func TestSetAccountAPIKeepsKeyOnBlankAndConvertsLogins(t *testing.T) {
	a := sealedAgents(t)
	ctx := context.Background()
	u := &model.User{ID: "u1"}
	login, _ := a.CreateAccount(ctx, u, "claude", "Work", nil)

	in := deepseekInput()
	in.APIKey = ""
	if _, err := a.SetAccountAPI(ctx, u, "claude", login.Account.ID, in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("converting without a key: %v", err)
	}
	in.APIKey = testKey
	v, err := a.SetAccountAPI(ctx, u, "claude", login.Account.ID, in)
	if err != nil || v.API == nil || !v.API.HasKey || v.Status != "signed_in" {
		t.Fatalf("convert: %+v %v", v, err)
	}
	noKey(t, "put response", v)

	in.APIKey = ""
	in.Models.Sonnet = "deepseek-v5"
	in.BaseURL = "https://api.deepseek.com/v2"
	v, err = a.SetAccountAPI(ctx, u, "claude", login.Account.ID, in)
	if err != nil || v.API.Models.Sonnet != "deepseek-v5" || !v.API.HasKey {
		t.Fatalf("edit: %+v %v", v, err)
	}
	_ = a.Activate(ctx, u, "claude", login.Account.ID)
	env, _, _ := a.RunEnv(ctx, "claude", "u1", "")
	has := func(s string) bool {
		for _, e := range env {
			if e == s {
				return true
			}
		}
		return false
	}
	if !has("ANTHROPIC_AUTH_TOKEN="+testKey) || !has("ANTHROPIC_DEFAULT_SONNET_MODEL=deepseek-v5") || !has("ANTHROPIC_BASE_URL=https://api.deepseek.com/v2") {
		t.Fatal("a blank api_key must keep the stored key while the rest changes")
	}

	if _, err := a.SetAccountAPI(ctx, u, "codex", "x", in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("codex: %v", err)
	}
	if _, err := a.SetAccountAPI(ctx, u, "claude", SystemAccountID, in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("system: %v", err)
	}
	if _, err := a.SetAccountAPI(ctx, &model.User{ID: "u2"}, "claude", login.Account.ID, in); !errors.Is(err, model.ErrAgentAccountNotFound) {
		t.Fatalf("another user's account: %v", err)
	}
	if err := a.DeleteAccount(ctx, u, "claude", login.Account.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := a.Store.ListAll(ctx, "claude"); len(all) != 0 {
		t.Fatal("deleting the account must remove the row and its sealed key")
	}
}

func TestLegacySettingsLiftIncludesModelVars(t *testing.T) {
	a, _ := newAgents(t)
	apiAccount(t, a, `{"env":{"ANTHROPIC_BASE_URL":"https://api.deepseek.com/anthropic","ANTHROPIC_AUTH_TOKEN":"k","ANTHROPIC_DEFAULT_OPUS_MODEL":"o","ANTHROPIC_DEFAULT_SONNET_MODEL":"s","ANTHROPIC_DEFAULT_HAIKU_MODEL":"h"}}`)
	env, _, err := a.RunEnv(context.Background(), "claude", "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(env, "|")
	for _, w := range []string{"ANTHROPIC_DEFAULT_OPUS_MODEL=o", "ANTHROPIC_DEFAULT_SONNET_MODEL=s", "ANTHROPIC_DEFAULT_HAIKU_MODEL=h"} {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %s in %v", w, env)
		}
	}
	r, _ := a.RoleAPI(context.Background(), "claude", "u1", model.AgentChat)
	if r.Models == nil || r.Models.Sonnet != "s" || r.Host != "api.deepseek.com" {
		t.Fatalf("legacy accounts still name their models: %+v", r)
	}
}

func TestMigrateAPIAccountsMovesKeyOutOfSettings(t *testing.T) {
	a := sealedAgents(t)
	ctx := context.Background()
	u := &model.User{ID: "u1"}
	n, _ := a.CreateAccount(ctx, u, "claude", "DeepSeek", nil)
	path := filepath.Join(a.DataDir, "accounts", "u1", "claude", n.Account.ID, "settings.json")
	src := `{"theme":"dark","env":{"ANTHROPIC_BASE_URL":"https://api.deepseek.com/anthropic","ANTHROPIC_AUTH_TOKEN":"` + testKey + `","ANTHROPIC_API_KEY":"","ANTHROPIC_DEFAULT_OPUS_MODEL":"deepseek-flash","ANTHROPIC_DEFAULT_SONNET_MODEL":"deepseek-v4-pro","ANTHROPIC_DEFAULT_HAIKU_MODEL":"deepseek-chat","DISABLE_TELEMETRY":"1","N":3}}`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	sealer := a.Secrets
	a.Secrets = nil
	if err := a.MigrateAPIAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != src {
		t.Fatal("without a sealer the file must stay as it was")
	}
	a.Secrets = sealer

	if err := a.MigrateAPIAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), testKey) || strings.Contains(string(b), "ANTHROPIC_") {
		t.Fatalf("settings.json still holds API settings: %s", b)
	}
	var cfg struct {
		Theme string         `json:"theme"`
		Env   map[string]any `json:"env"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil || cfg.Theme != "dark" || cfg.Env["DISABLE_TELEMETRY"] != "1" || cfg.Env["N"] != float64(3) || len(cfg.Env) != 2 {
		t.Fatalf("other keys must survive: %s %v", b, err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	acc, _ := a.Store.Get(ctx, "u1", "claude", n.Account.ID)
	if acc.APIProvider != "deepseek" || acc.APIBaseURL != "https://api.deepseek.com/anthropic" || len(acc.APIKeySealed) == 0 || strings.Contains(string(acc.APIKeySealed), testKey) {
		t.Fatalf("db row %+v", acc)
	}
	_ = a.Activate(ctx, u, "claude", n.Account.ID)
	env, _, err := a.RunEnv(ctx, "claude", "u1", "")
	if err != nil || !strings.Contains(strings.Join(env, "|"), "ANTHROPIC_AUTH_TOKEN="+testKey) || !strings.Contains(strings.Join(env, "|"), "ANTHROPIC_DEFAULT_OPUS_MODEL=deepseek-flash") {
		t.Fatalf("env after migration: %d %v", len(env), err)
	}

	if err := a.MigrateAPIAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(path)
	acc2, _ := a.Store.Get(ctx, "u1", "claude", n.Account.ID)
	if string(b2) != string(b) || string(acc2.APIKeySealed) != string(acc.APIKeySealed) {
		t.Fatal("a second run must change nothing")
	}
}

func TestMigrateAPIAccountsSkipsLoginAndBrokenFiles(t *testing.T) {
	a := sealedAgents(t)
	ctx := context.Background()
	u := &model.User{ID: "u1"}
	login, _ := a.CreateAccount(ctx, u, "claude", "Work", nil)
	bad, _ := a.CreateAccount(ctx, u, "claude", "Bad", nil)
	lp := filepath.Join(a.DataDir, "accounts", "u1", "claude", login.Account.ID, "settings.json")
	bp := filepath.Join(a.DataDir, "accounts", "u1", "claude", bad.Account.ID, "settings.json")
	_ = os.WriteFile(lp, []byte(`{"env":{"FOO":"bar"}}`), 0o600)
	_ = os.WriteFile(bp, []byte(`{"env":{"ANTHROPIC_AUTH_TOKEN":"`+testKey), 0o600)
	err := a.MigrateAPIAccounts(ctx)
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("a broken file is reported without its content: %v", err)
	}
	if b, _ := os.ReadFile(lp); string(b) != `{"env":{"FOO":"bar"}}` {
		t.Fatal("a login account's settings must not be touched")
	}
	if acc, _ := a.Store.Get(ctx, "u1", "claude", login.Account.ID); acc.APIBaseURL != "" {
		t.Fatal("login account became an API account")
	}
}

func TestFoldModels(t *testing.T) {
	list := []model.ModelInfo{
		{ProviderID: "claude", ModelID: "default", Name: "Default"}, {ProviderID: "claude", ModelID: "opus[1m]", Name: "Opus 1M"},
		{ProviderID: "claude", ModelID: "sonnet", Name: "Sonnet"}, {ProviderID: "claude", ModelID: "claude-sonnet-5-5", Name: "S"},
		{ProviderID: "claude", ModelID: "haiku", Name: "Haiku"}, {ProviderID: "claude", ModelID: "fable", Name: "Fable"},
	}
	got := FoldModels(APIModels{Opus: "ds-flash", Sonnet: "ds-pro"}, list)
	if len(got) != 2 || got[0].ModelID != "default" || got[0].Name != "ds-flash" || got[1].ModelID != "sonnet" || got[1].Name != "ds-pro" {
		t.Fatalf("%+v", got)
	}
	if got := FoldModels(APIModels{Sonnet: "s", Haiku: "h"}, list); len(got) != 2 || got[0].ModelID != "sonnet" || got[1].ModelID != "haiku" {
		t.Fatalf("%+v", got)
	}
}

func TestServedUsesTheRoleAccount(t *testing.T) {
	a := sealedAgents(t)
	ctx := context.Background()
	u := &model.User{ID: "u1"}
	login, _ := a.CreateAccount(ctx, u, "claude", "Work", nil)
	in := deepseekInput()
	api, _ := a.CreateAccount(ctx, u, "claude", "DS", &in)
	_ = a.Activate(ctx, u, "claude", login.Account.ID)
	if err := a.SetAccountRoles(ctx, u, "claude", api.Account.ID, []string{"chat"}); err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]string{"": "deepseek-flash[1m]", "claude/default": "deepseek-flash[1m]", "claude/opus[1m]": "deepseek-flash[1m]", "claude/sonnet": "deepseek-v4-pro", "claude/haiku": "deepseek-chat", "claude/fable": ""} {
		if s, h := a.Served(ctx, "claude", "u1", model.AgentChat, ref); s != want || h != "api.deepseek.com" {
			t.Fatalf("%q: %q %q", ref, s, h)
		}
	}
	if s, h := a.Served(ctx, "claude", "u1", model.AgentBuilder, "claude/opus"); s != "" || h != "" {
		t.Fatalf("a login account leaves both empty: %q %q", s, h)
	}
	r1, _ := a.RoleAPI(ctx, "claude", "u1", model.AgentChat)
	in.Models.Opus = "other"
	in.APIKey = ""
	_, _ = a.SetAccountAPI(ctx, u, "claude", api.Account.ID, in)
	if r2, _ := a.RoleAPI(ctx, "claude", "u1", model.AgentChat); r1.Sig == r2.Sig {
		t.Fatal("editing the model map must change the cache signature")
	}
}
