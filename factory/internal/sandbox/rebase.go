package sandbox

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Fetch updates origin/<branch>.
func Fetch(ctx context.Context, sb Sandbox, dir, branch string, auth *GitAuth) error {
	_, err := git(ctx, sb, dir, auth, "fetch", "--quiet", "origin", branch)
	return err
}

// PushLease force-pushes HEAD to branch only if the remote branch is still at expect.
func PushLease(ctx context.Context, sb Sandbox, dir, branch, expect string, auth *GitAuth) error {
	_, err := git(ctx, sb, dir, auth, "push", "--quiet", "--force-with-lease=refs/heads/"+branch+":"+expect, "origin", "HEAD:refs/heads/"+branch)
	return err
}

var gitIdent = []string{"-c", "user.name=legos-factory", "-c", "user.email=factory@localhost"}

// Rebase squashes the branch into one commit and rebases it onto onto; conflicts lists the unmerged files when it stops, leaving the rebase in progress.
func Rebase(ctx context.Context, sb Sandbox, dir, onto string) (conflicts []string, err error) {
	base, err := git(ctx, sb, dir, nil, "merge-base", "HEAD", onto)
	if err != nil {
		return nil, err
	}
	ontoSHA, err := git(ctx, sb, dir, nil, "rev-parse", onto)
	if err != nil {
		return nil, err
	}
	if base == ontoSHA {
		return nil, nil
	}
	subject, err := git(ctx, sb, dir, nil, "log", "-1", "--format=%s")
	if err != nil {
		return nil, err
	}
	if _, err := git(ctx, sb, dir, nil, "reset", "--soft", base); err != nil {
		return nil, err
	}
	if _, _, err := CommitAll(ctx, sb, dir, subject); err != nil {
		return nil, err
	}
	return runRebase(ctx, sb, dir, onto)
}

func runRebase(ctx context.Context, sb Sandbox, dir string, args ...string) ([]string, error) {
	cmd := append(append([]string{"git", "-c", "core.editor=true"}, gitIdent...), "rebase")
	stdout, stderr, code, err := sb.Exec(ctx, dir, nil, append(cmd, args...)...)
	if err != nil {
		return nil, err
	}
	if code == 0 {
		return nil, nil
	}
	if files, uerr := Unmerged(ctx, sb, dir); uerr == nil && len(files) > 0 {
		return files, nil
	}
	return nil, fmt.Errorf("git rebase: exit %d: %s", code, strings.TrimSpace(stderr+stdout))
}

// Unmerged lists files with unresolved conflicts.
func Unmerged(ctx context.Context, sb Sandbox, dir string) ([]string, error) {
	out, err := git(ctx, sb, dir, nil, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\n") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, filepath.ToSlash(f))
		}
	}
	return files, nil
}

// RebaseInProgress reports whether dir is stopped in the middle of a rebase.
func RebaseInProgress(ctx context.Context, sb Sandbox, dir string) (bool, error) {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		p, err := git(ctx, sb, dir, nil, "rev-parse", "--git-path", name)
		if err != nil {
			return false, err
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if _, _, code, err := sb.Exec(ctx, "", nil, "test", "-d", p); err != nil {
			return false, err
		} else if code == 0 {
			return true, nil
		}
	}
	return false, nil
}

// HasConflictMarkers reports whether any of files still contains a conflict marker line.
func HasConflictMarkers(ctx context.Context, sb Sandbox, dir string, files []string) (bool, error) {
	if len(files) == 0 {
		return false, nil
	}
	args := append([]string{"grep", "-l", "-e", "^<<<<<<< ", "--"}, files...)
	_, _, code, err := sb.Exec(ctx, dir, nil, args...)
	if err != nil {
		return false, err
	}
	return code == 0, nil
}

// RebaseContinue stages everything and continues; conflicts are the files the next commit stopped on, nil when the rebase is finished.
func RebaseContinue(ctx context.Context, sb Sandbox, dir string) ([]string, error) {
	if in, err := RebaseInProgress(ctx, sb, dir); err != nil || !in {
		return nil, err
	}
	if _, err := git(ctx, sb, dir, nil, "add", "-A"); err != nil {
		return nil, err
	}
	return runRebase(ctx, sb, dir, "--continue")
}

// RebaseAbort abandons a rebase in progress.
func RebaseAbort(ctx context.Context, sb Sandbox, dir string) error {
	if in, err := RebaseInProgress(ctx, sb, dir); err != nil || !in {
		return err
	}
	_, err := git(ctx, sb, dir, nil, "rebase", "--abort")
	return err
}
