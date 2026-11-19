package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"legos-factory/backend/internal/model"
)

type rig struct{ folder, remote string }

// newRig is a bare remote with main and dev, and the user's folder: a clone checked out on a local
// branch "wip" that is behind main and holds an unpushed commit. The user's global git config is isolated.
func newRig(t *testing.T) rig {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitIn(t, root, "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(root, "seed")
	gitIn(t, root, "clone", "-q", remote, seed)
	gitIn(t, seed, "config", "user.email", "t@t")
	gitIn(t, seed, "config", "user.name", "t")
	gitIn(t, seed, "checkout", "-q", "-b", "main")
	gitIn(t, seed, "commit", "-q", "--allow-empty", "-m", "one")
	gitIn(t, seed, "push", "-q", "origin", "main")
	folder := filepath.Join(root, "folder")
	gitIn(t, root, "clone", "-q", remote, folder)
	gitIn(t, folder, "config", "user.email", "me@example.com")
	gitIn(t, folder, "config", "user.name", "Me")
	gitIn(t, folder, "checkout", "-q", "-b", "wip")
	gitIn(t, folder, "commit", "-q", "--allow-empty", "-m", "local only")
	gitIn(t, seed, "commit", "-q", "--allow-empty", "-m", "two")
	gitIn(t, seed, "push", "-q", "origin", "main")
	gitIn(t, seed, "checkout", "-q", "-b", "dev")
	gitIn(t, seed, "commit", "-q", "--allow-empty", "-m", "dev work")
	gitIn(t, seed, "push", "-q", "origin", "dev")
	return rig{folder, remote}
}

func subject(t *testing.T, dir string) string { return gitIn(t, dir, "log", "-1", "--format=%s") }

func TestPrepareStartsFromFetchedRemoteDefaultAndLeavesTheFolderAlone(t *testing.T) {
	r := newRig(t)
	l := Local{DataDir: t.TempDir()}
	dir, base, err := l.Prepare(context.Background(), "p1", "wo_1", r.folder, "")
	if err != nil {
		t.Fatal(err)
	}
	if base != "main" || dir != model.WorktreeDir(l.DataDir, "p1", "wo_1") {
		t.Fatalf("base=%q dir=%q", base, dir)
	}
	if got := subject(t, dir); got != "two" {
		t.Fatalf("worktree starts at %q, want the latest remote main", got)
	}
	if got := gitIn(t, dir, "symbolic-ref", "--short", "HEAD"); got != "legos/wo_1" {
		t.Fatalf("branch = %q", got)
	}
	if got := gitIn(t, r.folder, "symbolic-ref", "--short", "HEAD"); got != "wip" || subject(t, r.folder) != "local only" {
		t.Fatalf("the user's folder changed: %q", got)
	}
	if subject(t, filepath.Join(r.folder)) != "local only" || gitIn(t, r.folder, "log", "-1", "--format=%s", "main") != "one" {
		t.Fatal("the user's local main must stay where it was")
	}
}

func TestPrepareBaseBranchPrefersRemoteThenLocal(t *testing.T) {
	r := newRig(t)
	l := Local{DataDir: t.TempDir()}
	dir, base, err := l.Prepare(context.Background(), "p1", "wo_dev", r.folder, "dev")
	if err != nil || base != "dev" || subject(t, dir) != "dev work" {
		t.Fatalf("remote base: base=%q err=%v subject=%q", base, err, subject(t, dir))
	}
	gitIn(t, r.folder, "branch", "localonly")
	if _, _, err := l.Prepare(context.Background(), "p1", "wo_local", r.folder, "localonly"); err != nil {
		t.Fatalf("a base that exists only locally is used: %v", err)
	}
	if _, _, err := l.Prepare(context.Background(), "p1", "wo_none", r.folder, "nope"); err == nil {
		t.Fatal("an unknown base must fail")
	}
	if _, statErr := os.Stat(model.WorktreeDir(l.DataDir, "p1", "wo_none")); statErr == nil {
		t.Fatal("a failed prepare leaves no worktree")
	}
}

func TestPrepareWithoutRemoteUsesTheLocalDefaultBranch(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "config", "user.email", "t@t")
	gitIn(t, repo, "config", "user.name", "t")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	l := Local{DataDir: t.TempDir()}
	dir, base, err := l.Prepare(context.Background(), "p1", "wo_1", repo, "")
	if err != nil || base != "main" || subject(t, dir) != "base" {
		t.Fatalf("base=%q err=%v", base, err)
	}
}

