package usecase

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestFailedVerificationLoopIsBounded(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorForeman(t, nil)
	db := wos.DB
	q := &fakeQueue{}
	f.Sched, f.Sandbox, f.DataDir = q, failSandbox{}, t.TempDir()
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

type failSandbox struct{ dirSandbox }

func (failSandbox) RunCheck(context.Context, string, model.Check, time.Duration) model.CheckResult {
	return model.CheckResult{Outcome: model.Fail}
}

func TestHumanRevisionResetsVerifyBudget(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorForeman(t, nil)
	f.Sched, f.Sandbox, f.DataDir = &fakeQueue{}, failSandbox{}, t.TempDir()
	if err := os.MkdirAll(f.workspaceDir(wo.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.workspaceDir(wo.ID), "go.mod"), []byte("module x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.Proj, f.Quality, f.Findings = &store.ProjectRepo{DB: wos.DB}, &store.QualityRepo{DB: wos.DB}, &store.FindingRepo{DB: wos.DB}
	if err := wos.UpdateFields(ctx, wo.ID, map[string]any{"revision": 2, "verify_failures": MaxRevisionCycles, "state": string(model.StateNeedsHumanReview)}); err != nil {
		t.Fatal(err)
	}
	if err := f.HumanPRDecision(ctx, wo.ID, "request_changes", "more"); err != nil {
		t.Fatal(err)
	}
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
	if got.State != model.StateImplementing || got.VerifyFailures != 1 {
		t.Fatalf("first failure after human revision: state %s, failures %d", got.State, got.VerifyFailures)
	}
}

func TestNoChecksEmitsSkippedNotPassed(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorForeman(t, nil)
	f.Sched, f.Sandbox, f.DataDir = &fakeQueue{}, dirSandbox{}, t.TempDir()
	if err := os.MkdirAll(f.workspaceDir(wo.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	f.Proj, f.Quality, f.Findings = &store.ProjectRepo{DB: wos.DB}, &store.QualityRepo{DB: wos.DB}, &store.FindingRepo{DB: wos.DB}
	if err := wos.UpdateState(ctx, wo.ID, model.StateVerifying); err != nil {
		t.Fatal(err)
	}
	if err := f.handleVerify(ctx, model.Job{WOID: wo.ID}); err != nil {
		t.Fatal(err)
	}
	evs, err := wos.Events(ctx, wo.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var skipped, passed bool
	for _, e := range evs {
		skipped = skipped || e.Kind == "verification_skipped"
		passed = passed || e.Kind == "verification_passed"
	}
	if !skipped || passed {
		t.Fatalf("skipped=%v passed=%v", skipped, passed)
	}
}
