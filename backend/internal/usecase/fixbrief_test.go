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
	long := strings.Repeat("x", 300)
	report := &architectReport{
		Verdicts: []ArchitectVerdict{
			{CriterionID: "c1", Status: "met", Evidence: "fine"},
			{CriterionID: "c2", Status: "not_met", Evidence: "round.go uses floats", Gap: "no int path", Fix: "use int64"},
		},
		Deviations: []string{"deviation " + long},
	}
	findings := []model.Finding{{Severity: "minor", Title: "naming", Detail: "rename x", Location: "a.go:3"}}
	b := fixBrief(findings, report, []string{"gate issue"}, "please redo")
	for _, want := range []string{"1. ", "please redo", "gate issue", "[minor] naming", "rename x", "a.go:3", "c2", "round.go uses floats", "no int path", "use int64", long} {
		if !strings.Contains(b, want) {
			t.Fatalf("brief missing %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "fine") {
		t.Fatalf("met verdicts do not belong in the brief:\n%s", b)
	}
	if fixBrief(nil, nil, nil, "") != "" {
		t.Fatal("an empty brief must render as empty")
	}
}

func TestImplementPromptCarriesSpecAndBriefOnEveryFixPath(t *testing.T) {
	long := strings.Repeat("d", 200)
	brief := fixBrief(
		[]model.Finding{{Severity: "blocker", Title: "nil deref", Detail: "x", Location: "svc.go:9"}},
		&architectReport{Verdicts: []ArchitectVerdict{{CriterionID: "c1", Status: "unclear", Evidence: "evidence-text"}}, Deviations: []string{long}},
		nil, "human-reason-text")
	wo := &model.WorkOrder{Title: "t", Spec: "SPEC-BODY"}
	tasks := []model.Task{{Title: "plan-task", Objective: "o"}}
	for _, p := range []map[string]any{
		{"reason": "fix_findings"},
		{"reason": "human_revision"},
		{"reason": "human_retry"},
		{"reason": "fix_done_gate", "note": "n"},
		{"reason": "fix_failed_verification", "failed_checks": "Check \"go test\""},
	} {
		p["brief"] = brief
		got := implementPrompt(wo, model.Job{Payload: p}, tasks)
		for _, want := range []string{"SPEC-BODY", "plan-task", "nil deref", "svc.go:9", "human-reason-text", "evidence-text", long, "N: fixed (file:line)", "N: not fixed because"} {
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

func TestDoneGateFixPayloadCarriesFullVerdictAndDeviation(t *testing.T) {
	long := strings.Repeat("z", 200)
	report := `{"revision":0,"verdicts":[{"criterion_id":"c1","status":"not_met","evidence":"evidence-text","gap":"gap-text","fix":"fix-text"}],"deviations":["` + long + `"]}`
	r := doneRig(t, &fakeJudge{}, report)
	r.evaluate(t)
	p := r.q.payloads[len(r.q.payloads)-1]
	b, _ := p["brief"].(string)
	for _, want := range []string{"evidence-text", "gap-text", "fix-text", long} {
		if !strings.Contains(b, want) {
			t.Fatalf("brief missing %q:\n%s", want, b)
		}
	}
	if got := r.f.retryPayload(context.Background(), r.reload(t)); got["brief"] != b {
		t.Fatalf("retry lost the brief: %v", got)
	}
}

const unmetVerify = `{"verdicts":[{"criterion_id":"c1","status":"not_met","evidence":"evidence-text","gap":"gap-text","fix":"fix-text"}],"deviations":["deviation-text"]}`

func lastPayload(r *rig) map[string]any { return r.q.payloads[len(r.q.payloads)-1] }

func TestInspectFixBriefCarriesTheArchitectReportAndOnlyThisInspection(t *testing.T) {
	ctx := context.Background()
	h := &replyHarness{verify: unmetVerify,
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
	for _, want := range []string{"NEW-FINDING", "evidence-text", "gap-text", "fix-text", "deviation-text"} {
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

func TestDoneGateBriefStatesEachFactOnce(t *testing.T) {
	long := strings.Repeat("z", 200)
	report := `{"revision":0,"verdicts":[{"criterion_id":"c1","status":"not_met","evidence":"evidence-text","gap":"gap-text","fix":"fix-text"}],"deviations":["` + long + `"]}`
	r := doneRig(t, &fakeJudge{}, report)
	r.evaluate(t)
	b, _ := lastPayload(r)["brief"].(string)
	if strings.Contains(b, "as not met") || strings.Count(b, "outside the spec or plan") != 1 || strings.Count(b, "criterion c1") != 1 {
		t.Fatalf("brief repeats the Architect's findings:\n%s", b)
	}
	if !strings.Contains(b, long) {
		t.Fatalf("the deviation must stay in full:\n%s", b)
	}
}

func TestPlanRevisionResumeSendsTheGateNoteAndForemanReview(t *testing.T) {
	r := reviewRig(t, &fakeJudge{over: map[string]float64{"scope_creep": 0.9}})
	r.f.Sched = &failOnceQueue{fakeQueue: r.q, fail: true}
	if err := r.f.handleReview(context.Background(), model.Job{WOID: r.wo.ID}); err == nil {
		t.Fatal("the lost enqueue must fail the job")
	}
	if err := r.f.handleReview(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	notes, _ := lastPayload(r)["notes"].(string)
	if !strings.Contains(notes, "the plan includes work the request does not ask for") || !strings.Contains(notes, "Foreman review: ok") || strings.Contains(notes, "activity trail") {
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

func atRevision(t *testing.T, r *rig, rev int) {
	t.Helper()
	ctx := context.Background()
	report := strings.Replace(unmetVerifyReport, `"revision":0`, fmt.Sprintf(`"revision":%d`, rev), 1)
	if err := r.wos.UpdateFields(ctx, r.wo.ID, map[string]any{"revision": rev, "architect_report": report}); err != nil {
		t.Fatal(err)
	}
	if err := r.f.Quality.Save(ctx, model.QualityCheck{ID: "qc_rev", WOID: r.wo.ID, Name: "go-test", Command: "go test ./...", Outcome: "PASS", Revision: int64(rev)}); err != nil {
		t.Fatal(err)
	}
}

const unmetVerifyReport = `{"revision":0,"verdicts":[{"criterion_id":"c1","status":"not_met","evidence":"evidence-text","gap":"gap-text","fix":"fix-text"}],"deviations":[]}`

const earlierHeading = "Already addressed in earlier revisions — keep these fixes intact, do not undo or regress them:"

func TestFixBriefCarriesEarlierRevisionsFixesForward(t *testing.T) {
	r := doneRig(t, &fakeJudge{}, unmetVerifyReport)
	seedFix(t, r, 1, "1. ITEM-A\n2. ITEM-B\n   Missing: B-detail")
	seedFix(t, r, 2, "1. ITEM-A\n2. ITEM-C\n\n"+earlierHeading+"\n- ITEM-A")
	atRevision(t, r, 2)
	r.evaluate(t)
	b, _ := lastPayload(r)["brief"].(string)
	head, tail, ok := strings.Cut(b, earlierHeading)
	if !ok || !strings.Contains(head, "1. ") || !strings.Contains(head, "evidence-text") {
		t.Fatalf("current items must stay numbered and complete above the section:\n%s", b)
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
	r := doneRig(t, &fakeJudge{}, unmetVerifyReport)
	for rev := 1; rev <= 4; rev++ {
		seedFix(t, r, rev, fmt.Sprintf("1. REV%d-ITEM", rev))
	}
	atRevision(t, r, 4)
	r.evaluate(t)
	b, _ := lastPayload(r)["brief"].(string)
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
	r := doneRig(t, &fakeJudge{}, unmetVerifyReport)
	r.evaluate(t)
	if b, _ := lastPayload(r)["brief"].(string); strings.Contains(b, "Already addressed") {
		t.Fatalf("unexpected section:\n%s", b)
	}
}

func TestFindingsAndHumanBriefsCarryEarlierFixesAndRetryDoesNotDoubleAppend(t *testing.T) {
	ctx := context.Background()
	h := &replyHarness{verify: unmetVerify,
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
