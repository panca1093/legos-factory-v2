package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

// Inspector: deterministic verification and specialist review findings.

func (f *Factory) handleVerify(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	profile, err := f.profile(ctx, wo)
	if err != nil {
		return err
	}
	f.event(wo.ID, "inspector", "verification_started",
		fmt.Sprintf("Running %d deterministic checks (revision %d)", len(profile.Checks), wo.Revision), nil)
	mu := f.wsLock(wo.ID)
	mu.Lock()
	f.runSetup(ctx, wo, profile, f.workspaceDir(wo.ID), "inspector")
	mu.Unlock()
	failed := ""
	var failedResults []model.CheckResult
	for _, c := range profile.Checks {
		r := f.Sandbox.RunCheck(ctx, f.workspaceDir(wo.ID), c, f.QualTimeout)
		if err := f.saveCheck(ctx, wo, c, r); err != nil {
			return err
		}
		f.event(wo.ID, "inspector", "check_"+strings.ToLower(string(r.Outcome)),
			fmt.Sprintf("%s → %s", c.Name, r.Outcome), map[string]any{"command": c.Command})
		if r.Outcome != model.Pass && failed == "" {
			failed = c.Name
		}
		if r.Outcome != model.Pass {
			failedResults = append(failedResults, r)
		}
	}
	if failed != "" {
		if wo.VerifyFailures >= MaxRevisionCycles {
			f.event(wo.ID, "factory", "escalated", "Verification failed after max revision cycles; blocking", nil)
			if err := f.step(ctx, wo, model.StateVerifying, model.StateSpecialistReview, "factory", "Final evaluation after exhausted retries"); err != nil {
				return err
			}
			if err := f.step(ctx, wo, model.StateSpecialistReview, model.StatePRPreparation, "factory", "Preparing change summary"); err != nil {
				return err
			}
			if err := f.step(ctx, wo, model.StatePRPreparation, model.StateMergeReadiness, "factory", "Evaluating merge readiness"); err != nil {
				return err
			}
			return f.evaluateReadiness(ctx, wo, true, "")
		}
		newRev, failures := wo.Revision+1, wo.VerifyFailures+1
		if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"revision": newRev, "commit_sha": "", "verify_failures": failures}); err != nil {
			return err
		}
		wo.Revision, wo.CommitSHA, wo.VerifyFailures = newRev, "", failures
		if err := f.step(ctx, wo, model.StateVerifying, model.StateImplementing, "factory",
			fmt.Sprintf("Check %q failed; returning to builder (attempt %d of %d)", failed, failures, MaxRevisionCycles)); err != nil {
			return err
		}
		_, err := f.Sched.Enqueue(ctx, JobImplement, wo.ID, "", map[string]any{
			"reason": "fix_failed_verification", "failed_checks": failedChecksSection(failedResults),
		})
		return err
	}
	if len(profile.Checks) == 0 {
		f.event(wo.ID, "factory", "verification_skipped", "No checks detected in the repository; add .legos/checks.json to define them", nil)
	} else {
		f.event(wo.ID, "inspector", "verification_passed", "All deterministic checks passed", nil)
	}
	if err := f.commitVerified(ctx, wo); err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StateVerifying, model.StateSpecialistReview, "factory", "Verification passed; specialist review"); err != nil {
		if wo.State != model.StateSpecialistReview {
			return err
		}
	}
	return f.enqueue(ctx, JobInspect, wo.ID, "")
}

const (
	maxFailedChecks    = 3
	maxFailedOutputLen = 3000
)

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// failedChecksSection pre-renders to a string because job payloads round-trip through JSON.
func failedChecksSection(failures []model.CheckResult) string {
	var b strings.Builder
	for i, r := range failures {
		if i == maxFailedChecks {
			fmt.Fprintf(&b, "(%d more failing checks omitted)\n", len(failures)-i)
			break
		}
		exit := "n/a"
		if r.ExitCode != nil {
			exit = fmt.Sprint(*r.ExitCode)
		}
		note := ""
		if r.Outcome != model.Fail {
			// timeouts and exec errors are reported on the first output line, which the tail cut can drop
			note = " - " + strings.TrimSpace(strings.SplitN(strings.TrimSpace(ansiEscape.ReplaceAllString(r.Output, "")), "\n", 2)[0])
		}
		fmt.Fprintf(&b, "Check %q: %s%s (exit code %s)\nCommand: %s\nOutput (tail):\n%s\n\n",
			r.Name, r.Outcome, note, exit, r.Command, outputTail(r.Output))
	}
	return strings.TrimSpace(b.String())
}

