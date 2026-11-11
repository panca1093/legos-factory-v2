package usecase

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type jobQueue struct {
	*store.JobRepo
	onCancel func(woID string)
}

func (q *jobQueue) Enqueue(context.Context, string, string, string, map[string]any) (string, error) {
	return "", nil
}

func (q *jobQueue) CancelWO(ctx context.Context, woID string) error {
	if q.onCancel != nil {
		q.onCancel(woID)
	}
	return q.JobRepo.CancelWO(ctx, woID)
}

func deleteFixture(t *testing.T, state model.State) (*Factory, *store.WorkOrderRepo) {
	t.Helper()
	f, wo, wos := monitorFactory(t, nil)
	f.Proj = &store.ProjectRepo{DB: wos.DB}
	f.Sched = &jobQueue{JobRepo: &store.JobRepo{DB: wos.DB}}
	f.DataDir = t.TempDir()
	if err := wos.UpdateFields(context.Background(), wo.ID, map[string]any{"state": string(state)}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"wo1", "wo1.tasks/t1"} {
		if err := os.MkdirAll(filepath.Join(f.DataDir, "workspaces", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f, wos
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestDeleteWorkOrderTerminalRemovesRowsAndDirs(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateCancelled)
	if _, err := wos.AppendEvent(ctx, "wo1", "a", "k", "m", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteWorkOrder(ctx, "wo1"); err != nil {
		t.Fatal(err)
	}
	if _, err := wos.Get(ctx, "wo1"); !errors.Is(err, model.ErrWorkOrderNotFound) {
		t.Fatalf("row still there: %v", err)
	}
	var n int
	if err := wos.DB.QueryRow(`SELECT COUNT(*) FROM wof_events WHERE wo_id = 'wo1'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("events left: %d %v", n, err)
	}
	for _, d := range []string{"wo1", "wo1.tasks"} {
		if exists(filepath.Join(f.DataDir, "workspaces", d)) {
			t.Fatalf("%s survived", d)
		}
	}
}

func TestDeleteWorkOrderRefusesActive(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateImplementing)
	if err := f.DeleteWorkOrder(ctx, "wo1"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("want state conflict, got %v", err)
	}
	if _, err := wos.Get(ctx, "wo1"); err != nil || !exists(filepath.Join(f.DataDir, "workspaces", "wo1")) {
		t.Fatal("active order was touched")
	}
}

func TestDeleteWorkOrderReadyDependsOnPR(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateReady)
	if err := wos.UpdateFields(ctx, "wo1", map[string]any{"pr_ref": "acme/app#7"}); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteWorkOrder(ctx, "wo1"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("open PR: want state conflict, got %v", err)
	}
	if err := wos.UpdateFields(ctx, "wo1", map[string]any{"pr_merged_at": 1700000000}); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteWorkOrder(ctx, "wo1"); err != nil {
		t.Fatalf("merged PR: %v", err)
	}
}

func TestDeleteWorkOrderReadyWithClosedPROrNoPR(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateReady)
	if err := wos.UpdateFields(ctx, "wo1", map[string]any{"pr_ref": "acme/app#7", "pr_closed_at": 1700000000}); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteWorkOrder(ctx, "wo1"); err != nil {
		t.Fatalf("closed PR: %v", err)
	}
	f, _ = deleteFixture(t, model.StateReady)
	if err := f.DeleteWorkOrder(ctx, "wo1"); err != nil {
		t.Fatalf("no PR: %v", err)
	}
}

func TestDeleteProjectKeepsLocalFolderAndRemovesOrders(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateCancelled)
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.Proj.Create(ctx, &model.Project{ID: "p2", Name: "local", RepoURL: folder}); err != nil {
		t.Fatal(err)
	}
	if err := wos.Create(ctx, &model.WorkOrder{ID: "wo2", ProjectID: "p2", Title: "t", Objective: "o", State: model.StateCancelled, CreatedBy: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.DataDir, "workspaces", "wo2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteProject(ctx, "p2"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Proj.Get(ctx, "p2"); !errors.Is(err, model.ErrProjectNotFound) {
		t.Fatalf("project still there: %v", err)
	}
	if _, err := wos.Get(ctx, "wo2"); !errors.Is(err, model.ErrWorkOrderNotFound) {
		t.Fatalf("order still there: %v", err)
	}
	if !exists(filepath.Join(folder, "main.go")) {
		t.Fatal("the user's own folder was touched")
	}
	if exists(filepath.Join(f.DataDir, "workspaces", "wo2")) {
		t.Fatal("workspace survived")
	}
	if _, err := wos.Get(ctx, "wo1"); err != nil {
		t.Fatalf("another project's order was deleted: %v", err)
	}
}

func TestDeleteProjectRefusesWhileOrdersActive(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateCancelled)
	if err := wos.Create(ctx, &model.WorkOrder{ID: "wo2", ProjectID: "p1", Title: "t", Objective: "o", State: model.StateQueued, CreatedBy: "u1"}); err != nil {
		t.Fatal(err)
	}
	err := f.DeleteProject(ctx, "p1")
	if !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("want state conflict, got %v", err)
	}
	if _, gerr := f.Proj.Get(ctx, "p1"); gerr != nil {
		t.Fatal("project removed")
	}
	if _, gerr := wos.Get(ctx, "wo1"); gerr != nil || !exists(filepath.Join(f.DataDir, "workspaces", "wo1")) {
		t.Fatal("terminal order removed although project delete was refused")
	}
}

func TestRemoveInsideRefusesOutsidePaths(t *testing.T) {
	data, outside := t.TempDir(), t.TempDir()
	root := filepath.Join(data, "workspaces")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{outside, root, data, filepath.Join(root, "..", "x"), ""} {
		if err := removeInside(root, p); err == nil {
			t.Fatalf("removed %q", p)
		}
	}
	if !exists(outside) || !exists(root) {
		t.Fatal("a protected path was removed")
	}
}

func addJob(t *testing.T, wos *store.WorkOrderRepo, id, woID, kind string, state model.JobState) {
	t.Helper()
	j := &store.JobRepo{DB: wos.DB}
	if err := j.Insert(context.Background(), model.Job{ID: id, Kind: kind, WOID: woID, MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	j.SetState(context.Background(), id, state)
}

func TestDeleteWorkOrderRefusesWhileJobRunning(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateCancelled)
	addJob(t, wos, "j1", "wo1", JobImplement, model.JobRunning)
	if err := f.DeleteWorkOrder(ctx, "wo1"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("want state conflict, got %v", err)
	}
	if _, err := wos.Get(ctx, "wo1"); err != nil || !exists(filepath.Join(f.DataDir, "workspaces", "wo1")) {
		t.Fatal("order with a running job was removed")
	}
}

func TestDeleteWorkOrderCancelsPendingOpenPRJob(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateReady)
	addJob(t, wos, "j1", "wo1", JobOpenPR, model.JobPending)
	var cancelled []string
	f.Sched.(*jobQueue).onCancel = func(id string) { cancelled = append(cancelled, id) }
	if err := f.DeleteWorkOrder(ctx, "wo1"); err != nil {
		t.Fatal(err)
	}
	if len(cancelled) != 1 || cancelled[0] != "wo1" {
		t.Fatalf("pending jobs not cancelled: %v", cancelled)
	}
}

func TestDeleteWorkOrderDoesNotCancelJobsOfActiveOrder(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateImplementing)
	addJob(t, wos, "j1", "wo1", JobImplement, model.JobPending)
	if err := f.DeleteWorkOrder(ctx, "wo1"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatal(err)
	}
	if !(&store.JobRepo{DB: wos.DB}).HasActive(ctx, "wo1", "", JobImplement) {
		t.Fatal("refused delete cancelled the job")
	}
}

func TestDeleteWorkOrderRefusesWhenSnapshotChanged(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateReady)
	f.Sched.(*jobQueue).onCancel = func(id string) {
		if err := wos.UpdateFields(ctx, id, map[string]any{"pr_ref": "acme/app#9"}); err != nil {
			t.Error(err)
		}
	}
	if err := f.DeleteWorkOrder(ctx, "wo1"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("want state conflict, got %v", err)
	}
	if _, err := wos.Get(ctx, "wo1"); err != nil {
		t.Fatal("order removed although a PR appeared after the check")
	}
}

func TestDeleteWorkOrderReadyWithTrackingFailure(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateReady)
	if err := wos.UpdateFields(ctx, "wo1", map[string]any{"pr_ref": "acme/app#7", "pr_track_failed_at": 1700000000}); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteWorkOrder(ctx, "wo1"); err != nil {
		t.Fatalf("tracking failed: %v", err)
	}
}

func TestDeleteProjectRefusesWhileJobRunning(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateCancelled)
	if err := wos.Create(ctx, &model.WorkOrder{ID: "wo2", ProjectID: "p1", Title: "t", Objective: "o", State: model.StateCancelled, CreatedBy: "u1"}); err != nil {
		t.Fatal(err)
	}
	addJob(t, wos, "j1", "wo2", JobImplement, model.JobRunning)
	if err := f.DeleteProject(ctx, "p1"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("want state conflict, got %v", err)
	}
	for _, id := range []string{"wo1", "wo2"} {
		if _, err := wos.Get(ctx, id); err != nil {
			t.Fatalf("%s removed: %v", id, err)
		}
	}
	if _, err := f.Proj.Get(ctx, "p1"); err != nil || !exists(filepath.Join(f.DataDir, "workspaces", "wo1")) {
		t.Fatal("project delete was not all-or-nothing")
	}
}

func TestDeleteProjectLateOrderIsConflictAndRemovesNothing(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateCancelled)
	f.Sched.(*jobQueue).onCancel = func(string) {
		_ = wos.Create(ctx, &model.WorkOrder{ID: "late", ProjectID: "p1", Title: "t", Objective: "o", State: model.StateQueued, CreatedBy: "u1"})
	}
	if err := f.DeleteProject(ctx, "p1"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("want state conflict, got %v", err)
	}
	for _, id := range []string{"wo1", "late"} {
		if _, err := wos.Get(ctx, id); err != nil {
			t.Fatalf("%s removed: %v", id, err)
		}
	}
	if !exists(filepath.Join(f.DataDir, "workspaces", "wo1")) {
		t.Fatal("workspace removed although nothing was deleted")
	}
}

func TestDeleteProjectDropsItsSearchIndex(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateCancelled)
	kbRepo := &store.KBRepo{DB: wos.DB}
	doc := model.KBParsed{
		KBDoc: model.KBDoc{ID: "d1", Source: model.KBSourceRepo, ProjectID: "p1", Path: "a.md", CommitSHA: "c", ContentHash: "h", Type: "Doc",
			Status: model.KBStatusStable, Trust: model.KBTrustUnverified, Title: "zebrafish", Meta: map[string]any{}},
		Body: "zebrafish quokka",
	}
	if err := kbRepo.Upsert(ctx, doc, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteProject(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := wos.DB.QueryRow(`SELECT COUNT(*) FROM kb_fts WHERE kb_fts MATCH 'quokka'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("fts rows left: %d %v", n, err)
	}
}

func TestDeleteProjectRemovesItsChatCheckout(t *testing.T) {
	ctx := context.Background()
	f, _ := deleteFixture(t, model.StateCancelled)
	if err := f.Proj.Create(ctx, &model.Project{ID: "p3", Name: "chatty"}); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(f.DataDir, "workspaces", "chat-p3")
	other := filepath.Join(f.DataDir, "workspaces", "chat-p1")
	for _, d := range []string{checkout, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.DeleteProject(ctx, "p3"); err != nil {
		t.Fatal(err)
	}
	if exists(checkout) {
		t.Fatal("the deleted project's chat checkout survived")
	}
	if !exists(other) {
		t.Fatal("another project's chat checkout was removed")
	}
}
