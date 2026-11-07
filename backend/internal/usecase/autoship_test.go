package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

func bindRig(t *testing.T, r *rig) {
	t.Helper()
	if _, err := r.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`UPDATE projects SET git_connection_id = 'conn1', remote_repo = 'acme/x' WHERE id = ?`, r.wo.ProjectID); err != nil {
		t.Fatal(err)
	}
	r.f.Integrations = &Integrations{}
}

func TestCleanConvergeOnBoundProjectOpensPRAutomatically(t *testing.T) {
	r := convergeRig(t, &fakeJudge{}, cleanReport)
	bindRig(t, r)
	if wo := r.evaluate(t); wo.State != model.StateReady {
		t.Fatalf("state %s", wo.State)
	}
	if !queuedOpenPR(r.q.enqueued) {
		t.Fatalf("open_pr not enqueued: %v", r.q.enqueued)
	}
	if len(r.events(t, "pr_auto_approved")) != 1 || len(r.events(t, "pr_approved")) != 0 {
		t.Fatal("expected one factory approval and no human approval")
	}
}

func TestCleanConvergeOnUnboundProjectWaitsForHuman(t *testing.T) {
	r := convergeRig(t, &fakeJudge{}, cleanReport)
	r.f.Integrations = &Integrations{}
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview || queuedOpenPR(r.q.enqueued) {
		t.Fatalf("state %s, enqueued %v", wo.State, r.q.enqueued)
	}
}

func TestConvergeEscalationOnBoundProjectWaitsForHuman(t *testing.T) {
	r := convergeRig(t, &fakeJudge{err: errors.New("boom")}, cleanReport)
	bindRig(t, r)
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview || queuedOpenPR(r.q.enqueued) {
		t.Fatalf("state %s, enqueued %v", wo.State, r.q.enqueued)
	}
}

func TestCancelledOrderIsNotAutoApproved(t *testing.T) {
	ctx := context.Background()
	r := convergeRig(t, &fakeJudge{}, cleanReport)
	bindRig(t, r)
	wo := r.reload(t)
	if err := r.f.step(ctx, wo, model.StateSpecialistReview, model.StatePRPreparation, "factory", "x"); err != nil {
		t.Fatal(err)
	}
	if err := r.f.step(ctx, wo, model.StatePRPreparation, model.StateMergeReadiness, "factory", "x"); err != nil {
		t.Fatal(err)
	}
	if err := r.wos.UpdateState(ctx, wo.ID, model.StateCancelled); err != nil {
		t.Fatal(err)
	}
	if err := r.f.evaluateReadiness(ctx, wo, false, nil); err != nil {
		t.Fatal(err)
	}
	if got := r.reload(t); got.State != model.StateCancelled || len(r.events(t, "pr_auto_approved")) != 0 || queuedOpenPR(r.q.enqueued) {
		t.Fatalf("state %s", got.State)
	}
}

func TestEvaluateRetryReenqueuesOpenPRAfterEnqueueFailure(t *testing.T) {
	r := convergeRig(t, &fakeJudge{}, cleanReport)
	bindRig(t, r)
	r.f.Sched = &failOnceQueue{fakeQueue: r.q, fail: true}
	if err := r.f.handleEvaluate(context.Background(), model.Job{WOID: r.wo.ID}); err == nil {
		t.Fatal("enqueue failure must surface so the job retries")
	}
	if got := r.reload(t); got.State != model.StateReady || queuedOpenPR(r.q.enqueued) {
		t.Fatalf("state %s, enqueued %v", got.State, r.q.enqueued)
	}
	if wo := r.evaluate(t); wo.State != model.StateReady || !queuedOpenPR(r.q.enqueued) {
		t.Fatalf("retry must queue open_pr: state %s, %v", wo.State, r.q.enqueued)
	}
}

func TestPRBodyNamesWhoApproved(t *testing.T) {
	wo := &model.WorkOrder{}
	if b := prBody(wo, false); !strings.Contains(b, "converge gate approved") || strings.Contains(b, "a human approved") {
		t.Fatal(b)
	}
	if b := prBody(wo, true); !strings.Contains(b, "a human approved") {
		t.Fatal(b)
	}
}
