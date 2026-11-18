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

type outputSandbox struct {
	dirSandbox
	out map[string]model.CheckResult
}

func (s outputSandbox) RunCheck(_ context.Context, _ string, c model.Check, _ time.Duration, _ []string) model.CheckResult {
	r := s.out[c.Name]
	r.Name, r.Command = c.Name, c.Command
	return r
}

func (outputSandbox) Commit(context.Context, string, string) (string, error) { return "abc1234", nil }

func TestVerifyToleratesFailuresTheBaselineAlreadyHad(t *testing.T) {
	pass := model.CheckResult{Outcome: model.Pass}
	failA := model.CheckResult{Outcome: model.Fail, Output: "--- FAIL: TestA (0.1s)\nFAIL"}
	failAB := model.CheckResult{Outcome: model.Fail, Output: "--- FAIL: TestA (0.1s)\n--- FAIL: TestB (0.2s)\nFAIL"}
	cases := []struct {
		name     string
		baseline string // go-test outcome before the change
		before   string
		now      model.CheckResult
		sentBack bool
	}{
		{"same failing test", "FAIL", failA.Output, failA, false},
		{"a new failing test", "FAIL", failA.Output, failAB, true},
		{"passed before", "PASS", "", failA, true},
		{"timed out before and now", "UNVERIFIED", "check timed out", model.CheckResult{Outcome: model.Unverified, Output: "check timed out"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f, wo, wos := monitorFactory(t, nil)
			q := &fakeQueue{}
			f.Sched, f.DataDir = q, t.TempDir()
			f.Sandbox = outputSandbox{out: map[string]model.CheckResult{"go-test": tc.now, "go-vet": pass}}
			f.Proj, f.Quality, f.Findings = &store.ProjectRepo{DB: wos.DB}, &store.QualityRepo{DB: wos.DB}, &store.FindingRepo{DB: wos.DB}
			if err := os.MkdirAll(f.workspaceDir(wo.ID), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.workspaceDir(wo.ID), "go.mod"), []byte("module x"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, c := range []model.QualityCheck{
				{ID: baselineIDPrefix + "1", Name: "go-test", Command: "go test ./...", Outcome: tc.baseline, Output: tc.before},
				{ID: baselineIDPrefix + "2", Name: "go-vet", Command: "go vet ./...", Outcome: "PASS"},
			} {
				c.WOID = wo.ID
				if err := f.Quality.Save(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			if err := wos.UpdateState(ctx, wo.ID, model.StateVerifying); err != nil {
				t.Fatal(err)
			}
			if err := f.handleVerify(ctx, model.Job{WOID: wo.ID}); err != nil {
				t.Fatal(err)
			}
			got, _ := wos.Get(ctx, wo.ID)
			if sent := got.State == model.StateImplementing; sent != tc.sentBack {
				t.Fatalf("sent back %v, want %v (state %s, enqueued %v)", sent, tc.sentBack, got.State, q.enqueued)
			}
			if tc.sentBack {
				return
			}
			evs, _ := wos.Events(ctx, wo.ID, 0, 200)
			noted := false
			for _, e := range evs {
				noted = noted || e.Kind == "check_failed_before_change"
			}
			if !noted || got.VerifyFailures != 0 {
				t.Fatalf("noted %v, verify failures %d", noted, got.VerifyFailures)
			}
		})
	}
}

func TestReadinessNeedsHumanWhenARequiredCheckStillFails(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, nil)
	f.Sched, f.Proj, f.Quality, f.Findings = &fakeQueue{}, &store.ProjectRepo{DB: wos.DB}, &store.QualityRepo{DB: wos.DB}, &store.FindingRepo{DB: wos.DB}
	for _, c := range []model.QualityCheck{
		{ID: "q1", Name: "go-test", Command: "go test ./...", Outcome: "FAIL"},
		{ID: "q2", Name: "go-vet", Command: "go vet ./...", Outcome: "PASS"},
	} {
		c.WOID = wo.ID
		if err := f.Quality.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := wos.UpdateState(ctx, wo.ID, model.StateMergeReadiness); err != nil {
		t.Fatal(err)
	}
	wo, _ = wos.Get(ctx, wo.ID)
	if err := f.evaluateReadiness(ctx, wo, false, nil); err != nil {
		t.Fatal(err)
	}
	evs, _ := wos.Events(ctx, wo.ID, 0, 200)
	for _, e := range evs {
		if e.Kind == "readiness_ready" {
			t.Fatal("a required check that still fails must not be READY")
		}
		if e.Kind == "readiness_needs_human_review" {
			return
		}
	}
	t.Fatal("no needs-human-review readiness")
}
