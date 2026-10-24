// Package orchestration implements the Foreman: a deterministic pipeline
// driver. Agent reasoning produces plans, code and findings; every workflow
// transition is validated by the workorder state machine and executed here.
package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"legos-factory/backend/internal/agents"
	"legos-factory/backend/internal/eventbus"
	"legos-factory/backend/internal/quality"
	"legos-factory/backend/internal/scheduler"
	"legos-factory/backend/internal/usage"
	"legos-factory/backend/internal/workorder"
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

// ErrStateConflict marks domain rejections that indicate an invalid
// operation for the current state (mapped to HTTP 409).
var ErrStateConflict = errors.New("STATE_CONFLICT")

func stateConflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrStateConflict, fmt.Sprintf(format, args...))
}


type Foreman struct {
	WO          *workorder.Repository
	DB          *sql.DB
	Proj        ProjectReader
	Resolve     func(role agents.Role, wo *workorder.WorkOrder) agents.Harness
	Model       func(role agents.Role, wo *workorder.WorkOrder) (provider, model string)
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

func (f *Foreman) handlePlan(ctx context.Context, job scheduler.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	switch wo.State {
	case workorder.StateCreated:
		if err := f.step(ctx, wo, workorder.StateCreated, workorder.StateRequirement, "foreman", "Analyzing requirements"); err != nil {
			return err
		}
		if err := f.step(ctx, wo, workorder.StateRequirement, workorder.StateDecomposition, "foreman", "Decomposing into tasks"); err != nil {
			return err
		}
	case workorder.StatePlanning:
		// revision loop: human requested a plan revision
		if err := f.step(ctx, wo, workorder.StatePlanning, workorder.StatePlanGeneration, "foreman", "Re-drafting plan after revision request"); err != nil {
			return err
		}
	case workorder.StateRequirement, workorder.StateDecomposition, workorder.StatePlanGeneration:
		// resume: a previous attempt was interrupted mid-planning
	default:
		return fmt.Errorf("plan job for WO in state %s", wo.State)
	}
	f.event(wo.ID, "architect", "planning_started", "Architect drafting plan", nil)

	provider, model := f.Model(agents.RoleArchitect, wo)
	res, _, err := f.runAgent(ctx, wo, agents.RoleArchitect, provider, model, planPrompt(wo), nil, "")
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
		return f.step(ctx, wo, workorder.StateArchitectReview, workorder.StateWaitingForHuman, "foreman", "Waiting for human clarification")
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
	tasks := make([]workorder.Task, 0, len(plan.Tasks))
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
		tasks = append(tasks, workorder.Task{
			ID: idMap[ref], WOID: wo.ID, Seq: i, Title: t.Title, Objective: t.Objective,
			Role: t.Role, Deps: deps,
		})
	}
	if err := workorder.ValidateDAG(tasks); err != nil {
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
func (f *Foreman) toReview(ctx context.Context, wo *workorder.WorkOrder) error {
	if wo.State == workorder.StateDecomposition {
		if err := f.step(ctx, wo, workorder.StateDecomposition, workorder.StatePlanGeneration, "foreman", "Generating specification and plan"); err != nil {
			return err
		}
	}
	return f.step(ctx, wo, workorder.StatePlanGeneration, workorder.StateArchitectReview, "foreman", "Plan drafted; architect review")
}

func (f *Foreman) handleReview(ctx context.Context, job scheduler.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if wo.State != workorder.StateWaitingForHuman {
		return fmt.Errorf("review job for WO in state %s", wo.State)
	}
	provider, model := f.Model(agents.RoleArchitect, wo)
	ctxData := map[string]string{
		"clarification_answer": wo.ClarifAnswer,
		"plan_json":            "present",
	}
	if _, _, err := f.runAgent(ctx, wo, agents.RoleArchitect, provider, model, reviewPrompt(wo), ctxData, ""); err != nil {
		return err
	}
	f.event(wo.ID, "architect", "architect_approved", "Architect re-review passed after clarification", nil)
	if err := f.step(ctx, wo, workorder.StateWaitingForHuman, workorder.StatePlanning, "foreman", "Plan updated with clarification"); err != nil {
		return err
	}
	if err := f.step(ctx, wo, workorder.StatePlanning, workorder.StatePlanGeneration, "foreman", "Finalizing plan"); err != nil {
		return err
	}
	return f.step(ctx, wo, workorder.StatePlanGeneration, workorder.StateArchitectReview, "foreman", "Architect review complete")
}

func (f *Foreman) handleImplement(ctx context.Context, job scheduler.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if err := f.step(ctx, wo, workorder.StateQueued, workorder.StateImplementing, "foreman", "Builder started implementation"); err != nil {
		if wo.State != workorder.StateImplementing {
			return err
		}
	}
	// first transition in: record the failing baseline before any change,
	// so verification evidence is revision-specific from the start
	if err := f.recordBaseline(ctx, wo); err != nil {
		return err
	}
	provider, model := f.Model(agents.RoleBuilder, wo)
	if _, _, err := f.runAgent(ctx, wo, agents.RoleBuilder, provider, model, implementPrompt(wo, job), nil, job.TaskID); err != nil {
		return err
	}
	f.event(wo.ID, "builder", "task_completed", fmt.Sprintf("Task %s implemented", job.TaskID), nil)
	return f.maybeAdvanceAfterImplementation(ctx, wo)
}

// recordBaseline runs the deterministic checks once, before implementation,
// when no verification evidence exists yet. A passing baseline is recorded
// as PASS — it still counts as evidence for the original revision.
func (f *Foreman) recordBaseline(ctx context.Context, wo *workorder.WorkOrder) error {
	var existing int
	if err := f.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM quality_checks WHERE wo_id = ?`, wo.ID).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	proj, err := f.Proj.Get(ctx, wo.ProjectID)
	if err != nil {
		return err
	}
	profile, err := quality.ParseProfile(proj.QualityProfile, proj.Language)
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
func (f *Foreman) maybeAdvanceAfterImplementation(ctx context.Context, wo *workorder.WorkOrder) error {
	tasks, err := f.WO.Plan(ctx, wo.ID)
	if err != nil {
		return err
	}
	done, err := f.completedTasks(ctx, wo.ID)
	if err != nil {
		return err
	}
	ready := workorder.ReadyTasks(tasks, done)
	for _, t := range ready {
		// duplicate enqueues are rejected by the unique active-job index
		if _, err := f.Sched.Enqueue(ctx, JobImplement, wo.ID, t.ID, map[string]any{}); err != nil {
			continue // another worker already owns this task
		}
	}
	if len(tasks) > 0 && len(done) == len(tasks) {
		if err := f.step(ctx, wo, workorder.StateImplementing, workorder.StateVerifying, "foreman", "Implementation complete; verifying"); err != nil {
			if wo.State != workorder.StateVerifying {
				return err
			}
		}
		_, err := f.Sched.Enqueue(ctx, JobVerify, wo.ID, "", map[string]any{})
		return err
	}
	return nil
}

func (f *Foreman) handleVerify(ctx context.Context, job scheduler.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	proj, err := f.Proj.Get(ctx, wo.ProjectID)
	if err != nil {
		return err
	}
	profile, err := quality.ParseProfile(proj.QualityProfile, proj.Language)
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
		if r.Outcome != quality.Pass && failed == "" {
			failed = c.Name
		}
	}
	if failed != "" {
		if wo.Revision >= MaxRevisionCycles {
			f.event(wo.ID, "foreman", "escalated", "Verification failed after max revision cycles; blocking", nil)
			if err := f.step(ctx, wo, workorder.StateVerifying, workorder.StateSpecialistReview, "foreman", "Final evaluation after exhausted retries"); err != nil {
				return err
			}
			if err := f.step(ctx, wo, workorder.StateSpecialistReview, workorder.StatePRPreparation, "foreman", "Preparing change summary"); err != nil {
				return err
			}
			if err := f.step(ctx, wo, workorder.StatePRPreparation, workorder.StateMergeReadiness, "foreman", "Evaluating merge readiness"); err != nil {
				return err
			}
			return f.evaluateReadiness(ctx, wo, true)
		}
		if err := f.step(ctx, wo, workorder.StateVerifying, workorder.StateImplementing, "foreman",
			fmt.Sprintf("Check %q failed; returning to builder", failed)); err != nil {
			return err
		}
		_, err := f.Sched.Enqueue(ctx, JobImplement, wo.ID, "", map[string]any{"reason": "fix_failed_verification"})
		return err
	}
	f.event(wo.ID, "inspector", "verification_passed", "All deterministic checks passed", nil)
	if err := f.step(ctx, wo, workorder.StateVerifying, workorder.StateSpecialistReview, "foreman", "Verification passed; specialist review"); err != nil {
		if wo.State != workorder.StateSpecialistReview {
			return err
		}
	}
	return f.enqueue(ctx, JobInspect, wo.ID, "")
}

func (f *Foreman) handleInspect(ctx context.Context, job scheduler.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	provider, model := f.Model(agents.RoleInspector, wo)
	res, runID, err := f.runAgent(ctx, wo, agents.RoleInspector, provider, model, inspectPrompt(wo), nil, "")
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
		if _, err := f.DB.ExecContext(ctx, `INSERT INTO findings (id, run_id, wo_id, severity, title, detail, location, status, created_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, id, runID, wo.ID, fd.Severity, fd.Title, fd.Detail, fd.Location, "open", time.Now().Unix()); err != nil {
			return err
		}
		f.event(wo.ID, "inspector", "finding", fmt.Sprintf("[%s] %s", fd.Severity, fd.Title), map[string]any{
			"finding_id": id, "severity": fd.Severity, "location": fd.Location,
		})
		if fd.Severity == "blocker" {
			blockers++
		}
	}

	if blockers == 0 {
		// later clean inspection supersedes earlier findings on this WO
		if _, err := f.DB.ExecContext(ctx, `UPDATE findings SET status = 'resolved' WHERE wo_id = ? AND status = 'open'`, wo.ID); err != nil {
			return err
		}
		return f.enqueue(ctx, JobEvaluate, wo.ID, "")
	}
	if wo.Revision >= MaxRevisionCycles {
		f.event(wo.ID, "foreman", "escalated", "Blocker findings remain after max revision cycles; escalating to human", nil)
		if err := f.step(ctx, wo, workorder.StateSpecialistReview, workorder.StateWaitingForHuman, "foreman", "Escalated: blocker findings unresolved"); err != nil {
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
	if err := f.step(ctx, wo, workorder.StateSpecialistReview, workorder.StateImplementing, "foreman", "Fix required; returning to builder"); err != nil {
		return err
	}
	// the fix runs as a builder job; its completion drives → VERIFYING again
	return f.enqueue(ctx, JobImplement, wo.ID, "fix_findings")
}

func (f *Foreman) handleEvaluate(ctx context.Context, job scheduler.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if err := f.step(ctx, wo, workorder.StateSpecialistReview, workorder.StatePRPreparation, "foreman", "Preparing change summary"); err != nil {
		return err
	}
	if err := f.step(ctx, wo, workorder.StatePRPreparation, workorder.StateMergeReadiness, "foreman", "Evaluating merge readiness"); err != nil {
		return err
	}
	return f.evaluateReadiness(ctx, wo, false)
}

// evaluateReadiness combines deterministic verification and review outcomes
// into the merge-readiness decision.
func (f *Foreman) evaluateReadiness(ctx context.Context, wo *workorder.WorkOrder, verificationBlocked bool) error {
	var checkCount int
	if err := f.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM quality_checks WHERE wo_id = ? AND revision = ?`,
		wo.ID, wo.Revision).Scan(&checkCount); err != nil {
		return err
	}
	openBlockers, err := f.openBlockerFindings(ctx, wo.ID)
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
	next := workorder.StateNeedsHumanReview
	if decision == "BLOCKED" {
		next = workorder.StateBlocked
	}
	if err := f.step(ctx, wo, workorder.StateMergeReadiness, next, "foreman", "Merge readiness: "+decision); err != nil {
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
	if err := f.step(ctx, wo, workorder.StateArchitectReview, workorder.StateQueued, "human", "Plan approved; execution authorized"); err != nil {
		return err
	}
	roots := workorder.ReadyTasks(tasks, map[string]bool{})
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
	if err := f.step(ctx, wo, workorder.StateArchitectReview, workorder.StatePlanning, "human", "Revision requested: "+notes); err != nil {
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
	if wo.State != workorder.StateWaitingForHuman {
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
	if wo.State != workorder.StateNeedsHumanReview {
		return stateConflict("WO %s is not awaiting a human PR decision (state %s)", woID, wo.State)
	}
	switch decision {
	case "approve":
		f.event(woID, "human", "pr_approved", reason, nil)
		return f.step(ctx, wo, workorder.StateNeedsHumanReview, workorder.StateReady, "human", "PR approved by human (no publish/merge performed)")
	case "request_changes":
		newRev := wo.Revision + 1
		if err := f.WO.UpdateFields(ctx, woID, map[string]any{"revision": newRev}); err != nil {
			return err
		}
		f.event(woID, "human", "revision_requested", reason, map[string]any{"revision": newRev})
		if err := f.step(ctx, wo, workorder.StateNeedsHumanReview, workorder.StateImplementing, "human", "Changes requested; invalidating prior quality approval"); err != nil {
			return err
		}
		_, err := f.Sched.Enqueue(ctx, JobImplement, woID, "", map[string]any{"reason": "human_revision"})
		return err
	case "decline":
		f.event(woID, "human", "pr_declined", reason, nil)
		return f.step(ctx, wo, workorder.StateNeedsHumanReview, workorder.StateCancelled, "human", "Work order declined")
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
	if err := workorder.MustTransition(wo.State, workorder.StateCancelled); err != nil {
		return err
	}
	if err := f.WO.UpdateState(ctx, woID, workorder.StateCancelled); err != nil {
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
	resumable := map[workorder.State]bool{
		workorder.StateCreated: true, workorder.StateRequirement: true,
		workorder.StateDecomposition: true, workorder.StatePlanGeneration: true,
		workorder.StatePlanning: true,
	}
	for _, wo := range wos {
		if resumable[wo.State] && !f.hasActiveJob(ctx, wo.ID, "", JobPlan) {
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
func (f *Foreman) runAgent(ctx context.Context, wo *workorder.WorkOrder, role agents.Role, provider, model, prompt string, extra map[string]string, taskID string) (agents.AgentResult, string, error) {
	if f.Usage != nil {
		release, ok := f.Usage.Reserve(50_000) // conservative estimate per agent run
		if !ok {
			// policy refusal: never retried, the human must raise the cap
			return agents.AgentResult{}, "", fmt.Errorf("%w: budget exhausted; the workspace spending cap does not cover another agent run", scheduler.ErrPermanent)
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
	req := agents.AgentRequest{
		Role: role, Prompt: prompt, ProviderID: provider, ModelID: model,
		WorkspaceDir: f.workspaceDir(wo.ID), Context: cctx,
	}
	h := f.Resolve(role, wo)
	runID := fmt.Sprintf("run_%d", time.Now().UnixNano())
	if _, err := f.DB.ExecContext(ctx, `INSERT INTO runs (id, work_order_id, task_id, kind, state, harness, created_at)
		VALUES (?,?,?,?,?,?,?)`, runID, wo.ID, nullStr(taskID), string(role), "running", h.Name(), time.Now().Unix()); err != nil {
		return agents.AgentResult{}, "", err
	}
	f.event(wo.ID, string(role), "agent_started",
		fmt.Sprintf("%s agent started (%s harness, model %s)", role, h.Name(), model), nil)

	ses, err := h.Start(ctx, req)
	if err != nil {
		f.failRun(ctx, runID, err)
		return agents.AgentResult{}, runID, err
	}
	res, err := h.Result(ctx, ses.ID())
	if err != nil {
		f.failRun(ctx, runID, err)
		return agents.AgentResult{}, runID, err
	}
	if _, err := f.DB.ExecContext(ctx, `UPDATE runs SET state = 'completed', output = ?, session_ref = ?,
		tokens_in = ?, tokens_out = ?, cost_micros = ?, finished_at = ? WHERE id = ?`,
		res.Text, res.SessionRef, res.TokensIn, res.TokensOut, res.CostMicros, time.Now().Unix(), runID); err != nil {
		return agents.AgentResult{}, runID, err
	}
	if f.Usage != nil {
		_ = f.Usage.Record(ctx, usage.Record{
			WOID: wo.ID, UserID: wo.CreatedBy, Role: string(role),
			Provider: provider, Model: model,
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
	_, _ = f.DB.ExecContext(ctx, `UPDATE runs SET state = 'failed', error = ?, finished_at = ? WHERE id = ?`,
		err.Error(), time.Now().Unix(), runID)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---- transitions & events ----

func (f *Foreman) step(ctx context.Context, wo *workorder.WorkOrder, from, to workorder.State, actor, msg string) error {
	if wo.State != from {
		return fmt.Errorf("INVALID_STATE_TRANSITION: WO %s is %s, expected %s (→ %s)", wo.ID, wo.State, from, to)
	}
	if err := workorder.MustTransition(from, to); err != nil {
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
	f.Bus.Publish(eventbus.Event{WOID: woID, Actor: actor, Kind: kind, Message: message, Payload: map[string]any{
		"id": ev.ID, "payload": payload,
	}})
}

// ---- helpers ----

func (f *Foreman) workspaceDir(woID string) string {
	return f.DataDir + "/workspaces/" + woID
}

func (f *Foreman) saveCheck(ctx context.Context, wo *workorder.WorkOrder, c quality.Check, r quality.Result) error {
	exit := 0
	if r.ExitCode != nil {
		exit = *r.ExitCode
	} else if r.Outcome != quality.Pass {
		exit = -1
	}
	_, err := f.DB.ExecContext(ctx, `INSERT INTO quality_checks (id, wo_id, name, command, outcome, exit_code, output, revision, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		fmt.Sprintf("qc_%d", time.Now().UnixNano()), wo.ID, c.Name, c.Command, string(r.Outcome), exit, r.Output, wo.Revision, time.Now().Unix())
	return err
}

func (f *Foreman) hasActiveJob(ctx context.Context, woID, taskID, kind string) bool {
	var n int
	err := f.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE wo_id = ? AND kind = ?
		AND state IN ('PENDING','CLAIMED','RUNNING') AND (task_id IS ? OR (? = '' AND task_id IS NULL))`,
		woID, kind, nullStr(taskID), taskID).Scan(&n)
	return err == nil && n > 0
}

func (f *Foreman) completedTasks(ctx context.Context, woID string) (map[string]bool, error) {
	rows, err := f.DB.QueryContext(ctx, `SELECT DISTINCT task_id FROM runs
		WHERE work_order_id = ? AND kind = 'builder' AND state = 'completed' AND task_id IS NOT NULL`, woID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	done := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		done[id] = true
	}
	return done, rows.Err()
}

func (f *Foreman) openBlockerFindings(ctx context.Context, woID string) (int, error) {
	var n int
	err := f.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM findings WHERE wo_id = ? AND severity = 'blocker' AND status = 'open'`, woID).Scan(&n)
	return n, err
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
