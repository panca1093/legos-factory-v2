package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"legos-factory/backend/internal/model"
)

// Local prepares, checks, commits and (only via PushBranch) publishes
// worktrees of the user's repository on this host.
type Local struct{ DataDir string }

var repoLocks sync.Map // common git dir → *sync.Mutex

// lockRepo serializes worktree and branch changes on one repository: parallel tasks of a work
// order would otherwise race on .git/worktrees and ref locks. dir is any worktree of it.
func lockRepo(ctx context.Context, dir string) func() {
	key := dir
	if out, err := git(ctx, dir, "rev-parse", "--git-common-dir"); err == nil {
		key = strings.TrimSpace(out)
		if !filepath.IsAbs(key) {
			key = filepath.Join(dir, key)
		}
		if real, err := filepath.EvalSymlinks(key); err == nil {
			key = real
		}
	}
	mu, _ := repoLocks.LoadOrStore(key, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

func woBranch(woID string) string { return "legos/" + woID }

func baseConfigKey(branch string) string { return "branch." + branch + ".legosbase" }

func hasOrigin(ctx context.Context, repo string) bool {
	_, err := git(ctx, repo, "remote", "get-url", "origin")
	return err == nil
}

func refExists(ctx context.Context, repo, ref string) bool {
	_, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// defaultBranch is origin's default branch (origin/HEAD, then main or master on origin), else the
// local main or master. It never falls back to whatever the user has checked out.
func defaultBranch(ctx context.Context, repo string, origin bool) (string, error) {
	if origin {
		if out, err := git(ctx, repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
			return strings.TrimPrefix(strings.TrimSpace(out), "origin/"), nil
		}
		if out, err := gitAuthed(ctx, repo, nil, "ls-remote", "--symref", "origin", "HEAD"); err == nil {
			for _, line := range strings.Split(out, "\n") {
				if rest, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
					if name, _, _ := strings.Cut(rest, "\t"); name != "" {
						return name, nil
					}
				}
			}
		}
		for _, b := range []string{"main", "master"} {
			if refExists(ctx, repo, "refs/remotes/origin/"+b) {
				return b, nil
			}
		}
	}
	for _, b := range []string{"main", "master"} {
		if refExists(ctx, repo, "refs/heads/"+b) {
			return b, nil
		}
	}
	return "", fmt.Errorf("cannot determine the default branch of %s (no origin/HEAD, main or master): name a base branch when creating the work order", repo)
}

// Dirty reports uncommitted changes (tracked or untracked) in the project folder.
func (Local) Dirty(ctx context.Context, repo string) bool {
	if repo == "" || !isLocalGitRepo(repo) {
		return false
	}
	out, err := git(ctx, repo, "status", "--porcelain")
	return err == nil && strings.TrimSpace(out) != ""
}

// Prepare gives a work order its worktree of repo on branch legos/<wo>, started from the fetched
// origin/<base> (the local base branch without a remote). base is baseBranch, else the default
// branch, and is returned for the pull request. The user's branches and working tree are never
// touched. A retry reuses the existing worktree and its commits. An empty repo gives an empty folder.
func (l Local) Prepare(ctx context.Context, projectID, woID, repo, baseBranch string) (dir, base string, err error) {
	dir = model.WorktreeDir(l.DataDir, projectID, woID)
	if repo == "" {
		return dir, "", os.MkdirAll(dir, 0o755)
	}
	if !isLocalGitRepo(repo) {
		return "", "", fmt.Errorf("project folder %s is not a git repository", repo)
	}
	defer lockRepo(ctx, repo)()
	origin := hasOrigin(ctx, repo)
	if base = baseBranch; base == "" {
		if base, err = defaultBranch(ctx, repo, origin); err != nil {
			return "", "", err
		}
	}
	start := ""
	if origin {
		_, _ = gitAuthed(ctx, repo, nil, "fetch", "--quiet", "origin", "+refs/heads/"+base+":refs/remotes/origin/"+base)
		if refExists(ctx, repo, "refs/remotes/origin/"+base) {
			start = "refs/remotes/origin/" + base
		}
	}
	if start == "" && refExists(ctx, repo, "refs/heads/"+base) {
		start = "refs/heads/" + base
	}
	if start == "" {
		return "", "", fmt.Errorf("base branch %q was not found on origin or in the project folder", base)
	}
	branch := woBranch(woID)
	if worktreeOnBranch(ctx, repo, dir, branch) {
		return dir, base, nil
	}
	_, _ = git(ctx, repo, "worktree", "remove", "--force", dir)
	if err := os.RemoveAll(dir); err != nil {
		return "", "", err
	}
	forgetWorktree(repo, dir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", "", err
	}
	args := []string{"worktree", "add", "--quiet", "-b", branch, dir, start}
	if refExists(ctx, repo, "refs/heads/"+branch) {
		args = []string{"worktree", "add", "--quiet", dir, branch} // an earlier attempt's commits stay
	}
	if out, err := git(ctx, repo, args...); err != nil {
		_ = os.RemoveAll(dir)
		forgetWorktree(repo, dir)
		return "", "", fmt.Errorf("git worktree add: %v: %s", err, strings.TrimSpace(out))
	}
	if sha, err := git(ctx, repo, "rev-parse", start); err == nil {
		_, _ = git(ctx, repo, "config", baseConfigKey(branch), strings.TrimSpace(sha))
	}
	return dir, base, nil
}

// worktreeOnBranch reports whether dir is a live worktree of repo with branch checked out.
func worktreeOnBranch(ctx context.Context, repo, dir, branch string) bool {
	if !isLocalGitRepo(dir) {
		return false
	}
	out, err := git(ctx, dir, "symbolic-ref", "--short", "HEAD")
	if err != nil || strings.TrimSpace(out) != branch {
		return false
	}
	a, err1 := git(ctx, dir, "rev-parse", "--git-common-dir")
	b, err2 := git(ctx, repo, "rev-parse", "--git-common-dir")
	if err1 != nil || err2 != nil {
		return false
	}
	ra, rb := strings.TrimSpace(a), strings.TrimSpace(b)
	if !filepath.IsAbs(ra) {
		ra = filepath.Join(dir, ra)
	}
	if !filepath.IsAbs(rb) {
		rb = filepath.Join(repo, rb)
	}
	ea, e1 := filepath.EvalSymlinks(ra)
	eb, e2 := filepath.EvalSymlinks(rb)
	return e1 == nil && e2 == nil && ea == eb
}

// Cleanup removes a work order's worktree and its task worktrees, then its legos/ branches. It is
// idempotent and touches no other branch. The work order's own branch is the only copy of unpublished
// work, so it is deleted only when force is set (the PR is merged or closed) or origin has its tip;
// otherwise it is kept and returned in kept. Task branches go once merged into it.
func (l Local) Cleanup(ctx context.Context, projectID, woID, repo string, force bool) (kept []string, err error) {
	dir := model.WorktreeDir(l.DataDir, projectID, woID)
	tasks := dir + ".tasks"
	root := filepath.Join(l.DataDir, "worktrees", projectID)
	if !isLocalGitRepo(repo) {
		return nil, removeUnder(root, dir, tasks)
	}
	defer lockRepo(ctx, repo)()
	if entries, err := os.ReadDir(tasks); err == nil {
		for _, e := range entries {
			p := filepath.Join(tasks, e.Name())
			_, _ = git(ctx, repo, "worktree", "remove", "--force", p)
			forgetWorktree(repo, p)
		}
	}
	_, _ = git(ctx, repo, "worktree", "remove", "--force", dir)
	if err := removeUnder(root, dir, tasks); err != nil {
		return nil, err
	}
	forgetWorktree(repo, dir)
	wob := woBranch(woID)
	wobRef := "refs/heads/" + wob
	wobAlive := refExists(ctx, repo, wobRef)
	if out, err := git(ctx, repo, "for-each-ref", "--format=%(refname:short)", wobRef+".tasks"); err == nil {
		for _, b := range strings.Fields(out) {
			if force || (wobAlive && isAncestor(ctx, repo, "refs/heads/"+b, wobRef)) {
				_, _ = git(ctx, repo, "branch", "-D", b)
			} else {
				kept = append(kept, b)
			}
		}
	}
	if wobAlive {
		if force || isAncestor(ctx, repo, wobRef, "refs/remotes/origin/"+wob) {
			_, _ = git(ctx, repo, "branch", "-D", wob)
		} else if _, err := git(ctx, repo, "branch", "-d", wob); err != nil {
			kept = append(kept, wob)
		}
	}
	return kept, nil
}

func isAncestor(ctx context.Context, repo, a, b string) bool {
	_, err := git(ctx, repo, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// forgetWorktree drops the repository's record of a worktree folder that is gone, leaving the user's own
// worktrees alone (a broad `git worktree prune` would drop any whose folder is temporarily missing).
func forgetWorktree(repo, path string) {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return
	}
	admin := filepath.Join(strings.TrimSpace(string(out)), "worktrees")
	entries, err := os.ReadDir(admin)
	if err != nil {
		return
	}
	want := map[string]bool{filepath.Join(path, ".git"): true}
	if real, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		want[filepath.Join(real, filepath.Base(path), ".git")] = true
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(admin, e.Name(), "gitdir"))
		if err == nil && want[filepath.Clean(strings.TrimSpace(string(b)))] {
			if _, serr := os.Stat(strings.TrimSpace(string(b))); serr != nil {
				_ = os.RemoveAll(filepath.Join(admin, e.Name()))
			}
		}
	}
}

func removeUnder(root string, paths ...string) error {
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("refusing to remove %q outside %q", p, root)
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

// gitSigned runs a commit-making git command with the repository's own signing config. It can
// never prompt: no terminal, no stdin, and git and ssh are told not to ask.
func gitSigned(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "SSH_ASKPASS_REQUIRE=never", "GPG_TTY=")
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	return string(out), err
}

var signingFailure = []string{"gpg failed to sign", "failed to write commit object", "signing failed", "ssh-keygen", "No secret key", "error: Load key"}

// plainGitFailures are git errors no hook can have caused, whatever hooks exist.
var plainGitFailures = []string{"would be overwritten", "index.lock", "unrelated histories", "not something we can merge", "Please commit your changes",
	"Unable to create", "cannot lock ref", "not a git repository", "Your local changes", "untracked working tree files"}

var hooksByOp = map[string][]string{
	"commit": {"pre-commit", "prepare-commit-msg", "commit-msg"},
	"merge":  {"pre-merge-commit", "prepare-commit-msg", "commit-msg"},
}

// installedHooks lists the hooks git may have run for op: files in the repository's effective hooks
// directory (core.hooksPath respected) that are executable.
func installedHooks(ctx context.Context, dir, op string) []string {
	out, err := git(ctx, dir, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return nil
	}
	hooks := strings.TrimSpace(out)
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(dir, hooks)
	}
	var found []string
	for _, name := range hooksByOp[op] {
		if fi, err := os.Stat(filepath.Join(hooks, name)); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			found = append(found, name)
		}
	}
	return found
}

