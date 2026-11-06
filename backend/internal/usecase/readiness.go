package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"legos-factory/backend/internal/model"
)

// handleEvaluate runs the converge gate (contract-flow SPEC section 6) while
// the order is still in SPECIALIST_REVIEW, so appended tasks can return to the
// Builder, then decides merge readiness. Without a Judge the gate is skipped
// and readiness stays the deterministic rule set.
func (f *Factory) handleEvaluate(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if wo.State == model.StateImplementing {
		return f.resumeAppend(ctx, wo) // a retry after an append got only partway
	}
	if wo.State != model.StateSpecialistReview {
		return nil // superseded or already evaluated
	}
	var escalation []string
	if f.Judge != nil {
		handled, note, err := f.convergeGate(ctx, wo)
		if err != nil || handled {
			return err
		}
		escalation = note
	}
	// this inspection's findings were kept open for the gate; they are settled now
	if err := f.Findings.ResolveOpen(ctx, wo.ID); err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StateSpecialistReview, model.StatePRPreparation, "factory", "Preparing change summary"); err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StatePRPreparation, model.StateMergeReadiness, "factory", "Evaluating merge readiness"); err != nil {
		return err
	}
	return f.evaluateReadiness(ctx, wo, false, escalation)
}

// convergeGate asks Jev about the Architect's gaps and applies the converge
// truth table. handled reports that the order left SPECIALIST_REVIEW's
// readiness path (tasks appended, or parked for a human answer); a non-empty
// escalation is the reason a human must decide.
func (f *Factory) convergeGate(ctx context.Context, wo *model.WorkOrder) (handled bool, escalation []string, err error) {
	criteria := splitCriteria(wo.Acceptance)
	appends, err := f.convergeAppends(ctx, wo.ID)
	if err != nil {
		return false, nil, err
	}
	report := f.currentArchitectReport(wo)
	if report != nil && report.Round <= appends {
		report = nil // that round already acted on this report
	}
	if len(criteria) == 0 || report == nil {
		note := "the Architect's converge report is missing for this revision"
		if len(criteria) == 0 {
			note = "no acceptance criteria to judge against"
		}
		out := GateOutcome{Outcome: GateEscalate, Row: 2, Note: note}
		f.recordGate(wo.ID, "converge", map[string]any{}, nil, JudgeResult{}, out, errors.New(note))
		return false, []string{note}, nil
	}
	_, checksFailed := f.checksText(ctx, wo)
	findings, err := f.Findings.List(ctx, wo.ID)
	if err != nil {
		return false, nil, err
	}
	var open []GateFinding
	for _, fd := range openFindings(findings) {
		open = append(open, GateFinding{Severity: fd.Severity, Path: fd.Location, Message: fd.Title + ": " + fd.Detail})
	}
	diff, derr := f.Sandbox.Diff(ctx, f.workspaceDir(wo.ID))
	if derr != nil {
		f.event(wo.ID, "factory", "diff_unavailable", derr.Error(), nil)
	}
	checks, _ := f.latestChecks(ctx, wo)
	gateChecks := make([]GateCheck, len(checks))
	for i, c := range checks {
		gateChecks[i] = GateCheck{Name: c.Name, Passed: c.Outcome == string(model.Pass)}
	}
	openBlockers, err := f.Findings.CountOpenBlockers(ctx, wo.ID)
	if err != nil {
		return false, nil, err
	}
	tasks, err := f.WO.Plan(ctx, wo.ID)
	if err != nil {
		return false, nil, err
	}
	in := ConvergeGateInput{
		Criteria: criteria, Spec: wo.Spec, Tasks: gateTasks(tasks), Diff: diff, Checks: gateChecks,
		OpenFindings: open, Gaps: append(slices.Clone(report.Gaps), citedFindingGaps(findings, criteria)...),
	}
	state, qs := convergeGateState(in)
	res, over, jerr := f.askJudge(ctx, state, qs)
	out := decideConvergeGate(in, qs, res.Answers, jerr, over, checksFailed, openBlockers, appends < MaxConvergeAppends)
	f.recordGate(wo.ID, "converge", state, qs, res, out, jerr)

	if out.Outcome != GateConverged && out.Outcome != GateLoop {
		if err := f.setBuilderNotes(ctx, wo.ID, "converge", convergeNotes(in, out)); err != nil {
			return false, nil, err
		}
	}
	switch out.Outcome {
	case GateAppend:
		return true, nil, f.appendRound(ctx, wo, in, out, report)
	case GateEscalate:
		if len(out.Issues) == 0 {
			return false, []string{out.Note}, nil
		}
		return false, out.Issues, nil
	}
	return false, nil, nil
}

