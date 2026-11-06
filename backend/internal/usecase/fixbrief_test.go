package usecase

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestFixBriefNumbersEverySourceInFull(t *testing.T) {
	findings := []model.Finding{{Severity: "minor", Title: "naming", Detail: "rename x", Location: "a.go:3"}}
	b := fixBrief(findings, "please redo")
	for _, want := range []string{"1. ", "please redo", "[minor] naming", "rename x", "a.go:3"} {
		if !strings.Contains(b, want) {
			t.Fatalf("brief missing %q:\n%s", want, b)
		}
	}
	if fixBrief(nil, "") != "" {
		t.Fatal("an empty brief must render as empty")
	}
}

func TestImplementPromptCarriesSpecAndBriefOnEveryFixPath(t *testing.T) {
	brief := fixBrief([]model.Finding{{Severity: "blocker", Title: "nil deref", Detail: "x", Location: "svc.go:9"}}, "human-reason-text")
	wo := &model.WorkOrder{Title: "t", Spec: "SPEC-BODY"}
	tasks := []model.Task{{Title: "plan-task", Objective: "o"}}
	for _, p := range []map[string]any{
		{"reason": "fix_findings"},
		{"reason": "human_revision"},
		{"reason": "human_retry"},
		{"reason": "fix_failed_verification", "failed_checks": "Check \"go test\""},
	} {
		p["brief"] = brief
		got := implementPrompt(wo, model.Job{Payload: p}, tasks)
		for _, want := range []string{"SPEC-BODY", "plan-task", "nil deref", "svc.go:9", "human-reason-text", "N: fixed (file:line)", "N: not fixed because"} {
			if !strings.Contains(got, want) {
				t.Errorf("%v prompt missing %q:\n%s", p["reason"], want, got)
			}
		}
	}
	if got := implementPrompt(wo, model.Job{TaskID: "x"}, nil); !strings.Contains(got, "SPEC-BODY") {
		t.Fatalf("the first build lacks the spec:\n%s", got)
	}
}

func TestHumanRevisionPayloadCarriesTheReason(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, nil)
	q := &fakeQueue{}
	f.Sched, f.Findings = q, &store.FindingRepo{DB: wos.DB}
	if err := wos.UpdateFields(ctx, wo.ID, map[string]any{"state": string(model.StateNeedsHumanReview)}); err != nil {
		t.Fatal(err)
	}
	if err := f.HumanPRDecision(ctx, wo.ID, "request_changes", "use the old format"); err != nil {
		t.Fatal(err)
	}
	last := q.payloads[len(q.payloads)-1]
	if b, _ := last["brief"].(string); last["reason"] != "human_revision" || !strings.Contains(b, "use the old format") {
		t.Fatalf("payload %v", last)
	}
}

func lastPayload(r *rig) map[string]any { return r.q.payloads[len(r.q.payloads)-1] }

