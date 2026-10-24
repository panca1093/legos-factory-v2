// Package orchestration implements the Foreman: a deterministic pipeline
// driver. Agent reasoning produces plans, code and findings; every workflow
// transition is validated by the workorder state machine and executed here.
package orchestration

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"legos-factory/backend/internal/agents"
	"legos-factory/backend/internal/eventbus"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/quality"
	"legos-factory/backend/internal/scheduler"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usage"
)

const (
	JobPlan      = "plan"
	JobReview    = "review" // architect re-review after clarification
	JobImplement = "implement"
	JobVerify    = "verify"
	JobInspect   = "inspect"
	JobEvaluate  = "evaluate"
)

// MaxRevisionCycles bounds the fix/inspect remediation loop.
const MaxRevisionCycles = 2

func stateConflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", model.ErrStateConflict, fmt.Sprintf(format, args...))
}


type Foreman struct {
	WO          *store.WorkOrderRepo
	Runs        *store.RunRepo
	Findings    *store.FindingRepo
	Quality     *store.QualityRepo
	Proj        ProjectReader
	Resolve     func(role model.AgentRole, wo *model.WorkOrder) agents.Harness
	Model       func(role model.AgentRole, wo *model.WorkOrder) (provider, modelID string)
	Sched       *scheduler.Scheduler
	Bus         *eventbus.Bus
	Usage       *usage.Tracker
	DataDir     string
	QualTimeout time.Duration
	Log         *slog.Logger
}

// ProjectReader provides the project row for a work order.
type ProjectReader interface {
	Get(ctx context.Context, id string) (*ProjectView, error)
}

type ProjectView struct {
	ID             string
	RepoURL        string
	Language       string
	QualityProfile string
}

func (f *Foreman) Register(s *scheduler.Scheduler) {
	s.Register(JobPlan, f.handlePlan)
	s.Register(JobReview, f.handleReview)
	s.Register(JobImplement, f.handleImplement)
	s.Register(JobVerify, f.handleVerify)
	s.Register(JobInspect, f.handleInspect)
	s.Register(JobEvaluate, f.handleEvaluate)
}

// ---- job handlers ----

func (f *Foreman) handlePlan(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	switch wo.State {
	case model.StateCreated:
		if err := f.step(ctx, wo, model.StateCreated, model.StateRequirement, "foreman", "Analyzing requirements"); err != nil {
			return err
		}
		if err := f.step(ctx, wo, model.StateRequirement, model.StateDecomposition, "foreman", "Decomposing into tasks"); err != nil {
			return err
		}
	case model.StatePlanning:
		// revision loop: human requested a plan revision
		if err := f.step(ctx, wo, model.StatePlanning, model.StatePlanGeneration, "foreman", "Re-drafting plan after revision request"); err != nil {
			return err
		}
	case model.StateRequirement, model.StateDecomposition, model.StatePlanGeneration:
		// resume: a previous attempt was interrupted mid-planning
	default:
		return fmt.Errorf("plan job for WO in state %s", wo.State)
	}
	f.event(wo.ID, "architect", "planning_started", "Architect drafting plan", nil)

	provider, modelID := f.Model(model.AgentArchitect, wo)
	res, _, err := f.runAgent(ctx, wo, model.AgentArchitect, provider, modelID, planPrompt(wo), nil, "")
	if err != nil {
		return err
	}
	var plan struct {
		Decision     string `json:"decision"`
		Question     string `json:"question"`
		QuestionText string `json:"question_text"`
		Summary      string `json:"summary"`
		Tasks        []struct {
			ID        string   `json:"id"`
			Title     string   `json:"title"`
			Objective string   `json:"objective"`
			Role      string   `json:"role"`
			Deps      []string `json:"deps"`
		} `json:"tasks"`
	}
	if err := parseStructured(res.Text, &plan); err != nil {
		return fmt.Errorf("architect returned unparseable plan: %w", err)
	}

	if plan.Decision == "CLARIFY" {
		q := plan.QuestionText
		if q == "" {
			q = plan.Question
		}
		if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"clarif_question": q}); err != nil {
			return err
		}
		f.event(wo.ID, "architect", "clarification_requested", q, nil)
		if err := f.toReview(ctx, wo); err != nil {
			return err
		}
		return f.step(ctx, wo, model.StateArchitectReview, model.StateWaitingForHuman, "foreman", "Waiting for human clarification")
	}
	if plan.Decision != "APPROVE" && plan.Decision != "" {
		return fmt.Errorf("unexpected architect decision %q", plan.Decision)
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
		tasks = append(tasks, model.Task{
			ID: idMap[ref], WOID: wo.ID, Seq: i, Title: t.Title, Objective: t.Objective,
			Role: t.Role, Deps: deps,
		})
	}
	if err := model.ValidateDAG(tasks); err != nil {
		return err
	}
	if err := f.WO.ReplacePlan(ctx, wo.ID, tasks); err != nil {
		return err
	}
	f.event(wo.ID, "architect", "plan_ready", fmt.Sprintf("Plan with %d tasks", len(tasks)),
		map[string]any{"summary": plan.Summary})
	if err := f.toReview(ctx, wo); err != nil {
		return err
	}
	f.event(wo.ID, "architect", "architect_approved", "Architect review passed", nil)
	return nil
}

