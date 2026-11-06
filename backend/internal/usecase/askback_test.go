package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestBuilderQuestion(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
		asked            bool
	}{
		{"final line", "tried a few things\nNEEDS_CONTEXT: Round half up or down?", "Round half up or down?", true},
		{"trailing newline", "NEEDS_CONTEXT: Which locale?\n\n", "Which locale?", true},
		{"decorated", "partial\n**NEEDS_CONTEXT: Which locale?**", "Which locale?", true},
		{"emphasis around the colon", "**NEEDS_CONTEXT:** Which locale?", "Which locale?", true},
		{"lower case", "needs_context: Which locale?", "Which locale?", true},
		{"bare marker", "NEEDS_CONTEXT:", "", true},
		{"done", "implemented rounding", "", false},
		{"marker mid-reply", "NEEDS_CONTEXT: Which locale?\nthen I decided anyway", "", false},
		{"empty reply", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, asked := builderQuestion(tc.text); got != tc.want || asked != tc.asked {
				t.Fatalf("builderQuestion(%q) = %q, %v; want %q, %v", tc.text, got, asked, tc.want, tc.asked)
			}
		})
	}
}

// askHarness replies per role from an ordered script; the last reply repeats.
type askHarness struct {
	mu      sync.Mutex
	script  map[model.AgentRole][]string
	reqs    map[string]model.AgentRequest
	prompts map[model.AgentRole][]string
}

func (*askHarness) Name() string                         { return "fake" }
func (*askHarness) Cancel(context.Context, string) error { return nil }
func (h *askHarness) Start(_ context.Context, req model.AgentRequest) (model.AgentSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reqs == nil {
		h.reqs, h.prompts = map[string]model.AgentRequest{}, map[model.AgentRole][]string{}
	}
	id := fmt.Sprintf("a%d", len(h.reqs)+1)
	h.reqs[id] = req
	h.prompts[req.Role] = append(h.prompts[req.Role], req.Prompt)
	return idSession(id), nil
}
func (h *askHarness) Result(_ context.Context, id string) (model.AgentResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	role := h.reqs[id].Role
	q := h.script[role]
	reply := q[0]
	if len(q) > 1 {
		h.script[role] = q[1:]
	}
	return model.AgentResult{Text: reply}, nil
}

func (h *askHarness) runs(role model.AgentRole) int { return len(h.prompts[role]) }

const (
	halfUp       = "Round half up or down?"
	askHalfUp    = "partial edits\nNEEDS_CONTEXT: " + halfUp
	archAnswer   = `{"decision":"ANSWER","answer":"Half up.","reason":"the brief rounds money to the nearest rupiah"}`
	archEscalate = `{"decision":"ESCALATE","reason":"a product call"}`
)

