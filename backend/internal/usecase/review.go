package usecase

import (
	"context"
	"encoding/json"
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
	var prev []reviewFinding
	scoped := false
	if wo.PlanRevisions > 0 { // a re-review inside the same gate budget checks the last round's criticals only
		// a restart finds this round's own event: only the previous round's counts
		if e, err := f.WO.LatestEvent(ctx, wo.ID, "plan_reviewed"); err == nil && e != nil && payloadRevision(e.Payload) == wo.PlanRevisions-1 {
			prev, scoped = payloadFindings(e.Payload), true
		}
	}
	var r reviewReply
	kbText, audit := f.KB.Context(ctx, wo.ProjectID, string(model.AgentForeman), "", woQuery(wo)+" "+taskList(tasks), f.workspaceDir(wo.ID), wo.KBBaseSHA)
	_, runID, err := f.runStructured(ctx, wo, model.AgentForeman, foremanReviewPrompt(wo, tasks, criticals(prev), scoped)+kbText,
		map[string]string{"stage": "review", "clarification_answer": wo.ClarifAnswer},
		"", &r, runOpts{Audit: audit, Planning: true, Prime: foremanPrime(wo)})
	if errors.Is(err, model.ErrPermanent) {
		return f.escalateReview(ctx, wo, err)
	}
	if err != nil {
		return err
	}
	for i := range r.Findings { // ids come from the factory: the model reuses "F1" every round
		r.Findings[i].ID = fmt.Sprintf("R%d-F%d", wo.PlanRevisions+1, i+1)
	}
	findings := r.Findings
	if scoped {
		findings = mergeFindings(prev, r)
	}
	r.Findings = findings
	review := r.text()
	f.Catalog.Record(ctx, wo, model.KnowledgePlanReview, model.TrustAIInterpretation, "foreman",
		"Foreman plan review notes", review, "run:"+runID)
	f.event(wo.ID, "foreman", "plan_reviewed", "Foreman review notes ready", map[string]any{"notes": review, "findings": findings, "coverage": r.Coverage, "revision": wo.PlanRevisions, "run_id": runID})
	if asked, err := f.settleRequirementFindings(ctx, wo, findings, prev); asked || err != nil {
		return err
	}
	return f.planGate(ctx, wo, tasks, findings, r.Coverage)
}

// blockingFindings are the critical and high findings: the ones the Architect or a human has to act on.
func blockingFindings(fs []reviewFinding) []reviewFinding {
	var out []reviewFinding
	for _, f := range fs {
		if f.Severity == "critical" || f.Severity == "high" {
			out = append(out, f)
		}
	}
	return out
}

func criticals(fs []reviewFinding) []reviewFinding {
	var out []reviewFinding
	for _, f := range fs {
		if f.Severity == "critical" {
			out = append(out, f)
		}
	}
	return out
}

// payloadFindings reads the findings back from a stored plan_reviewed event.
func payloadFindings(payload map[string]any) []reviewFinding {
	raw, _ := json.Marshal(payload["findings"])
	var out []reviewFinding
	_ = json.Unmarshal(raw, &out)
	return out
}

