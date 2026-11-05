package usecase

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestFailedVerificationLoopIsBounded(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorForeman(t, nil)
	db := wos.DB
	q := &fakeQueue{}
	f.Sched, f.Sandbox, f.DataDir = q, dirSandbox{}, t.TempDir()
	if err := os.MkdirAll(f.workspaceDir(wo.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.workspaceDir(wo.ID), "go.mod"), []byte("module x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.Proj, f.Quality, f.Findings = &store.ProjectRepo{DB: db}, &store.QualityRepo{DB: db}, &store.FindingRepo{DB: db}

	for i := 0; i <= MaxRevisionCycles+1; i++ {
		if err := wos.UpdateState(ctx, wo.ID, model.StateVerifying); err != nil {
			t.Fatal(err)
		}
		if err := f.handleVerify(ctx, model.Job{WOID: wo.ID}); err != nil {
			t.Fatal(err)
		}
		got, err := wos.Get(ctx, wo.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != model.StateImplementing {
			if i != MaxRevisionCycles {
				t.Fatalf("stopped after %d failed verifications, want %d", i+1, MaxRevisionCycles+1)
			}
			return
		}
	}
	t.Fatalf("still returning to builder after %d failed verifications (enqueued %v)", MaxRevisionCycles+2, q.enqueued)
}
