// Package usecase implements the Foreman: a deterministic pipeline
// driver. Agent reasoning produces plans, code and findings; every workflow
// transition is validated by the model state machine and executed here.
package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
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
	WO          WorkOrderStore
	Runs        RunStore
	Findings    FindingStore
	Quality     QualityStore
	Proj        ProjectStore
	Resolve     func(role model.AgentRole, wo *model.WorkOrder) Harness
	Model       func(role model.AgentRole, wo *model.WorkOrder) (provider, modelID string)
	Sched       Queue
	Bus         Publisher
	Usage       Budget
	Catalog     *Cataloger
	Sandbox     Sandbox
	DataDir     string
	QualTimeout time.Duration
	Log         *slog.Logger
}

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
	return StructuredJSON(StripFences(text), v)
}

func (f *Foreman) enqueue(ctx context.Context, kind, woID, reason string) error {
	return f.enqueuePayload(ctx, kind, woID, map[string]any{"reason": reason})
}

func (f *Foreman) enqueuePayload(ctx context.Context, kind, woID string, payload map[string]any) error {
	_, err := f.Sched.Enqueue(ctx, kind, woID, "", payload)
	return err
}

// StructuredJSON extracts the first JSON object/array from agent text and
// unmarshals it into v. Agent replies are prose+JSON; this normalizes the
// extraction. Callers must still validate semantics before acting on it.
func StructuredJSON(text string, v any) error {
	start := strings.Index(text, "{")
	if start < 0 {
		return fmt.Errorf("no JSON object found in agent response")
	}
	// find matching closing brace from start
	depth := 0
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return json.Unmarshal([]byte(text[start:i+1]), v)
			}
		}
	}
	return fmt.Errorf("unterminated JSON object in agent response")
}

var jsonFence = regexp.MustCompile("(?s)```(?:json)?\\s*")

// StripFences removes markdown code fences (agents often fence their JSON).
func StripFences(s string) string {
	return jsonFence.ReplaceAllString(s, "")
}
