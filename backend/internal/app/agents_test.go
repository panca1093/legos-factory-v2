package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

func bootACP(t *testing.T) *App {
	t.Helper()
	cfg := &model.Config{
		DataDir: t.TempDir(), AuthMode: "dev", DevUserID: "usr-dev", DevUserName: "Dev",
		Harness: "acp", ACPCommand: []string{"claude-agent-acp"}, MaxConcurrentRuns: 2,
		FrontendOrigin: "http://localhost:5173", TypeSafeModel: "jev-1.14.0",
	}
	a, err := Build(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.DBCloser() })
	a.Server.Agents.LookPath = func(string) (string, error) { return "/bin/x", nil }
	return a
}

func TestResolvePicksTheRolesAgentAndWorkOrdersKeepTheirSnapshot(t *testing.T) {
	a := bootACP(t)
	ctx := context.Background()
	f := a.Server.Factory
	if got := f.Resolve(model.AgentBuilder, &model.WorkOrder{}).Name(); got != "claude-acp" {
		t.Fatalf("default agent: %s", got)
	}
	if _, err := a.Server.WS.Patch(ctx, model.SettingsPatch{RoleAgents: map[string]string{"builder": "codex", "chat": "opencode"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.Resolve(model.AgentBuilder, &model.WorkOrder{}).Name(); got != "codex-acp" {
		t.Fatalf("builder: %s", got)
	}
	if got := f.Resolve(model.AgentArchitect, &model.WorkOrder{}).Name(); got != "claude-acp" {
		t.Fatalf("architect: %s", got)
	}
	if got := f.Resolve(model.AgentCataloger, &model.WorkOrder{}).Name(); got != "claude-acp" {
		t.Fatalf("cataloger follows the architect: %s", got)
	}
	// a work order made before the change keeps resolving the agents it started with, also after a restart
	wo := &model.WorkOrder{RoleAgents: map[string]string{"builder": "claude"}}
	if got := f.Resolve(model.AgentBuilder, wo).Name(); got != "claude-acp" {
		t.Fatalf("snapshot ignored: %s", got)
	}
}

func TestResolveFailsClearlyForUnusableAgents(t *testing.T) {
	a := bootACP(t)
	ctx := context.Background()
	f := a.Server.Factory
	start := func(role model.AgentRole, wo *model.WorkOrder) error {
		_, err := f.Resolve(role, wo).Start(ctx, model.AgentRequest{Role: role})
		return err
	}
	if err := start(model.AgentInspector, &model.WorkOrder{RoleAgents: map[string]string{"inspector": "codex"}}); !errors.Is(err, model.ErrPermanent) || !strings.Contains(err.Error(), "Codex cannot run the inspector role") {
		t.Fatalf("inspector on codex: %v", err)
	}
	if err := a.Server.Agents.SetEnabled(ctx, "claude", false); err != nil {
		t.Fatal(err)
	}
	if err := start(model.AgentBuilder, &model.WorkOrder{}); !errors.Is(err, model.ErrPermanent) || !strings.Contains(err.Error(), "Claude agent is switched off") {
		t.Fatalf("disabled agent: %v", err)
	}
	a.Server.Agents.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	_ = a.Server.Agents.SetEnabled(ctx, "claude", true)
	if err := start(model.AgentBuilder, &model.WorkOrder{}); !errors.Is(err, model.ErrPermanent) || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("missing agent: %v", err)
	}
	// Result must not hang either
	if _, err := f.Resolve(model.AgentBuilder, &model.WorkOrder{}).Result(ctx, "x"); err == nil {
		t.Fatal("result of an unusable agent must fail")
	}
}

func TestSettingsChangesReachTheRunningProcess(t *testing.T) {
	a := bootACP(t)
	ctx := context.Background()
	f := a.Server.Factory
	if f.JudgeFn() != nil {
		t.Fatal("no key, no judge")
	}
	on := true
	key := "sk-test"
	if _, err := a.Server.WS.Patch(ctx, model.SettingsPatch{JevEnabled: &on, JevKey: &key}); err != nil {
		t.Fatal(err)
	}
	if f.JudgeFn() == nil {
		t.Fatal("judge must follow the toggle without a restart")
	}
	off := false
	if _, err := a.Server.WS.Patch(ctx, model.SettingsPatch{JevEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	if f.JudgeFn() != nil {
		t.Fatal("judge must stop when toggled off")
	}
	n := 7
	if _, err := a.Server.WS.Patch(ctx, model.SettingsPatch{MaxConcurrentRuns: &n}); err != nil {
		t.Fatal(err)
	}
}