var citesMarker = regexp.MustCompile(`\(cites ([^)]*)\)$`)

// citedFindingGaps turn the open non-blocker findings that cite an acceptance criterion into
// gaps, so Jev, not the Inspector, decides whether they send work back.
func citedFindingGaps(findings []model.Finding, criteria []Criterion) []convergeGap {
	var out []convergeGap
	for _, fd := range openFindings(findings) {
		m := citesMarker.FindStringSubmatch(strings.TrimSpace(fd.Detail))
		if fd.Severity == model.SeverityBlocker || m == nil || !citesCriterion(m[1], criteria) {
			continue
		}
		out = append(out, convergeGap{ID: fd.ID, Type: "partial", Layer: "code", Cites: m[1], Evidence: fd.Location, Fix: fd.Title + ": " + fd.Detail})
	}
	return out
}

// convergeNotes are what the next Builder run should keep in mind without being blocked by it:
// the gaps that are not blocking and the blocking ones Jev could not settle.
func convergeNotes(in ConvergeGateInput, out GateOutcome) []string {
	blocking := map[string]bool{}
	for _, g := range blockingGaps(in.Gaps, in.Criteria) {
		blocking[g.ID] = true
	}
	var notes []string
	for _, g := range in.Gaps {
		if !blocking[g.ID] {
			notes = append(notes, gapLine(g))
		}
	}
	return append(notes, out.Builder...)
}

