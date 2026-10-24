package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"legos-factory/backend/internal/agents"
	"legos-factory/backend/internal/model"
)

// Harness is a deterministic, scripted agent harness used for local
// development, tests and the demo lifecycle. It performs REAL filesystem
// actions for builder runs but produces canned reasoning. Every run records
// harness="mock" so the UI can label it clearly as scripted.
type Harness struct {
	next sessionIDGen
	mu   sync.Mutex
	ses  map[string]*mockSession
}

type sessionIDGen struct{ n atomic.Int64 }

type mockSession struct {
	id     string
	req    model.AgentRequest
	result model.AgentResult
	done   chan struct{}
	err    error
}

func New() *Harness {
	return &Harness{ses: map[string]*mockSession{}}
}

func (h *Harness) Name() string { return "mock" }

func (s *mockSession) ID() string { return s.id }

func (h *Harness) Start(ctx context.Context, req model.AgentRequest) (agents.AgentSession, error) {
	s := &mockSession{id: fmt.Sprintf("mock-%d", h.next.n.Add(1)), req: req, done: make(chan struct{})}
	h.mu.Lock()
	h.ses[s.id] = s
	h.mu.Unlock()
	go func() {
		defer close(s.done)
		s.result, s.err = h.execute(ctx, req)
	}()
	return s, nil
}

func (h *Harness) Cancel(_ context.Context, sessionID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, ok := h.ses[sessionID]; ok {
		select {
		case <-s.done:
		default:
			s.err = fmt.Errorf("cancelled")
			close(s.done)
		}
		return nil
	}
	return agents.ErrSessionNotFound
}

func (h *Harness) Result(_ context.Context, sessionID string) (model.AgentResult, error) {
	h.mu.Lock()
	s, ok := h.ses[sessionID]
	h.mu.Unlock()
	if !ok {
		return model.AgentResult{}, agents.ErrSessionNotFound
	}
	<-s.done
	if s.err != nil {
		return model.AgentResult{}, s.err
	}
	return s.result, nil
}

// execute produces the scripted behavior per role.
func (h *Harness) execute(ctx context.Context, req model.AgentRequest) (model.AgentResult, error) {
	res := model.AgentResult{
		SessionRef: "mock-session",
		CostSource: "estimated",
		CostMicros: 5000, // illustrative fixed step cost, clearly not billing
		TokensIn:   500,
		TokensOut:  400,
	}
	switch req.Role {
	case model.AgentArchitect:
		if isPlanReview(req) {
			return h.architectReview(req, res)
		}
		return h.architectPlan(req, res)
	case model.AgentBuilder:
		return h.builder(req, res)
	case model.AgentInspector:
		return h.inspector(req, res)
	case model.AgentCataloger:
		res.Text = fmt.Sprintf("Scripted mock summary of %s knowledge record(s); not a real model output.", req.Context["record_count"])
		return res, nil
	default:
		return res, nil
	}
}

func isPlanReview(req model.AgentRequest) bool {
	_, ok := req.Context["plan_json"]
	return ok
}

// architectPlan decomposes the objective into a plan. When the objective
// contains an explicit open question marker ("?") the architect requests
// human clarification instead of inventing product decisions.
func (h *Harness) architectPlan(req model.AgentRequest, res model.AgentResult) (model.AgentResult, error) {
	obj := req.Context["objective"]
	acc := req.Context["acceptance"]
	if hasOpenQuestion(obj) {
		res.Text = fmt.Sprintf(`{"decision":"CLARIFY","question":"Objective contains an unresolved question: %s","question_text":"Please specify the exact expected behavior so the plan can be finalized."}`, jsEscape(firstSentence(obj)))
		return res, nil
	}
	plan := map[string]any{
		"decision": "APPROVE",
		"summary":  "Scripted decomposition of: " + firstSentence(obj),
		"tasks": []map[string]any{
			{
				"id": "task-1", "title": "Record failing baseline",
				"objective": "Reproduce the gap described by the acceptance criteria.", "role": "builder", "deps": []string{},
			},
			{
				"id": "task-2", "title": "Implement change",
				"objective": "Implement the smallest change satisfying: " + firstSentence(acc), "role": "builder", "deps": []string{"task-1"},
			},
			{
				"id": "task-3", "title": "Contract tests",
				"objective": "Add/adjust tests covering the acceptance criteria.", "role": "builder", "deps": []string{"task-1"},
			},
			{
				"id": "task-4", "title": "Integrate and verify",
				"objective": "Merge branch work and run the full verification suite.", "role": "builder", "deps": []string{"task-2", "task-3"},
			},
		},
	}
	return finishJSON(res, plan)
}

// architectReview re-reviews a recorded plan (and clarification answer).
func (h *Harness) architectReview(req model.AgentRequest, res model.AgentResult) (model.AgentResult, error) {
	answer := req.Context["clarification_answer"]
	if answer == "" && req.Context["has_open_question"] == "true" {
		res.Text = `{"decision":"CLARIFY","question_text":"The recorded clarification has not been answered yet."}`
		return res, nil
	}
	return finishJSON(res, map[string]any{"decision": "APPROVE", "notes": "Plan is complete after clarification."})
}

// builder performs a real filesystem change in the workspace: it writes the
// implementation required by the fixture project's failing test.
func (h *Harness) builder(req model.AgentRequest, res model.AgentResult) (model.AgentResult, error) {
	dir := req.WorkspaceDir
	if dir == "" {
		return res, fmt.Errorf("builder run requires a workspace")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, err
	}
	impl := `package greeter

// Greet returns the greeting used by the acceptance test.
func Greet(name string) string {
	if name == "" {
		return "Hello, stranger!"
	}
	return "Hello, " + name + "!"
}
`
	target := filepath.Join(dir, "greeter", "greeter.go")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return res, err
	}
	if err := os.WriteFile(target, []byte(impl), 0o644); err != nil {
		return res, err
	}
	res.Text = fmt.Sprintf("Mock builder wrote %s (real filesystem change, scripted content).", target)
	return res, nil
}

// inspector produces one scripted blocker finding on the first inspection of
// a work order (revision 0), mirroring the prototype's QA story. Later
// revisions pass once the fix has landed.
func (h *Harness) inspector(req model.AgentRequest, res model.AgentResult) (model.AgentResult, error) {
	if req.Context["revision"] == "0" {
		res.Text = `{"findings":[{"severity":"blocker","title":"Sample regression: acceptance check failed on first revision","detail":"Scripted mock finding to exercise the remediation loop.","location":"greeter/greeter.go"}]}`
		return res, nil
	}
	res.Text = `{"findings":[]}`
	return res, nil
}

func finishJSON(res model.AgentResult, v any) (model.AgentResult, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return res, err
	}
	res.Text = string(raw)
	return res, nil
}

func hasOpenQuestion(s string) bool {
	return strings.Contains(s, "?")
}

func firstSentence(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '.' {
			return s[:i]
		}
	}
	if len(s) > 120 {
		return s[:120]
	}
	return s
}

func jsEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `"`, `\"`), "\n", "  ")
}