// toReview advances whichever pre-review planning state the WO is in to
// ARCHITECT_REVIEW via SPEC_AND_PLAN_GENERATION.
func (f *Foreman) toReview(ctx context.Context, wo *model.WorkOrder) error {
	if wo.State == model.StateDecomposition {
		if err := f.step(ctx, wo, model.StateDecomposition, model.StatePlanGeneration, "foreman", "Generating specification and plan"); err != nil {
			return err
		}
	}
	return f.step(ctx, wo, model.StatePlanGeneration, model.StateArchitectReview, "foreman", "Plan drafted; architect review")
}

func (f *Foreman) handleReview(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if wo.State != model.StateWaitingForHuman {
		return fmt.Errorf("review job for WO in state %s", wo.State)
	}
	provider, modelID := f.Model(model.AgentArchitect, wo)
	ctxData := map[string]string{
		"clarification_answer": wo.ClarifAnswer,
		"plan_json":            "present",
	}
	if _, _, err := f.runAgent(ctx, wo, model.AgentArchitect, provider, modelID, reviewPrompt(wo), ctxData, ""); err != nil {
		return err
	}
	f.event(wo.ID, "architect", "architect_approved", "Architect re-review passed after clarification", nil)
	if err := f.step(ctx, wo, model.StateWaitingForHuman, model.StatePlanning, "foreman", "Plan updated with clarification"); err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StatePlanning, model.StatePlanGeneration, "foreman", "Finalizing plan"); err != nil {
		return err
	}
	return f.step(ctx, wo, model.StatePlanGeneration, model.StateArchitectReview, "foreman", "Architect review complete")
}

func (f *Foreman) handleImplement(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StateQueued, model.StateImplementing, "foreman", "Builder started implementation"); err != nil {
		if wo.State != model.StateImplementing {
			return err
		}
	}
	// first transition in: record the failing baseline before any change,
	// so verification evidence is revision-specific from the start
	if err := f.recordBaseline(ctx, wo); err != nil {
		return err
	}
	provider, modelID := f.Model(model.AgentBuilder, wo)
	if _, _, err := f.runAgent(ctx, wo, model.AgentBuilder, provider, modelID, implementPrompt(wo, job), nil, job.TaskID); err != nil {
		return err
	}
	f.event(wo.ID, "builder", "task_completed", fmt.Sprintf("Task %s implemented", job.TaskID), nil)
	return f.maybeAdvanceAfterImplementation(ctx, wo)
}