func TestPrepareIsIdempotentAndNeverRecreatesOverWork(t *testing.T) {
	r := newRig(t)
	l := Local{DataDir: t.TempDir()}
	ctx := context.Background()
	dir, _, err := l.Prepare(ctx, "p1", "wo_1", r.folder, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wip.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Prepare(ctx, "p1", "wo_1", r.folder, ""); err != nil {
		t.Fatalf("a retry reuses the worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wip.txt")); err != nil {
		t.Fatal("a retry must not recreate over a dirty worktree")
	}
	sha, err := l.Commit(ctx, dir, "work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil { // folder lost, branch and registration remain
		t.Fatal(err)
	}
	dir, _, err = l.Prepare(ctx, "p1", "wo_1", r.folder, "")
	if err != nil {
		t.Fatalf("prepare after the folder vanished: %v", err)
	}
	if got := gitIn(t, dir, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("the earlier attempt's commits must survive: %s != %s", got, sha)
	}
}

func TestCleanupRemovesOnlyThisWorkOrdersWorktreesAndBranchesAndIsIdempotent(t *testing.T) {
	r := newRig(t)
	l := Local{DataDir: t.TempDir()}
	ctx := context.Background()
	dir, _, err := l.Prepare(ctx, "p1", "wo_1", r.folder, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Prepare(ctx, "p1", "wo_2", r.folder, ""); err != nil {
		t.Fatal(err)
	}
	taskDir := dir + ".tasks/t1"
	if ok, err := l.TaskWorktree(ctx, dir, taskDir, "legos/wo_1.tasks/t1"); err != nil || !ok {
		t.Fatalf("task worktree: %v %v", ok, err)
	}
	for i := 0; i < 2; i++ {
		if _, err := l.Cleanup(ctx, "p1", "wo_1", r.folder, true); err != nil {
			t.Fatalf("cleanup #%d: %v", i+1, err)
		}
	}
	for _, p := range []string{dir, dir + ".tasks"} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s must be gone", p)
		}
	}
	branches := gitIn(t, r.folder, "branch", "--format=%(refname:short)")
	if strings.Contains(branches, "legos/wo_1") || !strings.Contains(branches, "legos/wo_2") || !strings.Contains(branches, "wip") {
		t.Fatalf("branches after cleanup:\n%s", branches)
	}
	if _, err := l.Cleanup(ctx, "p1", "wo_never", r.folder, false); err != nil {
		t.Fatalf("cleanup of an unknown order: %v", err)
	}
	if _, err := l.Cleanup(ctx, "p1", "wo_2", filepath.Join(t.TempDir(), "missing"), false); err != nil {
		t.Fatalf("cleanup with the repo folder gone: %v", err)
	}
}

