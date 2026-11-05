package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

// ---- done gate ----

type diffSandbox struct {
	filesSandbox
	diff []FileDiff
}

func (s diffSandbox) Diff(context.Context, string) ([]FileDiff, error) { return s.diff, nil }

const metReport = `{"revision":0,"verdicts":[{"criterion_id":"c1","status":"met","evidence":"round.go"}],"deviations":[]}`

func doneRig(t *testing.T, j *fakeJudge, report string) *rig {
	t.Helper()
	r := newRig(t)
	ctx := context.Background()
	r.workspace(t, map[string]string{"README.md": "# x\n"})
	r.f.Sandbox = diffSandbox{filesSandbox{files: []string{"round.go"}}, []FileDiff{{Path: "round.go", Added: 3, Removed: 1, Hunks: "@@ -1 +1,3 @@\n+rupiah\n"}}}
	if j != nil {
		r.f.Judge = j
	}
	r.f.Bus = &liveBus{}
	if err := r.wos.UpdateFields(ctx, r.wo.ID, map[string]any{"acceptance": "no floats", "spec": "Round in integer rupiah", "architect_report": report}); err != nil {
		t.Fatal(err)
	}
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateSpecialistReview); err != nil {
		t.Fatal(err)
	}
	if err := r.f.Quality.Save(ctx, model.QualityCheck{ID: "qc1", WOID: r.wo.ID, Name: "go-test", Command: "go test ./...", Outcome: "PASS"}); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) evaluate(t *testing.T) *model.WorkOrder {
	t.Helper()
	if err := r.f.handleEvaluate(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	return r.reload(t)
}

func TestDoneGateDoneContinuesToReadiness(t *testing.T) {
	j := &fakeJudge{}
	r := doneRig(t, j, metReport)
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	ev := eventByKind(t, r, "done_gate")
	if ev.Actor != "jev" || ev.Payload["outcome"] != GateDone || ev.Payload["row"] != float64(5) {
		t.Fatalf("gate event: %+v", ev)
	}
	if len(r.events(t, "readiness_ready")) != 1 || len(r.events(t, "foreman_validated")) != 1 {
		t.Fatal("readiness runs as today, after the Foreman's validation")
	}
	state := j.calls[0].state.(map[string]any)
	diff := state["diff"].([]FileDiff)
	verdicts := state["architect_verdicts"].([]ArchitectVerdict)
	checks := state["checks"].([]GateCheck)
	if len(diff) != 1 || diff[0].Path != "round.go" || len(verdicts) != 1 || verdicts[0].Status != "met" ||
		len(checks) != 1 || !checks[0].Passed || state["foreman_notes"] != "ok" || state["spec"] != "Round in integer rupiah" {
		t.Fatalf("done-gate state: %+v", state)
	}
	prompt := r.h.prompts["foreman"][0]
	if !strings.Contains(prompt, "c1: met — round.go") || !strings.Contains(prompt, "go-test") {
		t.Fatalf("the validation prompt lacks the Architect report and the checks:\n%s", prompt)
	}
}

func TestDoneGateFixReturnsToTheBuilderWithinTheBudget(t *testing.T) {
	ctx := context.Background()
	j := &fakeJudge{over: map[string]float64{"met_c1": 0.1}}
	r := doneRig(t, j, metReport)
	wo := r.evaluate(t)
	if wo.State != model.StateImplementing || wo.Revision != 1 || wo.ReviewFixes != 1 || wo.CommitSHA != "" {
		t.Fatalf("state %s revision %d fixes %d", wo.State, wo.Revision, wo.ReviewFixes)
	}
	if r.q.enqueued[len(r.q.enqueued)-1] != "implement:" {
		t.Fatalf("enqueued %v", r.q.enqueued)
	}
	p := r.q.payloads[len(r.q.payloads)-1]
	if p["reason"] != "fix_done_gate" || !strings.Contains(p["note"].(string), "acceptance criterion c1") {
		t.Fatalf("payload %v", p)
	}
	if ev := eventByKind(t, r, "done_gate"); ev.Payload["outcome"] != GateFix || ev.Payload["row"] != float64(3) {
		t.Fatalf("event %+v", ev.Payload)
	}
	if len(r.events(t, "readiness_ready")) != 0 {
		t.Fatal("a fix must not reach readiness")
	}
	if ev := stateChangedTo(t, r, model.StateImplementing); ev.Actor != "factory" {
		t.Fatalf("actor %q", ev.Actor)
	}

	// the budget is shared with failed-checks remediation: when spent, a fix escalates
	if err := r.wos.UpdateFields(ctx, r.wo.ID, map[string]any{"review_fixes": MaxRevisionCycles, "architect_report": strings.Replace(metReport, `"revision":0`, `"revision":1`, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateSpecialistReview); err != nil {
		t.Fatal(err)
	}
	if err := r.f.Quality.Save(ctx, model.QualityCheck{ID: "qc2", WOID: r.wo.ID, Name: "go-test", Command: "go test ./...", Outcome: "PASS", Revision: 1}); err != nil {
		t.Fatal(err)
	}
	wo = r.evaluate(t)
	if wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	if ev := eventByKind(t, r, "done_gate"); ev.Payload["outcome"] != GateEscalate || ev.Payload["row"] != float64(3) {
		t.Fatalf("event %+v", ev.Payload)
	}
	ready := eventByKind(t, r, "readiness_needs_human_review")
	if !strings.Contains(ready.Message, "done gate needs a human decision") || !strings.Contains(ready.Message, "criterion c1") {
		t.Fatalf("readiness: %+v", ready)
	}
}

func TestDoneGateEscalatesOnJevFailureOrMissingArchitectReport(t *testing.T) {
	r := doneRig(t, &fakeJudge{err: errors.New("boom")}, metReport)
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	if ev := eventByKind(t, r, "done_gate"); ev.Payload["outcome"] != GateEscalate || !strings.Contains(ev.Payload["error"].(string), "boom") {
		t.Fatalf("event %+v", ev.Payload)
	}

	j := &fakeJudge{}
	stale := doneRig(t, j, strings.Replace(metReport, `"revision":0`, `"revision":4`, 1))
	if wo := stale.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	if len(j.calls) != 0 || len(stale.events(t, "foreman_validated")) != 0 {
		t.Fatal("without a verification of this revision nothing is sent to Jev")
	}
	if ev := eventByKind(t, stale, "done_gate"); ev.Payload["outcome"] != GateEscalate || !strings.Contains(ev.Payload["note"].(string), "Architect's verification is missing") {
		t.Fatalf("event %+v", ev.Payload)
	}
}

func TestDoneGateIsSkippedWithoutJev(t *testing.T) {
	r := doneRig(t, nil, metReport)
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	if len(r.events(t, "done_gate")) != 0 || len(r.events(t, "foreman_validated")) != 0 || len(r.h.prompts["foreman"]) != 0 {
		t.Fatal("no gate and no Foreman validation run without Jev")
	}
	if len(r.events(t, "readiness_ready")) != 1 {
		t.Fatal("today's deterministic readiness still decides")
	}
}

// ---- Architect verification ----

// barrierHarness holds the first two results until both have been requested,
// which only happens when the Architect and an Inspector run at the same time.
type barrierHarness struct {
	replyHarness
	arrived atomic.Int32
	both    chan struct{}
}

func (h *barrierHarness) Result(ctx context.Context, id string) (model.AgentResult, error) {
	if n := h.arrived.Add(1); n == 2 {
		close(h.both)
	} else if n < 2 {
		select {
		case <-h.both:
		case <-time.After(3 * time.Second):
			return model.AgentResult{}, errors.New("runs did not overlap")
		}
	}
	return h.replyHarness.Result(ctx, id)
}

func inspectRig(t *testing.T, h Harness) *rig {
	t.Helper()
	r := doneRig(t, &fakeJudge{}, "")
	r.f.Resolve = func(model.AgentRole, *model.WorkOrder) Harness { return h }
	if err := r.wos.ReplacePlan(context.Background(), r.wo.ID, []model.Task{{ID: "wo1_t1", WOID: r.wo.ID, Title: "Round money", Objective: "o", Role: "builder", Scope: []string{"round.go"}}}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestArchitectVerificationRunsAlongsideTheSpecialistsAndIsStored(t *testing.T) {
	h := &barrierHarness{both: make(chan struct{})}
	r := inspectRig(t, h)
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	wo := r.reload(t)
	var rep architectReport
	if err := json.Unmarshal([]byte(wo.ArchitectReport), &rep); err != nil || rep.Revision != 0 || len(rep.Verdicts) != 1 || rep.Verdicts[0].Status != "met" {
		t.Fatalf("stored report %q: %v", wo.ArchitectReport, err)
	}
	if len(r.events(t, "architect_failed")) != 0 || len(r.events(t, "architect_verify_failed")) != 0 {
		t.Fatalf("verification failed: %+v", r.events(t, "architect_verify_failed"))
	}
	ev := eventByKind(t, r, "architect_verified")
	if ev.Actor != "architect" || !strings.Contains(ev.Message, "1 of 1 criteria met") {
		t.Fatalf("event %+v", ev)
	}
	prompt := h.prompts["architect"][0]
	for _, want := range []string{"c1: no floats", "Round in integer rupiah", "--- round.go (+3 -1)", "+rupiah", "go-test (go test ./...) → PASS", "Round money"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("verification prompt lacks %q:\n%s", want, prompt)
		}
	}
	if len(r.events(t, "inspection_finished")) != 1 || r.q.enqueued[len(r.q.enqueued)-1] != "evaluate:" {
		t.Fatalf("inspection still hands over to evaluation: %v", r.q.enqueued)
	}
	var runs int
	for _, e := range r.events(t, "agent_completed") {
		if e.Actor == "architect" {
			runs++
		}
	}
	if runs != 1 {
		t.Fatalf("the Architect verification must be one accounted run, got %d", runs)
	}
}

func TestArchitectVerificationFailureDoesNotFailTheInspection(t *testing.T) {
	h := &replyHarness{verify: `{"verdicts":[{"criterion_id":"c1","status":"maybe"}]}`}
	r := inspectRig(t, h)
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatalf("the inspection must survive a bad verification: %v", err)
	}
	if wo := r.reload(t); wo.ArchitectReport != "" {
		t.Fatalf("nothing valid to store: %q", wo.ArchitectReport)
	}
	if len(r.events(t, "architect_verify_failed")) != 1 || r.q.enqueued[len(r.q.enqueued)-1] != "evaluate:" {
		t.Fatalf("failure must be visible and evaluation still follow: %v", r.q.enqueued)
	}
}

func TestNoArchitectVerificationWithoutJev(t *testing.T) {
	r := inspectRig(t, &replyHarness{})
	r.f.Judge = nil
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if len(r.events(t, "architect_verified")) != 0 || len(r.events(t, "architect_verify_failed")) != 0 {
		t.Fatal("nothing reads the verdicts without Jev, so the Architect must not run")
	}
	if r.q.enqueued[len(r.q.enqueued)-1] != "evaluate:" {
		t.Fatalf("enqueued %v", r.q.enqueued)
	}
}

func TestArchitectVerificationErrorsRequeueUntilTheLastAttempt(t *testing.T) {
	h := &replyHarness{archErr: errors.New("model offline")}
	r := inspectRig(t, h)
	err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID, MaxAttempts: 3, Attempts: 0})
	if err == nil || !strings.Contains(err.Error(), "model offline") {
		t.Fatalf("a transient error must requeue the job: %v", err)
	}
	if len(r.events(t, "architect_verify_failed")) != 0 {
		t.Fatal("not reported as failed while a retry is coming")
	}
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID, MaxAttempts: 3, Attempts: 2}); err != nil {
		t.Fatalf("the last attempt must carry on: %v", err)
	}
	if len(r.events(t, "architect_verify_failed")) != 1 {
		t.Fatal("the last attempt reports the failure")
	}
}

