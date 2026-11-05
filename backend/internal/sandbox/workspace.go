// Package sandbox prepares isolated work-order workspaces, runs
// verification commands in them and commits verified changes locally.
package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
		return &Workspace{Dir: root}, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Workspace{Dir: root}, nil
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

// PushBranch pushes the workspace HEAD to branch on origin and returns the
// remote default branch the change should merge into.
func (Local) PushBranch(ctx context.Context, dir, branch string, auth *model.GitAuth) (string, error) {
	if !isLocalGitRepo(dir) {
		return "", fmt.Errorf("workspace is not a git checkout; nothing to push")
	}
	base := ""
	if out, err := git(ctx, dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		base = strings.TrimPrefix(strings.TrimSpace(out), "origin/")
	} else if out, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		base = strings.TrimSpace(out)
	}
	if base == "" || base == "HEAD" {
		return "", fmt.Errorf("cannot determine the default branch of origin")
	}
	if out, err := gitAuthed(ctx, dir, auth, "push", "--quiet", "origin", "HEAD:refs/heads/"+branch); err != nil {
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
	if auth != nil {
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

// Diff reports every file changed relative to the cloned base (committed or
// not) with its line counts and unified-diff hunks. Untracked text files
// count as fully added. A workspace without git history reports none.
func (Local) Diff(ctx context.Context, dir string) ([]model.FileDiff, error) {
	if !isLocalGitRepo(dir) {
		return nil, nil
	}
	base := diffBase(ctx, dir)
	numstat, err := git(ctx, dir, "diff", "--no-renames", "--numstat", base)
	if err != nil {
		return nil, fmt.Errorf("git diff --numstat: %v: %s", err, numstat)
	}
	patch, err := git(ctx, dir, "diff", "--no-renames", "--no-color", "--no-ext-diff", "-U3", base)
	if err != nil {
		return nil, fmt.Errorf("git diff: %v: %s", err, patch)
	}
	hunks := splitPatch(patch)
	var out []model.FileDiff
	for _, line := range strings.Split(strings.TrimSpace(numstat), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		added, _ := strconv.Atoi(parts[0]) // "-" for binary files
		removed, _ := strconv.Atoi(parts[1])
		out = append(out, model.FileDiff{Path: parts[2], Added: added, Removed: removed, Hunks: hunks[parts[2]]})
	}
	untracked, err := git(ctx, dir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %v: %s", err, untracked)
	}
	for _, name := range strings.Split(strings.TrimSpace(untracked), "\n") {
		if name == "" {
			continue
		}
		fd := model.FileDiff{Path: name}
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil && len(b) <= maxUntrackedDiffBytes && !bytes.Contains(b, []byte{0}) {
			text := strings.TrimSuffix(string(b), "\n")
			lines := strings.Split(text, "\n")
			fd.Added = len(lines)
			fd.Hunks = fmt.Sprintf("@@ -0,0 +1,%d @@\n+%s\n", len(lines), strings.Join(lines, "\n+"))
		}
		out = append(out, fd)
	}
	return out, nil
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
