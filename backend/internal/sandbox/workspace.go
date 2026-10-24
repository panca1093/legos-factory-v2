// Package sandbox prepares isolated work-order workspaces, runs
// verification commands in them and commits verified changes locally.
package sandbox

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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
// repositories when auth is given, or an empty directory otherwise.
func Prepare(ctx context.Context, dataDir, woID, repoURL string, auth *model.GitAuth) (*Workspace, error) {
	root := filepath.Join(dataDir, "workspaces", woID)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return nil, err
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
		if out, err := git(ctx, dir, "-c", "user.name=legos-factory", "-c", "user.email=factory@localhost",
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
	base := "HEAD"
	if out, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/HEAD"); err == nil && strings.TrimSpace(out) != "" {
		base = "refs/remotes/origin/HEAD"
	}
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