func TestParallelTaskWorktreesDoNotRace(t *testing.T) {
	r := newRig(t)
	l := Local{DataDir: t.TempDir()}
	ctx := context.Background()
	dir, _, err := l.Prepare(ctx, "p1", "wo_1", r.folder, "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := l.TaskWorktree(ctx, dir, fmt.Sprintf("%s.tasks/t%d", dir, i), fmt.Sprintf("legos/wo_1.tasks/t%d", i))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDirtyReportsUncommittedChanges(t *testing.T) {
	r := newRig(t)
	l := Local{}
	if l.Dirty(context.Background(), r.folder) || l.Dirty(context.Background(), "") {
		t.Fatal("a clean folder is not dirty")
	}
	if err := os.WriteFile(filepath.Join(r.folder, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !l.Dirty(context.Background(), r.folder) {
		t.Fatal("an untracked file is a change")
	}
}

func TestCommitUsesRepoIdentityAndHooksAndReportsRejections(t *testing.T) {
	r := newRig(t)
	l := Local{DataDir: t.TempDir()}
	ctx := context.Background()
	dir, _, err := l.Prepare(ctx, "p1", "wo_1", r.folder, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Commit(ctx, dir, "first"); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, dir, "log", "-1", "--format=%an <%ae>"); got != "Me <me@example.com>" {
		t.Fatalf("commit identity = %q, want the repo's own", got)
	}

	hook := filepath.Join(r.folder, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'lint: trailing whitespace in b.txt' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = l.Commit(ctx, dir, "second")
	var he *model.HookError
	if !errors.As(err, &he) || !strings.Contains(he.Output, "trailing whitespace in b.txt") || he.Op != "commit" {
		t.Fatalf("a rejecting hook must be a HookError carrying its output, got %v", err)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Commit(ctx, dir, "second"); err != nil {
		t.Fatalf("the retry after the fix: %v", err)
	}
}

func TestCommitWithoutAnyIdentityFailsClearly(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Local{}.Commit(context.Background(), repo, "x")
	if !errors.Is(err, model.ErrNoGitIdentity) || !strings.Contains(err.Error(), "user.name") {
		t.Fatalf("got %v", err)
	}
}

func TestCommitSigningFailureIsNotAHookFailure(t *testing.T) {
	r := newRig(t)
	l := Local{DataDir: t.TempDir()}
	ctx := context.Background()
	dir, _, err := l.Prepare(ctx, "p1", "wo_1", r.folder, "")
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, r.folder, "config", "commit.gpgsign", "true")
	gitIn(t, r.folder, "config", "gpg.format", "ssh")
	gitIn(t, r.folder, "config", "user.signingkey", filepath.Join(t.TempDir(), "no-such-key.pub"))
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = l.Commit(ctx, dir, "signed")
	var se *model.SigningError
	var he *model.HookError
	if !errors.As(err, &se) || errors.As(err, &he) || !errors.Is(err, model.ErrPermanent) {
		t.Fatalf("a signing failure must be a permanent SigningError, got %T %v", err, err)
	}
}

func TestMergeTaskReportsHookRejection(t *testing.T) {
	r := newRig(t)
	l := Local{DataDir: t.TempDir()}
	ctx := context.Background()
	dir, _, err := l.Prepare(ctx, "p1", "wo_1", r.folder, "")
	if err != nil {
		t.Fatal(err)
	}
	taskDir := dir + ".tasks/t1"
	if _, err := l.TaskWorktree(ctx, dir, taskDir, "legos/wo_1.tasks/t1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "t.txt"), []byte("t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Commit(ctx, taskDir, "task"); err != nil {
		t.Fatal(err)
	}
	// the work order moves on, so the merge needs a merge commit
	if err := os.WriteFile(filepath.Join(dir, "o.txt"), []byte("o\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Commit(ctx, dir, "other"); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(r.folder, ".git", "hooks", "commit-msg")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'message must start with feat:' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	conflict, err := l.MergeTask(ctx, dir, "legos/wo_1.tasks/t1", "merge")
	var he *model.HookError
	if conflict || !errors.As(err, &he) || !strings.Contains(he.Output, "must start with feat") {
		t.Fatalf("conflict=%v err=%v", conflict, err)
	}
	if st := gitIn(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("a rejected merge must leave the worktree clean:\n%s", st)
	}
}

func TestCleanupKeepsUnpublishedWorkUnlessThePRIsDoneOrOriginHasIt(t *testing.T) {
	r := newRig(t)
	l := Local{DataDir: t.TempDir()}
	ctx := context.Background()
	branches := func() string { return gitIn(t, r.folder, "branch", "--format=%(refname:short)") }

	// no commits beyond the base: nothing to lose
	if _, _, err := l.Prepare(ctx, "p1", "wo_empty", r.folder, ""); err != nil {
		t.Fatal(err)
	}
	if kept, err := l.Cleanup(ctx, "p1", "wo_empty", r.folder, false); err != nil || len(kept) != 0 || strings.Contains(branches(), "legos/wo_empty") {
		t.Fatalf("an untouched branch goes: kept=%v err=%v\n%s", kept, err, branches())
	}

	// local commits, nothing published: kept
	dir, _, err := l.Prepare(ctx, "p1", "wo_work", r.folder, "")
	if err != nil {
		t.Fatal(err)
	}
	taskDir := dir + ".tasks/t1"
	if _, err := l.TaskWorktree(ctx, dir, taskDir, "legos/wo_work.tasks/t1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "t.txt"), []byte("t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Commit(ctx, taskDir, "chore(legos): task"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.MergeTask(ctx, dir, "legos/wo_work.tasks/t1", "chore(legos): merge t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.TaskWorktree(ctx, dir, dir+".tasks/t2", "legos/wo_work.tasks/t2"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir+".tasks/t2", "u.txt"), []byte("u\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Commit(ctx, dir+".tasks/t2", "chore(legos): task 2"); err != nil {
		t.Fatal(err)
	}
	kept, err := l.Cleanup(ctx, "p1", "wo_work", r.folder, false)
	if err != nil || len(kept) != 2 {
		t.Fatalf("the order branch and its unmerged task branch stay: kept=%v err=%v", kept, err)
	}
	got := branches()
	if !strings.Contains(got, "legos/wo_work\n") && !strings.HasSuffix(got, "legos/wo_work") || strings.Contains(got, "wo_work.tasks/t1") || !strings.Contains(got, "wo_work.tasks/t2") {
		t.Fatalf("merged task branch goes, unmerged one stays:\n%s", got)
	}

	// once origin has the tip, the branch can go
	dir, _, err = l.Prepare(ctx, "p1", "wo_work", r.folder, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.PushBranch(ctx, dir, "legos/wo_work", nil); err != nil {
		t.Fatal(err)
	}
	if kept, err := l.Cleanup(ctx, "p1", "wo_work", r.folder, false); err != nil || len(kept) != 1 || kept[0] != "legos/wo_work.tasks/t2" {
		t.Fatalf("published: only the unmerged task branch stays: kept=%v err=%v", kept, err)
	}
	if strings.Contains(branches(), "legos/wo_work\n") {
		t.Fatalf("the published branch should be gone:\n%s", branches())
	}
}

func TestPrepareAndCleanupLeaveTheUsersOwnWorktreesAlone(t *testing.T) {
	r := newRig(t)
	l := Local{DataDir: t.TempDir()}
	ctx := context.Background()
	own := filepath.Join(t.TempDir(), "mine")
	gitIn(t, r.folder, "worktree", "add", "-q", "-b", "mine", own)
	if err := os.RemoveAll(own); err != nil { // temporarily missing, e.g. an unmounted disk
		t.Fatal(err)
	}
	if _, _, err := l.Prepare(ctx, "p1", "wo_1", r.folder, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Cleanup(ctx, "p1", "wo_1", r.folder, true); err != nil {
		t.Fatal(err)
	}
	if out := gitIn(t, r.folder, "worktree", "list", "--porcelain"); !strings.Contains(out, "mine") {
		t.Fatalf("the user's worktree registration was pruned:\n%s", out)
	}
}

func TestDefaultBranchNeverFallsBackToTheCheckedOutBranch(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "trunk")
	gitIn(t, repo, "config", "user.email", "t@t")
	gitIn(t, repo, "config", "user.name", "t")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	l := Local{DataDir: t.TempDir()}
	if _, _, err := l.Prepare(context.Background(), "p1", "wo_1", repo, ""); err == nil || !strings.Contains(err.Error(), "base branch") {
		t.Fatalf("want an error asking for a base branch, got %v", err)
	}
	if _, base, err := l.Prepare(context.Background(), "p1", "wo_2", repo, "trunk"); err != nil || base != "trunk" {
		t.Fatalf("an explicit base works: %q %v", base, err)
	}
}

func TestRejectionOnlyBlamesHooksThatExist(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	hooks := filepath.Join(r.folder, ".git", "hooks")
	plain := "error: Your local changes to the following files would be overwritten by merge:\n\ta.txt"

	if err := rejection(ctx, r.folder, "commit", "boom"); !isPlain(err) {
		t.Fatalf("no hook installed: a plain error, got %T %v", err, err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "commit-msg"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var he *model.HookError
	if err := rejection(ctx, r.folder, "commit", "subject must be conventional"); !errors.As(err, &he) || he.Hook != "commit-msg" {
		t.Fatalf("an installed commit-msg hook: %T %v", err, err)
	}
	if err := rejection(ctx, r.folder, "merge", plain); !isPlain(err) {
		t.Fatalf("git's own failure is not a hook's even when hooks exist: %T %v", err, err)
	}
	if err := rejection(ctx, r.folder, "commit", "fatal: Unable to create '.git/index.lock': File exists."); !isPlain(err) {
		t.Fatalf("index.lock is plain: %T %v", err, err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := rejection(ctx, r.folder, "commit", "lint failed"); !errors.As(err, &he) || he.Hook != "" {
		t.Fatalf("two hooks could have rejected it, so none is named: %T %v", err, err)
	}
}

func isPlain(err error) bool {
	var he *model.HookError
	var se *model.SigningError
	return err != nil && !errors.As(err, &he) && !errors.As(err, &se)
}

func TestAuthHeaderIsScopedToTheRemote(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	gitIn(t, r.folder, "remote", "set-url", "origin", "https://user:pw@git.example.com/acme/app.git")
	if got := authScope(ctx, r.folder, []string{"push", "--quiet", "origin", "HEAD:refs/heads/x"}); got != "https://git.example.com/acme/app.git" {
		t.Fatalf("scope = %q", got)
	}
	gitIn(t, r.folder, "remote", "set-url", "origin", "git@git.example.com:acme/app.git")
	if got := authScope(ctx, r.folder, []string{"fetch", "origin"}); got != "" {
		t.Fatalf("an ssh remote gets no header, got %q", got)
	}
	if got := authScope(ctx, "", []string{"clone", "--quiet", "https://h.example/x.git", "/tmp/y"}); got != "https://h.example/x.git" {
		t.Fatalf("clone scope = %q", got)
	}
}
