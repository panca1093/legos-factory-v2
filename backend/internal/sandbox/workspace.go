// Package sandbox prepares isolated work-order workspaces, runs
// verification commands in them and commits verified changes locally.
package sandbox

import (
	"context"
	"fmt"
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
// repositories (shared objects, cheap) or a plain copy otherwise.
func Prepare(ctx context.Context, dataDir, woID, repoURL string) (*Workspace, error) {
	root := filepath.Join(dataDir, "workspaces", woID)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return nil, err
	}
	if repoURL != "" && isLocalGitRepo(repoURL) {
		cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", repoURL, root)
		if out, err := cmd.CombinedOutput(); err != nil {
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

// Local runs checks and commits inside isolated workspaces on this host.
// It never pushes.
type Local struct{}

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

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
