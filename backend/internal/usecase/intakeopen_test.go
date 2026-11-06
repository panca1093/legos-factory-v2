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
	var intakes []string
	for i := 0; i <= maxHumanQuestions; i++ {
		intakes = append(intakes, fmt.Sprintf(`{"outcome":"o","open_questions":[{"question":"Q%d?","recommended":"r"}]}`, i))
	}
	h := &sessHarness{intakes: intakes}
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
	if strings.Contains(wo.Brief, openQuestionsHeader) || !strings.Contains(wo.Brief, "Unanswered, default taken: Q5?") {
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

func TestOpenQuestionsTextInABriefDoesNotRerunIntake(t *testing.T) {
	h := &sessHarness{}
	r := sessionRig(t, h)
	ctx := context.Background()
	if err := r.wos.UpdateFields(ctx, r.wo.ID, map[string]any{"brief": "Do X\n\n" + openQuestionsHeader + "\n- stale text"}); err != nil {
		t.Fatal(err)
	}
	r.plan(t)
	for _, s := range h.starts {
		if s.Context["stage"] == "intake" {
			t.Fatal("intake must run only when the order says it is pending")
		}
	}
	if wo := r.reload(t); wo.State != model.StateArchitectReview || wo.IntakePending {
		t.Fatalf("state %s pending %v", wo.State, wo.IntakePending)
	}
}

func TestIntakePendingIsStoredAndClearedWithTheQuestions(t *testing.T) {
	h := &sessHarness{intakes: []string{intakeAsksJSON, intakeJSON}}
	r := sessionRig(t, h)
	r.plan(t)
	if wo := r.reload(t); !wo.IntakePending {
		t.Fatal("asking the human must set intake_pending")
	}
	if err := r.f.AnswerClarification(context.Background(), r.wo.ID, "Up."); err != nil {
		t.Fatal(err)
	}
	r.plan(t)
	if wo := r.reload(t); wo.IntakePending {
		t.Fatal("a brief without open questions must clear intake_pending")
	}
}

func TestIntakeDoesNotAskAQuestionTheHumanAlreadyAnswered(t *testing.T) {
	h := &sessHarness{intakes: []string{intakeAsksJSON}}
	r := sessionRig(t, h)
	asked := openQuestion{Question: "Round half up or down?", Options: []string{"up", "down"}, Recommended: "up, matches invoices"}
	other := openQuestion{Question: "Which currency?", Options: []string{"IDR"}, Recommended: "IDR"}
	log := fmt.Sprintf(`[{"question":%q,"answer":"Down.","by":"human"},{"question":%q,"answer":"IDR.","by":"human"}]`, asked.ask(), other.ask())
	if err := r.wos.UpdateFields(context.Background(), r.wo.ID, map[string]any{"clarif_log": log}); err != nil {
		t.Fatal(err)
	}
	r.plan(t)
	wo := r.reload(t)
	if wo.State != model.StateArchitectReview || wo.IntakePending {
		t.Fatalf("state %s pending %v; answered questions must not go to the human again", wo.State, wo.IntakePending)
	}
	if !strings.Contains(wo.Brief, "Settled: Round half up or down? -> Down.") || strings.Contains(wo.Brief, openQuestionsHeader) {
		t.Fatalf("answered questions become assumptions:\n%s", wo.Brief)
	}
}

func TestRewriteErrIgnoresOnlyInvalidReplies(t *testing.T) {
	invalid := fmt.Errorf("%w: %w: x", model.ErrPermanent, errReplyInvalid)
	if rewriteErr(invalid) != nil || rewriteErr(nil) != nil {
		t.Fatal("an invalid rewrite keeps the first draft")
	}
	for _, err := range []error{fmt.Errorf("%w: budget exhausted", model.ErrPermanent), context.Canceled} {
		if rewriteErr(err) == nil {
			t.Fatalf("%v must not be swallowed", err)
		}
	}
}

func TestHumanAnswerIsReusedWhenTheArchitectAsksAgain(t *testing.T) {
	h := &sessHarness{architect: []string{clarifyJSON, planJSON}, answer: answerJSON}
	r := sessionRig(t, h)
	ctx := context.Background()
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateWaitingForHuman); err != nil {
		t.Fatal(err)
	}
	if err := r.wos.UpdateFields(ctx, r.wo.ID, map[string]any{"clarif_question": "Round half up or down?"}); err != nil {
		t.Fatal(err)
	}
	if err := r.f.AnswerClarification(ctx, r.wo.ID, "Half down."); err != nil {
		t.Fatal(err)
	}
	r.plan(t)
	for _, s := range h.starts {
		if s.Role == model.AgentForeman && s.Context["stage"] == "answer" {
			t.Fatal("the Foreman must not be asked a question the human answered")
		}
	}
	ev := r.events(t, "clarification_answered")
	if wo := r.reload(t); wo.State != model.StateArchitectReview || len(ev) < 2 || ev[len(ev)-1].Message != "Half down." {
		t.Fatalf("state %s events %d; the stored human answer must be reused", wo.State, len(ev))
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
