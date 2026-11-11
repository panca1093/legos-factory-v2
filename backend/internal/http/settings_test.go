package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/agent"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

func bootSettings(t *testing.T, cfg *model.Config) *Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	users := &store.UserRepo{DB: db}
	for id, role := range map[string]model.Role{"eng": model.RoleEngineer, "adm": model.RoleAdmin} {
		if err := users.Create(context.Background(), model.User{ID: id, Email: id + "@t", Name: id, Role: role}); err != nil {
			t.Fatal(err)
		}
		if err := users.CreateSession(context.Background(), "sess-"+id, id, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	cfg.FrontendOrigin = "http://localhost:5173"
	srv := New(cfg, slog.New(slog.NewTextHandler(testDiscard{}, nil)))
	srv.Auth = &usecase.Identity{Users: users, Mode: "dev"}
	srv.Settings = &store.SettingsRepo{DB: db}
	srv.Usage = usecase.NewUsageTracker(&store.UsageRepo{DB: db})
	srv.Chat = &usecase.Chat{
		Store: &store.ChatRepo{DB: db}, Harness: agent.NewMock(),
		Models: func(context.Context) ([]model.ModelInfo, error) {
			return []model.ModelInfo{{ProviderID: "mock", ModelID: "mock-local", Name: "Mock", Harness: "mock"}}, nil
		},
		Default: func(ctx context.Context) string {
			s, _ := srv.Settings.Get(ctx)
			return usecase.ChatAuto(s, "mock/mock-local")
		},
	}
	return srv
}

func TestPatchSettingsOnlyChangesSentKeys(t *testing.T) {
	srv := bootSettings(t, &model.Config{})
	if code, body := chatDo(srv, "adm", "PATCH", "/api/v1/settings", `{"builder_model":"a/b","budget_limit_micros":500}`); code != 200 {
		t.Fatalf("patch: %d %s", code, body)
	}
	code, body := chatDo(srv, "adm", "PATCH", "/api/v1/settings", `{"chat_model":"c/d"}`)
	if code != 200 {
		t.Fatalf("patch: %d %s", code, body)
	}
	var got model.Settings
	_ = json.Unmarshal([]byte(body), &got)
	if got.BuilderModel != "a/b" || got.BudgetLimitMicros != 500 || got.ChatModel != "c/d" {
		t.Fatalf("omitted keys were touched: %s", body)
	}
	if code, _ := chatDo(srv, "adm", "PATCH", "/api/v1/settings", `{"builder_model":""}`); code != 200 {
		t.Fatal("clearing a key must work")
	}
	_, body = chatDo(srv, "adm", "GET", "/api/v1/settings", "")
	_ = json.Unmarshal([]byte(body), &got)
	if got.BuilderModel != "" || got.BudgetLimitMicros != 500 {
		t.Fatalf("after clear: %s", body)
	}
}

func TestSettingsWriteGuards(t *testing.T) {
	srv := bootSettings(t, &model.Config{})
	if code, _ := chatDo(srv, "eng", "PATCH", "/api/v1/settings", `{"builder_model":"x/y"}`); code != http.StatusForbidden {
		t.Fatalf("engineer patch: %d", code)
	}
	if code, _ := chatDo(srv, "", "PATCH", "/api/v1/settings", `{}`); code != http.StatusUnauthorized {
		t.Fatalf("anonymous patch: %d", code)
	}
	if code, _ := chatDo(srv, "adm", "PUT", "/api/v1/settings", `{}`); code != http.StatusMethodNotAllowed && code != http.StatusNotFound {
		t.Fatalf("PUT must be gone: %d", code)
	}
	if code, _ := chatDo(srv, "adm", "PATCH", "/api/v1/settings", `{"budget_limit_micros":-1}`); code != http.StatusBadRequest {
		t.Fatalf("negative cap: %d", code)
	}
}

func TestRuntimeSettingsShapeAndNoSecrets(t *testing.T) {
	srv := bootSettings(t, &model.Config{
		Harness: "acp", ACPCommand: []string{"opencode", "acp"}, MaxConcurrentRuns: 3,
		TypeSafeKey: "sk-very-secret", TypeSafeModel: "jev-1.14.0",
		OpenCodePass: "pw-secret",
	})
	secrets, err := store.NewSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	srv.Integrations = &usecase.Integrations{Secrets: secrets}
	code, body := chatDo(srv, "eng", "GET", "/api/v1/settings/runtime", "")
	if code != 200 {
		t.Fatalf("runtime: %d %s", code, body)
	}
	for _, secret := range []string{"sk-very-secret", "pw-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("secret leaked: %s", body)
		}
	}
	var got struct {
		Harness    string `json:"harness"`
		ACPCommand string `json:"acp_command"`
		Jev        struct {
			Enabled bool   `json:"enabled"`
			Model   string `json:"model"`
		} `json:"jev"`
		CredentialsConfigured bool `json:"credentials_configured"`
		MaxConcurrentRuns     int  `json:"max_concurrent_runs"`
	}
	_ = json.Unmarshal([]byte(body), &got)
	if got.Harness != "acp" || got.ACPCommand != "opencode acp" || !got.Jev.Enabled || got.Jev.Model != "jev-1.14.0" || !got.CredentialsConfigured || got.MaxConcurrentRuns != 3 {
		t.Fatalf("shape: %s", body)
	}
	if code, _ := chatDo(srv, "", "GET", "/api/v1/settings/runtime", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
}

func TestChatModelsReportsConfiguredChatModel(t *testing.T) {
	srv := bootSettings(t, &model.Config{})
	_, body := chatDo(srv, "eng", "GET", "/api/v1/chat/models", "")
	if !strings.Contains(body, `"default":"mock/mock-local"`) {
		t.Fatalf("blank chat_model: %s", body)
	}
	chatDo(srv, "adm", "PATCH", "/api/v1/settings", `{"chat_model":"mock/other"}`)
	_, body = chatDo(srv, "eng", "GET", "/api/v1/chat/models", "")
	if !strings.Contains(body, `"default":"mock/other"`) {
		t.Fatalf("set chat_model: %s", body)
	}
}
