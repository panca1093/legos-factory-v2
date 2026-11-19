package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"legos-factory/backend/internal/model"
)

// Each review run owns its checkout and ref, named by the run id, so two users reviewing the same PR
// never remove each other's files.
func (l Local) reviewDir(projectID string, number int, runID string) string {
	return filepath.Join(l.DataDir, "worktrees", projectID, fmt.Sprintf("review-%d-%s", number, runID))
}

func reviewRef(number int, runID string) string {
	return fmt.Sprintf("refs/legos-review/%d-%s", number, runID)
}

// PrepareReview checks out a pull request's head, fetched from origin as ref, in a detached worktree of the
// project folder at <data>/worktrees/<project>/review-<number>-<run>. It returns the checked-out commit. The
// user's branches and working tree are never touched.
func (l Local) PrepareReview(ctx context.Context, projectID string, number int, runID, repo, ref string, auth *model.GitAuth) (dir, sha string, err error) {
	if !isLocalGitRepo(repo) {
		return "", "", fmt.Errorf("project folder %s is not a git repository", repo)
	}
	dir = l.reviewDir(projectID, number, runID)
	defer lockRepo(ctx, repo)()
	if err := l.removeReviewLocked(ctx, projectID, number, runID, repo); err != nil {
		return "", "", err
	}
	if !hasOrigin(ctx, repo) {
		return "", "", fmt.Errorf("project folder %s has no origin to fetch the pull request from", repo)
	}
	local := reviewRef(number, runID)
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
		_, _ = git(ctx, repo, "update-ref", "-d", local)
		return "", "", fmt.Errorf("git worktree add: %v: %s", err, strings.TrimSpace(out))
	}
	return dir, sha, nil
}

// RemoveReview deletes one run's review checkout and its private ref; it is safe to repeat.
func (l Local) RemoveReview(ctx context.Context, projectID string, number int, runID, repo string) error {
	if !isLocalGitRepo(repo) {
		return removeUnder(filepath.Join(l.DataDir, "worktrees", projectID), l.reviewDir(projectID, number, runID))
	}
	defer lockRepo(ctx, repo)()
	return l.removeReviewLocked(ctx, projectID, number, runID, repo)
}

func (l Local) removeReviewLocked(ctx context.Context, projectID string, number int, runID, repo string) error {
	dir := l.reviewDir(projectID, number, runID)
	_, _ = git(ctx, repo, "worktree", "remove", "--force", dir)
	if err := removeUnder(filepath.Join(l.DataDir, "worktrees", projectID), dir); err != nil {
		return err
	}
	forgetWorktree(repo, dir)
	if ref := reviewRef(number, runID); refExists(ctx, repo, ref) {
		_, _ = git(ctx, repo, "update-ref", "-d", ref)
	}
	return nil
}

// CleanReviews removes every review checkout and private review ref of a project; only safe while no
// review run is active (at boot).
func (l Local) CleanReviews(ctx context.Context, projectID, repo string) error {
	root := filepath.Join(l.DataDir, "worktrees", projectID)
	entries, _ := os.ReadDir(root)
	if isLocalGitRepo(repo) {
		defer lockRepo(ctx, repo)()
		if out, err := git(ctx, repo, "for-each-ref", "--format=%(refname)", "refs/legos-review/"); err == nil {
			for _, ref := range strings.Fields(out) {
				_, _ = git(ctx, repo, "update-ref", "-d", ref)
			}
		}
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "review-") {
			continue
		}
		p := filepath.Join(root, e.Name())
		if isLocalGitRepo(repo) {
			_, _ = git(ctx, repo, "worktree", "remove", "--force", p)
		}
		if err := removeUnder(root, p); err != nil {
			return err
		}
		if isLocalGitRepo(repo) {
			forgetWorktree(repo, p)
		}
	}
	return nil
}
