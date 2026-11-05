package usecase

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"legos-factory/backend/internal/model"
)

const (
	intakeJSON   = `{"brief":"Round money in integer rupiah","constraints":["keep API"],"risks":[],"suggested_task_shape":"one task"}`
	clarifyJSON  = `{"decision":"CLARIFY","question_text":"Round half up or down?"}`
	answerJSON   = `{"decision":"ANSWER","answer":"Half up."}`
	escalateJSON = `{"decision":"ESCALATE","reason":"product call"}`
)

type idSession string

func (s idSession) ID() string { return string(s) }

// sessHarness scripts replies by role and Foreman stage, and records how the
// factory opened, continued and closed sessions.
type sessHarness struct {
	mu        sync.Mutex
	n         int
	reqs      map[string]model.AgentRequest // last request by session id
	architect []string                      // replies in order; the last repeats
	answer    string
	starts    []model.AgentRequest
	conts     []string // "session: prompt"
	closed    []string
	dead      bool // Continue reports every session gone
}

func (*sessHarness) Name() string                         { return "fake" }
func (*sessHarness) Cancel(context.Context, string) error { return nil }
func (h *sessHarness) text(req model.AgentRequest) string {
	switch req.Role {
	case model.AgentArchitect:
		reply := planJSON
		if len(h.architect) > 0 {
			reply = h.architect[0]
			if len(h.architect) > 1 {
				h.architect = h.architect[1:]
			}
		}
		return reply
	case model.AgentForeman:
		switch req.Context["stage"] {
		case "intake":
			return intakeJSON
		case "answer":
			return h.answer
		}
		return reviewJSON
	}
	return "{}"
}

func (h *sessHarness) Start(_ context.Context, req model.AgentRequest) (model.AgentSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.n++
	id := fmt.Sprintf("s%d", h.n)
	if h.reqs == nil {
		h.reqs = map[string]model.AgentRequest{}
	}
	h.reqs[id] = req
	h.starts = append(h.starts, req)
	return idSession(id), nil
}

func (h *sessHarness) Continue(_ context.Context, id string, req model.AgentRequest) (model.AgentSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dead {
		return nil, model.ErrAgentSessionNotFound
	}
	h.reqs[id] = req
	h.conts = append(h.conts, id+": "+req.Prompt)
	return idSession(id), nil
}

func (h *sessHarness) Close(id string) {
	h.mu.Lock()
	h.closed = append(h.closed, id)
	h.mu.Unlock()
}

func (h *sessHarness) Result(_ context.Context, id string) (model.AgentResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return model.AgentResult{Text: h.text(h.reqs[id])}, nil
}

// plainHarness is a harness without the session capability.
type plainHarness struct{ h *sessHarness }

func (p plainHarness) Name() string { return "plain" }
func (p plainHarness) Start(ctx context.Context, req model.AgentRequest) (model.AgentSession, error) {
	return p.h.Start(ctx, req)
}
func (p plainHarness) Cancel(ctx context.Context, id string) error { return p.h.Cancel(ctx, id) }
func (p plainHarness) Result(ctx context.Context, id string) (model.AgentResult, error) {
	return p.h.Result(ctx, id)
}

func sessionRig(t *testing.T, h Harness) *rig {
	t.Helper()
	r := newRig(t)
	r.f.Resolve = func(model.AgentRole, *model.WorkOrder) Harness { return h }
	r.workspace(t, map[string]string{"README.md": "# x\n"})
	if err := r.wos.UpdateState(context.Background(), r.wo.ID, model.StateCreated); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) reload(t *testing.T) *model.WorkOrder {
	t.Helper()
	wo, err := r.wos.Get(context.Background(), r.wo.ID)
	if err != nil {
		t.Fatal(err)
	}
	return wo
}

