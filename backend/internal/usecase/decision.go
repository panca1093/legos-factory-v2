package usecase

import (
	"context"
	"fmt"

	"legos-factory/backend/internal/model"
)

// Human decisions: plan approval/revision, clarification, PR disposition, cancel.

// ApprovePlan authorizes execution (human decision or policy, enforced by
// the HTTP layer). Moves ARCHITECT_REVIEW → QUEUED and enqueues root tasks.
func (f *Foreman) ApprovePlan(ctx context.Context, woID string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	tasks, err := f.WO.Plan(ctx, woID)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return stateConflict("plan has no tasks; approve requires a reviewed plan")
	}
	if err := f.step(ctx, wo, model.StateArchitectReview, model.StateQueued, "human", "Plan approved; execution authorized"); err != nil {
		return err
	}
	roots := model.ReadyTasks(tasks, map[string]bool{})
	for _, t := range roots {
		if _, err := f.Sched.Enqueue(ctx, JobImplement, woID, t.ID, map[string]any{}); err != nil {
			return err
		}
	}
	f.event(woID, "foreman", "execution_queued", fmt.Sprintf("%d task(s) queued", len(roots)), nil)
	return nil
}

// RevisePlan sends the plan back to the Architect (ARCHITECT_REVIEW → PLANNING).
func (f *Foreman) RevisePlan(ctx context.Context, woID, notes string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StateArchitectReview, model.StatePlanning, "human", "Revision requested: "+notes); err != nil {
		return err
	}
	_, err = f.Sched.Enqueue(ctx, JobPlan, woID, "", map[string]any{"notes": notes})
	return err
}

// AnswerClarification records the human answer and triggers re-review.
func (f *Foreman) AnswerClarification(ctx context.Context, woID, answer string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if wo.State != model.StateWaitingForHuman {
		return stateConflict("WO %s is not waiting for clarification (state %s)", woID, wo.State)
	}
	if err := f.WO.UpdateFields(ctx, woID, map[string]any{"clarif_answer": answer}); err != nil {
		return err
	}
	f.event(woID, "human", "clarification_answered", answer, nil)
	_, err = f.Sched.Enqueue(ctx, JobReview, woID, "", map[string]any{})
	return err
}

// HumanPRDecision records the final human disposition from
// NEEDS_HUMAN_REVIEW: approve → READY, request_changes → new revision fix
// loop, decline → CANCELLED. It never merges or publishes anything.
func (f *Foreman) HumanPRDecision(ctx context.Context, woID, decision, reason string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if wo.State != model.StateNeedsHumanReview {
		return stateConflict("WO %s is not awaiting a human PR decision (state %s)", woID, wo.State)
	}
	switch decision {
	case "approve":
		f.event(woID, "human", "pr_approved", reason, nil)
		return f.step(ctx, wo, model.StateNeedsHumanReview, model.StateReady, "human", "PR approved by human (no publish/merge performed)")
	case "request_changes":
		newRev := wo.Revision + 1
		if err := f.WO.UpdateFields(ctx, woID, map[string]any{"revision": newRev}); err != nil {
			return err
		}
		f.event(woID, "human", "revision_requested", reason, map[string]any{"revision": newRev})
		if err := f.step(ctx, wo, model.StateNeedsHumanReview, model.StateImplementing, "human", "Changes requested; invalidating prior quality approval"); err != nil {
			return err
		}
		_, err := f.Sched.Enqueue(ctx, JobImplement, woID, "", map[string]any{"reason": "human_revision"})
		return err
	case "decline":
		f.event(woID, "human", "pr_declined", reason, nil)
		return f.step(ctx, wo, model.StateNeedsHumanReview, model.StateCancelled, "human", "Work order declined")
	default:
		return fmt.Errorf("unknown PR decision %q", decision)
	}
}

// Cancel stops pending jobs and transitions the WO to CANCELLED.
func (f *Foreman) Cancel(ctx context.Context, woID string) error {
	if err := f.Sched.CancelWO(ctx, woID); err != nil {
		return err
	}
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if wo.State.Terminal() {
		return stateConflict("WO %s already terminal (%s)", woID, wo.State)
	}
	if err := model.MustTransition(wo.State, model.StateCancelled); err != nil {
		return err
	}
	if err := f.WO.UpdateState(ctx, woID, model.StateCancelled); err != nil {
		return err
	}
	f.event(woID, "human", "cancelled", "Work order cancelled", map[string]any{"from": string(wo.State)})
	return nil
}