func TestArchitectVerificationPanicBecomesAFailureEvent(t *testing.T) {
	r := inspectRig(t, &replyHarness{archBoom: true})
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if ev := eventByKind(t, r, "architect_verify_failed"); !strings.Contains(ev.Message, "panicked") {
		t.Fatalf("event %+v", ev)
	}
}

func TestDoneGateSeesTheFindingsOfThisInspection(t *testing.T) {
	ctx := context.Background()
	j := &fakeJudge{}
	h := &replyHarness{inspect: `{"findings":[{"severity":"minor","title":"Unused import","detail":"round.go imports fmt","location":"round.go:3"}]}`}
	r := inspectRig(t, h)
	r.f.Judge = j
	if err := r.f.handleInspect(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	open := j.calls[0].state.(map[string]any)["open_findings"].([]GateFinding)
	if len(open) == 0 || !strings.Contains(open[0].Message, "Unused import") {
		t.Fatalf("the gate must see this inspection's findings: %+v", open)
	}
	fds, _ := r.f.Findings.List(ctx, r.wo.ID)
	for _, fd := range fds {
		if fd.Status != model.FindingResolved {
			t.Fatalf("findings are settled once the gate has decided: %+v", fd)
		}
	}
}

func TestFailedForemanValidationEscalatesInsteadOfStrandingTheOrder(t *testing.T) {
	j := &fakeJudge{}
	r := doneRig(t, j, metReport)
	r.h.foreman = "no json at all"
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("a failed validation must not strand the order, state %s", wo.State)
	}
	ev := eventByKind(t, r, "done_gate")
	if ev.Payload["outcome"] != GateEscalate || !strings.Contains(ev.Payload["note"].(string), "Foreman validation failed") {
		t.Fatalf("event %+v", ev.Payload)
	}
	if len(j.calls) != 0 {
		t.Fatal("no Jev request without validation notes")
	}
	ready := eventByKind(t, r, "readiness_needs_human_review")
	if !strings.Contains(ready.Message, "Foreman validation failed") {
		t.Fatalf("readiness: %+v", ready)
	}
}

