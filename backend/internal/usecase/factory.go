// Package usecase implements the Factory: a deterministic pipeline
// driver. Agent reasoning produces plans, code and findings; every workflow
// transition is validated by the model state machine and executed here.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
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

// lastAttempt reports that a failure of job will not be retried, mirroring the scheduler.
func lastAttempt(job model.Job) bool {
	return job.MaxAttempts > 0 && job.Attempts+1 >= job.MaxAttempts
}

func stateConflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", model.ErrStateConflict, fmt.Sprintf(format, args...))
}

type Factory struct {
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
	KB           *KB
	Integrations *Integrations
	Sandbox      Sandbox
	Judge        Judge // nil when TYPESAFE_API_KEY is unset
	DataDir      string
	wsMu         sync.Map // work order id → *sync.Mutex serializing writes to its workspace: task merges, dependency setup
	baselineMu   sync.Map // work order id → *sync.Mutex so one task records the baseline
	approveMu    sync.Map // work order id → *sync.Mutex so the plan gate and a human cannot both approve
	live         sync.Map // run id → liveSession, while the agent runs
	planSes      sync.Map // work order id → planSession: the Foreman's open planning session
	QualTimeout  time.Duration
	StallAfter   time.Duration // no new agent step for this long reports a stall; 0 = defaultStallAfter
	Log          *slog.Logger
}

const defaultStallAfter = 3 * time.Minute

var runSeq atomic.Int64

// Handlers maps each job kind to the step that executes it; app registers
// them with the scheduler.
func (f *Factory) Handlers() map[string]func(context.Context, model.Job) error {
	return map[string]func(context.Context, model.Job) error{
		JobPlan:      f.handlePlan,
		JobReview:    f.handleReview,
		JobImplement: f.handleImplement,
		JobVerify:    f.handleVerify,
		JobInspect:   f.handleInspect,
		JobEvaluate:  f.handleEvaluate,
		JobOpenPR:    f.handleOpenPR,
	}
}

// ResumeOnStart re-enqueues planning jobs for work orders interrupted in
// pre-execution states. Planning is side-effect free (reasoning + DB
// writes), so automatic resume is safe; execution-phase work orders are
// NOT auto-resumed and wait for an explicit human action.
func (f *Factory) ResumeOnStart(ctx context.Context) error {
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
			f.event(wo.ID, "factory", "resumed", "Planning resumed after restart", nil)
		}
	}
	return nil
}

// ---- agent run plumbing ----

// runAgent creates a run row, invokes the harness and records usage. A
// conservative budget reservation is taken before execution so parallel
// runs cannot over-commit the remaining budget.
// runOpts.Dir overrides the work order workspace; with Hold the run stays
// running on success so the caller completes it (completeRuns) or fails it
// (failRuns) once its work is integrated; agent_completed is left to the caller.
type runOpts struct {
	Dir   string
	Hold  bool
	Audit map[string]any // knowledge commits and document ids, recorded on agent_started

	// Planning runs the prompt in the work order's Foreman planning session; Prime
	// is prepended only when that starts a new session, restoring earlier decisions.
	Planning bool
	Prime    string
}

