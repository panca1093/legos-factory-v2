package usecase

import (
	"context"
	"fmt"

	"legos-factory/backend/internal/model"
)

// Architect: plan drafting, CLARIFY/APPROVE decision and re-review after clarification.

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
		// revision loop: human requested a plan revision
		if err := f.step(ctx, wo, model.StatePlanning, model.StatePlanGeneration, "foreman", "Re-drafting plan after revision request"); err != nil {
			return err
		}
	case model.StateRequirement, model.StateDecomposition, model.StatePlanGeneration:
		// resume: a previous attempt was interrupted mid-planning
	default:
		return fmt.Errorf("plan job for WO in state %s", wo.State)
	}
	f.event(wo.ID, "architect", "planning_started", "Architect drafting plan", nil)

	provider, modelID := f.Model(model.AgentArchitect, wo)
	res, _, err := f.runAgent(ctx, wo, model.AgentArchitect, provider, modelID, planPrompt(wo), nil, "")
	if err != nil {
		return err
	}
	var plan struct {
		Decision     string `json:"decision"`
		Question     string `json:"question"`
		QuestionText string `json:"question_text"`
		Summary      string `json:"summary"`
		Tasks        []struct {
			ID        string   `json:"id"`
			Title     string   `json:"title"`
			Objective string   `json:"objective"`
			Role      string   `json:"role"`
			Deps      []string `json:"deps"`
		} `json:"tasks"`
	}
	if err := parseStructured(res.Text, &plan); err != nil {
		return fmt.Errorf("architect returned unparseable plan: %w", err)
	}

	if plan.Decision == "CLARIFY" {
		q := plan.QuestionText
		if q == "" {
			q = plan.Question
		}
		if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"clarif_question": q}); err != nil {
			return err
		}
		f.event(wo.ID, "architect", "clarification_requested", q, nil)
		if err := f.toReview(ctx, wo); err != nil {
			return err
		}
		return f.step(ctx, wo, model.StateArchitectReview, model.StateWaitingForHuman, "foreman", "Waiting for human clarification")
	}
	if plan.Decision != "APPROVE" && plan.Decision != "" {
		return fmt.Errorf("unexpected architect decision %q", plan.Decision)
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
		tasks = append(tasks, model.Task{
			ID: idMap[ref], WOID: wo.ID, Seq: i, Title: t.Title, Objective: t.Objective,
			Role: t.Role, Deps: deps,
		})
	}
	if err := model.ValidateDAG(tasks); err != nil {
		return err
	}
	if err := f.WO.ReplacePlan(ctx, wo.ID, tasks); err != nil {
		return err
	}
	f.event(wo.ID, "architect", "plan_ready", fmt.Sprintf("Plan with %d tasks", len(tasks)),
		map[string]any{"summary": plan.Summary})
	if err := f.toReview(ctx, wo); err != nil {
		return err
	}
	f.event(wo.ID, "architect", "architect_approved", "Architect review passed", nil)
	return nil
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

func (f *Foreman) handleReview(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if wo.State != model.StateWaitingForHuman {
		return fmt.Errorf("review job for WO in state %s", wo.State)
	}
	provider, modelID := f.Model(model.AgentArchitect, wo)
	ctxData := map[string]string{
		"clarification_answer": wo.ClarifAnswer,
		"plan_json":            "present",
	}
	if _, _, err := f.runAgent(ctx, wo, model.AgentArchitect, provider, modelID, reviewPrompt(wo), ctxData, ""); err != nil {
		return err
	}
	f.event(wo.ID, "architect", "architect_approved", "Architect re-review passed after clarification", nil)
	if err := f.step(ctx, wo, model.StateWaitingForHuman, model.StatePlanning, "foreman", "Plan updated with clarification"); err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StatePlanning, model.StatePlanGeneration, "foreman", "Finalizing plan"); err != nil {
		return err
	}
	return f.step(ctx, wo, model.StatePlanGeneration, model.StateArchitectReview, "foreman", "Architect review complete")
}
