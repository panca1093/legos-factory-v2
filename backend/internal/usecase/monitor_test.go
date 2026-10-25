package usecase

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

// steppingHarness reports its scripted steps, idles for `idle`, then returns err (or a reply).
type steppingHarness struct {
	steps []model.AgentStep
	idle  time.Duration
	err   error
	req   model.AgentRequest
}

func (h *steppingHarness) Name() string { return "fake" }

func (h *steppingHarness) Start(_ context.Context, req model.AgentRequest) (model.AgentSession, error) {
	h.req = req
	return &fakeSession{}, nil
}

func (h *steppingHarness) Cancel(context.Context, string) error { return nil }

func (h *steppingHarness) Result(context.Context, string) (model.AgentResult, error) {
	for _, s := range h.steps {
		h.req.OnStep(s)
	}
	time.Sleep(h.idle)
	if h.err != nil {
		return model.AgentResult{}, h.err
	}
	return model.AgentResult{Text: "{}"}, nil
}

type fakeSession struct{}

func (fakeSession) ID() string { return "ses" }

func monitorForeman(t *testing.T, h Harness) (*Foreman, *model.WorkOrder, *store.WorkOrderRepo) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := (&store.UserRepo{DB: db}).Create(ctx, model.User{ID: "u1", Email: "u1@t", Name: "U", Role: model.RoleEngineer}); err != nil {
		t.Fatal(err)
	}
	if err := (&store.ProjectRepo{DB: db}).Create(ctx, &model.Project{ID: "p1", Name: "p"}); err != nil {
		t.Fatal(err)
	}
	wos := &store.WorkOrderRepo{DB: db}
	wo := &model.WorkOrder{ID: "wo1", ProjectID: "p1", Title: "t", Objective: "o", State: model.StatePlanning, CreatedBy: "u1"}
	if err := wos.Create(ctx, wo); err != nil {
		t.Fatal(err)
	}
	f := &Foreman{
		WO: wos, Runs: &store.RunRepo{DB: db}, Bus: nopBus{}, Log: slog.Default(),
		Resolve:    func(model.AgentRole, *model.WorkOrder) Harness { return h },
		StallAfter: 60 * time.Millisecond,
	}
	return f, wo, wos
}

func agentEvents(t *testing.T, wos *store.WorkOrderRepo) []model.Event {
	t.Helper()
	evs, err := wos.Events(context.Background(), "wo1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestRunAgentReportsStepsAndStall(t *testing.T) {
	h := &steppingHarness{
		steps: []model.AgentStep{
			{N: 1, Tools: []model.AgentTool{{Name: "read", Target: "D06.md"}}},
			{N: 2, Tools: []model.AgentTool{{Name: "grep", Target: "tenant"}, {Name: "glob", Target: "*.go"}, {Name: "read", Target: "a.go"}}},
		},
		idle: 200 * time.Millisecond,
	}
	f, wo, wos := monitorForeman(t, h)
	_, runID, err := f.runAgent(context.Background(), wo, model.AgentArchitect, "p", "m", "prompt", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var kinds, messages []string
	for _, e := range agentEvents(t, wos) {
		if e.Payload["run_id"] != runID {
			t.Fatalf("event %s lacks run_id %s: %+v", e.Kind, runID, e.Payload)
		}
		kinds = append(kinds, e.Kind)
		messages = append(messages, e.Message)
	}
	want := []string{"agent_started", "agent_step", "agent_step", "agent_stalled", "agent_completed"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
	if messages[2] != "Architect step 2: grep tenant, glob *.go +1 more" {
		t.Fatalf("step message = %q", messages[2])
	}
}

func TestRunAgentFailureEndsTheRun(t *testing.T) {
	h := &steppingHarness{err: errors.New("opencode execution timed out")}
	f, wo, wos := monitorForeman(t, h)
	if _, _, err := f.runAgent(context.Background(), wo, model.AgentForeman, "p", "m", "prompt", nil, ""); err == nil {
		t.Fatal("expected the harness error")
	}
	evs := agentEvents(t, wos)
	last := evs[len(evs)-1]
	if last.Kind != "agent_failed" || last.Message != "foreman agent failed: opencode execution timed out" {
		t.Fatalf("last event = %s %q", last.Kind, last.Message)
	}
	for _, e := range evs {
		if e.Kind == "agent_stalled" {
			t.Fatal("a run that ends quickly must not be reported as stalled")
		}
	}
}
