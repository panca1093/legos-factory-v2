package usecase

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"legos-factory/backend/internal/model"
)

// Plan review (SPEC section 4): the Foreman agent writes review notes, then
// the plan gate decides in code from Jev's typed answers (gates.go): approve
// starts the build, revise sends the plan back to the Architect (bounded by
// maxPlanRevisions), escalate leaves it for a human.

func (f *Factory) handleReview(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	defer f.closePlanning(wo.ID) // the plan review ends the Foreman's planning session, whatever the gate decides
	if wo.State == model.StatePlanning {
		// a revision got as far as the state change before a failure: finish it
		if f.Sched.HasActive(ctx, wo.ID, "", JobPlan) {
			return nil
		}
		if wo.PlanReview == "pending" {
			if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"plan_review": ""}); err != nil {
				return err
			}
		}
		return f.enqueuePayload(ctx, JobPlan, wo.ID, map[string]any{"notes": f.planRevisionNotes(ctx, wo)})
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
	kbText, audit := f.KB.Context(ctx, wo.ProjectID, string(model.AgentForeman), "", woQuery(wo)+" "+taskList(tasks), f.workspaceDir(wo.ID), wo.KBBaseSHA)
	_, runID, err := f.runStructured(ctx, wo, model.AgentForeman, foremanReviewPrompt(wo, tasks)+kbText,
		map[string]string{"stage": "review", "clarification_answer": wo.ClarifAnswer},
		"", &r, runOpts{Audit: audit, Planning: true, Prime: foremanPrime(wo)})
	if err != nil {
		return err
	}
	review := r.text()
	f.Catalog.Record(ctx, wo, model.KnowledgePlanReview, model.TrustAIInterpretation, "foreman",
		"Foreman plan review notes", review, "run:"+runID)
	f.event(wo.ID, "foreman", "plan_reviewed", "Foreman review notes ready", map[string]any{"notes": review, "run_id": runID})
	return f.planGate(ctx, wo, tasks, review)
}

// planRevisionNotes rebuilds the Architect's revision note from the stored gate and Foreman events of the revision that was requested.
func (f *Factory) planRevisionNotes(ctx context.Context, wo *model.WorkOrder) string {
	e, err := f.WO.LatestEvent(ctx, wo.ID, "plan_revision_requested")
	if err != nil || e == nil {
		return "Plan gate requested a revision; see the last plan_gate event in the activity trail."
	}
	notes := "Plan gate: " + e.Message
	if r, err := f.WO.LatestEvent(ctx, wo.ID, "plan_reviewed"); err == nil && r != nil {
		if s, _ := r.Payload["notes"].(string); s != "" {
			notes += "\n\nForeman review: " + s
		}
	}
	return notes
}

func (f *Factory) planGate(ctx context.Context, wo *model.WorkOrder, tasks []model.Task, review string) error {
	in := PlanGateInput{
		Request:       wo.Title + "\n\n" + wo.Objective,
		Criteria:      splitCriteria(wo.Acceptance),
		Brief:         wo.Brief,
		Spec:          wo.Spec,
		Tasks:         gateTasks(tasks),
		ForemanReview: review,
		PlanRevisions: wo.PlanRevisions,
	}
	state, qs := planGateState(in)
	res, over, jerr := f.askJudge(ctx, state, qs)
	out := decidePlanGate(in, qs, res.Answers, jerr, over)
	if !errors.Is(jerr, errNoJudge) {
		f.recordGate(wo.ID, "plan", state, qs, res, out, jerr)
	}
	switch out.Outcome {
	case GateApprove:
		if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"plan_review": "approved"}); err != nil {
			return err
		}
		return f.approvePlan(ctx, wo, "factory", "Plan gate approved the plan; build starts")
	case GateRevise:
		revisions := wo.PlanRevisions + 1
		if err := f.stepWith(ctx, wo, model.StateArchitectReview, model.StatePlanning, "factory", "Plan gate requested a plan revision",
			map[string]any{"plan_revisions": revisions, "plan_review": "", "clarif_rounds": 0}); err != nil {
			return err
		}
		f.event(wo.ID, "factory", "plan_revision_requested", out.Note, map[string]any{"revision": revisions})
		return f.enqueuePayload(ctx, JobPlan, wo.ID, map[string]any{"notes": "Plan gate: " + out.Note + "\n\nForeman review: " + review})
	default: // escalate: the order stays in ARCHITECT_REVIEW and the human approves or revises
		if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"plan_review": "approved"}); err != nil {
			return err
		}
		msg := "The plan needs a human decision: " + out.Note
		if errors.Is(jerr, errNoJudge) {
			msg = "Jev is not configured; the plan needs human approval"
		}
		f.event(wo.ID, "factory", "plan_escalated", msg, map[string]any{"reason": out.Note, "row": out.Row})
		return nil
	}
}

// gateTasks numbers the plan t1..tn for Jev; stored task ids carry the work order id.
func gateTasks(tasks []model.Task) []PlanTask {
	ids := make(map[string]string, len(tasks))
	for i, t := range tasks {
		ids[t.ID] = fmt.Sprintf("t%d", i+1)
	}
	out := make([]PlanTask, len(tasks))
	for i, t := range tasks {
		deps := make([]string, 0, len(t.Deps))
		for _, d := range t.Deps {
			if id, ok := ids[d]; ok {
				deps = append(deps, id)
			}
		}
		out[i] = PlanTask{ID: ids[t.ID], Title: t.Title, Objective: t.Objective, Scope: t.Scope, Deps: deps}
	}
	return out
}

// askJudge sends one Jev request. Without a Judge it returns errNoJudge, and
// over the context limit it sends nothing and reports over.
func (f *Factory) askJudge(ctx context.Context, state any, qs map[string]Noul) (res JudgeResult, over bool, err error) {
	switch {
	case f.Judge == nil:
		return res, false, errNoJudge
	case overLimit(state, qs):
		return res, true, nil
	}
	res, err = f.Judge.Ask(ctx, state, qs)
	return res, false, err
}

// recordGate stores the gate's audit event with the exact Jev request; the
// live copy drops the request, which can be 100KB of plan or diff.
func (f *Factory) recordGate(woID, gate string, state any, qs map[string]Noul, res JudgeResult, out GateOutcome, jerr error) {
	full := gateEvent(gate, state, qs, res, out, jerr)
	live := maps.Clone(full)
	delete(live, "request")
	delete(live, "unsent_request")
	msg := fmt.Sprintf("%s gate: %s (row %d)", strings.ToUpper(gate[:1])+gate[1:], out.Outcome, out.Row)
	f.eventLive(woID, "jev", gate+"_gate", msg, full, live)
}
