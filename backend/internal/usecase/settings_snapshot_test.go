package usecase

import (
	"context"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestCreateSnapshotsResolvedForemanAndArchitect(t *testing.T) {
	f, _, wos := monitorFactory(t, nil)
	f.DataDir = t.TempDir()
	f.Proj = &store.ProjectRepo{DB: wos.DB}
	w := &WorkOrders{
		Store: wos, Projects: &Projects{Store: f.Proj}, Sandbox: &cloneSandbox{}, Settings: fixedSettings{},
		ResolveDefault: func(role model.AgentRole, s *model.Settings) string {
			return ResolveModel(role, &model.WorkOrder{}, s, map[string]string{"foreman": "p/first", "architect": "p/arch"})
		},
	}
	wo, err := w.Create(context.Background(), &model.User{ID: "u1", Role: model.RoleAdmin}, model.CreateWorkOrderRequest{ProjectID: "p1", Title: "t", Objective: "o"})
	if err != nil {
		t.Fatal(err)
	}
	if wo.ForemanModel != "p/first" || wo.ArchitectModel != "p/arch" {
		t.Fatalf("snapshot: %q %q", wo.ForemanModel, wo.ArchitectModel)
	}
}

func TestChatAuto(t *testing.T) {
	if got := ChatAuto(&model.Settings{}, "a/b"); got != "a/b" {
		t.Fatalf("blank: %q", got)
	}
	if got := ChatAuto(&model.Settings{ChatModel: "c/d"}, "a/b"); got != "c/d" {
		t.Fatalf("set: %q", got)
	}
	if got := ChatAuto(nil, "a/b"); got != "a/b" {
		t.Fatalf("nil: %q", got)
	}
}
