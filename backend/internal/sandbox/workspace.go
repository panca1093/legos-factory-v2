// Package sandbox prepares isolated work-order workspaces, runs
// verification commands in them and commits verified changes locally.
package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

// Workspace is an isolated per-work-order copy of a project repository.
// Builders never touch the project source directory directly.
type Workspace struct {
	Dir string
}

// Prepare creates a workspace for a work order: a git clone for local
// repositories (shared objects, cheap), an authenticated clone for remote
// repositories when auth is given, or an empty directory when there is no
// repository. A remote repository without auth is an error.
func Prepare(ctx context.Context, dataDir, woID, repoURL string, auth *model.GitAuth) (*Workspace, error) {
	root := filepath.Join(dataDir, "workspaces", woID)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return nil, err
	}
	if repoURL != "" && auth == nil && isRemote(repoURL) {
		return nil, fmt.Errorf("repository %s needs a git connection: link the project to a connection in project settings", repoURL)
	}
	if repoURL != "" && (isLocalGitRepo(repoURL) || (auth != nil && isRemote(repoURL))) {
		if out, err := gitAuthed(ctx, "", auth, "clone", "--quiet", repoURL, root); err != nil {
			return nil, fmt.Errorf("clone workspace: %v: %s", err, out)
		}
		if auth != nil && auth.RemoteURL != "" {
			if err := retarget(ctx, root, auth); err != nil {
				_ = os.RemoveAll(root)
				return nil, err
			}
		}
		return &Workspace{Dir: root}, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Workspace{Dir: root}, nil
}

// retarget points the clone's origin at the real remote and starts the
// workspace from its latest default branch. A failed fetch falls back to the
// folder's own copy of that branch; without either, preparation fails rather
// than building on whatever branch the folder has checked out.
func retarget(ctx context.Context, dir string, auth *model.GitAuth) error {
	b := auth.DefaultBranch
	if b == "" {
		return fmt.Errorf("cannot determine the default branch of %s", auth.RemoteURL)
	}
	if out, err := git(ctx, dir, "remote", "set-url", "origin", auth.RemoteURL); err != nil {
		return fmt.Errorf("git remote set-url: %v: %s", err, out)
	}
	_, _ = gitAuthed(ctx, dir, auth, "fetch", "--quiet", "origin", "+refs/heads/"+b+":refs/remotes/origin/"+b)
	if out, err := git(ctx, dir, "checkout", "--quiet", "-B", b, "origin/"+b); err != nil {
		return fmt.Errorf("default branch %s is available neither from the remote nor in the project folder: %s", b, strings.TrimSpace(out))
	}
	_, _ = git(ctx, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+b)
	return nil
}

// isRemoteURL reports whether u names a host (scheme URL or scp-style), so a
// local filesystem path can never be a publish target.
func isRemoteURL(u string) bool {
	if strings.Contains(u, "://") {
		return true
	}
	at, colon := strings.Index(u, "@"), strings.Index(u, ":")
	return at > 0 && colon > at && !strings.Contains(u[:at], "/")
}

// Destroy removes the workspace.
func Destroy(_ context.Context, woID, dataDir string) error {
	return os.RemoveAll(filepath.Join(dataDir, "workspaces", woID))
}

func isLocalGitRepo(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}

// Local prepares, checks, commits and (only via PushBranch) publishes
// isolated workspaces on this host.
type Local struct{ DataDir string }

func (l Local) Prepare(ctx context.Context, woID, repoURL string, auth *model.GitAuth) (string, error) {
	ws, err := Prepare(ctx, l.DataDir, woID, repoURL, auth)
	if err != nil {
		return "", err
	}
	return ws.Dir, nil
}

func (l Local) Destroy(ctx context.Context, woID string) error {
	return Destroy(ctx, woID, l.DataDir)
}

var checkoutLocks sync.Map // checkout id → *sync.Mutex

// Checkout returns a read-only clone of a project at workspaces/<id> and its HEAD, cloning it like a work
// order workspace on first use. refresh brings an existing clone to the latest default branch in place.
func (l Local) Checkout(ctx context.Context, id, repoURL string, auth *model.GitAuth, refresh bool) (dir, sha string, err error) {
	mu, _ := checkoutLocks.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	dir = filepath.Join(l.DataDir, "workspaces", id)
	if !isLocalGitRepo(dir) || !ownsRepo(ctx, dir) {
		if err := os.RemoveAll(dir); err != nil {
			return "", "", err
		}
		if _, err := Prepare(ctx, l.DataDir, id, repoURL, auth); err != nil {
			return "", "", err
		}
		if !isLocalGitRepo(dir) {
			_ = os.RemoveAll(dir)
			return "", "", fmt.Errorf("project repository %q is not a git repository", repoURL)
		}
		if err := removeSymlinks(dir); err != nil {
			return "", "", err
		}
	} else if refresh {
		if err := refreshCheckout(ctx, dir, auth); err != nil {
			return "", "", err
		}
		if err := removeSymlinks(dir); err != nil {
			return "", "", err
		}
	}
	out, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("git rev-parse: %v: %s", err, out)
	}
	return dir, strings.TrimSpace(out), nil
}

