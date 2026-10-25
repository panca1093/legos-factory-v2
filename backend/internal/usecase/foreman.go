// Package usecase implements the Foreman: a deterministic pipeline
// driver. Agent reasoning produces plans, code and findings; every workflow
// transition is validated by the model state machine and executed here.
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"legos-factory/backend/internal/model"
)

const (
	JobPlan      = "plan"
	JobReview    = "review" // Foreman review of the Architect's draft
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
	WO           WorkOrderStore
	Runs         RunStore
	Findings     FindingStore
	Quality      QualityStore
	Proj         ProjectStore
	Resolve      func(role model.AgentRole, wo *model.WorkOrder) Harness
	Model        func(role model.AgentRole, wo *model.WorkOrder) (provider, modelID string)
	Sched        Queue
	Bus          Publisher
	Usage        Budget
	Catalog      *Cataloger
	Integrations *Integrations
	Sandbox      Sandbox
	DataDir      string
	QualTimeout  time.Duration
	StallAfter   time.Duration // no new agent step for this long reports a stall; 0 = defaultStallAfter
	Log          *slog.Logger
}

const defaultStallAfter = 3 * time.Minute

// Handlers maps each job kind to the step that executes it; app registers
// them with the scheduler.
func (f *Foreman) Handlers() map[string]func(context.Context, model.Job) error {
	return map[string]func(context.Context, model.Job) error{
		JobPlan:      f.handlePlan,
		JobReview:    f.handleReview,
		JobImplement: f.handleImplement,
		JobVerify:    f.handleVerify,
		JobInspect:   f.handleInspect,
		JobEvaluate:  f.handleEvaluate,

		JobCatalogSummary: f.handleCatalogSummary,
		JobOpenPR:         f.handleOpenPR,
	}
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
			return model.AgentResult{}, "", fmt.Errorf("%w: budget exhausted; the workspace spending cap does not cover another agent run", model.ErrPermanent)
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
	runRef := map[string]any{"run_id": runID}
	f.event(wo.ID, string(role), "agent_started",
		fmt.Sprintf("%s agent started (%s harness, model %s)", role, h.Name(), modelID), runRef)

	var lastStep atomic.Int64
	lastStep.Store(time.Now().UnixNano())
	req.OnStep = func(st model.AgentStep) {
		lastStep.Store(time.Now().UnixNano())
		f.event(wo.ID, string(role), "agent_step", stepMessage(role, st),
			map[string]any{"run_id": runID, "step": st.N, "tools": st.Tools, "tokens_out": st.TokensOut})
	}
	stopWatch := f.watchStall(wo.ID, role, runID, &lastStep)
	ses, err := h.Start(ctx, req)
	if err != nil {
		stopWatch()
		f.failRun(ctx, wo.ID, role, runID, err)
		return model.AgentResult{}, runID, err
	}
	res, err := h.Result(ctx, ses.ID())
	stopWatch()
	if err != nil {
		f.failRun(ctx, wo.ID, role, runID, err)
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
	f.event(wo.ID, string(role), "agent_completed", fmt.Sprintf("%s agent finished", role), runRef)
	return res, runID, nil
}

func (f *Foreman) release(release func(actual int64)) {
	release(0)
}

func (f *Foreman) failRun(ctx context.Context, woID string, role model.AgentRole, runID string, err error) {
	_ = f.Runs.Fail(ctx, runID, err.Error())
	f.event(woID, string(role), "agent_failed", fmt.Sprintf("%s agent failed: %v", role, err), map[string]any{"run_id": runID})
}

// watchStall reports once when a run goes StallAfter without a new step, and
// re-arms after progress. stop waits for the watcher so no stall lands after the run ends.
func (f *Foreman) watchStall(woID string, role model.AgentRole, runID string, lastStep *atomic.Int64) (stop func()) {
	after := f.StallAfter
	if after <= 0 {
		after = defaultStallAfter
	}
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		tick := time.NewTicker(after / 6)
		defer tick.Stop()
		stalled := false
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				idle := time.Since(time.Unix(0, lastStep.Load()))
				if idle < after {
					stalled = false
				} else if !stalled {
					stalled = true
					f.event(woID, string(role), "agent_stalled",
						fmt.Sprintf("%s agent has made no progress for %s", role, idle.Round(time.Second)),
						map[string]any{"run_id": runID, "idle_seconds": int(idle.Seconds())})
				}
			}
		}
	}()
	return func() {
		close(done)
		<-exited
	}
}

// stepMessage summarizes a step for the activity trail, e.g. "Architect step 2: read D06.md, grep tenant".
func stepMessage(role model.AgentRole, st model.AgentStep) string {
	var calls []string
	for _, t := range st.Tools[:min(2, len(st.Tools))] {
		calls = append(calls, strings.TrimSpace(t.Name+" "+t.Target))
	}
	more := ""
	if len(st.Tools) > 2 {
		more = fmt.Sprintf(" +%d more", len(st.Tools)-2)
	}
	name := string(role)
	if name != "" {
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	return fmt.Sprintf("%s step %d: %s%s", name, st.N, strings.Join(calls, ", "), more)
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

// runStructured runs role and decodes its reply into v. An invalid reply is
// retried once with the schema error; a second failure is permanent, so a
// malformed answer never drives a transition.
func (f *Foreman) runStructured(ctx context.Context, wo *model.WorkOrder, role model.AgentRole, prompt string, extra map[string]string, taskID string, v reply) (model.AgentResult, string, error) {
	provider, modelID := f.Model(role, wo)
	res, runID, err := f.runAgent(ctx, wo, role, provider, modelID, prompt, extra, taskID)
	if err != nil {
		return res, runID, err
	}
	derr := decodeReply(res.Text, v)
	if derr == nil {
		return res, runID, nil
	}
	f.event(wo.ID, string(role), "agent_reply_invalid", fmt.Sprintf("%s reply rejected: %v; asking once more", role, derr), nil)
	retry := prompt + "\n\nYour previous reply was rejected: " + derr.Error() + "\nReply again with ONLY the JSON object."
	res, runID, err = f.runAgent(ctx, wo, role, provider, modelID, retry, extra, taskID)
	if err != nil {
		return res, runID, err
	}
	if derr = decodeReply(res.Text, v); derr != nil {
		return res, runID, fmt.Errorf("%w: %s reply invalid after retry: %v", model.ErrPermanent, role, derr)
	}
	return res, runID, nil
}

func (f *Foreman) enqueue(ctx context.Context, kind, woID, reason string) error {
	return f.enqueuePayload(ctx, kind, woID, map[string]any{"reason": reason})
}

func (f *Foreman) enqueuePayload(ctx context.Context, kind, woID string, payload map[string]any) error {
	_, err := f.Sched.Enqueue(ctx, kind, woID, "", payload)
	return err
}

var jsonFence = regexp.MustCompile("(?s)```(?:json)?\\s*")

// StripFences removes markdown code fences (agents often fence their JSON).
func StripFences(s string) string {
	return jsonFence.ReplaceAllString(s, "")
}