// recordBaseline runs the deterministic checks once, before implementation,
// when no verification evidence exists yet. A passing baseline is recorded
// as PASS — it still counts as evidence for the original revision.
func (f *Foreman) recordBaseline(ctx context.Context, wo *model.WorkOrder) error {
	existing, err := f.Quality.Count(ctx, wo.ID)
	if err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	proj, err := f.Proj.Get(ctx, wo.ProjectID)
	if err != nil {
		return err
	}
	profile, err := model.ParseProfile(proj.QualityProfile, proj.Language)
	if err != nil {
		return err
	}
	if len(profile.Checks) == 0 {
		return nil
	}
	f.event(wo.ID, "builder", "baseline_started", "Recording baseline verification (pre-change)", nil)
	for _, c := range profile.Checks {
		r := quality.RunCheck(ctx, f.workspaceDir(wo.ID), c, f.QualTimeout)
		if err := f.saveCheck(ctx, wo, c, r); err != nil {
			return err
		}
		f.event(wo.ID, "builder", "baseline_"+strings.ToLower(string(r.Outcome)),
			fmt.Sprintf("baseline %s → %s", c.Name, r.Outcome), map[string]any{"command": c.Command})
	}
	return nil
}

// maybeAdvanceAfterImplementation enqueues newly ready tasks or, when all
// tasks are done, moves to verification.
func (f *Foreman) maybeAdvanceAfterImplementation(ctx context.Context, wo *model.WorkOrder) error {
	tasks, err := f.WO.Plan(ctx, wo.ID)
	if err != nil {
		return err
	}
	done, err := f.Runs.CompletedBuilderTasks(ctx, wo.ID)
	if err != nil {
		return err
	}
	ready := model.ReadyTasks(tasks, done)
	for _, t := range ready {
		// duplicate enqueues are rejected by the unique active-job index
		if _, err := f.Sched.Enqueue(ctx, JobImplement, wo.ID, t.ID, map[string]any{}); err != nil {
			continue // another worker already owns this task
		}
	}
	if len(tasks) > 0 && len(done) == len(tasks) {
		if err := f.step(ctx, wo, model.StateImplementing, model.StateVerifying, "foreman", "Implementation complete; verifying"); err != nil {
			if wo.State != model.StateVerifying {
				return err
			}
		}
		_, err := f.Sched.Enqueue(ctx, JobVerify, wo.ID, "", map[string]any{})
		return err
	}
	return nil
}

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

func (f *Foreman) handleEvaluate(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StateSpecialistReview, model.StatePRPreparation, "foreman", "Preparing change summary"); err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StatePRPreparation, model.StateMergeReadiness, "foreman", "Evaluating merge readiness"); err != nil {
		return err
	}
	return f.evaluateReadiness(ctx, wo, false)
}

// evaluateReadiness combines deterministic verification and review outcomes
// into the merge-readiness decision.
func (f *Foreman) evaluateReadiness(ctx context.Context, wo *model.WorkOrder, verificationBlocked bool) error {
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
	}
	f.event(wo.ID, "foreman", "readiness_"+strings.ToLower(decision), reason, map[string]any{"decision": decision})
	// Final PR disposition stays human: evidence-complete orders land in
	// NEEDS_HUMAN_REVIEW; the human approve/decline endpoint decides READY.
	next := model.StateNeedsHumanReview
	if decision == "BLOCKED" {
		next = model.StateBlocked
	}
	if err := f.step(ctx, wo, model.StateMergeReadiness, next, "foreman", "Merge readiness: "+decision); err != nil {
		return err
	}
	return nil
}

// ApprovePlan authorizes execution (human decision or policy, enforced by
// the HTTP layer). Moves ARCHITECT_REVIEW → QUEUED and enqueues root tasks.
func (f *Foreman) ApprovePlan(ctx context.Context, woID string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	tasks, err := f.WO.Plan(ctx, woID)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return stateConflict("plan has no tasks; approve requires a reviewed plan")
	}
	if err := f.step(ctx, wo, model.StateArchitectReview, model.StateQueued, "human", "Plan approved; execution authorized"); err != nil {
		return err
	}
	roots := model.ReadyTasks(tasks, map[string]bool{})
	for _, t := range roots {
		if _, err := f.Sched.Enqueue(ctx, JobImplement, woID, t.ID, map[string]any{}); err != nil {
			return err
		}
	}
	f.event(woID, "foreman", "execution_queued", fmt.Sprintf("%d task(s) queued", len(roots)), nil)
	return nil
}