// ownsRepo reports whether dir is the top of its own git repository rather than a directory git resolves to an enclosing one.
func ownsRepo(ctx context.Context, dir string) bool {
	// a symlinked checkout would make git act on the link's target, possibly the user's own folder
	if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return false
	}
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	top, err1 := filepath.EvalSymlinks(strings.TrimSpace(out))
	real, err2 := filepath.EvalSymlinks(dir)
	return err1 == nil && err2 == nil && top == real
}

// removeSymlinks deletes every symlink of a checkout, which an agent could otherwise read through to files outside it.
func removeSymlinks(dir string) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" && p == filepath.Join(dir, ".git") {
			return filepath.SkipDir
		}
		if d.Type()&os.ModeSymlink != 0 {
			return os.Remove(p)
		}
		return nil
	})
}

// refreshCheckout moves a clone to its source's head without deleting it, as other chats may be reading it.
func refreshCheckout(ctx context.Context, dir string, auth *model.GitAuth) error {
	if out, err := git(ctx, dir, "reset", "--quiet", "--hard"); err != nil {
		return fmt.Errorf("git reset: %v: %s", err, out)
	}
	if auth != nil && auth.RemoteURL != "" {
		return retarget(ctx, dir, auth)
	}
	if out, err := gitAuthed(ctx, dir, auth, "fetch", "--quiet", "origin", "HEAD"); err != nil {
		return fmt.Errorf("git fetch: %v: %s", err, redact(out, auth))
	}
	if out, err := git(ctx, dir, "reset", "--quiet", "--hard", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("git reset: %v: %s", err, out)
	}
	return nil
}

// PushBranch pushes the workspace HEAD to branch on origin and returns the
// remote default branch the change should merge into.
func (Local) PushBranch(ctx context.Context, dir, branch string, auth *model.GitAuth) (string, error) {
	if !isLocalGitRepo(dir) {
		return "", fmt.Errorf("workspace is not a git checkout; nothing to push")
	}
	target, base := "origin", ""
	if auth != nil && auth.RemoteURL != "" {
		// the workspace's origin may still be the user's folder, so push to the remote itself
		if !isRemoteURL(auth.RemoteURL) || auth.DefaultBranch == "" {
			return "", fmt.Errorf("refusing to push: %q is not a remote URL or its default branch is unknown", auth.RemoteURL)
		}
		target, base = auth.RemoteURL, auth.DefaultBranch
	} else if out, err := git(ctx, dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		base = strings.TrimPrefix(strings.TrimSpace(out), "origin/")
	} else if out, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		base = strings.TrimSpace(out)
	}
	if base == "" || base == "HEAD" {
		return "", fmt.Errorf("cannot determine the default branch of origin")
	}
	if out, err := gitAuthed(ctx, dir, auth, "push", "--quiet", target, "HEAD:refs/heads/"+branch); err != nil {
		return "", fmt.Errorf("git push: %v: %s", err, redact(out, auth))
	}
	return base, nil
}

func (Local) RunCheck(ctx context.Context, dir string, check model.Check, timeout time.Duration) model.CheckResult {
	return RunCheck(ctx, dir, check, timeout)
}

// Commit records all workspace changes as a local commit and returns its
// SHA. A non-git workspace returns "" without error; a clean tree returns
// the current HEAD.
func (Local) Commit(ctx context.Context, dir, message string) (string, error) {
	if !isLocalGitRepo(dir) {
		return "", nil
	}
	if out, err := git(ctx, dir, "add", "-A"); err != nil {
		return "", fmt.Errorf("git add: %v: %s", err, out)
	}
	status, err := git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git status: %v: %s", err, status)
	}
	if strings.TrimSpace(status) != "" {
		if out, err := git(ctx, dir, "-c", "user.name=legos-factory", "-c", "user.email=factory@localhost", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
			"commit", "--quiet", "--no-verify", "-m", message); err != nil {
			return "", fmt.Errorf("git commit: %v: %s", err, out)
		}
	}
	sha, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %v: %s", err, sha)
	}
	return strings.TrimSpace(sha), nil
}

