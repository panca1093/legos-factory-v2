package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"legos-factory/backend/internal/model"
)

// Human decisions: plan approval/revision, clarification, PR disposition, cancel.

// ApprovePlan is the human authorization to execute a Foreman-reviewed plan.
func (f *Foreman) ApprovePlan(ctx context.Context, woID string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if wo.State == model.StateArchitectReview && wo.PlanReview != "approved" {
		return stateConflict("the Foreman has not finished reviewing this plan")
	}
	return f.approvePlan(ctx, wo, "human", "Plan approved; execution authorized")
}

// approvePlan moves ARCHITECT_REVIEW → QUEUED and enqueues root tasks; actor
// is "human" or "policy" (delegated autonomy).
func (f *Foreman) approvePlan(ctx context.Context, wo *model.WorkOrder, actor, msg string) error {
	woID := wo.ID
	tasks, err := f.WO.Plan(ctx, woID)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return stateConflict("plan has no tasks; approve requires a reviewed plan")
	}
	if err := f.step(ctx, wo, model.StateArchitectReview, model.StateQueued, actor, msg); err != nil {
		return err
	}
	roots := model.ReadyTasks(tasks, map[string]bool{})
	for _, t := range roots {
		if _, err := f.Sched.Enqueue(ctx, JobImplement, woID, t.ID, map[string]any{}); err != nil {
			return err
		}
	}
	f.event(woID, "foreman", "execution_queued", fmt.Sprintf("%d task(s) queued", len(roots)), nil)
	trust := model.TrustHumanDecision
	if actor != "human" {
		trust = model.TrustVerified // a deterministic policy decision, not a person's
	}
	f.Catalog.Record(ctx, wo, model.KnowledgePlanApproved, trust, actor,
		fmt.Sprintf("Plan approved for %q (%d tasks) by %s", wo.Title, len(tasks), actor), taskList(tasks))
	return nil
}

// RevisePlan sends the plan back to the Architect (ARCHITECT_REVIEW → PLANNING).
func (f *Foreman) RevisePlan(ctx context.Context, woID, notes string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if err := f.WO.UpdateFields(ctx, woID, map[string]any{"plan_review": ""}); err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StateArchitectReview, model.StatePlanning, "human", "Revision requested: "+notes); err != nil {
		return err
	}
	f.Catalog.Record(ctx, wo, model.KnowledgePlanRevision, model.TrustHumanDecision, "human",
		fmt.Sprintf("Plan revision requested for %q", wo.Title), notes)
	_, err = f.Sched.Enqueue(ctx, JobPlan, woID, "", map[string]any{"notes": notes})
	return err
}

// AnswerClarification records the human answer and has the Architect
// re-draft the plan with it; the Foreman then reviews the new draft.
func (f *Foreman) AnswerClarification(ctx context.Context, woID, answer string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if wo.State != model.StateWaitingForHuman || wo.ClarifQuestion == "" || wo.ClarifAnswer != "" {
		return stateConflict("WO %s is not waiting for clarification (state %s)", woID, wo.State)
	}
	if err := f.WO.UpdateFields(ctx, woID, map[string]any{"clarif_answer": answer, "review_rounds": 0}); err != nil {
		return err
	}
	wo.ClarifAnswer = answer
	f.event(woID, "human", "clarification_answered", answer, nil)
	f.Catalog.Record(ctx, wo, model.KnowledgeClarification, model.TrustHumanDecision, "human",
		"Clarification: "+wo.ClarifQuestion, fmt.Sprintf("Question: %s\nAnswer: %s", wo.ClarifQuestion, answer))
	if err := f.step(ctx, wo, model.StateWaitingForHuman, model.StatePlanning, "human", "Clarification answered; re-drafting plan"); err != nil {
		return err
	}
	return f.enqueuePayload(ctx, JobPlan, woID, map[string]any{"notes": "Incorporate the human clarification."})
}

