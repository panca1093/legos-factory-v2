package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	escalation := ""
	if f.Judge != nil {
		fixed, note, err := f.doneGate(ctx, wo)
		if err != nil || fixed {
			return err
		}
		escalation = note
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
func (f *Factory) doneGate(ctx context.Context, wo *model.WorkOrder) (fixed bool, escalation string, err error) {
	criteria := splitCriteria(wo.Acceptance)
	report := f.currentArchitectReport(wo)
	if report == nil {
		out := GateOutcome{GateEscalate, 2, "the Architect's verification is missing for this revision"}
		f.recordGate(wo.ID, "done", map[string]any{}, nil, JudgeResult{}, out, errors.New(out.Note))
		return false, out.Note, nil
	}
	checksTxt, checksFailed := f.checksText(ctx, wo)
	findings, err := f.Findings.List(ctx, wo.ID)
	if err != nil {
		return false, "", err
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
		return false, "", err
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
		return false, "", err
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
		newRev, fixes := wo.Revision+1, wo.ReviewFixes+1
		if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"revision": newRev, "commit_sha": "", "review_fixes": fixes}); err != nil {
			return false, "", err
		}
		wo.Revision, wo.CommitSHA, wo.ReviewFixes = newRev, "", fixes
		f.event(wo.ID, "factory", "fix_requested", fmt.Sprintf("Revision %d: done gate requests a fix: %s", newRev, out.Note), nil)
		if err := f.step(ctx, wo, model.StateSpecialistReview, model.StateImplementing, "factory", "Done gate requested a fix; returning to builder"); err != nil {
			return false, "", err
		}
		return true, "", f.enqueuePayload(ctx, JobImplement, wo.ID, map[string]any{"reason": "fix_done_gate", "note": out.Note})
	case GateEscalate:
		return false, out.Note, nil
	}
	return false, "", nil
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
func (f *Factory) evaluateReadiness(ctx context.Context, wo *model.WorkOrder, verificationBlocked bool, escalation string) error {
	checkCount, err := f.Quality.CountForRevision(ctx, wo.ID, wo.Revision)
	if err != nil {
		return err
	}
	openBlockers, err := f.Findings.CountOpenBlockers(ctx, wo.ID)
	if err != nil {
		return err
	}
	decision, reason := "READY", "All checks passed on the current revision and no blocker findings remain."
	switch {
	case verificationBlocked || openBlockers > 0:
		decision = "BLOCKED"
		reason = fmt.Sprintf("Blocked: %d unresolved blocker finding(s) or failed verification.", openBlockers)
	case checkCount == 0:
		decision = "NEEDS_VERIFICATION"
		reason = "No verification evidence recorded for the current revision."
	case escalation != "":
		decision = "NEEDS_HUMAN_REVIEW"
		reason = "The done gate needs a human decision: " + escalation
	}
	f.event(wo.ID, "factory", "readiness_"+strings.ToLower(decision), reason, map[string]any{"decision": decision})
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
		f.Catalog.Completed(ctx, wo, "BLOCKED")
	}
	return nil
}
