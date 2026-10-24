package usecase

import (
	"context"
	"fmt"
	"strings"

	"legos-factory/backend/internal/model"
)

func (f *Foreman) handleEvaluate(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StateSpecialistReview, model.StatePRPreparation, "foreman", "Preparing change summary"); err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StatePRPreparation, model.StateMergeReadiness, "foreman", "Evaluating merge readiness"); err != nil {
		return err
	}
	return f.evaluateReadiness(ctx, wo, false)
}

// evaluateReadiness combines deterministic verification and review outcomes
// into the merge-readiness decision.
func (f *Foreman) evaluateReadiness(ctx context.Context, wo *model.WorkOrder, verificationBlocked bool) error {
	checkCount, err := f.Quality.CountForRevision(ctx, wo.ID, wo.Revision)
	if err != nil {
		return err
	}
	openBlockers, err := f.Findings.CountOpenBlockers(ctx, wo.ID)
	if err != nil {
		return err
	}
	decision, reason := "READY", "All checks passed on the current revision and no blocker findings remain."
	switch {
	case verificationBlocked || openBlockers > 0:
		decision = "BLOCKED"
		reason = fmt.Sprintf("Blocked: %d unresolved blocker finding(s) or failed verification.", openBlockers)
	case checkCount == 0:
		decision = "NEEDS_VERIFICATION"
		reason = "No verification evidence recorded for the current revision."
	}
	f.event(wo.ID, "foreman", "readiness_"+strings.ToLower(decision), reason, map[string]any{"decision": decision})
	// Final PR disposition stays human: evidence-complete orders land in
	// NEEDS_HUMAN_REVIEW; the human approve/decline endpoint decides READY.
	next := model.StateNeedsHumanReview
	if decision == "BLOCKED" {
		next = model.StateBlocked
	}
	if err := f.step(ctx, wo, model.StateMergeReadiness, next, "foreman", "Merge readiness: "+decision); err != nil {
		return err
	}
	return nil
}