func askRig(t *testing.T, script map[model.AgentRole][]string) (*rig, *askHarness) {
	t.Helper()
	h := &askHarness{script: script}
	r := newRig(t)
	r.f.Resolve = func(model.AgentRole, *model.WorkOrder) Harness { return h }
	r.f.Sandbox = filesSandbox{files: []string{"x.go"}}
	r.workspace(t, map[string]string{"README.md": "# x\n"})
	ctx := context.Background()
	if err := r.wos.ReplacePlan(ctx, r.wo.ID, []model.Task{{ID: "T1", WOID: r.wo.ID, Title: "round", Role: "builder"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.wos.UpdateStateAndFields(ctx, r.wo.ID, model.StateQueued, map[string]any{"spec": "SPEC"}); err != nil {
		t.Fatal(err)
	}
	return r, h
}

// implement runs the task's job the way the scheduler does: an ErrRequeue runs it again.
func (r *rig) implement(t *testing.T) {
	t.Helper()
	for i := 0; i < 8; i++ {
		err := r.f.handleImplement(context.Background(), model.Job{WOID: r.wo.ID, TaskID: "T1"})
		if err == nil {
			return
		}
		if !errors.Is(err, model.ErrRequeue) {
			t.Fatal(err)
		}
	}
	t.Fatal("job keeps requeueing")
}

func (r *rig) setLog(t *testing.T, log string) {
	t.Helper()
	if err := r.wos.UpdateFields(context.Background(), r.wo.ID, map[string]any{"clarif_log": log}); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) doneTasks(t *testing.T) int {
	t.Helper()
	done, err := r.f.Runs.CompletedBuilderTasks(context.Background(), r.wo.ID)
	if err != nil {
		t.Fatal(err)
	}
	return len(done)
}

func TestArchitectAnswerAmendsSpecAndRerunsTheTask(t *testing.T) {
	r, h := askRig(t, map[model.AgentRole][]string{
		model.AgentBuilder:   {askHalfUp, "rounded"},
		model.AgentArchitect: {archAnswer},
	})
	r.implement(t)
	wo := r.reload(t)
	if !strings.Contains(wo.Spec, "Amendment: "+halfUp+" → Half up. (reason: the brief rounds money") || !strings.HasPrefix(wo.Spec, "SPEC") {
		t.Fatalf("spec = %q", wo.Spec)
	}
	if len(wo.Clarifications) != 1 || wo.Clarifications[0].By != "architect" || wo.Clarifications[0].Answer != "Half up." {
		t.Fatalf("clarifications = %+v", wo.Clarifications)
	}
	if h.runs(model.AgentBuilder) != 2 || h.runs(model.AgentArchitect) != 1 || h.runs(model.AgentForeman) != 0 {
		t.Fatalf("runs: builder %d architect %d foreman %d", h.runs(model.AgentBuilder), h.runs(model.AgentArchitect), h.runs(model.AgentForeman))
	}
	if second := h.prompts[model.AgentBuilder][1]; !strings.Contains(second, "A: Half up.") || !strings.Contains(second, "Amendment: "+halfUp) {
		t.Fatalf("the re-run must carry the answer:\n%s", second)
	}
	if !strings.Contains(h.prompts[model.AgentArchitect][0], halfUp) {
		t.Fatal("the Architect was not given the question")
	}
	if r.doneTasks(t) != 1 || wo.State != model.StateVerifying {
		t.Fatalf("done = %d state = %s; the task completes only on the second run", r.doneTasks(t), wo.State)
	}
	if len(r.events(t, "clarification_asked")) != 1 || len(r.events(t, "clarification_answered")) != 1 {
		t.Fatal("expected one clarification_asked and one clarification_answered event")
	}
}

func TestNeedsContextRunIsNotTaskCompletion(t *testing.T) {
	r, _ := askRig(t, map[model.AgentRole][]string{
		model.AgentBuilder:   {askHalfUp},
		model.AgentArchitect: {archEscalate},
		model.AgentForeman:   {archEscalate},
	})
	r.implement(t)
	runs, _ := r.f.Runs.(*store.RunRepo).List(context.Background(), r.wo.ID)
	for _, run := range runs {
		if run.Kind == "builder" && run.State == "completed" {
			t.Fatalf("a NEEDS_CONTEXT run must not complete: %+v", run)
		}
	}
	if r.doneTasks(t) != 0 {
		t.Fatal("the task counted as done")
	}
}

func TestArchitectEscalationGoesToTheForeman(t *testing.T) {
	r, h := askRig(t, map[model.AgentRole][]string{
		model.AgentBuilder:   {askHalfUp, "rounded"},
		model.AgentArchitect: {archEscalate},
		model.AgentForeman:   {`{"decision":"ANSWER","answer":"Half up."}`},
	})
	r.implement(t)
	wo := r.reload(t)
	if len(wo.Clarifications) != 1 || wo.Clarifications[0].By != "foreman" || !strings.Contains(wo.Spec, "Amendment: "+halfUp+" → Half up. (reason: answered by the foreman)") {
		t.Fatalf("clarifications = %+v spec = %q", wo.Clarifications, wo.Spec)
	}
	fp := h.prompts[model.AgentForeman][0]
	if !strings.Contains(fp, halfUp) || !strings.Contains(fp, "a product call") {
		t.Fatalf("the Foreman needs the question and the Architect's reason:\n%s", fp)
	}
	if r.doneTasks(t) != 1 {
		t.Fatal("the task should finish after the Foreman's answer")
	}
}

func TestForemanEscalationParksTheOrderForAHumanWhoResumesTheTask(t *testing.T) {
	r, h := askRig(t, map[model.AgentRole][]string{
		model.AgentBuilder:   {askHalfUp, "rounded"},
		model.AgentArchitect: {archEscalate},
		model.AgentForeman:   {archEscalate},
	})
	r.implement(t)
	wo := r.reload(t)
	if wo.State != model.StateWaitingForHuman || wo.ClarifQuestion != halfUp || wo.ClarifTask != "T1" || r.doneTasks(t) != 0 {
		t.Fatalf("state %s question %q task %q done %d", wo.State, wo.ClarifQuestion, wo.ClarifTask, r.doneTasks(t))
	}
	if len(r.events(t, "clarification_requested")) != 1 {
		t.Fatal("the human question was not announced")
	}

	// a sibling task's job that starts while the order waits does nothing
	r.q.enqueued, r.q.payloads = nil, nil
	if err := r.f.handleImplement(context.Background(), model.Job{WOID: r.wo.ID, TaskID: "T2"}); err != nil || h.runs(model.AgentBuilder) != 1 {
		t.Fatalf("err = %v, builder runs = %d", err, h.runs(model.AgentBuilder))
	}

	if err := r.f.AnswerClarification(context.Background(), r.wo.ID, "Half up."); err != nil {
		t.Fatal(err)
	}
	wo = r.reload(t)
	if wo.State != model.StateImplementing || wo.ClarifTask != "" || len(wo.Clarifications) != 1 || wo.Clarifications[0].By != "human" ||
		!strings.Contains(wo.Spec, "Amendment: "+halfUp+" → Half up. (reason: decided by a human)") {
		t.Fatalf("state %s task %q clarifications %+v spec %q", wo.State, wo.ClarifTask, wo.Clarifications, wo.Spec)
	}
	if wo.Clarifications[0].Task != "T1" || len(r.q.enqueued) == 0 || r.q.enqueued[0] != JobImplement+":T1" {
		t.Fatalf("enqueued %v; the asking task must be ready again", r.q.enqueued)
	}
	r.implement(t)
	if r.doneTasks(t) != 1 || !strings.Contains(h.prompts[model.AgentBuilder][1], "A: Half up.") {
		t.Fatal("the resumed task did not finish with the human answer in its prompt")
	}
}

func TestRepeatedBuilderQuestionReusesTheStoredAnswer(t *testing.T) {
	r, h := askRig(t, map[model.AgentRole][]string{
		model.AgentBuilder: {"NEEDS_CONTEXT:  " + strings.ToUpper(halfUp), "rounded"},
	})
	raw := `[{"question":"` + halfUp + `","answer":"Half up.","by":"architect"}]`
	if err := r.wos.UpdateFields(context.Background(), r.wo.ID, map[string]any{"clarif_log": raw}); err != nil {
		t.Fatal(err)
	}
	r.implement(t)
	if h.runs(model.AgentArchitect) != 0 || h.runs(model.AgentForeman) != 0 || h.runs(model.AgentBuilder) != 2 || r.doneTasks(t) != 1 {
		t.Fatalf("architect %d foreman %d builder %d", h.runs(model.AgentArchitect), h.runs(model.AgentForeman), h.runs(model.AgentBuilder))
	}
	if evs := r.events(t, "clarification_answered"); len(evs) != 1 || evs[0].Payload["repeated"] != true {
		t.Fatalf("events = %+v", evs)
	}
}

func TestQuestionCap(t *testing.T) {
	t.Run("third question skips the Architect", func(t *testing.T) {
		r, h := askRig(t, map[model.AgentRole][]string{
			model.AgentBuilder:   {"NEEDS_CONTEXT: q1?", "NEEDS_CONTEXT: q2?", "NEEDS_CONTEXT: q3?", "NEEDS_CONTEXT: q4?"},
			model.AgentArchitect: {`{"decision":"ANSWER","answer":"a"}`},
			model.AgentForeman:   {archEscalate},
		})
		r.implement(t)
		wo := r.reload(t)
		if h.runs(model.AgentArchitect) != 2 || h.runs(model.AgentForeman) != 1 || h.runs(model.AgentBuilder) != 3 ||
			wo.State != model.StateWaitingForHuman || wo.ClarifQuestion != "q3?" {
			t.Fatalf("architect %d foreman %d builder %d state %s question %q",
				h.runs(model.AgentArchitect), h.runs(model.AgentForeman), h.runs(model.AgentBuilder), wo.State, wo.ClarifQuestion)
		}
	})
	t.Run("a fourth question goes straight to a human", func(t *testing.T) {
		r, h := askRig(t, map[model.AgentRole][]string{model.AgentBuilder: {"NEEDS_CONTEXT: q4?"}})
		r.setLog(t, `[{"question":"a?","answer":"x","task":"T1"},{"question":"b?","answer":"x","task":"T1"},{"question":"c?","answer":"x","task":"T1"}]`)
		r.implement(t)
		wo := r.reload(t)
		if h.runs(model.AgentArchitect) != 0 || h.runs(model.AgentForeman) != 0 || wo.State != model.StateWaitingForHuman || wo.ClarifTask != "T1" {
			t.Fatalf("architect %d foreman %d state %s", h.runs(model.AgentArchitect), h.runs(model.AgentForeman), wo.State)
		}
	})
}
