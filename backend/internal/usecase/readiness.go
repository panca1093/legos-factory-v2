package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"legos-factory/backend/internal/model"
)

// handleEvaluate runs the done gate (SPEC section 7) while the order is still
// in SPECIALIST_REVIEW, so a fix can return to the Builder, then decides
// merge readiness. Without a Judge the gate is skipped and readiness stays
// the deterministic rule set.
func (f *Factory) handleEvaluate(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if wo.State == model.StateImplementing {
		return f.resumeDoneGateFix(ctx, wo) // a retry after a fix got only partway
	}
	if wo.State != model.StateSpecialistReview {
		return nil // superseded or already evaluated
	}
	var escalation []string
	if f.Judge != nil {
		fixed, note, err := f.doneGate(ctx, wo, lastAttempt(job))
		if err != nil || fixed {
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

// doneGate has the Foreman write validation notes, asks Jev and applies the done
// truth table. fixed reports that the order went back to the Builder; a
// non-empty escalation is the reason a human must decide.
func (f *Factory) doneGate(ctx context.Context, wo *model.WorkOrder, lastTry bool) (fixed bool, escalation []string, err error) {
	criteria := splitCriteria(wo.Acceptance)
	report := f.currentArchitectReport(wo)
	if len(criteria) == 0 || report == nil {
		note := "the Architect's verification is missing for this revision"
		if len(criteria) == 0 {
			note = "no acceptance criteria to judge against"
		}
		out := GateOutcome{Outcome: GateEscalate, Row: 2, Note: note}
		f.recordGate(wo.ID, "done", map[string]any{}, nil, JudgeResult{}, out, errors.New(note))
		return false, []string{note}, nil
	}
	checksTxt, checksFailed := f.checksText(ctx, wo)
	findings, err := f.Findings.List(ctx, wo.ID)
	if err != nil {
		return false, nil, err
	}
	var open []GateFinding
	var findingsTxt strings.Builder
	for _, fd := range findings {
		if fd.Status == model.FindingOpen || fd.Status == model.FindingConfirmed || fd.Status == model.FindingEscalated {
			open = append(open, GateFinding{Severity: fd.Severity, Path: fd.Location, Message: fd.Title + ": " + fd.Detail})
			fmt.Fprintf(&findingsTxt, "[%s] %s — %s (%s)\n", fd.Severity, fd.Title, fd.Detail, fd.Location)
		}
	}
	if findingsTxt.Len() == 0 {
		findingsTxt.WriteString("(none open)\n")
	}

	f.event(wo.ID, "foreman", "validation_started", "Foreman validating the finished work", nil)
	var v validationReply
	kbText, audit := f.KB.Context(ctx, wo.ProjectID, string(model.AgentForeman), "", woQuery(wo)+" "+wo.Spec, f.workspaceDir(wo.ID), wo.KBBaseSHA)
	_, runID, err := f.runStructured(ctx, wo, model.AgentForeman,
		foremanValidatePrompt(wo, findingsTxt.String(), reportText(*report), checksTxt)+kbText,
		map[string]string{"stage": "validate"}, "", &v, runOpts{Audit: audit, Prime: foremanPrime(wo)})
	if err != nil {
		if !errors.Is(err, model.ErrPermanent) && !lastTry {
			return false, nil, err // transient: the job requeues
		}
		note := "Foreman validation failed: " + err.Error()
		out := GateOutcome{Outcome: GateEscalate, Row: 2, Note: note}
		f.recordGate(wo.ID, "done", map[string]any{}, nil, JudgeResult{}, out, errors.New(note))
		return false, []string{note}, nil
	}
	f.Catalog.Record(ctx, wo, model.KnowledgePlanReview, model.TrustAIInterpretation, "foreman",
		"Foreman validation notes", v.Notes, "run:"+runID)
	f.event(wo.ID, "foreman", "foreman_validated", "Foreman validation notes ready", map[string]any{"notes": v.Notes, "run_id": runID})

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
	in := DoneGateInput{
		Criteria: criteria, Spec: wo.Spec, Diff: diff, Checks: gateChecks, OpenFindings: open,
		ArchitectVerdicts: report.Verdicts, Deviations: report.Deviations, ForemanNotes: v.Notes,
	}
	state, qs := doneGateState(in)
	res, over, jerr := f.askJudge(ctx, state, qs)
	out := decideDoneGate(in, qs, res.Answers, jerr, over, checksFailed, openBlockers, wo.ReviewFixes < MaxRevisionCycles)
	f.recordGate(wo.ID, "done", state, qs, res, out, jerr)

	switch out.Outcome {
	case GateFix:
		// state and revision bump land in one UPDATE: a retry that finds IMPLEMENTING completes the fix (resumeDoneGateFix)
		newRev, fixes := wo.Revision+1, wo.ReviewFixes+1
		if err := f.stepWith(ctx, wo, model.StateSpecialistReview, model.StateImplementing, "factory", "Done gate requested a fix; returning to builder",
			map[string]any{"revision": newRev, "commit_sha": "", "review_fixes": fixes}); err != nil {
			return false, nil, err
		}
		wo.Revision, wo.CommitSHA, wo.ReviewFixes = newRev, "", fixes
		note := out.Note + architectFixes(report)
		issues := slices.DeleteFunc(slices.Clone(out.Issues), func(s string) bool { return slices.Contains(out.InReport, s) })
		if strings.TrimSpace(v.Notes) != "" {
			issues = append(issues, "The Foreman's validation notes: "+v.Notes)
		}
		payload := map[string]any{"reason": "fix_done_gate", "note": note}
		evPayload := map[string]any{"revision": newRev, "reason": "fix_done_gate", "note": note}
		if b := fixBrief(openFindings(findings), report, issues, ""); b != "" {
			payload["brief"], evPayload["brief"] = b, b
		}
		f.event(wo.ID, "factory", "fix_requested", fmt.Sprintf("Revision %d: done gate requests a fix: %s", newRev, out.Note), evPayload)
		return true, nil, f.enqueuePayload(ctx, JobImplement, wo.ID, payload)
	case GateEscalate:
		if len(out.Issues) == 0 {
			return false, []string{out.Note}, nil
		}
		return false, out.Issues, nil
	}
	return false, nil, nil
}

// resumeDoneGateFix finishes a fix whose evaluate job failed after the order
// had already returned to IMPLEMENTING: it queues the Builder unless one is
// already queued.
func (f *Factory) resumeDoneGateFix(ctx context.Context, wo *model.WorkOrder) error {
	if f.Sched.HasActive(ctx, wo.ID, "", JobImplement) {
		return nil
	}
	note := f.doneGateNote(ctx, wo)
	if note == "" {
		note = "The done gate asked for a fix; its reasons are in the last done_gate event of the activity trail."
	}
	payload := map[string]any{"reason": "fix_done_gate", "note": note}
	if b := f.storedBrief(ctx, wo); b != "" {
		payload["brief"] = b
	}
	return f.enqueuePayload(ctx, JobImplement, wo.ID, payload)
}

// architectFixes lists the Architect's suggested fixes for unmet criteria, for the Builder's fix note.
func architectFixes(r *architectReport) string {
	var b strings.Builder
	for _, v := range r.Verdicts {
		if strings.ToLower(strings.TrimSpace(v.Status)) != "met" && v.Fix != "" {
			fmt.Fprintf(&b, "\n- %s: %s", v.CriterionID, v.Fix)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n\nThe Architect's suggested fixes:" + b.String()
}

// currentArchitectReport is the stored Architect verification when it was made on the order's current revision.
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
		reason = "The done gate needs a human decision: " + joinNotes(issues)
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