// retryPayload keeps the failing checks in front of the Builder when a human retries after a failed verification.
func (f *Factory) retryPayload(ctx context.Context, wo *model.WorkOrder) map[string]any {
	payload := map[string]any{"reason": "human_retry"}
	if note := f.doneGateNote(ctx, wo); note != "" {
		return map[string]any{"reason": "fix_done_gate", "note": note}
	}
	if wo.VerifyFailures == 0 || wo.Revision < 1 {
		return payload
	}
	stored, err := f.Quality.ForRevision(ctx, wo.ID, wo.Revision-1)
	if err != nil {
		return payload
	}
	// the revision also holds the pre-change baseline and re-run duplicates; only each check's latest run counts
	latest := map[string]int{}
	for i, c := range stored {
		latest[c.Name] = i
	}
	var failed []model.CheckResult
	for i, c := range stored {
		if latest[c.Name] != i || c.Outcome == string(model.Pass) {
			continue
		}
		r := model.CheckResult{Name: c.Name, Command: c.Command, Outcome: model.Outcome(c.Outcome), Output: c.Output}
		if c.ExitCode >= 0 {
			r.ExitCode = &c.ExitCode
		}
		failed = append(failed, r)
	}
	if len(failed) == 0 {
		return payload
	}
	return map[string]any{"reason": "fix_failed_verification", "failed_checks": failedChecksSection(failed)}
}

// doneGateNote is the note of the done-gate fix that opened the order's current revision, or "".
func (f *Factory) doneGateNote(ctx context.Context, wo *model.WorkOrder) string {
	e, err := f.WO.LatestEvent(ctx, wo.ID, "fix_requested")
	if err != nil || e == nil || e.Payload["reason"] != "fix_done_gate" {
		return ""
	}
	if rev, _ := e.Payload["revision"].(float64); int(rev) != wo.Revision {
		return ""
	}
	note, _ := e.Payload["note"].(string)
	return note
}

func outputTail(out string) string {
	out = strings.TrimSpace(ansiEscape.ReplaceAllString(out, ""))
	if len(out) <= maxFailedOutputLen {
		return out
	}
	out = out[len(out)-maxFailedOutputLen:]
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[i+1:]
	}
	return "…\n" + out
}

