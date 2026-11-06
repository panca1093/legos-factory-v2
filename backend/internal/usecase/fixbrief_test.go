package usecase

import (
	"context"
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
