package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"legos-factory/backend/internal/model"
)

// Architect: drafts the plan (or asks a human for a missing decision). It
// never approves its own plan; the Foreman reviews it in review.go.

func (f *Factory) handlePlan(ctx context.Context, job model.Job) (err error) {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.closePlanning(wo.ID) // a failed job must not leave the Foreman's session parked
		}
	}()
	switch wo.State {
	case model.StateCreated:
		if err := f.step(ctx, wo, model.StateCreated, model.StateRequirement, "factory", "Analyzing requirements"); err != nil {
			return err
		}
	case model.StatePlanning:
		// re-draft after a clarification, a Foreman REVISE or a human revision request
		if err := f.step(ctx, wo, model.StatePlanning, model.StatePlanGeneration, "factory", "Re-drafting plan"); err != nil {
			return err
		}
	case model.StateRequirement, model.StateDecomposition, model.StatePlanGeneration:
		// resume: a previous attempt was interrupted mid-planning
	default:
		return fmt.Errorf("plan job for WO in state %s", wo.State)
	}
	f.syncRepo(ctx, wo, true)
	facts := repoFacts(ctx, f.workspaceDir(wo.ID))
	if wo.State == model.StateRequirement {
		if wo.Brief == "" {
			if err := f.intake(ctx, wo, facts); err != nil {
				return err
			}
		}
		if err := f.step(ctx, wo, model.StateRequirement, model.StateDecomposition, "factory", "Decomposing into tasks"); err != nil {
			return err
		}
	}
	f.event(wo.ID, "architect", "planning_started", "Architect drafting plan", nil)

	prior := f.relevantDocs(ctx, wo)
	priorRefs := make([]string, 0, len(prior))
	for _, d := range prior {
		priorRefs = append(priorRefs, "kb:"+d.ID)
	}
	if len(prior) > 0 {
		f.event(wo.ID, "cataloger", "knowledge_retrieved",
			fmt.Sprintf("%d prior project document(s) provided to the Architect", len(prior)), map[string]any{"refs": priorRefs})
	}

	kbText, audit := f.KB.Context(ctx, wo.ProjectID, string(model.AgentArchitect), "", woQuery(wo), f.workspaceDir(wo.ID), wo.KBBaseSHA)
	notes, _ := job.Payload["notes"].(string)
	prev, err := f.WO.Plan(ctx, wo.ID)
	if err != nil {
		return err
	}
	var plan planReply
	_, runID, err := f.runStructured(ctx, wo, model.AgentArchitect, planPrompt(wo, notes, facts, prev)+f.priorKnowledgePrompt(prior)+kbText,
		map[string]string{"clarification_answer": wo.ClarifAnswer, "clarification_log": clarificationLog(wo)}, "", &plan, runOpts{Audit: audit})
	if err != nil {
		return err
	}
	if plan.Decision == "CLARIFY" {
		q := plan.QuestionText
		if q == "" {
			q = plan.Question
		}
		answered, err := f.foremanAnswers(ctx, wo, q, facts)
		if err != nil || !answered {
			return err
		}
		// the answer is stored; a fresh job re-drafts, so one job never chains every agent run
		return f.enqueuePayload(ctx, JobPlan, wo.ID, map[string]any{"reason": "foreman_answer", "notes": notes})
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
			Role: role, Deps: deps, Scope: t.Scope,
		})
	}
	if err := model.ValidateDAG(tasks); err != nil {
		return fmt.Errorf("%w: %v", model.ErrPermanent, err)
	}
	if err := f.WO.ReplacePlan(ctx, wo.ID, tasks); err != nil {
		return err
	}
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"spec": plan.Spec}); err != nil {
		return err
	}
	wo.Spec = plan.Spec
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

func findClarification(list []model.Clarification, question string) *model.Clarification {
	q := strings.ToLower(strings.TrimSpace(question))
	for i := range list {
		if strings.ToLower(strings.TrimSpace(list[i].Question)) == q {
			return &list[i]
		}
	}
	return nil
}

// maxForemanAnswers is how many Architect questions the Foreman may answer
// before the next one goes to a human.
const maxForemanAnswers = 2