// gitAuthed runs git with an HTTPS basic credential passed through
// GIT_CONFIG_* environment variables, so it never lands in argv, the remote
// URL or .git/config. Local remotes ignore it.
func gitAuthed(ctx context.Context, dir string, auth *model.GitAuth, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if auth != nil && auth.Password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password))
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+token)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func isRemote(repoURL string) bool {
	u, err := url.Parse(repoURL)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.User == nil
}

func redact(out string, auth *model.GitAuth) string {
	if auth != nil && auth.Password != "" {
		out = strings.ReplaceAll(out, auth.Password, "[REDACTED]")
	}
	return out
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ChangedFiles lists files the work order changed relative to the cloned
// base (committed or not). A workspace without git history reports none.
func (Local) ChangedFiles(ctx context.Context, dir string) ([]string, error) {
	if !isLocalGitRepo(dir) {
		return nil, nil
	}
	base := diffBase(ctx, dir)
	seen := map[string]bool{}
	var files []string
	for _, args := range [][]string{
		{"diff", "--name-only", base},
		{"ls-files", "--others", "--exclude-standard"},
	} {
		out, err := git(ctx, dir, args...)
		if err != nil {
			return nil, fmt.Errorf("git %s: %v: %s", args[0], err, out)
		}
		for _, f := range strings.Split(strings.TrimSpace(out), "\n") {
			if f != "" && !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	return files, nil
}

func diffBase(ctx context.Context, dir string) string {
	if out, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/HEAD"); err == nil && strings.TrimSpace(out) != "" {
		return "refs/remotes/origin/HEAD"
	}
	return "HEAD"
}

const maxUntrackedDiffBytes = 200_000

// gitOut returns stdout only, so warnings on stderr never reach a parser.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotePath=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), fmt.Errorf("%v: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
	}
	return string(out), err
}

// secretName marks files whose content must never travel in a diff.
func secretName(p string) bool { return strings.HasPrefix(filepath.Base(p), ".env") }

// Diff reports every file changed relative to the cloned base (committed or
// not) with its line counts and unified-diff hunks. Untracked regular text
// files count as fully added; symlinks, files resolving outside the workspace
// and .env* files are listed without content. A workspace without git
// history reports none.
func (Local) Diff(ctx context.Context, dir string) ([]model.FileDiff, error) {
	if !isLocalGitRepo(dir) {
		return nil, nil
	}
	base := diffBase(ctx, dir)
	numstat, err := gitOut(ctx, dir, "diff", "--no-renames", "--numstat", "-z", base)
	if err != nil {
		return nil, fmt.Errorf("git diff --numstat: %v", err)
	}
	patch, err := gitOut(ctx, dir, "diff", "--no-renames", "--no-color", "--no-ext-diff", "-U3", base)
	if err != nil {
		return nil, fmt.Errorf("git diff: %v", err)
	}
	hunks := splitPatch(patch)
	var out []model.FileDiff
	for _, rec := range strings.Split(numstat, "\x00") {
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		added, _ := strconv.Atoi(parts[0]) // "-" for binary files
		removed, _ := strconv.Atoi(parts[1])
		fd := model.FileDiff{Path: parts[2], Added: added, Removed: removed, Hunks: hunks[parts[2]]}
		if secretName(fd.Path) {
			fd.Hunks = ""
		}
		out = append(out, fd)
	}
	untracked, err := gitOut(ctx, dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %v", err)
	}
	realDir, _ := filepath.EvalSymlinks(dir)
	for _, name := range strings.Split(untracked, "\x00") {
		if name == "" {
			continue
		}
		fd := model.FileDiff{Path: name}
		full := filepath.Join(dir, name)
		if secretName(name) || !insideRegular(realDir, full) {
			out = append(out, fd)
			continue
		}
		fd.Added, fd.Hunks = readAdded(full)
		out = append(out, fd)
	}
	return out, nil
}