// RevisePlan sends the plan back to the Architect (ARCHITECT_REVIEW → PLANNING).
func (f *Foreman) RevisePlan(ctx context.Context, woID, notes string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StateArchitectReview, model.StatePlanning, "human", "Revision requested: "+notes); err != nil {
		return err
	}
	_, err = f.Sched.Enqueue(ctx, JobPlan, woID, "", map[string]any{"notes": notes})
	return err
}

// AnswerClarification records the human answer and triggers re-review.
func (f *Foreman) AnswerClarification(ctx context.Context, woID, answer string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if wo.State != model.StateWaitingForHuman {
		return stateConflict("WO %s is not waiting for clarification (state %s)", woID, wo.State)
	}
	if err := f.WO.UpdateFields(ctx, woID, map[string]any{"clarif_answer": answer}); err != nil {
		return err
	}
	f.event(woID, "human", "clarification_answered", answer, nil)
	_, err = f.Sched.Enqueue(ctx, JobReview, woID, "", map[string]any{})
	return err
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
		return f.step(ctx, wo, model.StateNeedsHumanReview, model.StateReady, "human", "PR approved by human (no publish/merge performed)")
	case "request_changes":
		newRev := wo.Revision + 1
		if err := f.WO.UpdateFields(ctx, woID, map[string]any{"revision": newRev}); err != nil {
			return err
		}
		f.event(woID, "human", "revision_requested", reason, map[string]any{"revision": newRev})
		if err := f.step(ctx, wo, model.StateNeedsHumanReview, model.StateImplementing, "human", "Changes requested; invalidating prior quality approval"); err != nil {
			return err
		}
		_, err := f.Sched.Enqueue(ctx, JobImplement, woID, "", map[string]any{"reason": "human_revision"})
		return err
	case "decline":
		f.event(woID, "human", "pr_declined", reason, nil)
		return f.step(ctx, wo, model.StateNeedsHumanReview, model.StateCancelled, "human", "Work order declined")
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
	return nil
}

// ResumeOnStart re-enqueues planning jobs for work orders interrupted in
// pre-execution states. Planning is side-effect free (reasoning + DB
// writes), so automatic resume is safe; execution-phase work orders are
// NOT auto-resumed and wait for an explicit human action.
func (f *Foreman) ResumeOnStart(ctx context.Context) error {
	wos, err := f.WO.List(ctx)
	if err != nil {
		return err
	}
	resumable := map[model.State]bool{
		model.StateCreated: true, model.StateRequirement: true,
		model.StateDecomposition: true, model.StatePlanGeneration: true,
		model.StatePlanning: true,
	}
	for _, wo := range wos {
		if resumable[wo.State] && !f.Sched.HasActive(ctx, wo.ID, "", JobPlan) {
			if _, err := f.Sched.Enqueue(ctx, JobPlan, wo.ID, "", map[string]any{"reason": "startup_resume"}); err != nil {
				return err
			}
			f.event(wo.ID, "foreman", "resumed", "Planning resumed after restart", nil)
		}
	}
	return nil
}

// ---- agent run plumbing ----

