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
	f, wo, wos := monitorForeman(t, nil)
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