// payloadRevision is the plan revision a stored plan_reviewed event belongs to; events without one read as -1.
func payloadRevision(payload map[string]any) int {
	switch v := payload["revision"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return -1
}

// mergeFindings is the analysis after a scoped re-review: the previous
// findings minus the criticals the Foreman marked resolved, plus new ones,
// criticals first, then this round's findings, then earlier ones, capped. A critical without a status stays unresolved.
func mergeFindings(prev []reviewFinding, r reviewReply) []reviewFinding {
	resolved := map[string]bool{}
	for _, s := range r.Resolved {
		resolved[s.ID] = s.Status == "resolved"
	}
	var crit, fresh, old []reviewFinding
	for i, f := range append(append([]reviewFinding{}, prev...), r.Findings...) {
		switch {
		case f.Severity == "critical" && resolved[f.ID]:
		case f.Severity == "critical":
			crit = append(crit, f)
		case i >= len(prev):
			fresh = append(fresh, f)
		default:
			old = append(old, f)
		}
	}
	return append(append(crit, fresh...), old...)[:min(maxReviewFindings, len(crit)+len(fresh)+len(old))]
}

const amendmentsHeader = "Amendments from plan review:"

// settleRequirementFindings handles the Foreman's own findings. A new
// requirement-layer critical finding amends the brief with its fix; a critical
// or high one asks the human (returns asked) when it carries a question that is
// neither answered nor over the human-question cap, a critical's question first.
// A high with only a fix stays a Builder note: nothing re-plans after an amendment.
func (f *Factory) settleRequirementFindings(ctx context.Context, wo *model.WorkOrder, findings, prev []reviewFinding) (asked bool, err error) {
	seen := map[string]bool{}
	for _, p := range prev {
		seen[p.ID] = true
	}
	var amends []string
	question, questionCritical := "", false
	for _, fd := range blockingFindings(findings) {
		if fd.Layer != "requirement" || seen[fd.ID] {
			continue
		}
		q := strings.TrimSpace(fd.Question)
		switch c := findClarification(wo.Clarifications, q); {
		case q == "":
		case c != nil:
			amends = append(amends, "Settled: "+q+" -> "+c.Answer)
			continue
		case humanAnswers(wo) >= maxHumanQuestions:
			amends = append(amends, "Unanswered, default taken: "+q)
		default: // a question for the human: its fix waits for the answer
			if question == "" || (fd.Severity == "critical" && !questionCritical) {
				question, questionCritical = q, fd.Severity == "critical"
			}
			continue
		}
		if fix := strings.TrimSpace(fd.Fix); fix != "" && fd.Severity == "critical" {
			amends = append(amends, fix)
		}
	}
	if len(amends) > 0 {
		if err := f.amendBrief(ctx, wo, amends); err != nil {
			return false, err
		}
	}
	if question != "" {
		return true, f.askHuman(ctx, wo, "foreman", question)
	}
	return false, nil
}

// amendBrief adds bullets to the brief's single amendments section, creating it on first use.
func (f *Factory) amendBrief(ctx context.Context, wo *model.WorkOrder, amends []string) error {
	var old []string
	head, tail := strings.TrimRight(wo.Brief, "\n"), ""
	if i := strings.Index(head, amendmentsHeader); i >= 0 {
		lines := strings.Split(strings.TrimPrefix(head[i+len(amendmentsHeader):], "\n"), "\n")
		n := 0
		for n < len(lines) && strings.HasPrefix(lines[n], "- ") {
			old = append(old, lines[n])
			n++
		}
		tail = strings.Join(lines[n:], "\n")
		head = strings.TrimRight(head[:i], "\n")
	}
	have := map[string]bool{}
	for _, o := range old {
		have[o] = true
	}
	for _, a := range amends {
		if line := "- " + a; !have[line] {
			old, have[line] = append(old, line), true
		}
	}
	brief := head + "\n\n" + amendmentsHeader + "\n" + strings.Join(old, "\n")
	if strings.TrimSpace(tail) != "" {
		brief += "\n\n" + strings.TrimSpace(tail)
	}
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"brief": brief}); err != nil {
		return err
	}
	wo.Brief = brief
	f.writeBriefDoc(ctx, wo)
	f.event(wo.ID, "foreman", "brief_amended", "Foreman amended the brief after plan review", map[string]any{"amendments": amends})
	if over := overBudget("brief", brief, briefWords); over != "" {
		f.event(wo.ID, "foreman", "budget_exceeded", "Brief accepted over budget: "+over, nil)
	}
	return nil
}

// planRevisionNotes rebuilds the Architect's revision note from the stored gate event of the revision that was requested.
func (f *Factory) planRevisionNotes(ctx context.Context, wo *model.WorkOrder) string {
	e, err := f.WO.LatestEvent(ctx, wo.ID, "plan_revision_requested")
	if err != nil || e == nil {
		return "Plan gate requested a revision; see the last plan_gate event in the activity trail."
	}
	if strings.HasPrefix(e.Message, "Plan gate:") {
		return e.Message
	}
	return "Plan gate: " + e.Message // events stored before the note carried its own header
}

// planFindingLine is one finding for the Architect or the human, cut to a readable length.
func planFindingLine(f reviewFinding) string {
	line := fmt.Sprintf("- %s [%s, %s, cites %s] %s Fix: %s", f.ID, f.Severity, f.Layer, strings.TrimSpace(f.Cites), strings.TrimSpace(f.Issue), strings.TrimSpace(f.Fix))
	if q := strings.TrimSpace(f.Question); q != "" {
		line += " Question for the human: " + q
	}
	return truncateText(line, 400)
}

// planReviseNote is the whole message the Architect gets for a revision: the gate's reasons plus the open contract findings.
func planReviseNote(revision int, reasons string, findings []reviewFinding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan gate: revise (revision %d of %d)\nWhy the gate asked for a revision:\n%s", revision, maxPlanRevisions, reasons)
	var lines []string
	for _, f := range blockingFindings(findings) {
		if f.Layer == "contract" {
			lines = append(lines, planFindingLine(f))
		}
	}
	if len(lines) > 0 {
		b.WriteString("\nOpen findings from the Foreman review to address (a finding the current draft already resolves needs no change):\n" + strings.Join(lines, "\n"))
	}
	b.WriteString("\nIf a point needs a decision only a human can make, answer with decision CLARIFY instead of guessing.")
	return b.String()
}

// planEscalationMessage tells the human what to decide: the gate's reasons and the open critical and high findings.
func planEscalationMessage(reason string, findings []reviewFinding) string {
	msg := "The plan needs a human decision: " + reason
	var lines []string
	for _, f := range blockingFindings(findings) {
		lines = append(lines, planFindingLine(f))
	}
	if len(lines) > 0 {
		msg += "\nOpen findings from the Foreman review:\n" + strings.Join(lines, "\n")
	}
	return msg
}

// builderNotesBy marks the clarification entries that carry the plan review's high and low notes to the Builder.
const builderNotesBy = "plan_review"

