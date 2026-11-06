package usecase

import (
	"context"
	"testing"

	"legos-factory/backend/internal/model"
)

type cancelOnStart struct {
	*askHarness
	cancel func()
}

func (h cancelOnStart) Start(ctx context.Context, req model.AgentRequest) (model.AgentSession, error) {
	if req.Role == model.AgentBuilder {
		h.cancel()
	}
	return h.askHarness.Start(ctx, req)
}

func TestCancelledOrderIsNeitherAskedNorRequeued(t *testing.T) {
	r, h := askRig(t, map[model.AgentRole][]string{
		model.AgentBuilder:   {askHalfUp},
		model.AgentArchitect: {archAnswer},
	})
	ch := cancelOnStart{h, func() { _ = r.wos.UpdateState(context.Background(), r.wo.ID, model.StateCancelled) }}
	r.f.Resolve = func(model.AgentRole, *model.WorkOrder) Harness { return ch }
	if err := r.f.handleImplement(context.Background(), model.Job{WOID: r.wo.ID, TaskID: "T1"}); err != nil {
		t.Fatalf("err = %v; a cancelled order's job ends quietly", err)
	}
	if h.runs(model.AgentArchitect) != 0 {
		t.Fatal("the Architect was asked after the cancel")
	}
	before := h.runs(model.AgentBuilder)
	if err := r.f.handleImplement(context.Background(), model.Job{WOID: r.wo.ID, TaskID: "T1"}); err != nil || h.runs(model.AgentBuilder) != before {
		t.Fatalf("err = %v; a job for a cancelled order must do nothing", err)
	}
}

func TestWaitStoresTheRunsCost(t *testing.T) {
	r, _ := askRig(t, nil)
	ctx := context.Background()
	if err := r.f.Runs.Start(ctx, "run1", r.wo.ID, "T1", "builder", "fake"); err != nil {
		t.Fatal(err)
	}
	if err := r.f.Runs.Wait(ctx, "run1", "q?", model.AgentResult{TokensIn: 7, TokensOut: 3, CostMicros: 42}); err != nil {
		t.Fatal(err)
	}
	var in, out, cost int64
	if err := r.db.QueryRow(`SELECT tokens_in, tokens_out, cost_micros FROM runs WHERE id = 'run1'`).Scan(&in, &out, &cost); err != nil || in != 7 || out != 3 || cost != 42 {
		t.Fatalf("tokens %d/%d cost %d err %v", in, out, cost, err)
	}
}
