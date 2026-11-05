package usecase

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

// steerHarness keeps its turn open until it is steered.
type steerHarness struct {
	steered chan string
}

func (*steerHarness) Name() string { return "steerable" }
func (*steerHarness) Start(context.Context, model.AgentRequest) (model.AgentSession, error) {
	return fakeSession{}, nil
}
func (*steerHarness) Cancel(context.Context, string) error { return nil }
func (h *steerHarness) Result(ctx context.Context, _ string) (model.AgentResult, error) {
	select {
	case msg := <-h.steered:
		return model.AgentResult{Text: "acted on: " + msg}, nil
	case <-ctx.Done():
		return model.AgentResult{}, ctx.Err()
	}
}
func (h *steerHarness) Steer(_ context.Context, _, text string) error {
	h.steered <- text
	return nil
}

func TestSteerReachesTheRunningAgent(t *testing.T) {
	h := &steerHarness{steered: make(chan string, 1)}
	f, wo, wos := monitorFactory(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type out struct {
		res   model.AgentResult
		runID string
		err   error
	}
	done := make(chan out, 1)
	go func() {
		res, runID, err := f.runAgent(ctx, wo, model.AgentBuilder, "p", "m", "go", nil, "", runOpts{})
		done <- out{res, runID, err}
	}()
	runID := waitLive(t, f, func() error {
		select {
		case r := <-done:
			return fmt.Errorf("run ended before it was steered: %v", r.err)
		default:
			return nil
		}
	})
	if err := f.Steer(ctx, wo.ID, runID, "  ", "human"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty message: %v", err)
	}
	if err := f.Steer(ctx, "other-wo", runID, "hi", "human"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("run of another work order: %v", err)
	}
	if err := f.Steer(ctx, wo.ID, runID, "use util.ts", "human"); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || r.res.Text != "acted on: use util.ts" {
		t.Fatalf("run: %q %v", r.res.Text, r.err)
	}
	if err := f.Steer(ctx, wo.ID, runID, "late", "human"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("finished run: %v", err)
	}
	var steered bool
	for _, e := range agentEvents(t, wos) {
		steered = steered || (e.Kind == "agent_steered" && e.Message == "use util.ts" && e.Payload["run_id"] == runID)
	}
	if !steered {
		t.Fatal("no agent_steered event for the run")
	}
}

func TestSteerNeedsASteerableHarness(t *testing.T) {
	h := &blockingHarness{release: make(chan struct{})}
	f, wo, _ := monitorFactory(t, h)
	go func() {
		_, _, _ = f.runAgent(context.Background(), wo, model.AgentBuilder, "p", "m", "go", nil, "", runOpts{})
	}()
	defer close(h.release)
	runID := waitLive(t, f, func() error { return nil })
	if err := f.Steer(context.Background(), wo.ID, runID, "hi", "human"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
}

func waitLive(t *testing.T, f *Factory, ended func() error) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var runID string
		f.live.Range(func(k, _ any) bool { runID = k.(string); return false })
		if runID != "" {
			return runID
		}
		if err := ended(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no live run")
	return ""
}

type blockingHarness struct{ release chan struct{} }

func (*blockingHarness) Name() string { return "plain" }
func (*blockingHarness) Start(context.Context, model.AgentRequest) (model.AgentSession, error) {
	return fakeSession{}, nil
}
func (*blockingHarness) Cancel(context.Context, string) error { return nil }
func (h *blockingHarness) Result(context.Context, string) (model.AgentResult, error) {
	<-h.release
	return model.AgentResult{}, nil
}

type fakeQueue struct {
	active   bool
	enqueued []string
}

func (q *fakeQueue) Enqueue(_ context.Context, kind, _, taskID string, _ map[string]any) (string, error) {
	q.enqueued = append(q.enqueued, kind+":"+taskID)
	return "job", nil
}
func (*fakeQueue) CancelWO(context.Context, string) error { return nil }
func (q *fakeQueue) HasActive(context.Context, string, string, string) bool {
	return q.active
}

func TestRetryTask(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, nil)
	q := &fakeQueue{}
	f.Sched = q
	if err := wos.ReplacePlan(ctx, wo.ID, []model.Task{
		{ID: "T1", WOID: wo.ID, Title: "a", Role: "builder"},
		{ID: "T2", WOID: wo.ID, Title: "b", Role: "builder", Deps: []string{"T1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.RetryTask(ctx, wo.ID, "T1", "human"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("not implementing: %v", err)
	}
	if err := wos.UpdateState(ctx, wo.ID, model.StateImplementing); err != nil {
		t.Fatal(err)
	}
	if err := f.RetryTask(ctx, wo.ID, "T9", "human"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown task: %v", err)
	}
	if err := f.RetryTask(ctx, wo.ID, "T2", "human"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("task with an unfinished dependency: %v", err)
	}
	q.active = true
	if err := f.RetryTask(ctx, wo.ID, "T1", "human"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("task already queued: %v", err)
	}
	q.active = false
	if err := f.RetryTask(ctx, wo.ID, "T1", "human"); err != nil {
		t.Fatal(err)
	}
	if len(q.enqueued) != 1 || q.enqueued[0] != JobImplement+":T1" {
		t.Fatalf("enqueued %v", q.enqueued)
	}
}

func TestRetryRemediation(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, nil)
	q := &fakeQueue{}
	f.Sched = q
	if err := wos.ReplacePlan(ctx, wo.ID, []model.Task{{ID: "T1", WOID: wo.ID, Title: "a", Role: "builder"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.RetryRemediation(ctx, wo.ID, "human"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("not implementing: %v", err)
	}
	if err := wos.UpdateState(ctx, wo.ID, model.StateImplementing); err != nil {
		t.Fatal(err)
	}
	if err := f.RetryRemediation(ctx, wo.ID, "human"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("unfinished tasks: %v", err)
	}
	if err := f.Runs.Start(ctx, "run_t1", wo.ID, "T1", "builder", "h"); err != nil {
		t.Fatal(err)
	}
	if err := f.Runs.Complete(ctx, "run_t1", model.AgentResult{}); err != nil {
		t.Fatal(err)
	}
	if err := f.RetryRemediation(ctx, wo.ID, "human"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("no remediation run: %v", err)
	}
	if err := f.Runs.Start(ctx, "run_fix", wo.ID, "", "builder", "h"); err != nil {
		t.Fatal(err)
	}
	if err := f.RetryRemediation(ctx, wo.ID, "human"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("remediation still running: %v", err)
	}
	if err := f.Runs.Fail(ctx, "run_fix", "no changes"); err != nil {
		t.Fatal(err)
	}
	q.active = true
	if err := f.RetryRemediation(ctx, wo.ID, "human"); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("already queued: %v", err)
	}
	q.active = false
	if err := f.RetryRemediation(ctx, wo.ID, "human"); err != nil {
		t.Fatal(err)
	}
	if len(q.enqueued) != 1 || q.enqueued[0] != JobImplement+":" {
		t.Fatalf("enqueued %v", q.enqueued)
	}
	var retried bool
	for _, e := range agentEvents(t, wos) {
		retried = retried || e.Kind == "remediation_retry"
	}
	if !retried {
		t.Fatal("no remediation_retry event")
	}
}
