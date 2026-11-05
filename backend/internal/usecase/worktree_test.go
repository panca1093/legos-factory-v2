package usecase

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/sandbox"
	"legos-factory/backend/internal/store"
)

// scriptHarness writes the files write returns for each request into its workspace.
type scriptHarness struct {
	mu    sync.Mutex
	dirs  []string
	setup []bool // whether the profile setup had run in the workspace
	write func(req model.AgentRequest) map[string]string
}

func (*scriptHarness) Name() string { return "script" }
func (h *scriptHarness) Start(_ context.Context, req model.AgentRequest) (model.AgentSession, error) {
	h.mu.Lock()
	h.dirs = append(h.dirs, req.WorkspaceDir)
	_, err := os.Stat(filepath.Join(req.WorkspaceDir, "setup-ran"))
	h.setup = append(h.setup, err == nil)
	h.mu.Unlock()
	for name, content := range h.write(req) {
		if err := os.WriteFile(filepath.Join(req.WorkspaceDir, name), []byte(content), 0o644); err != nil {
			return nil, err
		}
	}
	return fakeSession{}, nil
}
func (*scriptHarness) Cancel(context.Context, string) error { return nil }
func (*scriptHarness) Result(context.Context, string) (model.AgentResult, error) {
	return model.AgentResult{Text: "done"}, nil
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func worktreeFactory(t *testing.T, h Harness, tasks ...string) (*Factory, *model.WorkOrder) {
	t.Helper()
	f, wo, wos := monitorFactory(t, h)
	var plan []model.Task
	for _, id := range tasks {
		plan = append(plan, model.Task{ID: id, WOID: wo.ID, Title: id, Role: "builder"})
	}
	if err := wos.ReplacePlan(context.Background(), wo.ID, plan); err != nil {
		t.Fatal(err)
	}
	db := f.Runs.(*store.RunRepo).DB
	f.Proj = &store.ProjectRepo{DB: db}
	if err := f.Proj.Create(context.Background(), &model.Project{ID: "p2", Name: "p2",
		QualityProfile: `{"setup":"touch setup-ran","checks":[]}`}); err != nil {
		t.Fatal(err)
	}
	wo.ProjectID = "p2"
	f.DataDir = t.TempDir()
	f.Sandbox = sandbox.Local{DataDir: f.DataDir}
	src := t.TempDir()
	gitRun(t, src, "init", "--quiet")
	for name, content := range map[string]string{"shared.txt": "base\n", ".gitignore": "setup-ran\n"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, src, "add", "-A")
	gitRun(t, src, "commit", "--quiet", "-m", "base")
	if _, err := f.Sandbox.Prepare(context.Background(), wo.ID, src, nil); err != nil {
		t.Fatal(err)
	}
	return f, wo
}

func runKinds(t *testing.T, f *Factory, woID string) map[string][]string {
	t.Helper()
	evs, err := f.WO.(*store.WorkOrderRepo).Events(context.Background(), woID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, e := range evs {
		if id, ok := e.Payload["run_id"].(string); ok && (e.Kind == "agent_completed" || e.Kind == "agent_failed") {
			out[id] = append(out[id], e.Kind)
		}
	}
	return out
}

func taskOf(req model.AgentRequest) string { return filepath.Base(req.WorkspaceDir) }

func TestParallelTasksRunInOwnWorktreesAndMerge(t *testing.T) {
	h := &scriptHarness{write: func(req model.AgentRequest) map[string]string {
		return map[string]string{taskOf(req) + ".txt": taskOf(req) + "\n"}
	}}
	f, wo := worktreeFactory(t, h, "T1", "T2")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []string{"T1", "T2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = f.runBuilder(context.Background(), wo, model.Job{TaskID: id}, "p", "m")
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	woDir := f.workspaceDir(wo.ID)
	for i, d := range h.dirs {
		if d == woDir || !h.setup[i] {
			t.Fatalf("a task ran in the shared workspace or before setup: %v %v", h.dirs, h.setup)
		}
	}
	for _, id := range []string{"T1", "T2"} {
		if _, err := os.Stat(filepath.Join(woDir, id+".txt")); err != nil {
			t.Fatalf("%s not merged: %v", id, err)
		}
		if _, err := os.Stat(f.taskDir(wo.ID, id)); !os.IsNotExist(err) {
			t.Fatalf("worktree of %s not removed: %v", id, err)
		}
	}
	done, err := f.Runs.CompletedBuilderTasks(context.Background(), wo.ID)
	if err != nil || !done["T1"] || !done["T2"] {
		t.Fatalf("done = %v, err = %v", done, err)
	}
	runs, err := f.Runs.(*store.RunRepo).List(context.Background(), wo.ID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %v, err = %v", runs, err)
	}
	kinds := runKinds(t, f, wo.ID)
	for _, r := range runs {
		if got := kinds[r.ID]; len(got) != 1 || got[0] != "agent_completed" {
			t.Fatalf("run %s events = %v; want one agent_completed", r.ID, got)
		}
	}
}

func TestConflictingTaskIsResolvedByBuilder(t *testing.T) {
	cases := []struct {
		name     string
		resolved string
		wantErr  bool
	}{
		{"resolved", "T1\nT2\n", false},
		{"markers left", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			h := &scriptHarness{write: func(req model.AgentRequest) map[string]string {
				if taskOf(req) == "T2" && !strings.Contains(req.Prompt, "conflict in") {
					close(started)
					<-release // T2 began from the base, before T1 merged
				}
				if strings.Contains(req.Prompt, "conflict in") {
					if tc.resolved == "" {
						return nil
					}
					return map[string]string{"shared.txt": tc.resolved}
				}
				return map[string]string{"shared.txt": taskOf(req) + "\n"}
			}}
			f, wo := worktreeFactory(t, h, "T1", "T2")
			ctx := context.Background()
			var err error
			t2 := make(chan struct{})
			go func() {
				defer close(t2)
				_, _, err = f.runBuilder(ctx, wo, model.Job{TaskID: "T2"}, "p", "m")
			}()
			<-started
			if _, _, err := f.runBuilder(ctx, wo, model.Job{TaskID: "T1"}, "p", "m"); err != nil {
				t.Fatal(err)
			}
			close(release)
			<-t2
			done, _ := f.Runs.CompletedBuilderTasks(ctx, wo.ID)
			got, _ := os.ReadFile(filepath.Join(f.workspaceDir(wo.ID), "shared.txt"))
			if tc.wantErr {
				if !errors.Is(err, model.ErrPermanent) || done["T2"] || string(got) != "T1\n" {
					t.Fatalf("err = %v, done = %v, shared = %q", err, done, got)
				}
				if _, serr := os.Stat(f.taskDir(wo.ID, "T2")); serr != nil {
					t.Fatalf("failed worktree should be kept: %v", serr)
				}
				runs, _ := f.Runs.(*store.RunRepo).List(ctx, wo.ID)
				kinds := runKinds(t, f, wo.ID)
				var failed int
				for _, r := range runs {
					if r.State != "failed" {
						continue
					}
					failed++
					if got := kinds[r.ID]; len(got) != 1 || got[0] != "agent_failed" {
						t.Fatalf("failed run %s events = %v; want only agent_failed", r.ID, got)
					}
				}
				if failed != 2 {
					t.Fatalf("failed runs = %d, want 2 (task and conflict resolution)", failed)
				}
				return
			}
			if err != nil || !done["T2"] || string(got) != tc.resolved {
				t.Fatalf("err = %v, done = %v, shared = %q", err, done, got)
			}
		})
	}
}