var evidencePath = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_./-]*\.[A-Za-z0-9]+`)

// evidencePaths are the file paths a gap's evidence names ("round.go:12" gives "round.go").
func evidencePaths(evidence string) []string {
	var out []string
	for _, p := range evidencePath.FindAllString(evidence, -1) {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// appendRound turns the valid gaps into work. A code gap becomes a new plan task; a
// contract gap amends the spec and, when its evidence names files, also becomes a task;
// a requirement gap goes to the Foreman, and to a human when the Foreman escalates.
// Earlier tasks are never rewritten. With tasks to build the order returns to the
// Builder, otherwise converge runs again on the amended contract.
func (f *Factory) appendRound(ctx context.Context, wo *model.WorkOrder, in ConvergeGateInput, out GateOutcome, report *architectReport) error {
	if fresh, err := f.WO.Get(ctx, wo.ID); err == nil {
		wo.Clarifications, wo.Spec = fresh.Clarifications, fresh.Spec
	}
	plan, err := f.WO.Plan(ctx, wo.ID)
	if err != nil {
		return err
	}
	var added []model.Task
	next := len(plan)
	addTask := func(key, title, objective string, scope []string) {
		if slices.ContainsFunc(plan, func(t model.Task) bool { return strings.HasPrefix(t.Title, key) }) {
			return // a retry of this round already appended it
		}
		id := func() string { return fmt.Sprintf("%s_t%d", wo.ID, next) }
		for next++; slices.ContainsFunc(plan, func(t model.Task) bool { return t.ID == id() }); next++ {
		}
		t := model.Task{ID: id(), WOID: wo.ID, Title: title, Objective: objective, Role: "builder", Scope: scope, Deps: []string{}}
		plan, added = append(plan, t), append(added, t)
	}
	covered := map[string]bool{}
	var contract, requirement []convergeGap
	for _, g := range in.Gaps {
		if !slices.Contains(out.Valid, g.ID) {
			continue
		}
		for _, c := range citedCriteria(g.Cites, in.Criteria) {
			covered[c] = true
		}
		objective := fmt.Sprintf("%s\nEvidence: %s\nGap %s (%s) against %s.", g.Fix, g.Evidence, g.ID, g.Type, g.Cites)
		key := "Close gap " + g.ID + ":"
		switch g.Layer {
		case "code":
			addTask(key, key+" "+truncateText(g.Fix, 60), objective, evidencePaths(g.Evidence))
		case "contract":
			contract = append(contract, g)
			if paths := evidencePaths(g.Evidence); len(paths) > 0 {
				addTask(key, key+" "+truncateText(g.Fix, 60), objective, paths)
			}
		default:
			requirement = append(requirement, g)
		}
	}
	for _, c := range in.Criteria {
		if slices.Contains(out.Unmet, c.ID) && !covered[c.ID] {
			key := fmt.Sprintf("Close criterion %s (C%d)", c.ID, report.Round)
			addTask(key, key, fmt.Sprintf("Acceptance criterion %s is not met by the change: %s\nImplement what it requires.", c.ID, c.Text), nil)
		}
	}
	if len(added) > 0 {
		if err := f.WO.AppendPlan(ctx, wo.ID, added); err != nil {
			return err
		}
	}
	for _, g := range contract {
		q := fmt.Sprintf("Converge gap %s (%s): what should the spec say?", g.ID, g.Cites)
		if findClarification(wo.Clarifications, q) != nil {
			continue
		}
		if err := f.recordBuildAnswer(ctx, wo, "", q, g.Fix, "converge found the contract "+g.Type+": "+g.Evidence, "architect", "", true); err != nil {
			return err
		}
	}
	tasks, err := f.WO.Plan(ctx, wo.ID)
	if err != nil {
		return err
	}
	done, err := f.Runs.CompletedBuilderTasks(ctx, wo.ID)
	if err != nil {
		return err
	}
	build := len(model.ReadyTasks(tasks, done)) > 0

	parked := false
	for _, g := range requirement {
		q := fmt.Sprintf("Converge gap %s (%s): the change %s the requirement as written (%s). Proposed resolution: %s. What should the requirement be?", g.ID, g.Cites, g.Type, g.Evidence, g.Fix)
		if findClarification(wo.Clarifications, q) != nil {
			continue
		}
		r, runID, err := f.askForeman(ctx, wo, "architect", q, "", repoFacts(ctx, f.workspaceDir(wo.ID)), false)
		if err != nil {
			return err
		}
		if r.Decision == "ANSWER" {
			if err := f.recordBuildAnswer(ctx, wo, "", q, r.Answer, r.Reason, "foreman", runID, true); err != nil {
				return err
			}
			continue
		}
		f.event(wo.ID, "foreman", "clarification_escalated", r.Reason, map[string]any{"run_id": runID})
		parked = true
		fields := map[string]any{"architect_report": ""}
		if build {
			fields["revision"], fields["commit_sha"] = wo.Revision+1, ""
		}
		if err := f.WO.UpdateFields(ctx, wo.ID, fields); err != nil {
			return err
		}
		if build {
			wo.Revision, wo.CommitSHA = wo.Revision+1, ""
		}
		// the human's answer resumes through resumeBuilder, which queues the new tasks or re-verifies
		wo.ClarifTask = "converge"
		if err := f.askHuman(ctx, wo, "architect", q); err != nil {
			return err
		}
		break
	}

	record := func() {
		f.event(wo.ID, "factory", "converge_append", fmt.Sprintf("Converge round %d: %d valid gap(s), %d task(s) appended", report.Round, len(out.Valid), len(added)),
			map[string]any{"round": report.Round, "gaps": out.Valid, "unmet": out.Unmet, "tasks": taskIDs(added), "revision": wo.Revision, "parked": parked})
	}
	switch {
	case parked:
		record()
		return nil
	case build:
		newRev := wo.Revision + 1
		if err := f.stepWith(ctx, wo, model.StateSpecialistReview, model.StateImplementing, "factory", "Converge appended tasks; returning to builder",
			map[string]any{"revision": newRev, "commit_sha": ""}); err != nil {
			return err
		}
		wo.Revision, wo.CommitSHA = newRev, ""
		record()
		return f.maybeAdvanceAfterImplementation(ctx, wo)
	}
	// nothing to build: converge alone judges the amended contract again, as the next round
	if err := f.enqueuePayload(ctx, JobInspect, wo.ID, map[string]any{"converge_only": true, "round": report.Round + 1}); err != nil {
		return err
	}
	record()
	return nil
}

func taskIDs(tasks []model.Task) []string {
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.ID
	}
	return ids
}

// resumeAppend finishes an append whose evaluate job failed after the order had
// already returned to IMPLEMENTING: it queues the new tasks unless a remediation
// build is already queued.
func (f *Factory) resumeAppend(ctx context.Context, wo *model.WorkOrder) error {
	if f.Sched.HasActive(ctx, wo.ID, "", JobImplement) {
		return nil
	}
	return f.maybeAdvanceAfterImplementation(ctx, wo)
}

// currentArchitectReport is the stored converge report when it was made on the order's current revision.
func (f *Factory) currentArchitectReport(wo *model.WorkOrder) *architectReport {
	if wo.ArchitectReport == "" {
		return nil
	}
	var r architectReport
	if json.Unmarshal([]byte(wo.ArchitectReport), &r) != nil || r.Revision != wo.Revision {
		return nil
	}
	return &r
}

// evaluateReadiness combines deterministic verification and review outcomes
// into the merge-readiness decision.
func (f *Factory) evaluateReadiness(ctx context.Context, wo *model.WorkOrder, verificationBlocked bool, escalation []string) error {
	checkCount, err := f.Quality.CountForRevision(ctx, wo.ID, wo.Revision)
	if err != nil {
		return err
	}
	openBlockers, err := f.Findings.CountOpenBlockers(ctx, wo.ID)
	if err != nil {
		return err
	}
	decision, reason := "READY", "All checks passed on the current revision and no blocker findings remain."
	var issues []string
	switch {
	case verificationBlocked || openBlockers > 0:
		decision = "BLOCKED"
		reason = fmt.Sprintf("Blocked: %d unresolved blocker finding(s) or failed verification.", openBlockers)
	case len(escalation) > 0:
		decision = "NEEDS_HUMAN_REVIEW"
		issues = escalation
		reason = "The converge gate needs a human decision: " + joinNotes(issues)
		if checkCount == 0 {
			reason += " (no verification evidence is recorded for the current revision either)"
		}
	case checkCount == 0:
		decision = "NEEDS_VERIFICATION"
		reason = "No verification evidence recorded for the current revision."
	}
	payload := map[string]any{"decision": decision}
	if len(issues) > 0 {
		payload["issues"] = issues
	}
	f.event(wo.ID, "factory", "readiness_"+strings.ToLower(decision), reason, payload)
	f.Catalog.Record(ctx, wo, model.KnowledgeReadiness, model.TrustVerified, "factory", "Merge readiness: "+decision, reason)
	// Final PR disposition stays human: evidence-complete orders land in
	// NEEDS_HUMAN_REVIEW; the human approve/decline endpoint decides READY.
	next := model.StateNeedsHumanReview
	if decision == "BLOCKED" {
		next = model.StateBlocked
	}
	if err := f.step(ctx, wo, model.StateMergeReadiness, next, "factory", "Merge readiness: "+decision); err != nil {
		return err
	}
	if next == model.StateBlocked {
		f.completeWO(ctx, wo, "BLOCKED")
	}
	return nil
}
