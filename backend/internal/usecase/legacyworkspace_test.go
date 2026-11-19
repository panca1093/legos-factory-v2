package usecase

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestLegacyWorkspacesKeepUnpublishedWork(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateCancelled) // wo1 is cancelled
	root := filepath.Join(f.DataDir, "workspaces")
	for _, d := range []string{"chat-p1", "checks-src-p1", "wo1", "wo1.tasks/t1", "wo2", "wo2.tasks/t1", "wo3"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"wo2", "wo3"} {
		if err := wos.Create(ctx, &model.WorkOrder{ID: id, ProjectID: "p1", Title: "t", Objective: "o", State: model.StateReady, CreatedBy: "u1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := wos.UpdateFields(ctx, "wo3", map[string]any{"pr_ref": "acme/x#1", "pr_url": "https://x/1"}); err != nil {
		t.Fatal(err)
	}
	f.RemoveLegacyWorkspaces(ctx)
	for _, gone := range []string{"chat-p1", "checks-src-p1", "wo1", "wo1.tasks", "wo3"} {
		if exists(filepath.Join(root, gone)) {
			t.Fatalf("%s should be removed", gone)
		}
	}
	for _, kept := range []string{"wo2", "wo2.tasks/t1"} {
		if !exists(filepath.Join(root, kept)) {
			t.Fatalf("%s holds work that was never published and must stay", kept)
		}
	}
	if err := wos.UpdateFields(ctx, "wo2", map[string]any{"pr_ref": "acme/x#2", "pr_url": "https://x/2"}); err != nil {
		t.Fatal(err)
	}
	f.RemoveLegacyWorkspaces(ctx)
	if exists(root) {
		t.Fatal("with every order cancelled or published the whole tree goes")
	}
}