// runAgent creates a run row, invokes the harness and records usage. A
// conservative budget reservation is taken before execution so parallel
// runs cannot over-commit the remaining budget.
func (f *Foreman) runAgent(ctx context.Context, wo *model.WorkOrder, role model.AgentRole, provider, modelID, prompt string, extra map[string]string, taskID string) (model.AgentResult, string, error) {
	if f.Usage != nil {
		release, ok := f.Usage.Reserve(50_000) // conservative estimate per agent run
		if !ok {
			// policy refusal: never retried, the human must raise the cap
			return model.AgentResult{}, "", fmt.Errorf("%w: budget exhausted; the workspace spending cap does not cover another agent run", scheduler.ErrPermanent)
		}
		defer f.release(release)
	}
	cctx := map[string]string{
		"objective":  wo.Objective,
		"acceptance": wo.Acceptance,
		"title":      wo.Title,
		"revision":   fmt.Sprintf("%d", wo.Revision),
	}
	for k, v := range extra {
		cctx[k] = v
	}
	req := model.AgentRequest{
		Role: role, Prompt: prompt, ProviderID: provider, ModelID: modelID,
		WorkspaceDir: f.workspaceDir(wo.ID), Context: cctx,
	}
	h := f.Resolve(role, wo)
	runID := fmt.Sprintf("run_%d", time.Now().UnixNano())
	if err := f.Runs.Start(ctx, runID, wo.ID, taskID, string(role), h.Name()); err != nil {
		return model.AgentResult{}, "", err
	}
	f.event(wo.ID, string(role), "agent_started",
		fmt.Sprintf("%s agent started (%s harness, model %s)", role, h.Name(), modelID), nil)

	ses, err := h.Start(ctx, req)
	if err != nil {
		f.failRun(ctx, runID, err)
		return model.AgentResult{}, runID, err
	}
	res, err := h.Result(ctx, ses.ID())
	if err != nil {
		f.failRun(ctx, runID, err)
		return model.AgentResult{}, runID, err
	}
	if err := f.Runs.Complete(ctx, runID, res); err != nil {
		return model.AgentResult{}, runID, err
	}
	if f.Usage != nil {
		_ = f.Usage.Record(ctx, model.UsageRecord{
			WOID: wo.ID, UserID: wo.CreatedBy, Role: string(role),
			Provider: provider, Model: modelID,
			TokensIn: res.TokensIn, TokensOut: res.TokensOut,
			CostMicros: res.CostMicros, Source: res.CostSource,
		})
	}
	f.event(wo.ID, string(role), "agent_completed", fmt.Sprintf("%s agent finished", role), nil)
	return res, runID, nil
}

func (f *Foreman) release(release func(actual int64)) {
	release(0)
}

func (f *Foreman) failRun(ctx context.Context, runID string, err error) {
	_ = f.Runs.Fail(ctx, runID, err.Error())
}

// ---- transitions & events ----

func (f *Foreman) step(ctx context.Context, wo *model.WorkOrder, from, to model.State, actor, msg string) error {
	if wo.State != from {
		return fmt.Errorf("INVALID_STATE_TRANSITION: WO %s is %s, expected %s (→ %s)", wo.ID, wo.State, from, to)
	}
	if err := model.MustTransition(from, to); err != nil {
		return err
	}
	if err := f.WO.UpdateState(ctx, wo.ID, to); err != nil {
		return err
	}
	wo.State = to
	f.event(wo.ID, actor, "state_changed", msg, map[string]any{"from": string(from), "to": string(to)})
	return nil
}

func (f *Foreman) event(woID, actor, kind, message string, payload map[string]any) {
	ev, err := f.WO.AppendEvent(context.Background(), woID, actor, kind, message, payload)
	if err != nil {
		f.Log.Error("append event failed", "err", err)
		return
	}
	f.Bus.Publish(model.LiveEvent{WOID: woID, Actor: actor, Kind: kind, Message: message, Payload: map[string]any{
		"id": ev.ID, "payload": payload,
	}})
}

// ---- helpers ----

func (f *Foreman) workspaceDir(woID string) string {
	return f.DataDir + "/workspaces/" + woID
}

func (f *Foreman) saveCheck(ctx context.Context, wo *model.WorkOrder, c model.Check, r model.CheckResult) error {
	exit := 0
	if r.ExitCode != nil {
		exit = *r.ExitCode
	} else if r.Outcome != model.Pass {
		exit = -1
	}
	return f.Quality.Save(ctx, model.QualityCheck{
		ID: fmt.Sprintf("qc_%d", time.Now().UnixNano()), WOID: wo.ID, Name: c.Name, Command: c.Command,
		Outcome: string(r.Outcome), ExitCode: exit, Output: r.Output, Revision: int64(wo.Revision),
	})
}

func parseStructured(text string, v any) error {
	return agents.StructuredJSON(agents.StripFences(text), v)
}

func (f *Foreman) enqueue(ctx context.Context, kind, woID, reason string) error {
	return f.enqueuePayload(ctx, kind, woID, map[string]any{"reason": reason})
}

func (f *Foreman) enqueuePayload(ctx context.Context, kind, woID string, payload map[string]any) error {
	_, err := f.Sched.Enqueue(ctx, kind, woID, "", payload)
	return err
}