// intake has the Foreman turn the request into the brief the Architect plans from.
func (f *Factory) intake(ctx context.Context, wo *model.WorkOrder, facts string) error {
	f.event(wo.ID, "foreman", "intake_started", "Foreman briefing the Architect", nil)
	kbText, audit := f.KB.Context(ctx, wo.ProjectID, string(model.AgentForeman), "", woQuery(wo), f.workspaceDir(wo.ID), wo.KBBaseSHA)
	var r intakeReply
	_, runID, err := f.runStructured(ctx, wo, model.AgentForeman, intakePrompt(wo, facts)+kbText,
		map[string]string{"stage": "intake"}, "", &r, runOpts{Audit: audit, Planning: true, Prime: foremanPrime(wo)})
	if err != nil {
		return err
	}
	brief := r.text()
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"brief": brief}); err != nil {
		return err
	}
	wo.Brief = brief
	f.writeBriefDoc(ctx, wo)
	f.event(wo.ID, "foreman", "brief_ready", "Foreman brief ready", map[string]any{"brief": brief, "run_id": runID})
	return nil
}

// foremanAnswers sends an Architect question to the Foreman. It reports true when
// the Foreman answered, so the Architect re-drafts; otherwise a human was asked.
func (f *Factory) foremanAnswers(ctx context.Context, wo *model.WorkOrder, question, facts string) (bool, error) {
	if wo.ClarifRounds >= maxForemanAnswers {
		return false, f.askHuman(ctx, wo, "architect", question)
	}
	if c := findClarification(wo.Clarifications, question); c != nil {
		// a repeated question reuses the stored answer; it still counts as a round so the cap bounds a looping Architect
		if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"clarif_rounds": wo.ClarifRounds + 1}); err != nil {
			return false, err
		}
		wo.ClarifRounds++
		f.event(wo.ID, "foreman", "clarification_answered", c.Answer, map[string]any{"question": question, "repeated": true})
		return true, nil
	}
	f.event(wo.ID, "architect", "clarification_asked", question, nil)
	kbText, audit := f.KB.Context(ctx, wo.ProjectID, string(model.AgentForeman), "", woQuery(wo)+" "+question, f.workspaceDir(wo.ID), wo.KBBaseSHA)
	var r foremanAnswerReply
	_, runID, err := f.runStructured(ctx, wo, model.AgentForeman, foremanAnswerPrompt(wo, facts, question)+kbText,
		map[string]string{"stage": "answer"}, "", &r, runOpts{Audit: audit, Planning: true, Prime: foremanPrime(wo)})
	if err != nil {
		return false, err
	}
	if r.Decision == "ESCALATE" {
		f.event(wo.ID, "foreman", "clarification_escalated", r.Reason, map[string]any{"run_id": runID})
		return false, f.askHuman(ctx, wo, "architect", question)
	}
	log := append(append([]model.Clarification{}, wo.Clarifications...), model.Clarification{Question: question, Answer: r.Answer})
	raw, _ := json.Marshal(log)
	rounds := wo.ClarifRounds + 1
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"clarif_log": string(raw), "clarif_rounds": rounds}); err != nil {
		return false, err
	}
	wo.Clarifications, wo.ClarifRounds = log, rounds
	f.event(wo.ID, "foreman", "clarification_answered", r.Answer, map[string]any{"question": question, "run_id": runID})
	return true, nil
}

// askHuman records a clarification question and parks the order in
// WAITING_FOR_HUMAN until AnswerClarification re-drafts the plan.
func (f *Factory) askHuman(ctx context.Context, wo *model.WorkOrder, actor, question string) error {
	f.closePlanning(wo.ID)
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
	return f.step(ctx, wo, model.StateArchitectReview, model.StateWaitingForHuman, "factory", "Waiting for human clarification")
}

// toReview advances whichever pre-review planning state the WO is in to
// ARCHITECT_REVIEW via SPEC_AND_PLAN_GENERATION.
func (f *Factory) toReview(ctx context.Context, wo *model.WorkOrder) error {
	if wo.State == model.StateDecomposition {
		if err := f.step(ctx, wo, model.StateDecomposition, model.StatePlanGeneration, "factory", "Generating specification and plan"); err != nil {
			return err
		}
	}
	return f.step(ctx, wo, model.StatePlanGeneration, model.StateArchitectReview, "factory", "Plan drafted; architect review")
}

func taskList(tasks []model.Task) string {
	var b strings.Builder
	for i, t := range tasks {
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, t.Title, t.Objective)
	}
	return b.String()
}