func (f *Factory) runAgent(ctx context.Context, wo *model.WorkOrder, role model.AgentRole, provider, modelID, prompt string, extra map[string]string, taskID string, opts runOpts) (model.AgentResult, string, error) {
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
	dir := opts.Dir
	if dir == "" {
		dir = f.workspaceDir(wo.ID)
	}
	req := model.AgentRequest{
		Role: role, Prompt: prompt, ProviderID: provider, ModelID: modelID,
		WorkspaceDir: dir, Context: cctx,
	}
	h := f.Resolve(role, wo)
	// the clock can be microsecond-grained, so parallel runs need the sequence to stay unique
	runID := fmt.Sprintf("run_%d_%d", time.Now().UnixNano(), runSeq.Add(1))
	if err := f.Runs.Start(ctx, runID, wo.ID, taskID, string(role), h.Name()); err != nil {
		return model.AgentResult{}, "", err
	}
	runRef := map[string]any{"run_id": runID, "task_id": taskID}
	started := map[string]any{"run_id": runID, "task_id": taskID}
	for k, v := range opts.Audit {
		started[k] = v
	}
	f.event(wo.ID, string(role), "agent_started",
		fmt.Sprintf("%s agent started (%s harness, model %s)", role, h.Name(), modelID), started)

	var lastStep atomic.Int64
	lastStep.Store(time.Now().UnixNano())
	req.OnStep = func(st model.AgentStep) {
		lastStep.Store(time.Now().UnixNano())
		f.event(wo.ID, string(role), "agent_step", stepMessage(role, st),
			map[string]any{"run_id": runID, "step": st.N, "tools": st.Tools, "tokens_out": st.TokensOut})
	}
	stopWatch := f.watchStall(wo.ID, role, runID, &lastStep)
	var ses model.AgentSession
	var err error
	if opts.Planning {
		ses, err = f.startPlanning(ctx, wo.ID, h, req, opts.Prime)
	} else {
		ses, err = h.Start(ctx, req)
	}
	if err != nil {
		stopWatch()
		f.failRun(ctx, wo.ID, role, runID, err)
		return model.AgentResult{}, runID, err
	}
	f.live.Store(runID, liveSession{woID: wo.ID, harness: h, session: ses.ID()})
	res, err := h.Result(ctx, ses.ID())
	f.live.Delete(runID)
	stopWatch()
	if err != nil {
		if opts.Planning {
			f.closePlanning(wo.ID)
		}
		f.failRun(ctx, wo.ID, role, runID, err)
		return model.AgentResult{}, runID, err
	}
	if !opts.Hold {
		if err := f.Runs.Complete(context.WithoutCancel(ctx), runID, res); err != nil {
			return model.AgentResult{}, runID, err
		}
	}
	if f.Usage != nil {
		_ = f.Usage.Record(ctx, model.UsageRecord{
			WOID: wo.ID, UserID: wo.CreatedBy, Role: string(role),
			Provider: provider, Model: modelID,
			TokensIn: res.TokensIn, TokensOut: res.TokensOut,
			CacheRead: res.CacheRead, CacheWrite: res.CacheWrite,
			CostMicros: res.CostMicros, Source: res.CostSource,
		})
	}
	if !opts.Hold {
		f.event(wo.ID, string(role), "agent_completed", fmt.Sprintf("%s agent finished", role), runRef)
	}
	return res, runID, nil
}

func (f *Factory) wsLock(woID string) *sync.Mutex       { return lockFor(&f.wsMu, woID) }
func (f *Factory) baselineLock(woID string) *sync.Mutex { return lockFor(&f.baselineMu, woID) }

