package store

import (
	"context"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestRunListReportsFinishedAtOnlyForEndedRuns(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	seedProject(t, db, "p1")
	if err := (&UserRepo{DB: db}).EnsureUser(ctx, model.User{ID: "u1", Email: "u1@example.test"}); err != nil {
		t.Fatal(err)
	}
	wo := &model.WorkOrder{ProjectID: "p1", Title: "t", State: model.StateCreated, CreatedBy: "u1"}
	if err := (&WorkOrderRepo{DB: db}).Create(ctx, wo); err != nil {
		t.Fatal(err)
	}
	repo := &RunRepo{DB: db}
	for _, id := range []string{"run_done", "run_failed", "run_live"} {
		if err := repo.Start(ctx, id, wo.ID, "", "builder", "acp"); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Complete(ctx, "run_done", model.AgentResult{Text: "ok", CacheRead: 4000, CacheWrite: 900}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Fail(ctx, "run_failed", "boom"); err != nil {
		t.Fatal(err)
	}

	runs, err := repo.List(ctx, wo.ID)
	if err != nil {
		t.Fatal(err)
	}
	finished := map[string]bool{}
	for _, r := range runs {
		if r.ID == "run_done" && (r.CacheRead != 4000 || r.CacheWrite != 900) {
			t.Fatalf("cache tokens not stored: %+v", r)
		}
		finished[r.ID] = r.FinishedAt != nil
		if r.FinishedAt != nil && r.FinishedAt.Before(r.CreatedAt) {
			t.Fatalf("%s finished before it started", r.ID)
		}
	}
	want := map[string]bool{"run_done": true, "run_failed": true, "run_live": false}
	for id, w := range want {
		if finished[id] != w {
			t.Fatalf("%s finished_at set = %v, want %v", id, finished[id], w)
		}
	}
}