func (f *Factory) handleInspect(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	files, err := f.Sandbox.ChangedFiles(ctx, f.workspaceDir(wo.ID))
	if err != nil {
		f.event(wo.ID, "inspector", "changed_files_unavailable", err.Error(), nil)
	}
	picks, routing := f.reviewers(ctx, wo, files)
	names := make([]string, 0, len(picks))
	for _, p := range picks {
		names = append(names, p.Specialty)
	}
	f.event(wo.ID, "factory", "inspection_specialists",
		fmt.Sprintf("Specialist review by %s (%d changed files)", strings.Join(names, ", "), len(files)),
		map[string]any{"specialists": picks, "routing": routing})

	// With Jev on, the Architect checks the result against its spec while the
	// specialists review the diff. Without Jev nothing reads the verdicts.
	var wg sync.WaitGroup
	var archErr error
	if f.Judge != nil {
		actx, cancel := context.WithCancel(ctx)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					f.event(wo.ID, "architect", "architect_verify_failed", fmt.Sprintf("verification panicked: %v", r), nil)
				}
			}()
			archErr = f.architectVerify(actx, *wo, files)
		}()
		// an early return must neither leave the verification running nor wait for it to finish
		defer func() {
			cancel()
			wg.Wait()
		}()
	}

	var runIDs []string
	blockers, total := 0, 0
	for _, p := range picks {
		var out findingsReply
		kbText, audit := f.KB.Context(ctx, wo.ProjectID, string(model.AgentInspector), p.Specialty,
			woQuery(wo)+" "+strings.Join(files, " ")+" "+p.Specialty, f.workspaceDir(wo.ID), wo.KBBaseSHA)
		_, runID, err := f.runStructured(ctx, wo, model.AgentInspector, inspectPrompt(wo, p, files)+kbText,
			map[string]string{"specialty": p.Specialty}, "", &out, runOpts{Audit: audit})
		if err != nil {
			return err
		}
		runIDs = append(runIDs, runID)
		var summary strings.Builder
		specBlockers := 0
		for _, fd := range out.Findings {
			id := fmt.Sprintf("fnd_%d", time.Now().UnixNano())
			if err := f.Findings.Create(ctx, model.Finding{
				ID: id, RunID: runID, WOID: wo.ID, Severity: fd.Severity, Title: fd.Title,
				Detail: fd.Detail, Location: fd.Location, Status: model.FindingOpen, Specialty: p.Specialty,
			}); err != nil {
				return err
			}
			fmt.Fprintf(&summary, "[%s] %s — %s (%s)\n", fd.Severity, fd.Title, fd.Detail, fd.Location)
			f.event(wo.ID, "inspector", "finding", fmt.Sprintf("[%s/%s] %s", p.Specialty, fd.Severity, fd.Title), map[string]any{
				"finding_id": id, "severity": fd.Severity, "location": fd.Location, "specialty": p.Specialty,
			})
			if fd.Severity == model.SeverityBlocker {
				specBlockers++
			}
		}
		blockers += specBlockers
		total += len(out.Findings)
		title := fmt.Sprintf("%s inspection of revision %d: %d finding(s), %d blocker(s)", p.Specialty, wo.Revision, len(out.Findings), specBlockers)
		f.Catalog.Record(ctx, wo, model.KnowledgeFindings, model.TrustAIInterpretation, "inspector", title, summary.String(), "run:"+runID)
	}
	wg.Wait()
	if archErr != nil && blockers == 0 {
		// retry only the verification: requeueing the job would re-run every specialist
		archErr = f.retryArchitectVerify(ctx, wo, files, archErr)
	}
	if archErr != nil {
		f.event(wo.ID, "architect", "architect_verify_failed", archErr.Error(), nil)
	}
	f.event(wo.ID, "inspector", "inspection_finished", fmt.Sprintf("%d finding(s), %d blocker(s)", total, blockers), nil)

	if blockers == 0 {
		// a later clean inspection supersedes earlier findings; this inspection's own
		// stay open for the done gate, which resolves them once it has decided
		if err := f.Findings.ResolveOpenExcept(ctx, wo.ID, runIDs); err != nil {
			return err
		}
		return f.enqueue(ctx, JobEvaluate, wo.ID, "")
	}
	if wo.ReviewFixes >= MaxRevisionCycles {
		f.event(wo.ID, "factory", "escalated", "Blocker findings remain after max revision cycles; escalating to human", nil)
		if err := f.step(ctx, wo, model.StateSpecialistReview, model.StateWaitingForHuman, "factory", "Escalated: blocker findings unresolved"); err != nil {
			return err
		}
		return nil
	}
	newRev, fixes := wo.Revision+1, wo.ReviewFixes+1
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"revision": newRev, "commit_sha": "", "review_fixes": fixes}); err != nil {
		return err
	}
	wo.Revision, wo.CommitSHA, wo.ReviewFixes = newRev, "", fixes
	f.event(wo.ID, "factory", "fix_requested", fmt.Sprintf("Revision %d: requesting bounded remediation", newRev), nil)
	if err := f.step(ctx, wo, model.StateSpecialistReview, model.StateImplementing, "factory", "Fix required; returning to builder"); err != nil {
		return err
	}
	// the fix runs as a builder job; its completion drives → VERIFYING again
	return f.enqueue(ctx, JobImplement, wo.ID, "fix_findings")
}

// commitVerified records the verified workspace as a local commit (never
// pushed) and stamps the work order and this revision's checks with its SHA.
func (f *Factory) commitVerified(ctx context.Context, wo *model.WorkOrder) error {
	sha, err := f.Sandbox.Commit(ctx, f.workspaceDir(wo.ID), fmt.Sprintf("legos-factory: %s revision %d (%s)", wo.ID, wo.Revision, wo.Title))
	if err != nil {
		return err
	}
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"commit_sha": sha}); err != nil {
		return err
	}
	if err := f.Quality.AttachCommit(ctx, wo.ID, wo.Revision, sha); err != nil {
		return err
	}
	wo.CommitSHA = sha
	f.event(wo.ID, "factory", "commit_recorded", fmt.Sprintf("Verified revision %d committed locally as %s (not pushed)", wo.Revision, orUnversioned(sha)),
		map[string]any{"commit_sha": sha, "revision": wo.Revision})
	return nil
}

const architectVerifyAttempts = 3

var architectRetryDelay = 5 * time.Second

