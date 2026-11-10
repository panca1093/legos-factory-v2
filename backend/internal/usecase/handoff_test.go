package usecase

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestBuilderNotes(t *testing.T) {
	long := strings.Repeat("x", 400)
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"no section", "implemented rounding", nil},
		{"none", "done\n\nNotes for review:\nnone", nil},
		{"none upper case with full stop", "done\nNOTES FOR REVIEW:\nNone.", nil},
		{"none inline", "done\n**Notes for review:** none", nil},
		{"heading with no items", "done\nNotes for review:\n", nil},
		{"dash list", "done\nNotes for review:\n- payload shape is a guess\n- spec rule 9 needs amending", []string{"payload shape is a guess", "spec rule 9 needs amending"}},
		{"star and numbered markers", "done\nNotes for review:\n* one\n1. two\n2) three", []string{"one", "two", "three"}},
		{"bold heading", "done\n**Notes for review:**\n- a", []string{"a"}},
		{"bold heading, colon outside", "done\n**Notes for review**:\n- a", []string{"a"}},
		{"markdown heading and lower case", "done\n## notes for review\n- a", []string{"a"}},
		{"italic heading without colon", "done\n_Notes for review_\n- a", []string{"a"}},
		{"item on the heading line", "done\nNotes for review: unverified on a live server", []string{"unverified on a live server"}},
		{"similar word is not a heading", "Notes for reviewers are welcome\n- a", nil},
		{"last heading wins", "Notes for review:\n- early\nmore work\nNotes for review:\n- late", []string{"late"}},
		{"more than five", "Notes for review:\n- 1\n- 2\n- 3\n- 4\n- 5\n- 6\n- 7", []string{"1", "2", "3", "4", "5"}},
		{"long item is cut", "Notes for review:\n- " + long, []string{long[:300]}},
		{"blank lines between items", "Notes for review:\n\n- a\n\n- b\n", []string{"a", "b"}},
		{"reply that asks a question", "partial edits\nNEEDS_CONTEXT: Round half up or down?", nil},
		{"prose that starts like the heading", "Notes for review are not needed here.", nil},
		{"closing fence is not a note", "Notes for review:\n- a\n```", []string{"a"}},
		{"n/a", "Notes for review:\nN/A", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := builderNotes(tc.text); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("builderNotes(%q) = %q; want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestBuilderQuestionIsFoundBeforeANotesSection(t *testing.T) {
	for _, text := range []string{
		"partial\nNEEDS_CONTEXT: half up?\n\nNotes for review:\nnone",
		"partial\nNEEDS_CONTEXT: half up?\n**Notes for review:**\n- unverified",
	} {
		if q, asked := builderQuestion(text); !asked || q != "half up?" {
			t.Errorf("builderQuestion(%q) = %q, %v", text, q, asked)
		}
	}
	if _, asked := builderQuestion("done\nNotes for review:\n- NEEDS_CONTEXT: not a question, a note"); asked {
		t.Error("a note is not a question")
	}
}

func TestImplementPromptAsksForNotesInEveryBranch(t *testing.T) {
	wo := &model.WorkOrder{Title: "t"}
	for name, job := range map[string]model.Job{
		"task":         {TaskID: "task-1"},
		"remediation":  {Payload: map[string]any{"reason": "fix_findings"}},
		"fix brief":    {Payload: map[string]any{"reason": "fix_findings", "brief": "1. a"}},
		"failed check": {Payload: map[string]any{"reason": "fix_failed_verification", "failed_checks": "Check x"}},
	} {
		p := implementPrompt(wo, job, nil)
		if !strings.Contains(p, notesInstruction) || !strings.Contains(p, `headed exactly "Notes for review:"`) {
			t.Errorf("%s prompt lacks the notes instruction:\n%s", name, p)
		}
	}
	task := implementPrompt(wo, model.Job{TaskID: "task-1"}, nil)
	if !strings.Contains(task, "NEEDS_CONTEXT: <one question>") || !strings.Contains(task, "has no notes section") {
		t.Errorf("task prompt lost the NEEDS_CONTEXT rule:\n%s", task)
	}
	fix := implementPrompt(wo, model.Job{Payload: map[string]any{"reason": "fix_findings", "brief": "1. a"}}, nil)
	if !strings.Contains(fix, `"N: fixed (file:line)"`) {
		t.Errorf("fix prompt lost the per-item reply:\n%s", fix)
	}
}

func TestBuilderNotesReachConvergeAndInspectPrompts(t *testing.T) {
	wo := &model.WorkOrder{Title: "t", Spec: "the spec body"}
	section := "\n" + builderNotesHead + "\n- t1: payload is a guess\n"
	if p := convergePrompt(wo, nil, nil, "", section); !strings.Contains(p, section) || !strings.Contains(p, "Do not trust any earlier claim") {
		t.Errorf("converge prompt lacks the notes:\n%s", p)
	}
	if p := inspectPrompt(wo, SpecialistPick{Specialty: "testing"}, nil, section, nil); !strings.Contains(p, section) {
		t.Errorf("inspect prompt lacks the notes:\n%s", p)
	}
	if p := convergePrompt(wo, nil, nil, "", ""); strings.Contains(p, "Builder notes") {
		t.Errorf("converge prompt shows a notes heading without notes:\n%s", p)
	}
	if p := inspectPrompt(wo, SpecialistPick{Specialty: "testing"}, nil, "", nil); strings.Contains(p, "Builder notes") {
		t.Errorf("inspect prompt shows a notes heading without notes:\n%s", p)
	}
}

func TestInspectPromptCarriesSpecDecisionRuleAndEarlierFindings(t *testing.T) {
	wo := &model.WorkOrder{Title: "t", Spec: "## Decisions\n| 1 | cap is 3.75 MB |"}
	pick := SpecialistPick{Specialty: "backend"}
	p := inspectPrompt(wo, pick, nil, "", []string{"Cap is 3.75 MB", "Missing index"})
	for _, want := range []string{
		"Spec (the contract the change was built against):\n## Decisions\n| 1 | cap is 3.75 MB |",
		"A choice recorded in the spec's Decisions table or Behaviour rules is settled",
		"report ONE finding that cites both the decision and the criterion",
		earlierFindingsHead + "\n- Cap is 3.75 MB\n- Missing index\n",
		"report it again with fresh evidence",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("inspect prompt missing %q:\n%s", want, p)
		}
	}
	bare := inspectPrompt(&model.WorkOrder{Title: "t"}, pick, nil, "", nil)
	if strings.Contains(bare, "Already reported as notes") || strings.Contains(bare, "Spec (the contract") {
		t.Errorf("sections must be omitted when empty:\n%s", bare)
	}
}

