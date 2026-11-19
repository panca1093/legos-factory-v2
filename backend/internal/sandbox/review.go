package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"legos-factory/backend/internal/model"
)

func (l Local) reviewDir(projectID string, number int) string {
	return filepath.Join(l.DataDir, "worktrees", projectID, fmt.Sprintf("review-%d", number))
}

func reviewRef(number int) string { return fmt.Sprintf("refs/legos-review/%d", number) }

// PrepareReview checks out a pull request's head, fetched from origin as ref, in a detached worktree of the
// project folder at <data>/worktrees/<project>/review-<number>. It returns the checked-out commit. The user's
// branches and working tree are never touched; an earlier checkout of the same PR is replaced.
func (l Local) PrepareReview(ctx context.Context, projectID string, number int, repo, ref string, auth *model.GitAuth) (dir, sha string, err error) {
	if !isLocalGitRepo(repo) {
		return "", "", fmt.Errorf("project folder %s is not a git repository", repo)
	}
	dir = l.reviewDir(projectID, number)
	defer lockRepo(ctx, repo)()
	if err := l.removeReviewLocked(ctx, projectID, number, repo); err != nil {
		return "", "", err
	}
	if !hasOrigin(ctx, repo) {
		return "", "", fmt.Errorf("project folder %s has no origin to fetch the pull request from", repo)
	}
	local := reviewRef(number)
	if out, err := gitAuthed(ctx, repo, auth, "fetch", "--quiet", "--no-tags", "origin", "+"+ref+":"+local); err != nil {
		return "", "", fmt.Errorf("git fetch %s: %v: %s", ref, err, strings.TrimSpace(out))
	}
	out, err := git(ctx, repo, "rev-parse", "--verify", local+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("git rev-parse %s: %v: %s", local, err, strings.TrimSpace(out))
	}
	sha = strings.TrimSpace(out)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", "", err
	}
	if out, err := git(ctx, repo, "worktree", "add", "--detach", "--quiet", dir, sha); err != nil {
		_ = os.RemoveAll(dir)
		forgetWorktree(repo, dir)
		return "", "", fmt.Errorf("git worktree add: %v: %s", err, strings.TrimSpace(out))
	}
	return dir, sha, nil
}

// RemoveReview deletes a pull request's review checkout and its private ref; it is safe to repeat.
func (l Local) RemoveReview(ctx context.Context, projectID string, number int, repo string) error {
	if !isLocalGitRepo(repo) {
		return removeUnder(filepath.Join(l.DataDir, "worktrees", projectID), l.reviewDir(projectID, number))
	}
	defer lockRepo(ctx, repo)()
	return l.removeReviewLocked(ctx, projectID, number, repo)
}

func (l Local) removeReviewLocked(ctx context.Context, projectID string, number int, repo string) error {
	dir := l.reviewDir(projectID, number)
	_, _ = git(ctx, repo, "worktree", "remove", "--force", dir)
	if err := removeUnder(filepath.Join(l.DataDir, "worktrees", projectID), dir); err != nil {
		return err
	}
	forgetWorktree(repo, dir)
	if refExists(ctx, repo, reviewRef(number)) {
		_, _ = git(ctx, repo, "update-ref", "-d", reviewRef(number))
	}
	return nil
}
