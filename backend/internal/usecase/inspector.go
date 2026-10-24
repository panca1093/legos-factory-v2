package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/quality"
)

// Inspector: deterministic verification and specialist review findings.

func (f *Foreman) handleVerify(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	proj, err := f.Proj.Get(ctx, wo.ProjectID)
	if err != nil {
		return err
	}
	profile, err := model.ParseProfile(proj.QualityProfile, proj.Language)
	if err != nil {
		return err
	}
	f.event(wo.ID, "inspector", "verification_started",
		fmt.Sprintf("Running %d deterministic checks (revision %d)", len(profile.Checks), wo.Revision), nil)
	failed := ""
	for _, c := range profile.Checks {
		r := quality.RunCheck(ctx, f.workspaceDir(wo.ID), c, f.QualTimeout)
		if err := f.saveCheck(ctx, wo, c, r); err != nil {
			return err
		}
		f.event(wo.ID, "inspector", "check_"+strings.ToLower(string(r.Outcome)),
			fmt.Sprintf("%s → %s", c.Name, r.Outcome), map[string]any{"command": c.Command})
		if r.Outcome != model.Pass && failed == "" {
			failed = c.Name
		}
	}
	if failed != "" {
		if wo.Revision >= MaxRevisionCycles {
			f.event(wo.ID, "foreman", "escalated", "Verification failed after max revision cycles; blocking", nil)
			if err := f.step(ctx, wo, model.StateVerifying, model.StateSpecialistReview, "foreman", "Final evaluation after exhausted retries"); err != nil {
				return err
			}
			if err := f.step(ctx, wo, model.StateSpecialistReview, model.StatePRPreparation, "foreman", "Preparing change summary"); err != nil {
				return err
			}
			if err := f.step(ctx, wo, model.StatePRPreparation, model.StateMergeReadiness, "foreman", "Evaluating merge readiness"); err != nil {
				return err
			}
			return f.evaluateReadiness(ctx, wo, true)
		}
		if err := f.step(ctx, wo, model.StateVerifying, model.StateImplementing, "foreman",
			fmt.Sprintf("Check %q failed; returning to builder", failed)); err != nil {
			return err
		}
		_, err := f.Sched.Enqueue(ctx, JobImplement, wo.ID, "", map[string]any{"reason": "fix_failed_verification"})
		return err
	}
	f.event(wo.ID, "inspector", "verification_passed", "All deterministic checks passed", nil)
	if err := f.step(ctx, wo, model.StateVerifying, model.StateSpecialistReview, "foreman", "Verification passed; specialist review"); err != nil {
		if wo.State != model.StateSpecialistReview {
			return err
		}
	}
	return f.enqueue(ctx, JobInspect, wo.ID, "")
}

func (f *Foreman) handleInspect(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	provider, modelID := f.Model(model.AgentInspector, wo)
	res, runID, err := f.runAgent(ctx, wo, model.AgentInspector, provider, modelID, inspectPrompt(wo), nil, "")
	if err != nil {
		return err
	}
	var out struct {
		Findings []struct {
			Severity string `json:"severity"`
			Title    string `json:"title"`
			Detail   string `json:"detail"`
			Location string `json:"location"`
		} `json:"findings"`
	}
	if err := parseStructured(res.Text, &out); err != nil {
		return fmt.Errorf("inspector returned unparseable findings: %w", err)
	}
	blockers := 0
	for _, fd := range out.Findings {
		id := fmt.Sprintf("fnd_%d", time.Now().UnixNano())
		if err := f.Findings.Create(ctx, model.Finding{
			ID: id, RunID: runID, WOID: wo.ID, Severity: fd.Severity, Title: fd.Title,
			Detail: fd.Detail, Location: fd.Location, Status: "open",
		}); err != nil {
			return err
		}
		f.event(wo.ID, "inspector", "finding", fmt.Sprintf("[%s] %s", fd.Severity, fd.Title), map[string]any{
			"finding_id": id, "severity": fd.Severity, "location": fd.Location,
		})
		if fd.Severity == model.SeverityBlocker {
			blockers++
		}
	}

	if blockers == 0 {
		// later clean inspection supersedes earlier findings on this WO
		if err := f.Findings.ResolveOpen(ctx, wo.ID); err != nil {
			return err
		}
		return f.enqueue(ctx, JobEvaluate, wo.ID, "")
	}
	if wo.Revision >= MaxRevisionCycles {
		f.event(wo.ID, "foreman", "escalated", "Blocker findings remain after max revision cycles; escalating to human", nil)
		if err := f.step(ctx, wo, model.StateSpecialistReview, model.StateWaitingForHuman, "foreman", "Escalated: blocker findings unresolved"); err != nil {
			return err
		}
		return nil
	}
	newRev := wo.Revision + 1
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"revision": newRev}); err != nil {
		return err
	}
	wo.Revision = newRev
	f.event(wo.ID, "foreman", "fix_requested", fmt.Sprintf("Revision %d: requesting bounded remediation", newRev), nil)
	if err := f.step(ctx, wo, model.StateSpecialistReview, model.StateImplementing, "foreman", "Fix required; returning to builder"); err != nil {
		return err
	}
	// the fix runs as a builder job; its completion drives → VERIFYING again
	return f.enqueue(ctx, JobImplement, wo.ID, "fix_findings")
}
