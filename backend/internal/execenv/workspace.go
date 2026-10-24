package execenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
