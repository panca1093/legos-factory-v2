package usecase

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestFailedVerificationLoopIsBounded(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, nil)
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
	f, wo, wos := monitorFactory(t, nil)
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
	f, wo, wos := monitorFactory(t, nil)
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

type failingSandbox struct {
	dirSandbox
	output string
}

func (s failingSandbox) RunCheck(_ context.Context, _ string, c model.Check, _ time.Duration) model.CheckResult {
	code := 2
	return model.CheckResult{Name: c.Name, Command: c.Command, Outcome: model.Fail, ExitCode: &code, Output: s.output}
}

func TestFailedVerificationPayloadCarriesCheckTail(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, nil)
	q := &fakeQueue{}
	f.Sched, f.DataDir = q, t.TempDir()
	f.Sandbox = failingSandbox{output: "\x1b[31mFAIL\x1b[0m TestThing: expected 1, got 2\n"}
	if err := os.MkdirAll(f.workspaceDir(wo.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.workspaceDir(wo.ID), "go.mod"), []byte("module x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.Proj, f.Quality, f.Findings = &store.ProjectRepo{DB: wos.DB}, &store.QualityRepo{DB: wos.DB}, &store.FindingRepo{DB: wos.DB}
	if err := wos.UpdateState(ctx, wo.ID, model.StateVerifying); err != nil {
		t.Fatal(err)
	}
	if err := f.handleVerify(ctx, model.Job{WOID: wo.ID}); err != nil {
		t.Fatal(err)
	}
	if len(q.payloads) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(q.payloads))
	}
	got, _ := q.payloads[0]["failed_checks"].(string)
	if !strings.Contains(got, "FAIL TestThing: expected 1, got 2") || !strings.Contains(got, "exit code 2") {
		t.Fatalf("payload missing check output: %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("payload still has ANSI codes: %q", got)
	}
}

func TestFailedChecksSectionBoundsOutputAndCount(t *testing.T) {
	long := strings.Repeat("early line that must be dropped\n", 400) + "last line"
	var rs []model.CheckResult
	for i := 0; i < 5; i++ {
		rs = append(rs, model.CheckResult{Name: fmt.Sprintf("c%d", i), Command: "cmd", Outcome: model.Fail, Output: long})
	}
	got := failedChecksSection(rs)
	if strings.Count(got, "Command: cmd") != maxFailedChecks || !strings.Contains(got, "2 more failing checks omitted") {
		t.Fatalf("failing checks not capped: %q", got)
	}
	if !strings.Contains(got, "last line") {
		t.Fatal("tail of output missing")
	}
	if len(got) > maxFailedChecks*(maxFailedOutputLen+200) {
		t.Fatalf("section too large: %d bytes", len(got))
	}
	tail := outputTail(long)
	if !strings.HasPrefix(tail, "…\nearly line") {
		t.Fatalf("tail not cut at a line boundary: %q", tail[:40])
	}
}

func TestFailedChecksSectionKeepsUnverifiedReason(t *testing.T) {
	out := "check timed out after 5m0s\n" + strings.Repeat("progress line\n", 400)
	got := failedChecksSection([]model.CheckResult{{Name: "slow", Command: "make", Outcome: model.Unverified, Output: out}})
	if !strings.Contains(strings.SplitN(got, "\n", 2)[0], "check timed out after 5m0s") {
		t.Fatalf("header hides the reason: %q", got)
	}
}

func TestHumanRetryKeepsFailedChecks(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, nil)
	q := &fakeQueue{}
	f.Sched, f.Quality = q, &store.QualityRepo{DB: wos.DB}
	if err := wos.ReplacePlan(ctx, wo.ID, []model.Task{{ID: "T1", WOID: wo.ID, Title: "a", Role: "builder"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.Quality.Save(ctx, model.QualityCheck{ID: "qc1", WOID: wo.ID, Name: "go test", Command: "go test ./...",
		Outcome: "FAIL", ExitCode: 1, Output: "--- FAIL: TestX", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := wos.UpdateFields(ctx, wo.ID, map[string]any{"state": string(model.StateImplementing), "revision": 2, "verify_failures": 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.RetryTask(ctx, wo.ID, "T1", "human"); err != nil {
		t.Fatal(err)
	}
	p := q.payloads[0]
	got, _ := p["failed_checks"].(string)
	if p["reason"] != "fix_failed_verification" || !strings.Contains(got, "--- FAIL: TestX") || !strings.Contains(got, "go test ./...") {
		t.Fatalf("retry payload lost the failing check: %v", p)
	}
}