func TestForemanSessionIsReusedFromIntakeToReview(t *testing.T) {
	ctx := context.Background()
	h := &sessHarness{architect: []string{clarifyJSON, planJSON}, answer: answerJSON}
	r := sessionRig(t, h)

	if err := r.f.handlePlan(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	wo := r.reload(t)
	if wo.State != model.StateArchitectReview || wo.PlanReview != "pending" {
		t.Fatalf("state %s review %q; the Foreman's answer must let the Architect finish", wo.State, wo.PlanReview)
	}
	if !strings.Contains(wo.Brief, "Round money in integer rupiah") || !strings.Contains(wo.Brief, "- keep API") || wo.Spec != "sp" {
		t.Fatalf("brief %q spec %q", wo.Brief, wo.Spec)
	}
	if wo.ClarifRounds != 1 || len(wo.Clarifications) != 1 || wo.Clarifications[0].Answer != "Half up." {
		t.Fatalf("clarifications: %d %+v", wo.ClarifRounds, wo.Clarifications)
	}
	if len(h.starts) != 3 { // intake + two Architect drafts
		t.Fatalf("starts = %d", len(h.starts))
	}
	if !h.starts[0].Keep || h.starts[0].Role != model.AgentForeman || h.starts[1].Keep || h.starts[2].Keep {
		t.Fatalf("only the Foreman intake keeps its session: %+v", h.starts)
	}
	if !strings.Contains(h.starts[2].Prompt, "Q: Round half up or down?\nA: Half up.") || !strings.Contains(h.starts[2].Prompt, "Foreman's brief:") {
		t.Fatalf("the redrafting Architect lacks the brief and the Foreman's answer:\n%s", h.starts[2].Prompt)
	}
	if len(h.conts) != 1 || !strings.HasPrefix(h.conts[0], "s1: ") || !strings.Contains(h.conts[0], "Round half up or down?") {
		t.Fatalf("the answer must continue the intake session: %v", h.conts)
	}

	if err := r.f.handleReview(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if len(h.conts) != 2 || !strings.HasPrefix(h.conts[1], "s1: ") || !strings.Contains(h.conts[1], "Architect's spec:\nsp") {
		t.Fatalf("the review must continue the same session: %v", h.conts)
	}
	if strings.Contains(h.conts[1], "You are continuing a work order") {
		t.Fatal("a continued session must not be primed")
	}
	if len(h.closed) != 1 || h.closed[0] != "s1" {
		t.Fatalf("the plan review must close the session: %v", h.closed)
	}
	if _, open := r.f.planSes.Load(r.wo.ID); open {
		t.Fatal("session still tracked after the review")
	}
	if n := len(r.events(t, "agent_started")); n != 5 {
		t.Fatalf("each prompt keeps its own run: %d agent_started events", n)
	}
}

func TestForemanFallsBackToPrimedNewSession(t *testing.T) {
	ctx := context.Background()
	for name, mk := range map[string]func(*sessHarness) Harness{
		"harness without sessions": func(h *sessHarness) Harness { return plainHarness{h} },
		"session gone":             func(h *sessHarness) Harness { h.dead = true; return h },
	} {
		t.Run(name, func(t *testing.T) {
			h := &sessHarness{architect: []string{clarifyJSON, planJSON}, answer: answerJSON}
			r := sessionRig(t, mk(h))
			if err := r.f.handlePlan(ctx, model.Job{WOID: r.wo.ID}); err != nil {
				t.Fatal(err)
			}
			var answerPrompt string
			for _, s := range h.starts {
				if s.Context["stage"] == "answer" {
					answerPrompt = s.Prompt
				}
			}
			for _, want := range []string{"You are continuing a work order you already started", "Round money in integer rupiah", "Round half up or down?"} {
				if !strings.Contains(answerPrompt, want) {
					t.Fatalf("the answer must be primed with %q:\n%s", want, answerPrompt)
				}
			}
			if strings.Contains(answerPrompt, "A: Half up.") {
				t.Fatal("the priming holds earlier answers only")
			}
			if err := r.f.handleReview(ctx, model.Job{WOID: r.wo.ID}); err != nil {
				t.Fatal(err)
			}
			review := h.starts[len(h.starts)-1]
			for _, want := range []string{"You are continuing a work order you already started", "Round money in integer rupiah", "A: Half up.", "Architect's spec:\nsp"} {
				if !strings.Contains(review.Prompt, want) {
					t.Fatalf("the review must be primed with %q:\n%s", want, review.Prompt)
				}
			}
			if review.Role != model.AgentForeman {
				t.Fatalf("last start is %s", review.Role)
			}
		})
	}
}

func TestEscalationClosesTheForemanSession(t *testing.T) {
	ctx := context.Background()
	h := &sessHarness{architect: []string{clarifyJSON}, answer: escalateJSON}
	r := sessionRig(t, h)
	if err := r.f.handlePlan(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	wo := r.reload(t)
	if wo.State != model.StateWaitingForHuman || wo.ClarifQuestion != "Round half up or down?" || wo.ClarifRounds != 0 {
		t.Fatalf("state %s question %q rounds %d", wo.State, wo.ClarifQuestion, wo.ClarifRounds)
	}
	if len(h.closed) != 1 || h.closed[0] != "s1" {
		t.Fatalf("escalating to a human must close the session: %v", h.closed)
	}
	if _, open := r.f.planSes.Load(r.wo.ID); open {
		t.Fatal("session still tracked after escalation")
	}
}

func TestThirdQuestionGoesStraightToTheHuman(t *testing.T) {
	ctx := context.Background()
	h := &sessHarness{architect: []string{clarifyJSON}, answer: answerJSON}
	r := sessionRig(t, h)
	if err := r.f.handlePlan(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	wo := r.reload(t)
	if wo.State != model.StateWaitingForHuman || wo.ClarifRounds != 2 || len(wo.Clarifications) != 2 {
		t.Fatalf("state %s rounds %d answers %d; two answers then a human", wo.State, wo.ClarifRounds, len(wo.Clarifications))
	}
	foremanRuns := 0
	for _, s := range h.starts {
		if s.Role == model.AgentForeman {
			foremanRuns++
		}
	}
	if foremanRuns != 1 || len(h.conts) != 2 { // intake starts the session; both answers continue it
		t.Fatalf("starts %d continues %d", foremanRuns, len(h.conts))
	}
}

func TestCancelClosesTheForemanSession(t *testing.T) {
	ctx := context.Background()
	h := &sessHarness{answer: answerJSON}
	r := sessionRig(t, h)
	r.f.Sched = &fakeQueue{}
	r.f.Catalog = &Cataloger{Knowledge: &JournalStore{KB: r.know, Proj: r.f.Proj}, Proj: r.f.Proj, Settings: nil, Log: r.f.Log}
	if err := r.f.handlePlan(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if err := r.f.Cancel(ctx, r.wo.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.closed) != 1 {
		t.Fatalf("cancel must close the session: %v", h.closed)
	}
}
