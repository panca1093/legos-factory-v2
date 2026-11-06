package usecase

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type deniedGH struct{ GitProvider }

func (deniedGH) CheckAuth(context.Context) (string, error) {
	return "", errors.New("gh: not logged in")
}

func approveEnqueued(t *testing.T, in *Integrations, repoURL string) []string {
	t.Helper()
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, nil)
	projs := &store.ProjectRepo{DB: wos.DB}
	if err := projs.Create(ctx, &model.Project{ID: "p2", Name: "p2", RepoURL: repoURL}); err != nil {
		t.Fatal(err)
	}
	if err := wos.UpdateFields(ctx, wo.ID, map[string]any{"project_id": "p2", "state": string(model.StateNeedsHumanReview)}); err != nil {
		t.Fatal(err)
	}
	q := &fakeQueue{}
	f.Sched, f.Proj, f.Integrations, f.Log = q, projs, in, slog.Default()
	if err := f.HumanPRDecision(ctx, wo.ID, "approve", ""); err != nil {
		t.Fatal(err)
	}
	return q.enqueued
}

func queuedOpenPR(l []string) bool {
	for _, s := range l {
		if s == JobOpenPR+":" {
			return true
		}
	}
	return false
}

func TestApproveOpensPRForGHModeProject(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "r")
	gitInit(t, repo)
	in := &Integrations{
		Host: &countingHost{origins: new(atomic.Int32)},
		GH:   func(string, string) GitProvider { return deniedGH{} },
	}
	// gh unauthenticated: still enqueued so the job fails visibly
	if got := approveEnqueued(t, in, repo); !queuedOpenPR(got) {
		t.Fatalf("gh-mode approve must enqueue %s, got %v", JobOpenPR, got)
	}
}

func TestApproveSkipsPRForUnboundProject(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "r")
	gitInit(t, repo)
	in := &Integrations{Host: &realGit{fakeHost{repos: map[string]string{}}}, GH: func(string, string) GitProvider { return deniedGH{} }}
	if got := approveEnqueued(t, in, repo); queuedOpenPR(got) {
		t.Fatalf("local-only approve must not enqueue %s, got %v", JobOpenPR, got)
	}
}