// HumanPRDecision records the final human disposition from
// NEEDS_HUMAN_REVIEW: approve → READY, request_changes → new revision fix
// loop, decline → CANCELLED. It never merges or publishes anything.
func (f *Foreman) HumanPRDecision(ctx context.Context, woID, decision, reason string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if wo.State != model.StateNeedsHumanReview {
		return stateConflict("WO %s is not awaiting a human PR decision (state %s)", woID, wo.State)
	}
	switch decision {
	case "approve":
		f.event(woID, "human", "pr_approved", reason, nil)
		if err := f.step(ctx, wo, model.StateNeedsHumanReview, model.StateReady, "human", "PR approved by human (no publish/merge performed)"); err != nil {
			return err
		}
		f.recordPRDecision(ctx, wo, "PR approved", reason)
		f.Catalog.Completed(ctx, wo, "READY")
		if f.Catalog.summariesEnabled(ctx) {
			if _, err := f.Sched.Enqueue(ctx, JobCatalogSummary, woID, "", map[string]any{}); err != nil {
				f.Log.Error("cataloger: summary enqueue failed", "wo", woID, "err", err)
			}
		}
		// the human approval is the explicit authorization to publish
		if proj, err := f.Proj.Get(ctx, wo.ProjectID); err == nil && proj.GitConnection != "" {
			if _, err := f.Sched.Enqueue(ctx, JobOpenPR, woID, "", map[string]any{}); err != nil {
				f.Log.Error("open PR enqueue failed", "wo", woID, "err", err)
			}
		}
		return nil
	case "request_changes":
		newRev := wo.Revision + 1
		if err := f.WO.UpdateFields(ctx, woID, map[string]any{"revision": newRev, "commit_sha": ""}); err != nil {
			return err
		}
		f.event(woID, "human", "revision_requested", reason, map[string]any{"revision": newRev})
		if err := f.step(ctx, wo, model.StateNeedsHumanReview, model.StateImplementing, "human", "Changes requested; invalidating prior quality approval"); err != nil {
			return err
		}
		f.recordPRDecision(ctx, wo, "Changes requested", reason)
		_, err := f.Sched.Enqueue(ctx, JobImplement, woID, "", map[string]any{"reason": "human_revision"})
		return err
	case "decline":
		f.event(woID, "human", "pr_declined", reason, nil)
		if err := f.step(ctx, wo, model.StateNeedsHumanReview, model.StateCancelled, "human", "Work order declined"); err != nil {
			return err
		}
		f.recordPRDecision(ctx, wo, "PR declined", reason)
		f.Catalog.Completed(ctx, wo, "DECLINED")
		return nil
	default:
		return fmt.Errorf("unknown PR decision %q", decision)
	}
}

// Cancel stops pending jobs and transitions the WO to CANCELLED.
func (f *Foreman) Cancel(ctx context.Context, woID string) error {
	if err := f.Sched.CancelWO(ctx, woID); err != nil {
		return err
	}
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if wo.State.Terminal() {
		return stateConflict("WO %s already terminal (%s)", woID, wo.State)
	}
	if err := model.MustTransition(wo.State, model.StateCancelled); err != nil {
		return err
	}
	if err := f.WO.UpdateState(ctx, woID, model.StateCancelled); err != nil {
		return err
	}
	f.event(woID, "human", "cancelled", "Work order cancelled", map[string]any{"from": string(wo.State)})
	f.Catalog.Completed(ctx, wo, "CANCELLED")
	return nil
}

// recordPRDecision stores the human disposition. The PR reference is local:
// nothing is pushed and no remote pull request is created.
func (f *Foreman) recordPRDecision(ctx context.Context, wo *model.WorkOrder, title, reason string) {
	ref := fmt.Sprintf("local:%s@%s", wo.ID, orUnversioned(wo.CommitSHA))
	f.Catalog.Record(ctx, wo, model.KnowledgePRDecision, model.TrustHumanDecision, "human",
		fmt.Sprintf("%s: %q", title, wo.Title), fmt.Sprintf("Reason: %s\nPR reference: %s (no remote PR)", reason, ref), "pr:"+ref)
}

// JobOpenPR publishes an approved work order: push its verified commit to
// a work branch and find-or-create the pull request, so retries are safe.
const JobOpenPR = "open_pr"