func (f *Factory) retryArchitectVerify(ctx context.Context, wo *model.WorkOrder, files []string, err error) error {
	for i := 1; i < architectVerifyAttempts && err != nil; i++ {
		select {
		case <-ctx.Done():
			return err
		case <-time.After(architectRetryDelay):
		}
		err = f.architectVerify(ctx, *wo, files)
	}
	return err
}

// architectReport is the Architect's verification of one revision; the done
// gate only trusts it for the revision it was made on.
type architectReport struct {
	Revision   int                `json:"revision"`
	Verdicts   []ArchitectVerdict `json:"verdicts"`
	Deviations []string           `json:"deviations"`
}

// architectVerify has the Architect check the built result against its spec and
// plan and stores the verdicts for the done gate. A failure is reported, not
// fatal: the done gate then has no verdicts for this revision.
func (f *Factory) architectVerify(ctx context.Context, wo model.WorkOrder, files []string) error {
	dir := f.workspaceDir(wo.ID)
	diff, err := f.Sandbox.Diff(ctx, dir)
	if err != nil {
		f.event(wo.ID, "architect", "diff_unavailable", err.Error(), nil)
	}
	tasks, err := f.WO.Plan(ctx, wo.ID)
	if err != nil {
		return err
	}
	checks, _ := f.checksText(ctx, &wo)
	kbText, audit := f.KB.Context(ctx, wo.ProjectID, string(model.AgentArchitect), "",
		woQuery(&wo)+" "+strings.Join(files, " "), dir, wo.KBBaseSHA)
	var r verifyReply
	_, runID, err := f.runStructured(ctx, &wo, model.AgentArchitect, architectVerifyPrompt(&wo, tasks, diff, checks)+kbText,
		map[string]string{"stage": "verify"}, "", &r, runOpts{Audit: audit})
	if err != nil {
		if errors.Is(err, model.ErrPermanent) || ctx.Err() != nil {
			f.event(wo.ID, "architect", "architect_verify_failed", err.Error(), nil)
			return nil
		}
		return err
	}
	report := architectReport{Revision: wo.Revision, Verdicts: r.Verdicts, Deviations: r.Deviations}
	raw, _ := json.Marshal(report)
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"architect_report": string(raw)}); err != nil {
		return err
	}
	text := reportText(report)
	met := 0
	for _, v := range report.Verdicts {
		if v.Status == "met" {
			met++
		}
	}
	f.event(wo.ID, "architect", "architect_verified",
		fmt.Sprintf("Architect verification: %d of %d criteria met, %d deviation(s)", met, len(report.Verdicts), len(report.Deviations)),
		map[string]any{"revision": wo.Revision, "verdicts": report.Verdicts, "deviations": report.Deviations, "run_id": runID})
	f.Catalog.Record(ctx, &wo, model.KnowledgeArchitectVerified, model.TrustAIInterpretation, "architect",
		fmt.Sprintf("Architect verification of revision %d: %d of %d criteria met", wo.Revision, met, len(report.Verdicts)), text, "run:"+runID)
	return nil
}

func reportText(r architectReport) string {
	var b strings.Builder
	for _, v := range r.Verdicts {
		fmt.Fprintf(&b, "%s: %s — %s\n", v.CriterionID, v.Status, v.Evidence)
	}
	for _, d := range r.Deviations {
		fmt.Fprintf(&b, "Deviation: %s\n", d)
	}
	return b.String()
}

// latestChecks is each check's most recent result for the current revision; the
// revision also holds the pre-change baseline and re-run duplicates.
func (f *Factory) latestChecks(ctx context.Context, wo *model.WorkOrder) ([]model.QualityCheck, error) {
	stored, err := f.Quality.ForRevision(ctx, wo.ID, wo.Revision)
	if err != nil {
		return nil, err
	}
	latest := map[string]int{}
	for i, c := range stored {
		latest[c.Name] = i
	}
	var out []model.QualityCheck
	for i, c := range stored {
		if latest[c.Name] == i {
			out = append(out, c)
		}
	}
	return out, nil
}

// checksText renders the current check results for a prompt and reports whether any failed.
func (f *Factory) checksText(ctx context.Context, wo *model.WorkOrder) (string, bool) {
	checks, err := f.latestChecks(ctx, wo)
	if err != nil || len(checks) == 0 {
		return "(no checks recorded)", false
	}
	var b strings.Builder
	failed := false
	for _, c := range checks {
		fmt.Fprintf(&b, "%s (%s) → %s\n", c.Name, c.Command, c.Outcome)
		failed = failed || c.Outcome != string(model.Pass)
	}
	return b.String(), failed
}
