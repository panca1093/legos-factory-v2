package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareReviewChecksOutThePRHeadDetachedAndRemovesCleanly(t *testing.T) {
	r := newRig(t)
	l := Local{DataDir: t.TempDir()}
	ctx := context.Background()
	gitIn(t, r.folder, "push", "-q", "origin", "wip:refs/pull/7/head")
	want := gitIn(t, r.folder, "rev-parse", "wip")

	dir, sha, err := l.PrepareReview(ctx, "p1", 7, r.folder, "refs/pull/7/head", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sha != want || dir != filepath.Join(l.DataDir, "worktrees", "p1", "review-7") {
		t.Fatalf("dir=%q sha=%q", dir, sha)
	}
	if got := subject(t, dir); got != "local only" {
		t.Fatalf("checkout is at %q", got)
	}
	if out, err := git(ctx, dir, "symbolic-ref", "-q", "HEAD"); err == nil {
		t.Fatalf("the review checkout must be detached, on %q", out)
	}
	if got := gitIn(t, r.folder, "symbolic-ref", "--short", "HEAD"); got != "wip" {
		t.Fatalf("the user's folder changed: %q", got)
	}

	if _, _, err := l.PrepareReview(ctx, "p1", 7, r.folder, "refs/pull/7/head", nil); err != nil {
		t.Fatalf("a second prepare replaces the first: %v", err)
	}
	if err := l.RemoveReview(ctx, "p1", 7, r.folder); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("checkout still there: %v", err)
	}
	if refExists(ctx, r.folder, reviewRef(7)) {
		t.Fatal("the private review ref must be deleted")
	}
	if out := gitIn(t, r.folder, "worktree", "list"); strings.Contains(out, "review-7") {
		t.Fatalf("worktree record left behind: %s", out)
	}
	if err := l.RemoveReview(ctx, "p1", 7, r.folder); err != nil {
		t.Fatalf("remove is repeatable: %v", err)
	}
}

func TestPrepareReviewReportsAMissingRef(t *testing.T) {
	r := newRig(t)
	l := Local{DataDir: t.TempDir()}
	if _, _, err := l.PrepareReview(context.Background(), "p1", 9, r.folder, "refs/pull/9/head", nil); err == nil || !strings.Contains(err.Error(), "refs/pull/9/head") {
		t.Fatalf("err = %v", err)
	}
}
