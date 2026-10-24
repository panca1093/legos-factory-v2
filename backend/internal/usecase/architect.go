package usecase

import (
	"context"
	"fmt"
	"strings"

	"legos-factory/backend/internal/model"
)

// Architect: drafts the plan (or asks a human for a missing decision). It
// never approves its own plan; the Foreman reviews it in review.go.

func (f *Foreman) handlePlan(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	switch wo.State {
	case model.StateCreated:
		if err := f.step(ctx, wo, model.StateCreated, model.StateRequirement, "foreman", "Analyzing requirements"); err != nil {
			return err
		}
		if err := f.step(ctx, wo, model.StateRequirement, model.StateDecomposition, "foreman", "Decomposing into tasks"); err != nil {
			return err
		}
	case model.StatePlanning:
		// re-draft after a clarification, a Foreman REVISE or a human revision request
		if err := f.step(ctx, wo, model.StatePlanning, model.StatePlanGeneration, "foreman", "Re-drafting plan"); err != nil {
			return err
		}
	case model.StateRequirement, model.StateDecomposition, model.StatePlanGeneration:
		// resume: a previous attempt was interrupted mid-planning
	default:
		return fmt.Errorf("plan job for WO in state %s", wo.State)
	}
	f.event(wo.ID, "architect", "planning_started", "Architect drafting plan", nil)

	prior := f.Catalog.Relevant(ctx, wo)
	priorRefs := make([]string, 0, len(prior))
	for _, k := range prior {
		priorRefs = append(priorRefs, "knowledge:"+k.ID)
	}
	if len(prior) > 0 {
		f.event(wo.ID, "cataloger", "knowledge_retrieved",
			fmt.Sprintf("%d prior project record(s) provided to the Architect", len(prior)), map[string]any{"refs": priorRefs})
	}

	notes, _ := job.Payload["notes"].(string)
	var plan planReply
	_, runID, err := f.runStructured(ctx, wo, model.AgentArchitect, planPrompt(wo, notes)+knowledgePrompt(prior),
		map[string]string{"clarification_answer": wo.ClarifAnswer}, "", &plan)
	if err != nil {
		return err
	}

	if plan.Decision == "CLARIFY" {
		q := plan.QuestionText
		if q == "" {
			q = plan.Question
		}
		return f.askHuman(ctx, wo, "architect", q)
	}

	// plan task ids from agents are only unique within one plan; namespace
	// them per work order so the plan_tasks primary key stays global
	idMap := make(map[string]string, len(plan.Tasks))
	for i, t := range plan.Tasks {
		ref := t.ID
		if ref == "" {
			ref = fmt.Sprintf("t%d", i+1)
		}
		idMap[ref] = fmt.Sprintf("%s_%s", wo.ID, ref)
	}
	tasks := make([]model.Task, 0, len(plan.Tasks))
	for i, t := range plan.Tasks {
		ref := t.ID
		if ref == "" {
			ref = fmt.Sprintf("t%d", i+1)
		}
		deps := make([]string, 0, len(t.Deps))
		for _, d := range t.Deps {
			if mapped, ok := idMap[d]; ok {
				deps = append(deps, mapped)
			}
		}
		role := t.Role
		if role == "" {
			role = "builder"
		}
		tasks = append(tasks, model.Task{
			ID: idMap[ref], WOID: wo.ID, Seq: i, Title: t.Title, Objective: t.Objective,
			Role: role, Deps: deps,
		})
	}
	if err := model.ValidateDAG(tasks); err != nil {
		return fmt.Errorf("%w: %v", model.ErrPermanent, err)
	}
	if err := f.WO.ReplacePlan(ctx, wo.ID, tasks); err != nil {
		return err
	}
	f.event(wo.ID, "architect", "plan_ready", fmt.Sprintf("Plan with %d tasks", len(tasks)),
		map[string]any{"summary": plan.Summary})
	f.Catalog.Record(ctx, wo, model.KnowledgeArchitectReview, model.TrustAIInterpretation, "architect",
		"Architect plan: "+plan.Summary, taskList(tasks), append([]string{"run:" + runID}, priorRefs...)...)
	if err := f.toReview(ctx, wo); err != nil {
		return err
	}
	// the Architect never approves its own plan: the Foreman reviews it
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"plan_review": "pending"}); err != nil {
		return err
	}
	return f.enqueue(ctx, JobReview, wo.ID, "foreman_review")
}

// askHuman records a clarification question and parks the order in
// WAITING_FOR_HUMAN until AnswerClarification re-drafts the plan.
func (f *Foreman) askHuman(ctx context.Context, wo *model.WorkOrder, actor, question string) error {
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"clarif_question": question, "clarif_answer": "", "plan_review": ""}); err != nil {
		return err
	}
	wo.ClarifQuestion, wo.ClarifAnswer = question, ""
	f.event(wo.ID, actor, "clarification_requested", question, nil)
	if wo.State != model.StateArchitectReview {
		if err := f.toReview(ctx, wo); err != nil {
			return err
		}
	}
	return f.step(ctx, wo, model.StateArchitectReview, model.StateWaitingForHuman, "foreman", "Waiting for human clarification")
}

// toReview advances whichever pre-review planning state the WO is in to
// ARCHITECT_REVIEW via SPEC_AND_PLAN_GENERATION.
func (f *Foreman) toReview(ctx context.Context, wo *model.WorkOrder) error {
	if wo.State == model.StateDecomposition {
		if err := f.step(ctx, wo, model.StateDecomposition, model.StatePlanGeneration, "foreman", "Generating specification and plan"); err != nil {
			return err
		}
	}
	return f.step(ctx, wo, model.StatePlanGeneration, model.StateArchitectReview, "foreman", "Plan drafted; architect review")
}

func taskList(tasks []model.Task) string {
	var b strings.Builder
	for i, t := range tasks {
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, t.Title, t.Objective)
	}
	return b.String()
}