func TestDoneGateWithoutCriteriaEscalatesWithoutAForemanRun(t *testing.T) {
	r := doneRig(t, &fakeJudge{}, metReport)
	if err := r.wos.UpdateFields(context.Background(), r.wo.ID, map[string]any{"acceptance": ""}); err != nil {
		t.Fatal(err)
	}
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	if len(r.h.prompts["foreman"]) != 0 {
		t.Fatal("no Foreman run without criteria")
	}
	if ev := eventByKind(t, r, "done_gate"); !strings.Contains(ev.Payload["note"].(string), "no acceptance criteria") {
		t.Fatalf("event %+v", ev.Payload)
	}
}

func TestEvaluateIsIdempotentAndResumesAHalfDoneFix(t *testing.T) {
	ctx := context.Background()
	r := doneRig(t, &fakeJudge{}, metReport)
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateNeedsHumanReview); err != nil {
		t.Fatal(err)
	}
	if err := r.f.handleEvaluate(ctx, model.Job{WOID: r.wo.ID}); err != nil || len(r.q.enqueued) != 0 {
		t.Fatalf("a superseded evaluate job is a no-op: %v %v", err, r.q.enqueued)
	}
	// the state moved to IMPLEMENTING but the revision bump and the Builder job were lost
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateImplementing); err != nil {
		t.Fatal(err)
	}
	if err := r.wos.UpdateFields(ctx, r.wo.ID, map[string]any{"commit_sha": "abc"}); err != nil {
		t.Fatal(err)
	}
	if err := r.f.handleEvaluate(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	wo := r.reload(t)
	if wo.Revision != 1 || wo.ReviewFixes != 1 || wo.CommitSHA != "" || r.q.enqueued[len(r.q.enqueued)-1] != "implement:" || r.q.payloads[len(r.q.payloads)-1]["reason"] != "fix_done_gate" {
		t.Fatalf("revision %d fixes %d sha %q enqueued %v", wo.Revision, wo.ReviewFixes, wo.CommitSHA, r.q.enqueued)
	}
}

func TestHumanRetryAfterAFailedDoneGateFixKeepsTheNote(t *testing.T) {
	ctx := context.Background()
	r := doneRig(t, &fakeJudge{over: map[string]float64{"met_c1": 0.1}}, metReport)
	r.evaluate(t)
	p := r.f.retryPayload(ctx, r.reload(t))
	if p["reason"] != "fix_done_gate" || !strings.Contains(p["note"].(string), "acceptance criterion c1") {
		t.Fatalf("retry payload %v", p)
	}
}
