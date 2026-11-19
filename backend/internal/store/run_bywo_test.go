package store

import (
	"context"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestRunningByWOListsEveryLiveRunOfThatOrder(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	seedProject(t, db, "p1")
	if err := (&UserRepo{DB: db}).EnsureUser(ctx, model.User{ID: "u1", Email: "u1@example.test"}); err != nil {
		t.Fatal(err)
	}
	woRepo := &WorkOrderRepo{DB: db}
	a := &model.WorkOrder{ProjectID: "p1", Title: "a", State: model.StateCreated, CreatedBy: "u1"}
	b := &model.WorkOrder{ProjectID: "p1", Title: "b", State: model.StateCreated, CreatedBy: "u1"}
	for _, wo := range []*model.WorkOrder{a, b} {
		if err := woRepo.Create(ctx, wo); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{"t1", "t2", "t3"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO plan_tasks (id, work_order_id, seq, title, created_at) VALUES (?,?,?,?,1)`, id, a.ID, i, id); err != nil {
			t.Fatal(err)
		}
	}
	repo := &RunRepo{DB: db}
	for _, r := range []struct{ id, wo, task, kind string }{
		{"r1", a.ID, "t1", "builder"}, {"r2", a.ID, "t2", "builder"}, {"r3", a.ID, "", "inspector"}, {"r4", b.ID, "", "architect"}, {"r5", a.ID, "t3", "builder"},
	} {
		if err := repo.Start(ctx, r.id, r.wo, r.task, r.kind, "acp"); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Complete(ctx, "r5", model.AgentResult{}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.RunningByWO(ctx, a.ID)
	if err != nil || len(got) != 3 {
		t.Fatalf("want the 3 live runs of a: %v %+v", err, got)
	}
	tasks := map[string]string{}
	for _, g := range got {
		if g.WOID != a.ID || g.WOTitle != "a" {
			t.Fatalf("wrong order: %+v", g)
		}
		tasks[g.TaskID] = g.Kind
	}
	if tasks["t1"] != "builder" || tasks["t2"] != "builder" || tasks[""] != "inspector" {
		t.Fatalf("task ids: %v", tasks)
	}
	if got, err = repo.RunningByWO(ctx, "wo_none"); err != nil || len(got) != 0 {
		t.Fatalf("unknown order: %v %+v", err, got)
	}
}
