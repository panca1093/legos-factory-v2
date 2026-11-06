package usecase

import (
	"context"
	"encoding/json"
	"errors"
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
	if (wo.State == model.StateRequirement && wo.Brief == "") || wo.IntakePending {
		if err := f.intake(ctx, wo, facts); err != nil || wo.State == model.StateWaitingForHuman {
			return err
		}
	}
	if wo.State == model.StateRequirement {
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
	if ids, ok := audit["kb_docs"].([]string); ok {
		for _, d := range prior {
			ids = append(ids, d.ID)
		}
		audit["kb_docs"] = ids
	}
	notes, _ := job.Payload["notes"].(string)
	prev, err := f.WO.Plan(ctx, wo.ID)
	if err != nil {
		return err
	}
	var plan planReply
	contractTests, err := f.contractTestsOn(ctx, wo)
	if err != nil {
		return err
	}
	prompt := planPrompt(wo, notes, facts, prev)
	if contractTests {
		prompt += contractTestsPlanRule
	}
	prompt += f.priorKnowledgePrompt(ctx, prior) + kbText
	extra := map[string]string{"clarification_answer": wo.ClarifAnswer, "clarification_log": clarificationLog(wo)}
	_, runID, err := f.runStructured(ctx, wo, model.AgentArchitect, prompt, extra, "", &plan, runOpts{Audit: audit})
	if err != nil {
		return err
	}
	if over := planOverBudget(&plan); over != "" {
		var shorter planReply
		_, id, err := f.runStructured(ctx, wo, model.AgentArchitect, prompt+rewriteAsk(over, plan), extra, "", &shorter, runOpts{Audit: audit})
		if err := rewriteErr(err); err != nil {
			return err
		}
		if err == nil && shorter.Decision == "APPROVE" {
			plan, runID = shorter, id
		}
		if over = planOverBudget(&plan); over != "" {
			f.event(wo.ID, "architect", "budget_exceeded", "Plan accepted over budget: "+over, nil)
		}
	}
	f.recordCodeFacts(ctx, wo, model.AgentArchitect, plan.CodeFacts)
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
	if contractTests {
		if err := requireContractTask(wo.ID, tasks); err != nil {
			return fmt.Errorf("%w: %v", model.ErrPermanent, err)
		}
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
		if list[i].By != builderNotesBy && strings.ToLower(strings.TrimSpace(list[i].Question)) == q {
			return &list[i]
		}
	}
	return nil
}

// maxForemanAnswers is how many Architect questions the Foreman may answer
// before the next one goes to a human.
const maxForemanAnswers = 2

// Word budgets for the artifacts agents write (docs/contract-flow/SPEC.md); the check allows 20% slack on the "~" figures.
const (
	briefWords = 350
	specWords  = 750
	taskWords  = 150
	// maxHumanQuestions is how many questions one work order may put to a human.
	maxHumanQuestions = 5
)

func overBudget(label, text string, budget int) string {
	if n := len(strings.Fields(text)); n*5 > budget*6 {
		return fmt.Sprintf("%s is %d words (budget %d)", label, n, budget)
	}
	return ""
}

func planOverBudget(p *planReply) string {
	if p.Decision != "APPROVE" {
		return ""
	}
	over := []string{overBudget("spec", p.Spec, specWords)}
	for i, t := range p.Tasks {
		over = append(over, overBudget(fmt.Sprintf("task %d objective", i+1), t.Objective, taskWords))
	}
	var out []string
	for _, o := range over {
		if o != "" {
			out = append(out, o)
		}
	}
	return strings.Join(out, "; ")
}

func rewriteAsk(over string, draft any) string {
	raw, _ := json.Marshal(draft)
	return "\n\nYour draft is over budget: " + over + ". Rewrite it within the budget: cut detail, keep every decision, rule and open question. Your draft:\n" +
		string(raw) + "\nReply with ONLY the JSON object."
}

// rewriteErr keeps a rewrite failure from hiding a budget cap or a cancel; only an invalid reply is ignored.
func rewriteErr(err error) error {
	if errors.Is(err, errReplyInvalid) {
		return nil
	}
	return err
}

func humanAnswers(wo *model.WorkOrder) int {
	n := 0
	for _, c := range wo.Clarifications {
		if c.By == "human" {
			n++
		}
	}
	return n
}

func unresolved(qs []openQuestion) []string {
	out := make([]string, 0, len(qs))
	for _, q := range qs {
		out = append(out, "Unanswered, default taken: "+q.ask())
	}
	return out
}

// intake has the Foreman turn the request into the brief the Architect plans from. With open questions
// it parks the order for a human, one question at a time; the answer re-enters here and updates the brief.
func (f *Factory) intake(ctx context.Context, wo *model.WorkOrder, facts string) error {
	f.event(wo.ID, "foreman", "intake_started", "Foreman briefing the Architect", nil)
	kbText, audit := f.KB.Context(ctx, wo.ProjectID, string(model.AgentForeman), "", woQuery(wo), f.workspaceDir(wo.ID), wo.KBBaseSHA)
	var r intakeReply
	prompt := intakePrompt(wo, facts) + kbText
	extra := map[string]string{"stage": "intake"}
	opts := runOpts{Audit: audit, Planning: true, Prime: foremanPrime(wo)}
	_, runID, err := f.runStructured(ctx, wo, model.AgentForeman, prompt, extra, "", &r, opts)
	if err != nil {
		return err
	}
	if over := overBudget("brief", r.text(), briefWords); over != "" {
		var shorter intakeReply
		// the Foreman's planning session already holds the intake prompt
		_, id, err := f.runStructured(ctx, wo, model.AgentForeman, rewriteAsk(over, r), extra, "", &shorter, opts)
		if err := rewriteErr(err); err != nil {
			return err
		}
		if err == nil {
			r, runID = shorter, id
		}
		if over = overBudget("brief", r.text(), briefWords); over != "" {
			f.event(wo.ID, "foreman", "budget_exceeded", "Brief accepted over budget: "+over, nil)
		}
	}
	var asks []openQuestion
	for _, q := range r.OpenQuestions {
		if c := findClarification(wo.Clarifications, q.ask()); c != nil {
			r.Assumptions = append(r.Assumptions, "Settled: "+q.Question+" -> "+c.Answer)
		} else {
			asks = append(asks, q)
		}
	}
	r.OpenQuestions = asks
	if humanAnswers(wo) >= maxHumanQuestions {
		r.Assumptions = append(r.Assumptions, unresolved(r.OpenQuestions)...)
		r.OpenQuestions = nil
	}
	brief := r.text()
	pending := 0
	if len(r.OpenQuestions) > 0 {
		pending = 1
	}
	f.recordCodeFacts(ctx, wo, model.AgentForeman, r.CodeFacts)
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"brief": brief, "intake_pending": pending}); err != nil {
		return err
	}
	wo.Brief, wo.IntakePending = brief, pending == 1
	f.writeBriefDoc(ctx, wo)
	f.event(wo.ID, "foreman", "brief_ready", "Foreman brief ready", map[string]any{"brief": brief, "run_id": runID})
	if len(r.OpenQuestions) == 0 {
		return nil
	}
	if wo.State == model.StateRequirement {
		if err := f.step(ctx, wo, model.StateRequirement, model.StateDecomposition, "factory", "Brief needs a human answer"); err != nil {
			return err
		}
	}
	return f.askHuman(ctx, wo, "foreman", r.OpenQuestions[0].ask())
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
	r, runID, err := f.askForeman(ctx, wo, "architect", question, "", facts, true)
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