func TestInspectFixBriefCarriesOnlyThisInspection(t *testing.T) {
	ctx := context.Background()
	h := &replyHarness{
		inspect: `{"findings":[{"severity":"blocker","title":"NEW-FINDING","detail":"d","location":"round.go:1"}]}`}
	r := inspectRig(t, h)
	if err := r.f.Runs.Start(ctx, "run_old", r.wo.ID, "", "inspector", "h"); err != nil {
		t.Fatal(err)
	}
	if err := r.f.Findings.Create(ctx, model.Finding{ID: "fnd_old", RunID: "run_old", WOID: r.wo.ID, Severity: "major", Title: "OLD-FINDING", Status: model.FindingOpen}); err != nil {
		t.Fatal(err)
	}
	if err := r.f.handleInspect(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	p := lastPayload(r)
	b, _ := p["brief"].(string)
	if p["reason"] != "fix_findings" {
		t.Fatalf("payload %v", p)
	}
	for _, want := range []string{"NEW-FINDING"} {
		if !strings.Contains(b, want) {
			t.Fatalf("brief missing %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "OLD-FINDING") {
		t.Fatalf("an earlier inspection's finding leaked into the brief:\n%s", b)
	}
	if got := r.f.retryPayload(ctx, r.reload(t)); got["brief"] != b {
		t.Fatalf("a human retry lost the fix brief: %v", got)
	}
}

func TestRetryRemediationAfterRequestedChangesKeepsTheHumanReason(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, nil)
	q := &fakeQueue{}
	f.Sched, f.Findings = q, &store.FindingRepo{DB: wos.DB}
	if err := wos.ReplacePlan(ctx, wo.ID, []model.Task{{ID: "T1", WOID: wo.ID, Title: "a", Role: "builder"}}); err != nil {
		t.Fatal(err)
	}
	if err := wos.UpdateState(ctx, wo.ID, model.StateNeedsHumanReview); err != nil {
		t.Fatal(err)
	}
	if err := f.HumanPRDecision(ctx, wo.ID, "request_changes", "use the old format"); err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return f.Runs.Start(ctx, "run_t1", wo.ID, "T1", "builder", "h") },
		func() error { return f.Runs.Complete(ctx, "run_t1", model.AgentResult{}) },
		func() error { return f.Runs.Start(ctx, "run_fix", wo.ID, "", "builder", "h") },
		func() error { return f.Runs.Fail(ctx, "run_fix", "boom") },
		func() error { return f.RetryRemediation(ctx, wo.ID, "human") },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	last := q.payloads[len(q.payloads)-1]
	if b, _ := last["brief"].(string); last["reason"] != "human_retry" || !strings.Contains(b, "use the old format") {
		t.Fatalf("retry payload %v", last)
	}
}

func TestPlanRevisionResumeSendsTheGateNote(t *testing.T) {
	r := reviewRig(t, &fakeJudge{over: map[string]float64{"scope_creep": 0.9}})
	r.f.Sched = &failOnceQueue{fakeQueue: r.q, fail: true}
	if err := r.f.handleReview(context.Background(), model.Job{WOID: r.wo.ID}); err == nil {
		t.Fatal("the lost enqueue must fail the job")
	}
	if err := r.f.handleReview(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	notes, _ := lastPayload(r)["notes"].(string)
	if !strings.Contains(notes, "the plan includes work the request does not ask for") || strings.Contains(notes, "activity trail") {
		t.Fatalf("resume notes %q", notes)
	}
}

func TestRetryTaskKeepsTheTaskSectionAndBrief(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, nil)
	q := &fakeQueue{}
	f.Sched, f.Findings = q, &store.FindingRepo{DB: wos.DB}
	tasks := []model.Task{{ID: "T1", WOID: wo.ID, Title: "task-one", Objective: "obj-one", Role: "builder"}, {ID: "T2", WOID: wo.ID, Title: "task-two", Role: "builder", Deps: []string{"T1"}}}
	if err := wos.ReplacePlan(ctx, wo.ID, tasks); err != nil {
		t.Fatal(err)
	}
	if err := wos.UpdateState(ctx, wo.ID, model.StateImplementing); err != nil {
		t.Fatal(err)
	}
	if err := f.Runs.Start(ctx, "r1", wo.ID, "", "inspector", "h"); err != nil {
		t.Fatal(err)
	}
	if err := f.Findings.Create(ctx, model.Finding{ID: "f1", RunID: "r1", WOID: wo.ID, Severity: "major", Title: "OPEN-FINDING", Status: model.FindingOpen}); err != nil {
		t.Fatal(err)
	}
	if err := f.RetryTask(ctx, wo.ID, "T1", "human"); err != nil {
		t.Fatal(err)
	}
	got := implementPrompt(wo, model.Job{TaskID: "T1", Payload: q.payloads[0]}, tasks)
	for _, want := range []string{"Your task: task-one", "obj-one", "do not implement these", "task-two", "OPEN-FINDING"} {
		if !strings.Contains(got, want) {
			t.Fatalf("retry prompt missing %q:\n%s", want, got)
		}
	}
}

func seedFix(t *testing.T, r *rig, rev int, brief string) {
	t.Helper()
	r.f.event(r.wo.ID, "factory", "fix_requested", "x", map[string]any{"revision": rev, "brief": brief})
}

const earlierHeading = "Already addressed in earlier revisions — keep these fixes intact, do not undo or regress them:"

func TestFixBriefCarriesEarlierRevisionsFixesForward(t *testing.T) {
	r := newRig(t)
	seedFix(t, r, 1, "1. ITEM-A\n2. ITEM-B\n   Missing: B-detail")
	seedFix(t, r, 2, "1. ITEM-A\n2. ITEM-C\n\n"+earlierHeading+"\n- ITEM-A")
	b := r.f.withEarlierFixes(context.Background(), r.wo.ID, 3, []string{"CURRENT"})
	head, tail, ok := strings.Cut(b, earlierHeading)
	if !ok || !strings.HasPrefix(head, "1. CURRENT") {
		t.Fatalf("current items must stay numbered above the section:\n%s", b)
	}
	for _, want := range []string{"- ITEM-A", "- ITEM-B\n   Missing: B-detail", "- ITEM-C"} {
		if !strings.Contains(tail, want) {
			t.Fatalf("section missing %q:\n%s", want, b)
		}
	}
	if strings.Count(tail, "ITEM-A") != 1 {
		t.Fatalf("duplicates must collapse:\n%s", b)
	}
}

func TestFixBriefCarriesOnlyTheThreeMostRecentEarlierRevisions(t *testing.T) {
	r := newRig(t)
	for rev := 1; rev <= 4; rev++ {
		seedFix(t, r, rev, fmt.Sprintf("1. REV%d-ITEM", rev))
	}
	b := r.f.withEarlierFixes(context.Background(), r.wo.ID, 5, []string{"CURRENT"})
	for _, want := range []string{"REV2-ITEM", "REV3-ITEM", "REV4-ITEM"} {
		if !strings.Contains(b, want) {
			t.Fatalf("brief missing %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "REV1-ITEM") {
		t.Fatalf("the 4th-oldest revision must be dropped:\n%s", b)
	}
}

func TestFixBriefWithoutEarlierRevisionsHasNoSection(t *testing.T) {
	r := newRig(t)
	if b := r.f.withEarlierFixes(context.Background(), r.wo.ID, 1, []string{"CURRENT"}); strings.Contains(b, "Already addressed") {
		t.Fatalf("unexpected section:\n%s", b)
	}
}

func TestFindingsAndHumanBriefsCarryEarlierFixesAndRetryDoesNotDoubleAppend(t *testing.T) {
	ctx := context.Background()
	h := &replyHarness{
		inspect: `{"findings":[{"severity":"blocker","title":"NEW-FINDING","detail":"d","location":"round.go:1"}]}`}
	r := inspectRig(t, h)
	seedFix(t, r, 1, "1. ITEM-A")
	if err := r.wos.UpdateFields(ctx, r.wo.ID, map[string]any{"revision": 1}); err != nil {
		t.Fatal(err)
	}
	if err := r.f.handleInspect(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	b, _ := lastPayload(r)["brief"].(string)
	if lastPayload(r)["reason"] != "fix_findings" || !strings.Contains(b, "NEW-FINDING") || !strings.Contains(b, earlierHeading+"\n- ITEM-A") {
		t.Fatalf("fix_findings brief:\n%s", b)
	}
	if got, _ := r.f.retryPayload(ctx, r.reload(t))["brief"].(string); got != b {
		t.Fatalf("retry must return the stored brief as is:\n%s", got)
	}

	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateNeedsHumanReview); err != nil {
		t.Fatal(err)
	}
	if err := r.f.HumanPRDecision(ctx, r.wo.ID, "request_changes", "use the old format"); err != nil {
		t.Fatal(err)
	}
	hb, _ := lastPayload(r)["brief"].(string)
	if !strings.Contains(hb, "use the old format") || !strings.Contains(hb, "- ITEM-A") {
		t.Fatalf("human_revision brief:\n%s", hb)
	}
	if got, _ := r.f.retryPayload(ctx, r.reload(t))["brief"].(string); got != hb || strings.Count(got, earlierHeading) != 1 {
		t.Fatalf("retry double-appended:\n%s", got)
	}
}

func TestImplementPromptTellsTheBuilderToKeepAlreadyAddressedItems(t *testing.T) {
	p := implementPrompt(&model.WorkOrder{Spec: "S"}, model.Job{Payload: map[string]any{"reason": "fix_findings", "brief": "1. x"}}, nil)
	if !strings.Contains(p, `"Already addressed"`) || !strings.Contains(p, "per-item reply") {
		t.Fatalf("prompt lacks the keep-intact rule:\n%s", p)
	}
}

func TestHumanReasonWithOwnNumberedListStaysOneEarlierItem(t *testing.T) {
	ctx := context.Background()
	r := inspectRig(t, &replyHarness{})
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateNeedsHumanReview); err != nil {
		t.Fatal(err)
	}
	reason := "Please:\n1. rename X\n2. add test"
	if err := r.f.HumanPRDecision(ctx, r.wo.ID, "request_changes", reason); err != nil {
		t.Fatal(err)
	}
	b := r.f.withEarlierFixes(ctx, r.wo.ID, 2, []string{"NEW"})
	_, tail, _ := strings.Cut(b, earlierHeading)
	if want := "\n- The reviewer asked for changes: " + reason; tail != want {
		t.Fatalf("earlier section %q, want %q", tail, want)
	}
}

func TestEarlierFixesDropReraisedItemsAndForemanNotes(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	seedFix(t, r, 1, "1. ITEM-A\n2. ITEM-B\n3. "+foremanNotesPrefix+"hm")
	r.f.event(r.wo.ID, "factory", "fix_requested", "x", map[string]any{"revision": 1, "items": []string{"ITEM-A", "ITEM-B", foremanNotesPrefix + "hm"}})
	b := r.f.withEarlierFixes(ctx, r.wo.ID, 2, []string{"ITEM-A"})
	head, tail, _ := strings.Cut(b, earlierHeading)
	if !strings.HasPrefix(head, "1. ITEM-A") || strings.Contains(tail, "ITEM-A") || !strings.Contains(tail, "- ITEM-B") || strings.Contains(b, "validation notes") {
		t.Fatalf("brief:\n%s", b)
	}
}
