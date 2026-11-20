package app

import (
	"context"
	"path/filepath"
	"testing"

	"legos-factory/backend/internal/agent"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

func TestChatModelListComesFromTheChatRoleAccount(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := (&store.UserRepo{DB: db}).Create(context.Background(), model.User{ID: "u1", Email: "u1@t", Name: "u1", Role: model.RoleEngineer}); err != nil {
		t.Fatal(err)
	}
	box, _ := store.NewSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	agents := usecase.NewAgents(&store.AgentAccountRepo{DB: db}, t.TempDir())
	agents.Secrets = box
	ctx := context.Background()
	u := &model.User{ID: "u1"}
	login, _ := agents.CreateAccount(ctx, u, "claude", "Work", nil)
	api, err := agents.CreateAccount(ctx, u, "claude", "DS", &usecase.APIInput{Provider: "deepseek", BaseURL: "https://api.deepseek.com/anthropic",
		APIKey: "k", Models: usecase.APIModels{Opus: "deepseek-flash", Sonnet: "deepseek-pro"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := agents.Activate(ctx, u, "claude", login.Account.ID); err != nil {
		t.Fatal(err)
	}
	if err := agents.SetAccountRoles(ctx, u, "claude", api.Account.ID, []string{"chat"}); err != nil {
		t.Fatal(err)
	}
	probes := 0
	cats := &catalogs{probe: func(_ context.Context, _ string, env, _ []string) ([]agent.Model, error) {
		probes++
		return []agent.Model{{ProviderID: "claude", ModelID: "default", Name: "Default"}, {ProviderID: "claude", ModelID: "sonnet", Name: "Sonnet"},
			{ProviderID: "claude", ModelID: "haiku", Name: "Haiku"}, {ProviderID: "claude", ModelID: "fable", Name: "Fable"}}, nil
	}}

	chat, err := cats.list(ctx, agents, "claude", "u1", model.AgentChat, false)
	if err != nil || len(chat) != 3 || chat[0].Name != "deepseek-flash" || chat[1].Name != "deepseek-pro" || chat[2].Name != "deepseek-pro" {
		t.Fatalf("chat list %+v %v", chat, err)
	}
	active, err := cats.list(ctx, agents, "claude", "u1", "", false)
	if err != nil || len(active) != 4 || active[0].Name != "Default" {
		t.Fatalf("the active login account keeps the full list: %+v %v", active, err)
	}
	if _, _ = cats.list(ctx, agents, "claude", "u1", model.AgentChat, false); probes != 2 {
		t.Fatalf("each account is probed once: %d", probes)
	}

	if _, err := agents.SetAccountAPI(ctx, u, "claude", api.Account.ID, usecase.APIInput{Provider: "deepseek", BaseURL: "https://api.deepseek.com/anthropic",
		Models: usecase.APIModels{Opus: "deepseek-next", Sonnet: "deepseek-pro"}}); err != nil {
		t.Fatal(err)
	}
	chat, _ = cats.list(ctx, agents, "claude", "u1", model.AgentChat, false)
	if len(chat) != 3 || chat[0].Name != "deepseek-next" {
		t.Fatalf("editing the account must not serve the stale list: %+v", chat)
	}
}

func TestCatalogFollowsRoleAccountAndPrefersAllowedIDs(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := (&store.UserRepo{DB: db}).Create(context.Background(), model.User{ID: "u1", Email: "u1@t", Name: "u1", Role: model.RoleEngineer}); err != nil {
		t.Fatal(err)
	}
	box, _ := store.NewSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	agents := usecase.NewAgents(&store.AgentAccountRepo{DB: db}, t.TempDir())
	agents.Secrets = box
	ctx := context.Background()
	u := &model.User{ID: "u1"}
	login, _ := agents.CreateAccount(ctx, u, "claude", "Work", nil)
	api, err := agents.CreateAccount(ctx, u, "claude", "DS", &usecase.APIInput{Provider: "deepseek", BaseURL: "https://api.deepseek.com/anthropic",
		APIKey: "k", Models: usecase.APIModels{Opus: "deepseek-flash", Sonnet: "deepseek-pro"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = agents.Activate(ctx, u, "claude", login.Account.ID)
	if err := agents.SetAccountRoles(ctx, u, "claude", api.Account.ID, []string{"architect"}); err != nil {
		t.Fatal(err)
	}
	var allowed []string
	cats := &catalogs{allowed: func(context.Context) []string { return allowed },
		probe: func(context.Context, string, []string, []string) ([]agent.Model, error) {
			return []agent.Model{{ProviderID: "claude", ModelID: "default"}, {ProviderID: "claude", ModelID: "claude-opus-5-5"},
				{ProviderID: "claude", ModelID: "sonnet"}, {ProviderID: "claude", ModelID: "fable"}}, nil
		}}
	for _, role := range []model.AgentRole{model.AgentArchitect, model.AgentCataloger} {
		ms, err := cats.list(ctx, agents, "claude", "u1", role, false)
		if err != nil || len(ms) != 2 || ms[0].Name != "deepseek-flash" || ms[0].ModelID != "default" {
			t.Fatalf("%s resolves under the architect's account: %+v %v", role, ms, err)
		}
	}
	if ms, _ := cats.list(ctx, agents, "claude", "u1", model.AgentBuilder, false); len(ms) != 4 {
		t.Fatalf("a role served by the login account keeps the full list: %+v", ms)
	}
	allowed = []string{"claude/claude-opus-5-5"}
	ms, _ := cats.list(ctx, agents, "claude", "u1", model.AgentArchitect, false)
	if len(ms) != 2 || ms[0].ModelID != "claude-opus-5-5" {
		t.Fatalf("an allowed id wins its family: %+v", ms)
	}
}
