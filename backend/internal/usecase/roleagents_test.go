package usecase

import (
	"context"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type agentSettings struct{ agents map[string]string }

func (s agentSettings) Get(context.Context) (*model.Settings, error) {
	return &model.Settings{RoleAgents: s.agents}, nil
}

func TestCreateSnapshotsRoleAgents(t *testing.T) {
	f, _, wos := monitorFactory(t, nil)
	f.DataDir = t.TempDir()
	f.Proj = &store.ProjectRepo{DB: wos.DB}
	box := &cloneSandbox{dataDir: f.DataDir, files: map[string]string{"go.mod": "module x"}}
	set := map[string]string{"builder": "codex", "foreman": "claude"}
	w := &WorkOrders{Store: wos, Projects: &Projects{Store: f.Proj}, Sandbox: box, Settings: agentSettings{set}}
	wo, err := w.Create(context.Background(), &model.User{ID: "u1", Role: model.RoleAdmin}, model.CreateWorkOrderRequest{ProjectID: "p1", Title: "t", Objective: "o"})
	if err != nil {
		t.Fatal(err)
	}
	set["builder"] = "opencode" // a later settings change must not reach the stored order
	stored, err := f.WO.Get(context.Background(), wo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RoleAgents["builder"] != "codex" || stored.RoleAgents["foreman"] != "claude" {
		t.Fatalf("snapshot not stored: %v", stored.RoleAgents)
	}
	if got := RoleAgent(model.AgentBuilder, stored, &model.Settings{RoleAgents: map[string]string{"builder": "opencode"}}, "claude"); got != "codex" {
		t.Fatalf("snapshot must win over settings: %s", got)
	}
	if got := RoleAgent(model.AgentInspector, &model.WorkOrder{}, &model.Settings{}, "claude"); got != "claude" {
		t.Fatalf("fallback: %s", got)
	}
}

func TestResolveAgentModelFallsBackToTheAgentsFirstModel(t *testing.T) {
	s := &model.Settings{BuilderModel: "claude/opus"}
	codex := []string{"codex/gpt-6.1-sol", "codex/gpt-6-luna"}
	if got := ResolveAgentModel(model.AgentBuilder, &model.WorkOrder{}, s, codex); got != "codex/gpt-6.1-sol" {
		t.Fatalf("model of another agent: %s", got)
	}
	if got := ResolveAgentModel(model.AgentBuilder, &model.WorkOrder{BuilderModel: "codex/gpt-6-luna"}, s, codex); got != "codex/gpt-6-luna" {
		t.Fatalf("model in the catalog: %s", got)
	}
	if got := ResolveAgentModel(model.AgentBuilder, &model.WorkOrder{}, s, nil); got != "claude/opus" {
		t.Fatalf("unknown catalog trusts the stored ref: %s", got)
	}
}
