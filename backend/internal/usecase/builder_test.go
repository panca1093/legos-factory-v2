package usecase

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

// dirSandbox reports every file under the workspace as changed.
type dirSandbox struct{}

func (dirSandbox) Prepare(context.Context, string, string, *model.GitAuth) (string, error) {
	return "", nil
}
func (dirSandbox) Destroy(context.Context, string) error { return nil }
func (dirSandbox) RunCheck(context.Context, string, model.Check, time.Duration) model.CheckResult {
	return model.CheckResult{}
}
func (dirSandbox) Commit(context.Context, string, string) (string, error) { return "", nil }
func (dirSandbox) PushBranch(context.Context, string, string, *model.GitAuth) (string, error) {
	return "", nil
}
func (dirSandbox) ChangedFiles(_ context.Context, dir string) ([]string, error) {
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, rel)
		}
		return nil
	})
	sort.Strings(files)
	return files, nil
}

func (dirSandbox) TaskWorktree(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (dirSandbox) MergeTask(context.Context, string, string, string) (bool, error) { return false, nil }
func (dirSandbox) SyncTask(context.Context, string, string) ([]string, error)      { return nil, nil }
func (dirSandbox) RemoveTaskWorktree(context.Context, string, string, string) error {
	return nil
}

// writingHarness writes content to file in the workspace, or nothing when file is empty.
type writingHarness struct{ file, content string }

func (writingHarness) Name() string { return "fake" }
func (writingHarness) Start(context.Context, model.AgentRequest) (model.AgentSession, error) {
	return fakeSession{}, nil
}
func (writingHarness) Cancel(context.Context, string) error { return nil }
func (h writingHarness) Result(context.Context, string) (model.AgentResult, error) {
	return model.AgentResult{Text: "done"}, nil
}

type writeOnStart struct {
	writingHarness
	dir *string
}

func (h writeOnStart) Start(context.Context, model.AgentRequest) (model.AgentSession, error) {
	if h.file != "" {
		p := filepath.Join(*h.dir, h.file)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(h.content), 0o644); err != nil {
			return nil, err
		}
	}
	return fakeSession{}, nil
}

func TestRunBuilderRequiresFileChanges(t *testing.T) {
	cases := []struct {
		name     string
		seed     map[string]string
		file     string
		content  string
		wantFail bool
	}{
		{"nothing written", nil, "", "", true},
		{"new file", nil, "a.go", "package a\n", false},
		{"earlier task already did the work", map[string]string{"a.go": "package a\n"}, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var dir string
			h := writeOnStart{writingHarness{tc.file, tc.content}, &dir}
			f, wo, wos := monitorFactory(t, h)
			if err := wos.ReplacePlan(context.Background(), wo.ID, []model.Task{{ID: "T1", WOID: wo.ID, Title: "t", Role: "builder"}}); err != nil {
				t.Fatal(err)
			}
			f.Sandbox = dirSandbox{}
			f.DataDir = t.TempDir()
			dir = f.workspaceDir(wo.ID)
			for name, content := range tc.seed {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err := f.runBuilder(context.Background(), wo, model.Job{TaskID: "T1"}, "p", "m")
			var sawNoChanges bool
			for _, e := range agentEvents(t, wos) {
				if e.Kind == "task_no_changes" {
					sawNoChanges = true
					if e.Actor != "builder" || e.Message != "Builder finished task T1 without changing any files" {
						t.Fatalf("event = %s %q", e.Actor, e.Message)
					}
				}
			}
			if tc.wantFail {
				if !errors.Is(err, model.ErrPermanent) || !sawNoChanges {
					t.Fatalf("err = %v, task_no_changes = %v; want permanent failure with event", err, sawNoChanges)
				}
				return
			}
			if err != nil || sawNoChanges {
				t.Fatalf("err = %v, task_no_changes = %v; want success", err, sawNoChanges)
			}
		})
	}
}

