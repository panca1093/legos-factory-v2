package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestPlanKeepsEveryTaskDependency(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	seedProject(t, db, "p1")
	if err := (&UserRepo{DB: db}).EnsureUser(ctx, model.User{ID: "u1", Email: "u1@example.test"}); err != nil {
		t.Fatal(err)
	}
	repo := &WorkOrderRepo{DB: db}
	wo := &model.WorkOrder{ProjectID: "p1", Title: "t", State: model.StateCreated, CreatedBy: "u1"}
	if err := repo.Create(ctx, wo); err != nil {
		t.Fatal(err)
	}
	var tasks []model.Task
	for i := 1; i <= 5; i++ {
		task := model.Task{ID: fmt.Sprintf("T%d", i), WOID: wo.ID, Seq: i, Title: "t", Role: "builder", Deps: []string{}}
		if i > 1 {
			task.Deps = []string{fmt.Sprintf("T%d", i-1)}
		}
		tasks = append(tasks, task)
	}
	if err := repo.ReplacePlan(ctx, wo.ID, tasks); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Plan(ctx, wo.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, task := range got {
		if !reflect.DeepEqual(task.Deps, tasks[i].Deps) {
			t.Errorf("%s deps = %v, want %v", task.ID, task.Deps, tasks[i].Deps)
		}
	}
	if roots := model.ReadyTasks(got, map[string]bool{}); len(roots) != 1 || roots[0].ID != "T1" {
		t.Fatalf("a dependency chain must start with only T1 ready, got %d ready", len(roots))
	}
}

func TestLatestEventsReturnsNewestAscendingWithHasMore(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	seedProject(t, db, "p1")
	if err := (&UserRepo{DB: db}).EnsureUser(ctx, model.User{ID: "u1", Email: "u1@example.test"}); err != nil {
		t.Fatal(err)
	}
	repo := &WorkOrderRepo{DB: db}
	wo := &model.WorkOrder{ProjectID: "p1", Title: "t", State: model.StateCreated, CreatedBy: "u1"}
	if err := repo.Create(ctx, wo); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 520; i++ {
		if _, err := repo.AppendEvent(ctx, wo.ID, "factory", "note", fmt.Sprintf("e%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	got, more, err := repo.LatestEvents(ctx, wo.ID, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !more || len(got) != 500 || got[0].Message != "e21" || got[499].Message != "e520" {
		t.Fatalf("more=%v len=%d first=%q last=%q", more, len(got), got[0].Message, got[len(got)-1].Message)
	}
	got, more, _ = repo.LatestEvents(ctx, wo.ID, 520)
	if more || len(got) != 520 || got[0].Message != "e1" {
		t.Fatalf("exact fit: more=%v len=%d", more, len(got))
	}
}
