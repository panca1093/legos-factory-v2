package sandbox

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// GitAuth is placed in a URL on the command line only, never in git config.
type GitAuth struct{ Username, Token string }

var gitEnv = map[string]string{"GIT_TERMINAL_PROMPT": "0"}

func (a *GitAuth) apply(repoURL string) string {
	if a == nil || a.Token == "" {
		return repoURL
	}
	u, err := url.Parse(repoURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return repoURL
	}
	user := a.Username
	if user == "" {
		user = "x-access-token"
	}
	u.User = url.UserPassword(user, a.Token)
	return u.String()
}

func (a *GitAuth) redact(s string) string {
	if a == nil || a.Token == "" {
		return s
	}
	s = strings.ReplaceAll(s, a.Token, "[REDACTED]")
	return strings.ReplaceAll(s, url.QueryEscape(a.Token), "[REDACTED]")
}

func git(ctx context.Context, sb Sandbox, dir string, auth *GitAuth, args ...string) (string, error) {
	stdout, stderr, code, err := sb.Exec(ctx, dir, gitEnv, append([]string{"git"}, args...)...)
	if err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], auth.redact(err.Error()))
	}
	if code != 0 {
		return "", fmt.Errorf("git %s: exit %d: %s", args[0], code, auth.redact(strings.TrimSpace(stderr+stdout)))
	}
	return strings.TrimSpace(stdout), nil
}

// Clone clones into ws.RepoDir(); a no-op when the repo is already there.
func Clone(ctx context.Context, sb Sandbox, repoURL string, auth *GitAuth, branch string) error {
	ws := sb.Workspace()
	if _, err := git(ctx, sb, ws.RepoDir(), nil, "rev-parse", "--git-dir"); err == nil {
		return nil
	}
	args := []string{"clone", "--quiet"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, auth.apply(repoURL), ws.RepoDir())
	if _, err := git(ctx, sb, ws.Root(), auth, args...); err != nil {
		return err
	}
	_, err := git(ctx, sb, ws.RepoDir(), nil, "remote", "set-url", "origin", repoURL)
	return err
}

// AddWorktree reuses an existing worktree so a re-queued work order resumes in place.
func AddWorktree(ctx context.Context, sb Sandbox, task, branch, base string) (string, error) {
	ws := sb.Workspace()
	dir := ws.WorktreeDir(task)
	if _, err := git(ctx, sb, dir, nil, "rev-parse", "--git-dir"); err == nil {
		return dir, nil
	}
	repo := ws.RepoDir()
	if _, err := git(ctx, sb, repo, nil, "worktree", "prune"); err != nil {
		return "", err
	}
	var err error
	if _, verr := git(ctx, sb, repo, nil, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); verr == nil {
		_, err = git(ctx, sb, repo, nil, "worktree", "add", dir, branch)
	} else {
		args := []string{"worktree", "add", "-b", branch, dir}
		if base != "" {
			args = append(args, base)
		}
		_, err = git(ctx, sb, repo, nil, args...)
	}
	return dir, err
}

// CommitAll commits everything in dir; changed is false (and sha is HEAD) when the tree was clean.
func CommitAll(ctx context.Context, sb Sandbox, dir, msg string) (sha string, changed bool, err error) {
	if _, err = git(ctx, sb, dir, nil, "add", "-A"); err != nil {
		return "", false, err
	}
	status, err := git(ctx, sb, dir, nil, "status", "--porcelain")
	if err != nil {
		return "", false, err
	}
	if status != "" {
		if _, err = git(ctx, sb, dir, nil, "-c", "user.name=legos-factory", "-c", "user.email=factory@localhost",
			"commit", "--quiet", "--no-verify", "-m", msg); err != nil {
			return "", false, err
		}
		changed = true
	}
	sha, err = git(ctx, sb, dir, nil, "rev-parse", "HEAD")
	return sha, changed && err == nil, err
}

// ChangedFiles lists files changed relative to base, committed or not, including untracked.
func ChangedFiles(ctx context.Context, sb Sandbox, dir, base string) ([]string, error) {
	seen := map[string]bool{}
	var files []string
	for _, args := range [][]string{
		{"diff", "--name-only", base},
		{"ls-files", "--others", "--exclude-standard"},
	} {
		out, err := git(ctx, sb, dir, nil, args...)
		if err != nil {
			return nil, err
		}
		for _, f := range strings.Split(out, "\n") {
			if f = filepath.ToSlash(strings.TrimSpace(f)); f != "" && !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	return files, nil
}

// Push pushes HEAD to branch on origin, using the credentialed URL for this call only.
func Push(ctx context.Context, sb Sandbox, dir, branch string, auth *GitAuth) error {
	remote := "origin"
	if auth != nil && auth.Token != "" {
		out, err := git(ctx, sb, dir, auth, "remote", "get-url", "origin")
		if err != nil {
			return err
		}
		remote = auth.apply(out)
	}
	_, err := git(ctx, sb, dir, auth, "push", "--quiet", remote, "HEAD:refs/heads/"+branch)
	return err
}