// insideRegular reports whether full is a regular file that really lives in realDir.
func insideRegular(realDir, full string) bool {
	if fi, err := os.Lstat(full); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(realDir, real)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// readAdded counts the lines of a new file and renders them as one added hunk
// unless the file is binary or larger than maxUntrackedDiffBytes (count only).
func readAdded(full string) (int, string) {
	f, err := os.Open(full)
	if err != nil {
		return 0, ""
	}
	defer f.Close()
	head := make([]byte, 8000)
	n, _ := f.Read(head)
	if bytes.Contains(head[:n], []byte{0}) {
		return 0, ""
	}
	if _, err := f.Seek(0, 0); err != nil {
		return 0, ""
	}
	b, err := io.ReadAll(io.LimitReader(f, maxUntrackedDiffBytes+1))
	if err != nil {
		return 0, ""
	}
	if len(b) <= maxUntrackedDiffBytes {
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if len(b) == 0 {
			return 0, ""
		}
		return len(lines), fmt.Sprintf("@@ -0,0 +1,%d @@\n+%s\n", len(lines), strings.Join(lines, "\n+"))
	}
	lines := bytes.Count(b, []byte{'\n'})
	buf := make([]byte, 32*1024)
	for {
		m, err := f.Read(buf)
		lines += bytes.Count(buf[:m], []byte{'\n'})
		if err != nil {
			break
		}
	}
	return lines, ""
}

// splitPatch maps each file path of a --no-renames unified diff to its text
// from the first @@ line on.
func splitPatch(patch string) map[string]string {
	out := map[string]string{}
	for _, section := range strings.Split("\n"+patch, "\ndiff --git ")[1:] {
		head, body, _ := strings.Cut(section, "\n")
		// head is "a/<p> b/<p>": the path has length (len(head)-5)/2
		n := (len(head) - 5) / 2
		if n <= 0 || len(head) < 2+n {
			continue
		}
		p := head[2 : 2+n]
		if i := strings.Index("\n"+body, "\n@@"); i >= 0 {
			out[p] = strings.TrimPrefix(("\n" + body)[i:], "\n")
		}
	}
	return out
}

// TaskWorktree creates a fresh git worktree at taskDir on branch, starting
// from the work order workspace HEAD. A retry replaces any earlier attempt.
// ok is false when woDir is not a git checkout and tasks must share it.
func (Local) TaskWorktree(ctx context.Context, woDir, taskDir, branch string) (bool, error) {
	if !isLocalGitRepo(woDir) {
		return false, nil
	}
	_, _ = git(ctx, woDir, "worktree", "remove", "--force", taskDir)
	if err := os.RemoveAll(taskDir); err != nil {
		return false, err
	}
	_, _ = git(ctx, woDir, "worktree", "prune")
	if err := os.MkdirAll(filepath.Dir(taskDir), 0o755); err != nil {
		return false, err
	}
	if out, err := git(ctx, woDir, "worktree", "add", "--quiet", "-B", branch, taskDir, "HEAD"); err != nil {
		return false, fmt.Errorf("git worktree add: %v: %s", err, out)
	}
	return true, nil
}

// MergeTask merges branch into the work order workspace. A conflicting merge
// is aborted, leaving the workspace untouched, and reported as conflict.
func (Local) MergeTask(ctx context.Context, woDir, branch, message string) (conflict bool, err error) {
	out, err := git(ctx, woDir, "-c", "user.name=legos-factory", "-c", "user.email=factory@localhost", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
		"merge", "--no-ff", "--no-verify", "-m", message, branch)
	if err == nil {
		return false, nil
	}
	if unmerged, _ := git(ctx, woDir, "diff", "--name-only", "--diff-filter=U"); strings.TrimSpace(unmerged) != "" {
		if aout, aerr := git(ctx, woDir, "merge", "--abort"); aerr != nil {
			return true, fmt.Errorf("git merge --abort: %v: %s", aerr, aout)
		}
		return true, nil
	}
	return false, fmt.Errorf("git merge %s: %v: %s", branch, err, out)
}

// SyncTask merges the work order workspace HEAD into the task worktree and
// leaves conflict markers in place; it returns the conflicted files.
func (Local) SyncTask(ctx context.Context, woDir, taskDir string) ([]string, error) {
	head, err := git(ctx, woDir, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git rev-parse: %v: %s", err, head)
	}
	out, err := git(ctx, taskDir, "-c", "user.name=legos-factory", "-c", "user.email=factory@localhost", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
		"merge", "--no-edit", "--no-verify", strings.TrimSpace(head))
	unmerged, uerr := git(ctx, taskDir, "diff", "--name-only", "--diff-filter=U")
	if uerr != nil {
		return nil, fmt.Errorf("git diff: %v: %s", uerr, unmerged)
	}
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(unmerged), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	if err != nil && len(files) == 0 {
		return nil, fmt.Errorf("git merge: %v: %s", err, out)
	}
	return files, nil
}

// RemoveTaskWorktree deletes a merged task's worktree and branch.
func (Local) RemoveTaskWorktree(ctx context.Context, woDir, taskDir, branch string) error {
	if out, err := git(ctx, woDir, "worktree", "remove", "--force", taskDir); err != nil {
		return fmt.Errorf("git worktree remove: %v: %s", err, out)
	}
	if out, err := git(ctx, woDir, "branch", "-D", branch); err != nil {
		return fmt.Errorf("git branch -D: %v: %s", err, out)
	}
	return nil
}