func TestRemediationWithoutChangesFailsItsRun(t *testing.T) {
	var dir string
	f, wo, wos := monitorFactory(t, writeOnStart{writingHarness{}, &dir})
	f.Sandbox = dirSandbox{}
	f.DataDir = t.TempDir()
	dir = f.workspaceDir(wo.ID)
	_, runID, err := f.runBuilder(context.Background(), wo, model.Job{}, "p", "m")
	if !errors.Is(err, model.ErrPermanent) {
		t.Fatalf("err = %v; want permanent failure", err)
	}
	runs, _ := f.Runs.(*store.RunRepo).List(context.Background(), wo.ID)
	if len(runs) != 1 || runs[0].ID != runID || runs[0].State != "failed" {
		t.Fatalf("runs = %+v", runs)
	}
	var failed, completed int
	for _, e := range agentEvents(t, wos) {
		if e.Payload["run_id"] != runID {
			continue
		}
		switch e.Kind {
		case "agent_failed":
			failed++
		case "agent_completed":
			completed++
		}
	}
	if failed != 1 || completed != 0 {
		t.Fatalf("agent_failed = %d, agent_completed = %d; want 1 and 0", failed, completed)
	}
}

func TestCompleteRunsRecordsEachRunsOwnResult(t *testing.T) {
	f, wo, _ := monitorFactory(t, writingHarness{})
	ctx := context.Background()
	runs := []heldRun{{"r1", model.AgentResult{Text: "task", TokensOut: 10}}, {"r2", model.AgentResult{Text: "resolve", TokensOut: 20}}}
	for _, r := range runs {
		if err := f.Runs.Start(ctx, r.id, wo.ID, "", "builder", "fake"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.completeRuns(ctx, wo.ID, runs); err != nil {
		t.Fatal(err)
	}
	got, err := f.Runs.(*store.RunRepo).List(ctx, wo.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("runs = %v, err = %v", got, err)
	}
	for _, r := range got {
		want := map[string]model.AgentResult{"r1": runs[0].res, "r2": runs[1].res}[r.ID]
		if r.Output == nil || *r.Output != want.Text || r.TokensOut != want.TokensOut {
			t.Fatalf("run %s = %v/%d; want %q/%d", r.ID, r.Output, r.TokensOut, want.Text, want.TokensOut)
		}
	}
}

func TestRunStateWritesSurviveExpiredJobContext(t *testing.T) {
	f, wo, _ := monitorFactory(t, writingHarness{})
	ctx, cancel := context.WithCancel(context.Background())
	for _, id := range []string{"r1", "r2"} {
		if err := f.Runs.Start(ctx, id, wo.ID, "", "builder", "fake"); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	f.failRun(ctx, wo.ID, model.AgentBuilder, "r1", errors.New("deadline exceeded"))
	if err := f.completeRuns(ctx, wo.ID, []heldRun{{"r2", model.AgentResult{Text: "ok"}}}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.Runs.(*store.RunRepo).List(context.Background(), wo.ID)
	states := map[string]string{}
	for _, r := range got {
		states[r.ID] = r.State
	}
	if states["r1"] != "failed" || states["r2"] != "completed" {
		t.Fatalf("states = %v; want r1 failed, r2 completed", states)
	}
}

func TestWorkspaceLocksArePerWorkOrder(t *testing.T) {
	f, _, _ := monitorFactory(t, writingHarness{})
	f.Sandbox = dirSandbox{}
	held := f.wsLock("woA")
	held.Lock()
	defer held.Unlock()
	f.baselineLock("woB").Lock()
	defer f.baselineLock("woB").Unlock()
	merged := func(woID string) chan struct{} {
		c := make(chan struct{})
		go func() {
			_, _ = f.mergeTask(context.Background(), woID, "dir", "b", "m")
			close(c)
		}()
		return c
	}
	select {
	case <-merged("woB"):
	case <-time.After(2 * time.Second):
		t.Fatal("a merge of woB waited on woA's lock or on woB's baseline")
	}
	select {
	case <-merged("woA"):
		t.Fatal("two merges of the same work order ran together")
	case <-time.After(100 * time.Millisecond):
	}
}