func (f *Foreman) handleOpenPR(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if wo.State != model.StateReady {
		return fmt.Errorf("%w: a pull request requires an approved work order (state %s)", model.ErrPermanent, wo.State)
	}
	proj, err := f.Proj.Get(ctx, wo.ProjectID)
	if err != nil {
		return err
	}
	g, bound, err := f.Integrations.GitFor(ctx, proj)
	if !bound {
		return nil
	}
	if err != nil {
		return f.prFailed(wo, fmt.Errorf("%w: %v", model.ErrPermanent, err))
	}
	auth := g.GitAuth()
	branch := "legos-factory/" + wo.ID
	base, err := f.Sandbox.PushBranch(ctx, f.workspaceDir(wo.ID), branch, &auth)
	if err != nil {
		return f.prFailed(wo, err)
	}
	pr, err := g.OpenPullRequest(ctx, model.PRRequest{Repo: proj.RemoteRepo, Head: branch, Base: base, Title: wo.Title, Body: prBody(wo)})
	if err != nil {
		var pe *model.ProviderError
		if errors.As(err, &pe) && pe.Permanent() {
			err = fmt.Errorf("%w: %v", model.ErrPermanent, err)
		}
		return f.prFailed(wo, err)
	}
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"pr_url": pr.URL, "pr_ref": pr.Ref}); err != nil {
		return err
	}
	f.event(wo.ID, "foreman", "pr_opened", fmt.Sprintf("Pull request %s opened from %s into %s", pr.Ref, branch, base),
		map[string]any{"url": pr.URL, "ref": pr.Ref, "branch": branch, "base": base})
	f.Catalog.Record(ctx, wo, model.KnowledgePROpened, model.TrustVerified, "foreman", "Pull request opened: "+pr.Ref,
		fmt.Sprintf("URL: %s\nBranch: %s → %s\nCommit: %s", pr.URL, branch, base, orUnversioned(wo.CommitSHA)), "pr:"+pr.Ref)
	return nil
}

func (f *Foreman) prFailed(wo *model.WorkOrder, err error) error {
	f.event(wo.ID, "foreman", "pr_open_failed", "Could not open pull request: "+err.Error(), nil)
	return err
}

func prBody(wo *model.WorkOrder) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n**Acceptance criteria**\n%s\n\n", wo.Objective, wo.Acceptance)
	fmt.Fprintf(&b, "Verified by legos-factory at revision %d (commit %s); deterministic checks passed and a human approved this change.",
		wo.Revision, orUnversioned(wo.CommitSHA))
	if wo.SourceRef != "" {
		fmt.Fprintf(&b, "\n\nSource: %s", wo.SourceRef)
	}
	return b.String()
}

// DecideFinding records a human disposition. Disputed findings stop
// blocking readiness but keep the reason; escalating pauses the order until a
// human resumes it.
func (f *Foreman) DecideFinding(ctx context.Context, woID, findingID, actorID string, req model.FindingDecisionRequest) (*model.Finding, error) {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return nil, err
	}
	if wo.State == model.StateReady || wo.State == model.StateCancelled {
		return nil, stateConflict("WO %s is %s; findings can no longer change", woID, wo.State)
	}
	fd, err := f.Findings.Get(ctx, woID, findingID)
	if err != nil {
		return nil, err
	}
	status := map[string]string{
		"confirm": model.FindingConfirmed, "dispute": model.FindingDisputed,
		"resolve": model.FindingResolved, "escalate": model.FindingEscalated,
	}[req.Action]
	note := strings.TrimSpace(req.Note)
	switch {
	case status == "":
		return nil, fmt.Errorf("%w: action must be confirm, dispute, resolve or escalate", ErrInvalid)
	case (status == model.FindingDisputed || status == model.FindingEscalated) && note == "":
		return nil, fmt.Errorf("%w: a %s needs a note explaining why", ErrInvalid, req.Action)
	}
	if err := f.Findings.SetDisposition(ctx, fd.ID, status, note, actorID); err != nil {
		return nil, err
	}
	f.event(woID, "human", "finding_"+status, fmt.Sprintf("%s: %s", fd.Title, note), map[string]any{"finding_id": fd.ID})
	f.Catalog.Record(ctx, wo, model.KnowledgeFindingDecision, model.TrustHumanDecision, "human",
		fmt.Sprintf("Finding %s: %s", status, fd.Title), fmt.Sprintf("Severity: %s\nSpecialty: %s\nNote: %s", fd.Severity, fd.Specialty, note),
		"finding:"+fd.ID)
	if status == model.FindingEscalated && wo.PausedAt == nil && !wo.State.Terminal() {
		if err := f.pause(ctx, wo, "human", "Finding escalated: "+fd.Title); err != nil {
			return nil, err
		}
	}
	return f.Findings.Get(ctx, woID, findingID)
}