// storeBuilderNotes replaces the previous review notes in the clarification log with the current ones; the Builder prompt already prints that log.
func (f *Factory) storeBuilderNotes(ctx context.Context, wo *model.WorkOrder, findings []reviewFinding, gateNotes []string) error {
	log, hadNotes := []model.Clarification{}, false
	for _, c := range wo.Clarifications {
		if c.By == builderNotesBy {
			hadNotes = true
		} else {
			log = append(log, c)
		}
	}
	kept := len(log)
	for _, fd := range findings {
		if fd.Severity != "critical" {
			log = append(log, model.Clarification{Question: fmt.Sprintf("Note from plan review (%s, %s)", fd.Severity, strings.TrimSpace(fd.Cites)), Answer: strings.TrimSpace(fd.Issue) + " " + strings.TrimSpace(fd.Fix), By: builderNotesBy})
		}
	}
	for _, n := range gateNotes {
		log = append(log, model.Clarification{Question: "Note from plan gate", Answer: n, By: builderNotesBy})
	}
	if len(log) == kept && !hadNotes {
		return nil
	}
	raw, _ := json.Marshal(log)
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"clarif_log": string(raw)}); err != nil {
		return err
	}
	wo.Clarifications = log
	return nil
}

// setBuilderNotes replaces the notes a source (inspector, converge) left earlier in the
// clarification log with the given ones; the Builder prompt already prints that log.
func (f *Factory) setBuilderNotes(ctx context.Context, woID, source string, notes []string) error {
	fresh, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	title := "Note from " + source
	log, hadNotes := []model.Clarification{}, false
	for _, c := range fresh.Clarifications {
		if c.By == builderNotesBy && c.Question == title {
			hadNotes = true
		} else {
			log = append(log, c)
		}
	}
	if len(notes) == 0 && !hadNotes {
		return nil
	}
	for _, n := range notes {
		log = append(log, model.Clarification{Question: title, Answer: n, By: builderNotesBy})
	}
	raw, _ := json.Marshal(log)
	return f.WO.UpdateFields(ctx, woID, map[string]any{"clarif_log": string(raw)})
}

func (f *Factory) planGate(ctx context.Context, wo *model.WorkOrder, tasks []model.Task, findings []reviewFinding, coverage []reviewCoverage) error {
	in := PlanGateInput{
		Request:        wo.Title + "\n\n" + wo.Objective,
		Criteria:       splitCriteria(wo.Acceptance),
		Brief:          wo.Brief,
		Clarifications: planClarifications(wo.Clarifications),
		Spec:           wo.Spec,
		Tasks:          gateTasks(tasks),
		Findings:       findings,
		Coverage:       coverage,
		PlanRevisions:  wo.PlanRevisions,
	}
	state, qs := planGateState(in)
	res, over, jerr := f.askJudge(ctx, state, qs)
	out := decidePlanGate(in, qs, res.Answers, jerr, over)
	if !errors.Is(jerr, errNoJudge) {
		f.recordGate(wo.ID, "plan", state, qs, res, out, jerr)
	}
	switch out.Outcome {
	case GateApprove:
		if err := f.storeBuilderNotes(ctx, wo, findings, out.Builder); err != nil {
			return err
		}
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
		note := planReviseNote(revisions, out.Note, findings)
		f.event(wo.ID, "factory", "plan_revision_requested", note, map[string]any{"revision": revisions})
		return f.enqueuePayload(ctx, JobPlan, wo.ID, map[string]any{"notes": note})
	default: // escalate: the order stays in ARCHITECT_REVIEW and the human approves or revises
		if err := f.storeBuilderNotes(ctx, wo, findings, out.Builder); err != nil {
			return err
		}
		if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"plan_review": "approved"}); err != nil {
			return err
		}
		msg := planEscalationMessage(out.Note, findings)
		if errors.Is(jerr, errNoJudge) {
			msg = "Jev is not configured; the plan needs human approval"
		}
		f.event(wo.ID, "factory", "plan_escalated", msg, map[string]any{"reason": out.Note, "row": out.Row, "findings": blockingFindings(findings)})
		return nil
	}
}

func planClarifications(all []model.Clarification) []model.Clarification {
	var out []model.Clarification
	for _, c := range all {
		if c.By != builderNotesBy {
			out = append(out, c)
		}
	}
	return out
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
	j := f.judge()
	switch {
	case j == nil:
		return res, false, errNoJudge
	case overLimit(state, qs):
		return res, true, nil
	}
	res, err = j.Ask(ctx, state, qs)
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

// escalateReview hands the plan to the human when the Foreman review cannot produce a valid reply; otherwise the failed job would leave the order in ARCHITECT_REVIEW with nothing queued.
func (f *Factory) escalateReview(ctx context.Context, wo *model.WorkOrder, cause error) error {
	if f.cancelled(ctx, wo.ID) {
		return nil
	}
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"plan_review": "approved"}); err != nil {
		return err
	}
	reason := "Foreman review failed: " + cause.Error()
	f.event(wo.ID, "factory", "plan_escalated", "The plan needs a human decision: "+reason, map[string]any{"reason": reason, "row": 1})
	return nil
}
