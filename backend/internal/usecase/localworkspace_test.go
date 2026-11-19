package usecase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestCreateRecordsTheResolvedBaseBranchAndWarnsAboutAnUncommittedFolder(t *testing.T) {
	ctx := context.Background()
	f, _, wos := monitorFactory(t, nil)
	f.DataDir = t.TempDir()
	f.Proj = &store.ProjectRepo{DB: wos.DB}
	box := &cloneSandbox{dataDir: f.DataDir, dirty: true}
	w := &WorkOrders{Store: wos, Projects: &Projects{Store: f.Proj}, Sandbox: box, Settings: fixedSettings{}}
	u := &model.User{ID: "u1", Role: model.RoleAdmin}
	wo, err := w.Create(ctx, u, model.CreateWorkOrderRequest{ProjectID: "p1", Title: "t", Objective: "o", BaseBranch: "release"})
	if err != nil {
		t.Fatal(err)
	}
	if box.baseAsked != "release" || wo.BaseBranch != "main" {
		t.Fatalf("asked for %q, stored %q", box.baseAsked, wo.BaseBranch)
	}
	if !strings.Contains(wo.Warning, "uncommitted changes") {
		t.Fatalf("warning = %q", wo.Warning)
	}
	stored, err := wos.Get(ctx, wo.ID)
	if err != nil || stored.BaseBranch != "main" || stored.Warning != "" {
		t.Fatalf("the base branch is stored, the warning is not: %+v %v", stored, err)
	}
	clean := &cloneSandbox{dataDir: f.DataDir}
	w.Sandbox = clean
	wo2, err := w.Create(ctx, u, model.CreateWorkOrderRequest{ProjectID: "p1", Title: "t", Objective: "o"})
	if err != nil || wo2.Warning != "" {
		t.Fatalf("a clean folder gives no warning: %q %v", wo2.Warning, err)
	}
}

func TestProjectsRejectRemoteURLs(t *testing.T) {
	ctx := context.Background()
	f, _, wos := monitorFactory(t, nil)
	projects := &Projects{Store: &store.ProjectRepo{DB: wos.DB}, Members: &store.MemberRepo{DB: wos.DB}}
	u := &model.User{ID: "u1", Role: model.RoleAdmin}
	for _, repo := range []string{"https://github.com/acme/greeter.git", "git@gitlab.com:acme/greeter.git", "ssh://git@host/x.git", "relative/path"} {
		err := projects.Create(ctx, u, &model.Project{Name: "x", RepoURL: repo})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: want ErrInvalid, got %v", repo, err)
		}
	}
	if err := projects.Create(ctx, u, &model.Project{Name: "local", RepoURL: t.TempDir()}); err != nil {
		t.Fatalf("an absolute folder is fine: %v", err)
	}
	_ = f
}