func TestBuilderNotesSection(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	complete := func(task string, notes ...string) {
		payload := map[string]any{"task_id": task}
		if len(notes) > 0 {
			payload["notes"] = notes
		}
		r.f.event(r.wo.ID, "builder", "task_completed", "done", payload)
	}
	if got := r.f.builderNotesSection(ctx, r.wo); got != "" {
		t.Fatalf("no events: %q", got)
	}
	pre := r.wo.ID + "_"
	complete(pre+"task-1", "a1", "a2")
	complete(pre+"task-2", "b1")
	complete(pre + "task-3")
	complete(pre+"task-1", "a3")
	complete("", "fix note")
	want := "\n" + builderNotesHead + "\n- fix: fix note\n- task-1: a3\n- task-2: b1\n"
	if got := r.f.builderNotesSection(ctx, r.wo); got != want {
		t.Fatalf("section:\n%q\nwant:\n%q", got, want)
	}
	complete(pre + "task-1")
	complete("")
	want = "\n" + builderNotesHead + "\n- task-2: b1\n"
	if got := r.f.builderNotesSection(ctx, r.wo); got != want {
		t.Fatalf("a later completion without notes supersedes the earlier notes:\n%q\nwant:\n%q", got, want)
	}
}

func TestBuilderNotesSectionDropsOldestTasksWholeWhenOverTheCap(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	item := strings.Repeat("y", maxBuilderNoteLen)
	for i := 1; i <= 8; i++ {
		r.f.event(r.wo.ID, "builder", "task_completed", "done", map[string]any{
			"task_id": fmt.Sprintf("%st%d", r.wo.ID+"_", i), "notes": []string{item, item, item},
		})
	}
	got := r.f.builderNotesSection(ctx, r.wo)
	if len(got) > maxNotesSection {
		t.Fatalf("section is %d bytes, cap %d", len(got), maxNotesSection)
	}
	if !strings.Contains(got, "- t8: ") || strings.Contains(got, "- t1: ") {
		t.Fatalf("newest tasks must be kept, oldest dropped:\n%s", got)
	}
	for _, l := range strings.Split(strings.TrimSpace(got), "\n")[1:] {
		if strings.Count(l, item) != 3 {
			t.Fatalf("a task line was cut mid-item: %q", l)
		}
	}
}

