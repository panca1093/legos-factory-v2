package usecase

import (
	"context"
	"fmt"

	"legos-factory/backend/internal/model"
)

// Foreman review: an independent agent run (the Foreman's own model) checks
// the Architect's draft. APPROVE readies it for execution, REVISE sends it
// back to the Architect once, CLARIFY asks a human. A second REVISE goes to a
// human too, so the two agents can never loop.

const maxForemanRevisions = 1

func (f *Foreman) handleReview(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if wo.State != model.StateArchitectReview || wo.PlanReview != "pending" {
		return nil // superseded: the plan was revised, approved or escalated meanwhile
	}
	tasks, err := f.WO.Plan(ctx, wo.ID)
	if err != nil {
		return err
	}
	f.event(wo.ID, "foreman", "plan_review_started", "Foreman reviewing the Architect's plan", nil)
	var r reviewReply
	_, runID, err := f.runStructured(ctx, wo, model.AgentForeman, foremanReviewPrompt(wo, tasks),
		map[string]string{"review_round": fmt.Sprint(wo.ReviewRounds), "clarification_answer": wo.ClarifAnswer}, "", &r)
	if err != nil {
		return err
	}
	f.Catalog.Record(ctx, wo, model.KnowledgePlanReview, model.TrustAIInterpretation, "foreman",
		"Foreman plan review: "+r.Decision, r.Notes+r.QuestionText, "run:"+runID)

	switch r.Decision {
	case "APPROVE":
		if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"plan_review": "approved"}); err != nil {
			return err
		}
		wo.PlanReview = "approved"
		f.event(wo.ID, "foreman", "plan_reviewed", "Foreman approved the plan; "+awaitingText(wo), map[string]any{"notes": r.Notes})
		if wo.Autonomy == "delegated" {
			return f.approvePlan(ctx, wo, "policy", "Delegated autonomy: execution authorized after Foreman review")
		}
		return nil
	case "REVISE":
		if wo.ReviewRounds >= maxForemanRevisions {
			return f.askHuman(ctx, wo, "foreman",
				"The Foreman still requests changes after a revision: "+r.Notes+" How should the plan proceed?")
		}
		if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"plan_review": "", "review_rounds": wo.ReviewRounds + 1}); err != nil {
			return err
		}
		f.event(wo.ID, "foreman", "plan_revision_requested", r.Notes, nil)
		if err := f.step(ctx, wo, model.StateArchitectReview, model.StatePlanning, "foreman", "Foreman requested a plan revision"); err != nil {
			return err
		}
		return f.enqueuePayload(ctx, JobPlan, wo.ID, map[string]any{"notes": "Foreman review: " + r.Notes})
	default: // CLARIFY
		return f.askHuman(ctx, wo, "foreman", r.QuestionText)
	}
}

func awaitingText(wo *model.WorkOrder) string {
	if wo.Autonomy == "delegated" {
		return "delegated autonomy starts execution"
	}
	return "awaiting human plan approval"
}
