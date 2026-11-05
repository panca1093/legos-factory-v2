package usecase

import (
	"context"
	"fmt"
	"strings"
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
	var results strings.Builder
	for _, c := range profile.Checks {
		r := f.Sandbox.RunCheck(ctx, f.workspaceDir(wo.ID), c, f.QualTimeout)
		if err := f.saveCheck(ctx, wo, c, r); err != nil {
			return err
		}
		f.event(wo.ID, "inspector", "check_"+strings.ToLower(string(r.Outcome)),
			fmt.Sprintf("%s → %s", c.Name, r.Outcome), map[string]any{"command": c.Command})
		fmt.Fprintf(&results, "%s (%s) → %s\n", c.Name, c.Command, r.Outcome)
		if r.Outcome != model.Pass && failed == "" {
			failed = c.Name
		}
	}
	if failed != "" {
		f.Catalog.Record(ctx, wo, model.KnowledgeVerification, model.TrustVerified, "inspector",
			fmt.Sprintf("Verification failed on revision %d (uncommitted workspace)", wo.Revision), results.String())
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
			return f.evaluateReadiness(ctx, wo, true)
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
		_, err := f.Sched.Enqueue(ctx, JobImplement, wo.ID, "", map[string]any{"reason": "fix_failed_verification"})
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
	f.Catalog.Record(ctx, wo, model.KnowledgeVerification, model.TrustVerified, "inspector",
		fmt.Sprintf("Verification passed on revision %d at %s", wo.Revision, orUnversioned(wo.CommitSHA)), results.String())
	if err := f.step(ctx, wo, model.StateVerifying, model.StateSpecialistReview, "factory", "Verification passed; specialist review"); err != nil {
		if wo.State != model.StateSpecialistReview {
			return err
		}
	}
	return f.enqueue(ctx, JobInspect, wo.ID, "")
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
	f.event(wo.ID, "inspector", "inspection_finished", fmt.Sprintf("%d finding(s), %d blocker(s)", total, blockers), nil)

	if blockers == 0 {
		// later clean inspection supersedes earlier findings on this WO
		if err := f.Findings.ResolveOpen(ctx, wo.ID); err != nil {
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