func TestEarlierFindingTitles(t *testing.T) {
	ctx := context.Background()
	r := inspectRig(t, &replyHarness{})
	run := func(id string) {
		if err := r.f.Runs.Start(ctx, id, r.wo.ID, "", "inspector", "h"); err != nil {
			t.Fatal(err)
		}
	}
	run("run_a")
	add := func(i int, specialty, title string, status string) {
		err := r.f.Findings.Create(ctx, model.Finding{
			ID: fmt.Sprintf("fnd_%03d", i), RunID: "run_a", WOID: r.wo.ID, Severity: "minor",
			Title: title, Status: status, Specialty: specialty,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	add(1, "backend", "old", model.FindingOpen)
	add(2, "testing", "other specialty", model.FindingOpen)
	add(3, "backend", "dup", model.FindingResolved)
	add(4, "backend", "dup", model.FindingOpen)
	if err := r.f.Findings.Create(ctx, model.Finding{ID: "fnd_005", RunID: "run_a", WOID: r.wo.ID, Severity: model.SeverityBlocker,
		Title: "still broken", Status: model.FindingOpen, Specialty: "backend"}); err != nil {
		t.Fatal(err)
	}
	got := r.f.earlierFindingTitles(ctx, r.wo.ID, "backend")
	if !reflect.DeepEqual(got, []string{"dup", "old"}) {
		t.Fatalf("titles = %q; want [dup old] (any status, de-duplicated, own specialty only, never a blocker)", got)
	}
	for i := 10; i < 40; i++ {
		add(i, "backend", fmt.Sprintf("t%d", i), model.FindingOpen)
	}
	got = r.f.earlierFindingTitles(ctx, r.wo.ID, "backend")
	if len(got) != maxEarlierFindings || got[0] != "t39" {
		t.Fatalf("want %d titles, newest first; got %d starting %q", maxEarlierFindings, len(got), got[0])
	}
	r.f.Findings = nil
	if got := r.f.earlierFindingTitles(ctx, r.wo.ID, "backend"); got != nil {
		t.Fatalf("nil store: %q", got)
	}
}

// notesHarness gives Builder runs a scripted reply and leaves every other role to replyHarness.
type notesHarness struct {
	*replyHarness
	builder string
}

func (h notesHarness) Result(ctx context.Context, id string) (model.AgentResult, error) {
	h.mu.Lock()
	role := h.reqs[id].Role
	h.mu.Unlock()
	if role == model.AgentBuilder {
		return model.AgentResult{Text: h.builder}, nil
	}
	return h.replyHarness.Result(ctx, id)
}

func TestBuilderNotesFlowFromTaskCompletedToConvergeAndInspector(t *testing.T) {
	ctx := context.Background()
	h := &replyHarness{}
	r := inspectRig(t, notesHarness{h, "added the cap\n\nNotes for review:\n- payload shape is a guess\n- spec rule 9 needs amending"})
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateQueued); err != nil {
		t.Fatal(err)
	}
	if err := r.f.handleImplement(ctx, model.Job{WOID: r.wo.ID, TaskID: "wo1_t1"}); err != nil {
		t.Fatal(err)
	}
	evs := r.events(t, "task_completed")
	if len(evs) != 1 {
		t.Fatalf("task_completed events = %d", len(evs))
	}
	if id, _ := evs[0].Payload["task_id"].(string); id != "wo1_t1" {
		t.Fatalf("payload = %v", evs[0].Payload)
	}
	if notes, _ := evs[0].Payload["notes"].([]any); len(notes) != 2 || notes[0] != "payload shape is a guess" {
		t.Fatalf("payload = %v", evs[0].Payload)
	}
	if len(r.reload(t).Clarifications) != 0 {
		t.Fatalf("notes must stay out of the clarification log: %+v", r.reload(t).Clarifications)
	}
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateSpecialistReview); err != nil {
		t.Fatal(err)
	}
	if err := r.f.handleInspect(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	want := "- " + strings.TrimPrefix("wo1_t1", r.wo.ID+"_") + ": payload shape is a guess; spec rule 9 needs amending"
	if len(h.prompts["architect"]) == 0 || len(h.prompts["inspector"]) == 0 {
		t.Fatalf("prompts: %d architect, %d inspector", len(h.prompts["architect"]), len(h.prompts["inspector"]))
	}
	for role, prompts := range map[string][]string{"architect": h.prompts["architect"], "inspector": h.prompts["inspector"]} {
		for _, p := range prompts {
			if !strings.Contains(p, builderNotesHead) || !strings.Contains(p, want) {
				t.Errorf("%s prompt lacks the Builder notes %q:\n%s", role, want, p)
			}
		}
	}
}

func TestSecondInspectionListsTheFirstOnesFindingTitles(t *testing.T) {
	ctx := context.Background()
	h := &replyHarness{inspect: `{"findings":[{"severity":"minor","title":"Cap is 3.75 MB, not 5 MB","detail":"d","location":"a.go:1"}]}`}
	r := inspectRig(t, h)
	if err := r.f.handleInspect(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	first := len(h.prompts["inspector"])
	if first == 0 {
		t.Fatal("no inspector ran")
	}
	for _, p := range h.prompts["inspector"] {
		if strings.Contains(p, earlierFindingsHead) {
			t.Fatalf("first inspection has no earlier findings:\n%s", p)
		}
	}
	if err := r.f.handleInspect(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	second := h.prompts["inspector"][first:]
	if len(second) != first {
		t.Fatalf("second inspection ran %d specialists, first %d", len(second), first)
	}
	for _, p := range second {
		if !strings.Contains(p, earlierFindingsHead+"\n- Cap is 3.75 MB, not 5 MB\n") {
			t.Errorf("second inspection prompt lacks the earlier title:\n%s", p)
		}
		if !strings.Contains(p, "Round in integer rupiah") {
			t.Errorf("inspector prompt lacks the spec:\n%s", p)
		}
	}
}