// rejection classifies a failed commit or merge. A signing failure is the user's to fix; a
// *model.HookError needs a hook that is installed and git output that no plain git failure explains;
// every other failure is a plain error.
func rejection(ctx context.Context, dir, op, out string) error {
	out = strings.TrimSpace(out)
	for _, m := range signingFailure {
		if strings.Contains(out, m) {
			return &model.SigningError{Op: op, Output: out}
		}
	}
	for _, m := range plainGitFailures {
		if strings.Contains(out, m) {
			return fmt.Errorf("git %s failed: %s", op, out)
		}
	}
	hooks := installedHooks(ctx, dir, op)
	if len(hooks) == 0 {
		return fmt.Errorf("git %s failed: %s", op, out)
	}
	he := &model.HookError{Op: op, Output: out}
	if len(hooks) == 1 {
		he.Hook = hooks[0]
	}
	return he
}

// requireIdentity fails with ErrNoGitIdentity when git has no author for dir (repo-local config, then global).
func requireIdentity(ctx context.Context, dir string) error {
	for _, k := range []string{"user.name", "user.email"} {
		out, err := git(ctx, dir, "config", "--get", k)
		if err != nil || strings.TrimSpace(out) == "" {
			return model.ErrNoGitIdentity
		}
	}
	return nil
}
