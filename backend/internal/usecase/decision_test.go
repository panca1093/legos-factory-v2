package usecase

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type nopBus struct{}

func (nopBus) Publish(model.LiveEvent) {}

func TestFindingDispositionsAndEscalationPause(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := (&store.UserRepo{DB: db}).Create(ctx, model.User{ID: "u1", Email: "u1@t", Name: "U", Role: model.RoleEngineer}); err != nil {
		t.Fatal(err)
	}
	if err := (&store.ProjectRepo{DB: db}).Create(ctx, &model.Project{ID: "p1", Name: "p"}); err != nil {
		t.Fatal(err)
	}
	wos := &store.WorkOrderRepo{DB: db}
	wo := &model.WorkOrder{ID: "wo1", ProjectID: "p1", Title: "t", Objective: "o", State: model.StateImplementing, CreatedBy: "u1"}
	if err := wos.Create(ctx, wo); err != nil {
		t.Fatal(err)
	}
	runs := &store.RunRepo{DB: db}
	if err := runs.Start(ctx, "run1", "wo1", "", "inspector", "mock"); err != nil {
		t.Fatal(err)
	}
	findings := &store.FindingRepo{DB: db}
	for _, id := range []string{"f1", "f2"} {
		if err := findings.Create(ctx, model.Finding{ID: id, RunID: "run1", WOID: "wo1", Severity: "blocker", Title: id, Status: model.FindingOpen, Specialty: "testing"}); err != nil {
			t.Fatal(err)
		}
	}
	f := &Foreman{WO: wos, Findings: findings, Bus: nopBus{}, Log: slog.Default()}

	if _, err := f.DecideFinding(ctx, "wo1", "f1", "u1", model.FindingDecisionRequest{Action: "ignore"}); err == nil {
		t.Fatal("unknown actions must be rejected")
	}
	if _, err := f.DecideFinding(ctx, "wo1", "f1", "u1", model.FindingDecisionRequest{Action: "dispute"}); err == nil {
		t.Fatal("a dispute needs a reason")
	}
	fd, err := f.DecideFinding(ctx, "wo1", "f1", "u1", model.FindingDecisionRequest{Action: "dispute", Note: "false positive"})
	if err != nil || fd.Status != model.FindingDisputed || fd.Note != "false positive" || fd.DecidedBy != "u1" {
		t.Fatalf("dispute: %+v %v", fd, err)
	}
	if _, err := f.DecideFinding(ctx, "wo1", "f2", "u1", model.FindingDecisionRequest{Action: "confirm"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := findings.CountOpenBlockers(ctx, "wo1"); n != 1 {
		t.Fatalf("confirmed still blocks, disputed does not: want 1 blocker, got %d", n)
	}
	if _, err := f.DecideFinding(ctx, "wo1", "f2", "u1", model.FindingDecisionRequest{Action: "escalate", Note: "needs a product call"}); err != nil {
		t.Fatal(err)
	}
	got, _ := wos.Get(ctx, "wo1")
	if got.PausedAt == nil || got.PauseReason != "Finding escalated: f2" {
		t.Fatalf("escalation must pause the order: %+v", got)
	}
	if err := f.Pause(ctx, "wo1", "human", "again"); err == nil {
		t.Fatal("pausing a paused order must conflict")
	}
	if err := f.Resume(ctx, "wo1", "human"); err != nil {
		t.Fatal(err)
	}
	if got, _ := wos.Get(ctx, "wo1"); got.PausedAt != nil || got.PauseReason != "" {
		t.Fatalf("resume must clear the pause: %+v", got)
	}
	if _, err := f.DecideFinding(ctx, "wo1", "missing", "u1", model.FindingDecisionRequest{Action: "confirm"}); !IsNotFound(err) {
		t.Fatalf("unknown finding: %v", err)
	}
}
