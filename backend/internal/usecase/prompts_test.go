package usecase

import (
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestPromptsCarryClarificationAnswer(t *testing.T) {
	wo := &model.WorkOrder{Title: "t", ClarifQuestion: "round what?", ClarifAnswer: "the discount"}
	for name, p := range map[string]string{
		"plan":        planPrompt(wo, "", "", nil),
		"review":      foremanReviewPrompt(wo, nil, nil, false),
		"implement":   implementPrompt(wo, model.Job{TaskID: "task-1"}, nil),
		"remediation": implementPrompt(wo, model.Job{Payload: map[string]any{"reason": "fix"}}, nil),
		"inspect":     inspectPrompt(wo, SpecialistPick{Specialty: "testing"}, nil),
	} {
		if !strings.Contains(p, "Q: round what?\nA: the discount") {
			t.Errorf("%s prompt lacks the clarification:\n%s", name, p)
		}
	}
}

func TestReviewPromptListsTaskScope(t *testing.T) {
	p := foremanReviewPrompt(&model.WorkOrder{Title: "t"}, []model.Task{{Title: "a", Objective: "b", Scope: []string{"x/", "y.go"}}}, nil, false)
	if !strings.Contains(p, "Scope: x/, y.go") {
		t.Fatalf("review prompt lacks the task scope:\n%s", p)
	}
}

func TestInspectPromptHasFocusForCustomSpecialty(t *testing.T) {
	p := inspectPrompt(&model.WorkOrder{}, SpecialistPick{Specialty: "payments"}, nil)
	if !strings.Contains(p, "Focus: Correctness, risks and regressions within the payments area.") {
		t.Fatalf("custom specialty has no focus:\n%s", p)
	}
}

func TestImplementPromptRendersFailedChecks(t *testing.T) {
	wo := &model.WorkOrder{Title: "t"}
	p := implementPrompt(wo, model.Job{Payload: map[string]any{
		"reason": "fix_failed_verification", "failed_checks": `Check "go test": FAIL (exit code 1)`,
	}}, nil)
	for _, want := range []string{"Verification failed on these checks; fix the cause.", "Do not edit .legos/checks.json", `Check "go test"`} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
}

func TestImplementPromptFallsBackWithoutFailedChecks(t *testing.T) {
	wo := &model.WorkOrder{Title: "t"}
	p := implementPrompt(wo, model.Job{Payload: map[string]any{"reason": "fix_failed_verification", "failed_checks": ""}}, nil)
	if !strings.Contains(p, "requires remediation") || strings.Contains(p, "Verification failed on these checks") {
		t.Fatalf("expected the generic remediation prompt:\n%s", p)
	}
}

func TestImplementPromptNamesTaskScopeAndOffLimitTasks(t *testing.T) {
	wo := &model.WorkOrder{Title: "t"}
	tasks := []model.Task{
		{ID: "wo_1_t1", Title: "Add the parser", Objective: "Parse dates", Scope: []string{"internal/parse/", "parse.go"}},
		{ID: "wo_1_t2", Title: "Wire the endpoint", Objective: "Expose it"},
		{ID: "wo_1_t3", Title: "Write the docs", Objective: "Document it"},
	}
	p := implementPrompt(wo, model.Job{TaskID: "wo_1_t1"}, tasks)
	for _, want := range []string{
		"Your task: Add the parser — Parse dates",
		"Scope (the files or areas you may touch): internal/parse/, parse.go",
		"Other tasks — do not implement these",
		"- Wire the endpoint", "- Write the docs",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "- Add the parser") {
		t.Errorf("the task itself must not be listed as off limits:\n%s", p)
	}
	solo := implementPrompt(wo, model.Job{TaskID: "wo_1_t1"}, tasks[:1])
	if strings.Contains(solo, "do not implement") {
		t.Errorf("a one-task plan has no other tasks:\n%s", solo)
	}
	if strings.Contains(implementPrompt(wo, model.Job{TaskID: "gone"}, tasks), "Your task:") {
		t.Error("an unknown task id must fall back to the plain prompt")
	}
}

func TestReviewAndConvergePromptsNumberTheCriteria(t *testing.T) {
	wo := &model.WorkOrder{Title: "t", Acceptance: "- rounds half up\n- never floats"}
	for name, p := range map[string]string{
		"review":   foremanReviewPrompt(wo, nil, nil, false),
		"converge": convergePrompt(wo, nil, []FileDiff{{Path: "a.go", Added: 1, Hunks: "@@ -0,0 +1 @@\n+x\n"}}, "go test → PASS"),
	} {
		if !strings.Contains(p, "c1: rounds half up\nc2: never floats") {
			t.Errorf("%s prompt lacks the numbered criteria:\n%s", name, p)
		}
	}
	if p := convergePrompt(wo, nil, []FileDiff{{Path: "a.go", Added: 1, Hunks: "@@ -0,0 +1 @@\n+x\n"}}, ""); !strings.Contains(p, "--- a.go (+1 -0)") || !strings.Contains(p, "+x") {
		t.Errorf("converge prompt lacks the diff:\n%s", p)
	}
}

func TestPlanPromptRevisionCarriesPreviousDraft(t *testing.T) {
	wo := &model.WorkOrder{ID: "wo1", Title: "t", Spec: "the old spec body"}
	prev := []model.Task{
		{ID: "wo1_task-1", Title: "Add cart endpoint", Objective: "expose POST", Scope: []string{"api/cart.go"}, Deps: nil},
		{ID: "wo1_task-2", Title: "Show total", Objective: "sum items", Scope: []string{"ui/total.go", "ui/cart.go"}, Deps: []string{"wo1_task-1"}},
	}
	p := planPrompt(wo, "1. criterion c1 is not covered\n2. tasks overlap", "", prev)
	for _, want := range []string{
		"the old spec body", "task-1", "Add cart endpoint", "expose POST", "api/cart.go",
		"task-2", "Show total", "ui/total.go, ui/cart.go", "deps: task-1",
		"1. criterion c1 is not covered\n2. tasks overlap",
		"keep every part the notes do not criticise", "keep task ids stable", "say in summary what changed",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("revision prompt missing %q:\n%s", want, p)
		}
	}
}

func TestPlanPromptFirstDraftHasNoRevisionText(t *testing.T) {
	p := planPrompt(&model.WorkOrder{Title: "t"}, "", "", nil)
	if strings.Contains(p, "Previous draft") || strings.Contains(p, "task ids stable") {
		t.Fatalf("first draft prompt has revision text:\n%s", p)
	}
}