func lockFor(m *sync.Map, id string) *sync.Mutex {
	mu, _ := m.LoadOrStore(id, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

type planSession struct {
	harness SessionHarness
	name    string
	id      string
}

// startPlanning sends req into the work order's open Foreman session, or opens
// a new one primed with prime when there is none or the harness cannot continue it.
func (f *Factory) startPlanning(ctx context.Context, woID string, h Harness, req model.AgentRequest, prime string) (model.AgentSession, error) {
	sh, ok := h.(SessionHarness)
	if !ok {
		req.Prompt = prime + req.Prompt
		return h.Start(ctx, req)
	}
	if v, ok := f.planSes.Load(woID); ok {
		cur := v.(planSession)
		if cur.name == h.Name() {
			ses, err := sh.Continue(ctx, cur.id, req)
			if err == nil {
				return ses, nil
			}
		}
		f.closePlanning(woID)
	}
	if f.cancelled(ctx, woID) {
		return nil, fmt.Errorf("%w: work order %s is cancelled", model.ErrPermanent, woID)
	}
	req.Prompt = prime + req.Prompt
	req.Keep = true
	ses, err := h.Start(ctx, req)
	if err != nil {
		return ses, err
	}
	f.planSes.Store(woID, planSession{harness: sh, name: h.Name(), id: ses.ID()})
	// a cancel that landed while the session was starting must not leave it parked
	if f.cancelled(ctx, woID) {
		f.closePlanning(woID)
	}
	return ses, nil
}

// cancelled re-reads the order: a running job is not stopped by Cancel.
func (f *Factory) cancelled(ctx context.Context, woID string) bool {
	wo, err := f.WO.Get(ctx, woID)
	return err == nil && wo.State == model.StateCancelled
}

// closePlanning ends the work order's Foreman planning session, if one is open.
func (f *Factory) closePlanning(woID string) {
	v, ok := f.planSes.LoadAndDelete(woID)
	if !ok {
		return
	}
	cur := v.(planSession)
	cur.harness.Close(cur.id)
}

func (f *Factory) release(release func(actual int64)) {
	release(0)
}

func (f *Factory) failRun(ctx context.Context, woID string, role model.AgentRole, runID string, err error) {
	_ = f.Runs.Fail(context.WithoutCancel(ctx), runID, err.Error())
	f.event(woID, string(role), "agent_failed", fmt.Sprintf("%s agent failed: %v", role, err), map[string]any{"run_id": runID})
}

// watchStall reports once when a run goes StallAfter without a new step, and
// re-arms after progress. stop waits for the watcher so no stall lands after the run ends.
func (f *Factory) watchStall(woID string, role model.AgentRole, runID string, lastStep *atomic.Int64) (stop func()) {
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

func (f *Factory) step(ctx context.Context, wo *model.WorkOrder, from, to model.State, actor, msg string) error {
	return f.stepWith(ctx, wo, from, to, actor, msg, nil)
}

// stepWith moves the state and writes fields in the same UPDATE.
func (f *Factory) stepWith(ctx context.Context, wo *model.WorkOrder, from, to model.State, actor, msg string, fields map[string]any) error {
	if wo.State != from {
		return fmt.Errorf("INVALID_STATE_TRANSITION: WO %s is %s, expected %s (→ %s)", wo.ID, wo.State, from, to)
	}
	if err := model.MustTransition(from, to); err != nil {
		return err
	}
	var err error
	if len(fields) == 0 {
		err = f.WO.UpdateState(ctx, wo.ID, to)
	} else {
		err = f.WO.UpdateStateAndFields(ctx, wo.ID, to, fields)
	}
	if err != nil {
		return err
	}
	wo.State = to
	f.event(wo.ID, actor, "state_changed", msg, map[string]any{"from": string(from), "to": string(to)})
	return nil
}

func (f *Factory) event(woID, actor, kind, message string, payload map[string]any) {
	f.eventLive(woID, actor, kind, message, payload, payload)
}

// eventLive stores payload and publishes live in its place, for payloads too
// large to stream (a gate's exact Jev request).
func (f *Factory) eventLive(woID, actor, kind, message string, payload, live map[string]any) {
	ev, err := f.WO.AppendEvent(context.Background(), woID, actor, kind, message, payload)
	if err != nil {
		f.Log.Error("append event failed", "err", err)
		return
	}
	f.Bus.Publish(model.LiveEvent{WOID: woID, Actor: actor, Kind: kind, Message: message, Payload: map[string]any{
		"id": ev.ID, "payload": live,
	}})
}

// ---- helpers ----

func (f *Factory) workspaceDir(woID string) string {
	return f.DataDir + "/workspaces/" + woID
}

func (f *Factory) saveCheck(ctx context.Context, wo *model.WorkOrder, c model.Check, r model.CheckResult) error {
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
// errReplyInvalid marks an agent reply that failed its schema twice.
var errReplyInvalid = errors.New("agent reply invalid")

func (f *Factory) runStructured(ctx context.Context, wo *model.WorkOrder, role model.AgentRole, prompt string, extra map[string]string, taskID string, v reply, opts runOpts) (model.AgentResult, string, error) {
	provider, modelID := f.Model(role, wo)
	res, runID, err := f.runAgent(ctx, wo, role, provider, modelID, prompt, extra, taskID, opts)
	if err != nil {
		return res, runID, err
	}
	derr := decodeReply(res.Text, v)
	if derr == nil {
		return res, runID, nil
	}
	f.event(wo.ID, string(role), "agent_reply_invalid", fmt.Sprintf("%s reply rejected: %v; asking once more", role, derr), nil)
	retry := prompt + "\n\nYour previous reply was rejected: " + derr.Error() + "\nReply again with ONLY the JSON object."
	res, runID, err = f.runAgent(ctx, wo, role, provider, modelID, retry, extra, taskID, opts)
	if err != nil {
		return res, runID, err
	}
	if derr = decodeReply(res.Text, v); derr != nil {
		return res, runID, fmt.Errorf("%w: %w: %s reply invalid after retry: %v", model.ErrPermanent, errReplyInvalid, role, derr)
	}
	return res, runID, nil
}

func (f *Factory) enqueue(ctx context.Context, kind, woID, reason string) error {
	return f.enqueuePayload(ctx, kind, woID, map[string]any{"reason": reason})
}

func (f *Factory) enqueuePayload(ctx context.Context, kind, woID string, payload map[string]any) error {
	_, err := f.Sched.Enqueue(ctx, kind, woID, "", payload)
	return err
}

var jsonFence = regexp.MustCompile("(?s)```(?:json)?\\s*")

// StripFences removes markdown code fences (agents often fence their JSON).
func StripFences(s string) string {
	return jsonFence.ReplaceAllString(s, "")
}
