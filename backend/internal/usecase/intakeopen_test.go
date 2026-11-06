package usecase

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

const intakeAsksJSON = `{"outcome":"Round money in integer rupiah","why":"floats drift",
 "open_questions":[{"question":"Round half up or down?","options":["up","down"],"recommended":"up, matches invoices"},
  {"question":"Which currency?","options":["IDR"],"recommended":"IDR"}]}`

func TestIntakeAsksTheHumanOneQuestionAtATimeAndUpdatesTheBrief(t *testing.T) {
	h := &sessHarness{intakes: []string{intakeAsksJSON, intakeJSON}}
	r := sessionRig(t, h)
	ctx := context.Background()
	r.plan(t)

	wo := r.reload(t)
	if wo.State != model.StateWaitingForHuman || !strings.HasPrefix(wo.ClarifQuestion, "Round half up or down?") ||
		!strings.Contains(wo.ClarifQuestion, "Options: up / down.") || !strings.Contains(wo.ClarifQuestion, "Recommended: up, matches invoices.") {
		t.Fatalf("state %s question %q", wo.State, wo.ClarifQuestion)
	}
	if !strings.Contains(wo.Brief, openQuestionsHeader) || wo.Spec != "" {
		t.Fatalf("the brief carries the open questions and no plan exists yet:\n%s", wo.Brief)
	}

	if err := r.f.AnswerClarification(ctx, r.wo.ID, "Up."); err != nil {
		t.Fatal(err)
	}
	r.plan(t)

	wo = r.reload(t)
	if wo.State != model.StateArchitectReview || wo.Spec != "sp" {
		t.Fatalf("state %s spec %q; the answered brief must lead to a plan", wo.State, wo.Spec)
	}
	if strings.Contains(wo.Brief, openQuestionsHeader) || !strings.Contains(wo.Brief, "Round money in integer rupiah") {
		t.Fatalf("the brief is updated in place without the open questions:\n%s", wo.Brief)
	}
	if len(wo.Clarifications) != 1 || wo.Clarifications[0].By != "human" || wo.Clarifications[0].Answer != "Up." {
		t.Fatalf("the answer joins the clarifications log: %+v", wo.Clarifications)
	}
	intakes := 0
	for _, s := range h.starts {
		if s.Context["stage"] == "intake" {
			intakes++
			if intakes == 2 && !strings.Contains(s.Prompt, "Q: Round half up or down?") {
				t.Fatalf("the second intake lacks the answer:\n%s", s.Prompt)
			}
		}
	}
	if intakes != 2 {
		t.Fatalf("intake ran %d times, want 2", intakes)
	}
}

func TestIntakeStopsAskingAfterFiveHumanAnswers(t *testing.T) {
	h := &sessHarness{intakes: []string{intakeAsksJSON}}
	r := sessionRig(t, h)
	ctx := context.Background()
	for i := 0; i < maxHumanQuestions; i++ {
		r.plan(t)
		if wo := r.reload(t); wo.State != model.StateWaitingForHuman {
			t.Fatalf("round %d: state %s", i, wo.State)
		}
		if err := r.f.AnswerClarification(ctx, r.wo.ID, fmt.Sprintf("a%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	r.plan(t)
	wo := r.reload(t)
	if wo.State != model.StateArchitectReview {
		t.Fatalf("state %s; the sixth question must become an assumption", wo.State)
	}
	if strings.Contains(wo.Brief, openQuestionsHeader) || !strings.Contains(wo.Brief, "Unanswered, default taken: Round half up or down?") {
		t.Fatalf("unresolved questions become assumptions:\n%s", wo.Brief)
	}
}

func TestOverBudgetBriefGetsOneRewriteThenAWarning(t *testing.T) {
	long := `{"outcome":"` + strings.Repeat("word ", 500) + `"}`
	h := &sessHarness{intakes: []string{long}}
	r := sessionRig(t, h)
	r.plan(t)
	if len(h.conts) != 1 || !strings.Contains(h.conts[0], "brief is 500 words (budget 350)") {
		t.Fatalf("one rewrite request with the counts is expected: %v", h.conts)
	}
	if ev := r.events(t, "budget_exceeded"); len(ev) != 1 {
		t.Fatalf("budget_exceeded events: %d", len(ev))
	}
}

func TestOverBudgetSpecAndTaskGetOneRewrite(t *testing.T) {
	long := fmt.Sprintf(`{"decision":"APPROVE","summary":"s","spec":"%s","tasks":[{"id":"t1","title":"a","objective":"%s"}]}`,
		strings.Repeat("rule ", 1000), strings.Repeat("do ", 200))
	short := `{"decision":"APPROVE","summary":"s","spec":"short","tasks":[{"id":"t1","title":"a","objective":"o"}]}`
	h := &sessHarness{architect: []string{long, short}}
	r := sessionRig(t, h)
	r.plan(t)
	var arch []model.AgentRequest
	for _, s := range h.starts {
		if s.Role == model.AgentArchitect {
			arch = append(arch, s)
		}
	}
	if len(arch) != 2 || !strings.Contains(arch[1].Prompt, "spec is 1000 words (budget 750); task 1 objective is 200 words (budget 150)") {
		t.Fatalf("architect runs %d; the rewrite must carry the counts", len(arch))
	}
	if wo := r.reload(t); wo.Spec != "short" {
		t.Fatalf("the rewrite replaces the draft: %q", wo.Spec)
	}
	if ev := r.events(t, "budget_exceeded"); len(ev) != 0 {
		t.Fatalf("a rewrite within budget must not warn: %d", len(ev))
	}
}

func TestPlanPromptAsksForTheContractShape(t *testing.T) {
	p := planPrompt(&model.WorkOrder{Title: "t"}, "", "", nil)
	for _, want := range []string{"Behaviour rules", "# | Decision | Why | Rejected alternative", "AC | Verification | Kind", "No file paths, line numbers or code", "under 750 words", "under 150 words"} {
		if !strings.Contains(p, want) {
			t.Errorf("plan prompt lacks %q", want)
		}
	}
}
