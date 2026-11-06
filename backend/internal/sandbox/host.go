package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"legos-factory/backend/internal/model"
)

// Host reads and creates git repositories on the machine the backend runs
// on, using the user's own git configuration.
type Host struct{}

func (Host) IsGitRepo(dir string) bool { return isLocalGitRepo(dir) }

// OriginURL returns the origin remote URL, or "" when there is none.
func (Host) OriginURL(ctx context.Context, dir string) string {
	out, err := gitOut(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// DefaultBranch returns origin's default branch, else the checked-out one.
func (Host) DefaultBranch(ctx context.Context, dir string) string {
	if out, err := gitOut(ctx, dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(strings.TrimSpace(out), "origin/")
	}
	out, _ := gitOut(ctx, dir, "symbolic-ref", "--short", "HEAD")
	return strings.TrimSpace(out)
}

func (Host) Clone(ctx context.Context, url, target string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", "--", url, target)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (Host) Init(ctx context.Context, dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	if out, err := git(ctx, dir, "init", "--quiet"); err != nil {
		return fmt.Errorf("git init: %v: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// PickFolder opens the native macOS folder chooser.
func (Host) PickFolder(ctx context.Context) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", model.ErrNotSupported
	}
	cmd := exec.CommandContext(ctx, "osascript", "-e", `POSIX path of (choose folder with prompt "Choose a project folder")`)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && strings.Contains(string(ee.Stderr), "User canceled") {
			return "", model.ErrCancelled
		}
		return "", fmt.Errorf("osascript: %v", err)
	}
	p := strings.TrimSpace(string(out))
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p, nil
}
