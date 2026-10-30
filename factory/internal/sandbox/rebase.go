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

// HasConflictMarkers reports whether any of files still contains a conflict marker line (<<<<<<<, =======, >>>>>>>).
func HasConflictMarkers(ctx context.Context, sb Sandbox, dir string, files []string) (bool, error) {
	if len(files) == 0 {
		return false, nil
	}
	args := append([]string{"grep", "-l", "-e", "^<<<<<<< ", "-e", "^=======$", "-e", "^>>>>>>> ", "--"}, files...)
	_, stderr, code, err := sb.Exec(ctx, dir, nil, args...)
	if err != nil {
		return false, err
	}
	switch code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, fmt.Errorf("grep for conflict markers: exit %d: %s", code, strings.TrimSpace(stderr))
}

// UnresolvableConflicts lists unmerged files a builder cannot fix by editing markers: binary, empty or missing from the worktree.
func UnresolvableConflicts(ctx context.Context, sb Sandbox, dir string, files []string) ([]string, error) {
	var bad []string
	for _, f := range files {
		_, stderr, code, err := sb.Exec(ctx, dir, nil, "grep", "-Il", "-e", "", "--", f)
		if err != nil {
			return nil, err
		}
		switch code {
		case 0:
		case 1, 2:
			if code == 2 && !strings.Contains(stderr, "No such file") {
				return nil, fmt.Errorf("grep %s: exit 2: %s", f, strings.TrimSpace(stderr))
			}
			bad = append(bad, f)
		default:
			return nil, fmt.Errorf("grep %s: exit %d: %s", f, code, strings.TrimSpace(stderr))
		}
	}
	return bad, nil
}

// BranchDiff lists the files HEAD changes relative to the merge base with base (the shape of a pull request).
func BranchDiff(ctx context.Context, sb Sandbox, dir, base string) ([]string, error) {
	out, err := git(ctx, sb, dir, nil, "diff", "--name-only", base+"...HEAD")
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

// IsLeaseRejection reports whether err is a push refused because the remote branch is no longer at the expected commit.
func IsLeaseRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "stale info") || strings.Contains(msg, "[rejected]") || strings.Contains(msg, "[remote rejected]")
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