// hookRepo makes the project's own pre-commit hook reject commits until ok.txt exists.
func hookRepo(t *testing.T, f *Factory, wo *model.WorkOrder) string {
	t.Helper()
	common := strings.TrimSpace(gitRun(t, f.workspaceDir(wo), "rev-parse", "--path-format=absolute", "--git-common-dir"))
	hook := filepath.Join(common, "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n[ -f ok.txt ] || { echo 'lint: ok.txt is required' >&2; exit 1; }\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return hook
}

func TestHookRejectionIsFedBackToTheBuilderLikeAFailedCheck(t *testing.T) {
	var fixes atomic.Int32
	var fixPrompt atomic.Value
	h := &scriptHarness{write: func(req model.AgentRequest) map[string]string {
		if strings.Contains(req.Prompt, "git hook rejected") {
			fixes.Add(1)
			fixPrompt.Store(req.Prompt)
			return map[string]string{"ok.txt": "ok\n"}
		}
		return map[string]string{"T1.txt": "T1\n"}
	}}
	f, wo := worktreeFactory(t, h, "T1")
	hookRepo(t, f, wo)
	if _, _, err := f.runBuilder(context.Background(), wo, model.Job{TaskID: "T1"}, "p", "m"); err != nil {
		t.Fatal(err)
	}
	if fixes.Load() != 1 || !strings.Contains(fmt.Sprint(fixPrompt.Load()), "lint: ok.txt is required") {
		t.Fatalf("the builder gets the hook output once: fixes=%d prompt=%v", fixes.Load(), fixPrompt.Load())
	}
	if _, err := os.Stat(filepath.Join(f.workspaceDir(wo), "T1.txt")); err != nil {
		t.Fatalf("the task was merged after the fix: %v", err)
	}
	evs, _ := f.WO.(*store.WorkOrderRepo).Events(context.Background(), wo.ID, 0, 100)
	n := 0
	for _, e := range evs {
		if e.Kind == "hook_rejected" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("hook_rejected events = %d", n)
	}
}

func TestHookRejectionGivesUpAfterTheRevisionCap(t *testing.T) {
	var runs atomic.Int32
	h := &scriptHarness{write: func(req model.AgentRequest) map[string]string {
		runs.Add(1)
		return map[string]string{"T1.txt": "T1\n"}
	}}
	f, wo := worktreeFactory(t, h, "T1")
	hookRepo(t, f, wo)
	_, _, err := f.runBuilder(context.Background(), wo, model.Job{TaskID: "T1"}, "p", "m")
	var he *model.HookError
	if !errors.Is(err, model.ErrPermanent) || !strings.Contains(err.Error(), "ok.txt is required") || errors.As(err, &he) && false {
		t.Fatalf("want a permanent error carrying the hook output, got %v", err)
	}
	if got := int(runs.Load()); got != 1+MaxRevisionCycles {
		t.Fatalf("builder runs = %d, want the task plus %d fixes", got, MaxRevisionCycles)
	}
}

type hookCommitSandbox struct {
	dirSandbox
	err error
}

func (hookCommitSandbox) RunCheck(context.Context, string, model.Check, time.Duration, []string) model.CheckResult {
	return model.CheckResult{Outcome: model.Pass}
}
func (s hookCommitSandbox) Commit(context.Context, string, string) (string, error) { return "", s.err }

func TestVerifyCommitHookFailureGoesBackToTheBuilderAndSigningFailureDoesNot(t *testing.T) {
	for name, tc := range map[string]struct {
		err         error
		wantState   model.State
		wantEnqueue bool
	}{
		"hook":    {&model.HookError{Op: "commit", Output: "eslint: no-unused-vars in a.js"}, model.StateImplementing, true},
		"signing": {&model.SigningError{Op: "commit", Output: "gpg failed to sign the data"}, model.StateVerifying, false},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, wo, wos := monitorFactory(t, nil)
			q := &fakeQueue{}
			f.Sched, f.Sandbox, f.DataDir = q, hookCommitSandbox{err: tc.err}, t.TempDir()
			if err := os.MkdirAll(f.workspaceDir(wo), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.workspaceDir(wo), "go.mod"), []byte("module x"), 0o644); err != nil {
				t.Fatal(err)
			}
			f.Proj, f.Quality, f.Findings = &store.ProjectRepo{DB: wos.DB}, &store.QualityRepo{DB: wos.DB}, &store.FindingRepo{DB: wos.DB}
			if err := wos.UpdateState(ctx, wo.ID, model.StateVerifying); err != nil {
				t.Fatal(err)
			}
			err := f.handleVerify(ctx, model.Job{WOID: wo.ID})
			got, _ := wos.Get(ctx, wo.ID)
			if got.State != tc.wantState {
				t.Fatalf("state = %s, want %s (err %v)", got.State, tc.wantState, err)
			}
			if tc.wantEnqueue {
				if err != nil || got.VerifyFailures != 1 || len(q.payloads) != 1 || !strings.Contains(fmt.Sprint(q.payloads[0]["failed_checks"]), "no-unused-vars") {
					t.Fatalf("hook output must reach the builder like a failed check: err=%v failures=%d payloads=%v", err, got.VerifyFailures, q.payloads)
				}
				return
			}
			if !errors.Is(err, model.ErrPermanent) || len(q.enqueued) != 0 {
				t.Fatalf("a signing failure is the user's to fix, not the builder's: err=%v enqueued=%v", err, q.enqueued)
			}
		})
	}
}

func TestWorktreesAreRemovedWhenAnOrderIsCancelled(t *testing.T) {
	ctx := context.Background()
	f, wos := deleteFixture(t, model.StateImplementing)
	if err := wos.UpdateFields(ctx, "wo1", map[string]any{"state": string(model.StateImplementing)}); err != nil {
		t.Fatal(err)
	}
	if err := f.Cancel(ctx, "wo1"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"wo1", "wo1.tasks/t1"} {
		if exists(wsOf(f, d)) {
			t.Fatalf("%s survived the cancel", d)
		}
	}
}