// askForeman puts an agent's question to the Foreman; note is extra context for the prompt only.
// Only planning questions share the Foreman's planning session; parallel Builders each get their own run.
func (f *Factory) askForeman(ctx context.Context, wo *model.WorkOrder, actor, question, note, facts string, planning bool) (foremanAnswerReply, string, error) {
	f.event(wo.ID, actor, "clarification_asked", question, nil)
	kbText, audit := f.KB.Context(ctx, wo.ProjectID, string(model.AgentForeman), "", woQuery(wo)+" "+question, f.workspaceDir(wo.ID), wo.KBBaseSHA)
	opts := runOpts{Audit: audit}
	if planning {
		opts.Planning, opts.Prime = true, foremanPrime(wo)
	}
	var r foremanAnswerReply
	_, runID, err := f.runStructured(ctx, wo, model.AgentForeman, foremanAnswerPrompt(wo, facts, question+note)+kbText,
		map[string]string{"stage": "answer"}, "", &r, opts)
	return r, runID, err
}

// askHuman records a clarification question and parks the order in
// WAITING_FOR_HUMAN until AnswerClarification re-drafts the plan, or resumes
// the Builder task in wo.ClarifTask.
func (f *Factory) askHuman(ctx context.Context, wo *model.WorkOrder, actor, question string) error {
	f.closePlanning(wo.ID)
	fields := map[string]any{"clarif_question": question, "clarif_answer": "", "clarif_task": wo.ClarifTask}
	if wo.ClarifTask == "" {
		fields["plan_review"] = ""
	}
	if err := f.WO.UpdateFields(ctx, wo.ID, fields); err != nil {
		return err
	}
	wo.ClarifQuestion, wo.ClarifAnswer = question, ""
	f.event(wo.ID, actor, "clarification_requested", question, nil)
	if wo.ClarifTask != "" {
		return f.step(ctx, wo, wo.State, model.StateWaitingForHuman, "factory", "Waiting for human clarification") // IMPLEMENTING, or SPECIALIST_REVIEW for a converge question
	}
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
