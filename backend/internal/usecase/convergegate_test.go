package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

// ---- converge gate ----

type diffSandbox struct {
	filesSandbox
	diff []FileDiff
}

func (s diffSandbox) Diff(context.Context, string) ([]FileDiff, error) { return s.diff, nil }

const (
	cleanReport = `{"revision":0,"round":1,"gaps":[],"converged":true}`
	codeGapJSON = `{"id":"C1-G1","type":"missing","layer":"code","cites":"c1","evidence":"round.go:12","fix":"round money to integer rupiah"}`
)

func gapsReport(gaps ...string) string {
	return fmt.Sprintf(`{"revision":0,"round":1,"gaps":[%s],"converged":false}`, strings.Join(gaps, ","))
}

func convergeRig(t *testing.T, j *fakeJudge, report string) *rig {
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

// builtPlan seeds one task whose Builder run completed.
func (r *rig) builtPlan(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	id := r.wo.ID + "_t1"
	if err := r.wos.ReplacePlan(ctx, r.wo.ID, []model.Task{{ID: id, WOID: r.wo.ID, Title: "Round money", Objective: "o", Role: "builder", Scope: []string{"round.go"}}}); err != nil {
		t.Fatal(err)
	}
	if err := r.f.Runs.Start(ctx, "run_t1", r.wo.ID, id, "builder", "h"); err != nil {
		t.Fatal(err)
	}
	if err := r.f.Runs.Complete(ctx, "run_t1", model.AgentResult{}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (r *rig) evaluate(t *testing.T) *model.WorkOrder {
	t.Helper()
	if err := r.f.handleEvaluate(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	return r.reload(t)
}

func (r *rig) markAppends(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		r.f.event(r.wo.ID, "factory", "converge_append", "x", map[string]any{"round": i + 1})
	}
}

func TestConvergeGateConvergedContinuesToReadiness(t *testing.T) {
	j := &fakeJudge{}
	r := convergeRig(t, j, cleanReport)
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	ev := eventByKind(t, r, "converge_gate")
	if ev.Actor != "jev" || ev.Payload["outcome"] != GateConverged || ev.Payload["row"] != float64(5) {
		t.Fatalf("gate event: %+v", ev)
	}
	if len(r.events(t, "readiness_ready")) != 1 || len(r.h.prompts["foreman"]) != 0 {
		t.Fatal("readiness runs as before; the Foreman validation run is gone")
	}
	state := j.calls[0].state.(map[string]any)
	diff := state["diff"].([]FileDiff)
	checks := state["checks"].([]GateCheck)
	if len(diff) != 1 || diff[0].Path != "round.go" || len(checks) != 1 || !checks[0].Passed || state["spec"] != "Round in integer rupiah" {
		t.Fatalf("converge-gate state: %+v", state)
	}
	if _, ok := j.calls[0].qs["met_c1"]; !ok || len(j.calls[0].qs) != 1 {
		t.Fatalf("one met_ question per criterion until contract tests exist: %v", j.calls[0].qs)
	}
}

func TestConvergeAppendCreatesNewTasksAndBuildsOnlyThem(t *testing.T) {
	ctx := context.Background()
	j := &fakeJudge{over: map[string]float64{"gap_valid_C1-G1": 0.9}}
	partial := `{"id":"C1-G2","type":"partial","layer":"code","cites":"spec §2","evidence":"round.go:30","fix":"rename a helper"}`
	r := convergeRig(t, j, gapsReport(codeGapJSON, partial))
	old := r.builtPlan(t)
	before, _ := r.wos.Plan(ctx, r.wo.ID)

	wo := r.evaluate(t)
	if wo.State != model.StateImplementing || wo.Revision != 1 || wo.CommitSHA != "" || wo.ReviewFixes != 0 || wo.ArchitectReport != "" {
		t.Fatalf("state %s revision %d fixes %d report %q", wo.State, wo.Revision, wo.ReviewFixes, wo.ArchitectReport)
	}
	plan, _ := r.wos.Plan(ctx, r.wo.ID)
	if len(plan) != 2 || plan[0].ID != old || plan[0].Title != before[0].Title || plan[0].Objective != before[0].Objective {
		t.Fatalf("the first task must stay as it was: %+v", plan)
	}
	nt := plan[1]
	if nt.ID != r.wo.ID+"_t2" || nt.Seq != 1 || !strings.HasPrefix(nt.Title, "Close gap C1-G1:") || len(nt.Deps) != 0 ||
		len(nt.Scope) != 1 || nt.Scope[0] != "round.go" || !strings.Contains(nt.Objective, "round money to integer rupiah") || !strings.Contains(nt.Objective, "round.go:12") {
		t.Fatalf("appended task: %+v", nt)
	}
	if len(r.q.enqueued) != 1 || r.q.enqueued[0] != "implement:"+nt.ID {
		t.Fatalf("only the new task is built, got %v", r.q.enqueued)
	}
	ev := eventByKind(t, r, "converge_append")
	if ev.Payload["round"] != float64(1) || len(ev.Payload["tasks"].([]any)) != 1 {
		t.Fatalf("event %+v", ev.Payload)
	}
	if g := eventByKind(t, r, "converge_gate"); g.Payload["outcome"] != GateAppend || g.Payload["row"] != float64(3) {
		t.Fatalf("gate %+v", g.Payload)
	}
	if len(r.events(t, "fix_requested")) != 0 {
		t.Fatal("an append is not a fix brief")
	}
	var note string
	for _, c := range wo.Clarifications {
		if c.By == builderNotesBy && c.Question == "Note from converge" {
			note = c.Answer
		}
	}
	if !strings.Contains(note, "rename a helper") || strings.Contains(note, "C1-G1") {
		t.Fatalf("the non-blocking gap is a note to the Builder, the appended one is not: %q", note)
	}
	prompt := implementPrompt(wo, model.Job{TaskID: nt.ID}, plan)
	if !strings.Contains(prompt, "Review notes") || !strings.Contains(prompt, "rename a helper") {
		t.Fatalf("the next Builder run must see the note:\n%s", prompt)
	}
}

func TestConvergeAppendForAnUnmetCriterionWithoutAGap(t *testing.T) {
	r := convergeRig(t, &fakeJudge{over: map[string]float64{"met_c1": 0.1}}, cleanReport)
	r.builtPlan(t)
	wo := r.evaluate(t)
	plan, _ := r.wos.Plan(context.Background(), r.wo.ID)
	if wo.State != model.StateImplementing || len(plan) != 2 || plan[1].Title != "Close criterion c1 (C1)" || !strings.Contains(plan[1].Objective, "no floats") {
		t.Fatalf("state %s plan %+v", wo.State, plan)
	}
}

func TestConvergeAppendDoesNotDuplicateATaskTheGapAlreadyCovers(t *testing.T) {
	r := convergeRig(t, &fakeJudge{over: map[string]float64{"gap_valid_C1-G1": 0.9, "met_c1": 0.1}}, gapsReport(codeGapJSON))
	r.builtPlan(t)
	r.evaluate(t)
	plan, _ := r.wos.Plan(context.Background(), r.wo.ID)
	if len(plan) != 2 {
		t.Fatalf("the valid gap cites c1, so the unmet criterion needs no second task: %+v", plan)
	}
}

func TestConvergeContractGapAmendsTheSpec(t *testing.T) {
	contract := `{"id":"C1-G1","type":"missing","layer":"contract","cites":"c1","evidence":"spec §2 is silent on negative amounts","fix":"Negative amounts round toward zero."}`
	j := &fakeJudge{over: map[string]float64{"gap_valid_C1-G1": 0.9}}
	r := convergeRig(t, j, gapsReport(contract))
	r.builtPlan(t)
	wo := r.evaluate(t)
	if !strings.Contains(wo.Spec, "Amendment: Converge gap C1-G1 (c1)") || !strings.Contains(wo.Spec, "Negative amounts round toward zero.") ||
		!strings.Contains(wo.Spec, "reason: converge found the contract missing: spec §2 is silent on negative amounts") {
		t.Fatalf("the spec is amended with the reason: %q", wo.Spec)
	}
	plan, _ := r.wos.Plan(context.Background(), r.wo.ID)
	if len(plan) != 1 || wo.State != model.StateSpecialistReview || wo.ArchitectReport != "" || r.q.enqueued[len(r.q.enqueued)-1] != "inspect:" {
		t.Fatalf("nothing to build, so converge runs again: state %s plan %d enqueued %v", wo.State, len(plan), r.q.enqueued)
	}
	if len(r.events(t, "converge_append")) != 1 {
		t.Fatal("an amendment round uses the append budget")
	}

	// a contract gap whose evidence names code also appends a task
	withCode := strings.Replace(contract, "spec §2 is silent on negative amounts", "round.go:40 rounds negatives away from zero", 1)
	r2 := convergeRig(t, &fakeJudge{over: map[string]float64{"gap_valid_C1-G1": 0.9}}, gapsReport(withCode))
	r2.builtPlan(t)
	wo2 := r2.evaluate(t)
	plan2, _ := r2.wos.Plan(context.Background(), r2.wo.ID)
	if wo2.State != model.StateImplementing || len(plan2) != 2 || !strings.Contains(wo2.Spec, "Amendment:") {
		t.Fatalf("state %s plan %+v spec %q", wo2.State, plan2, wo2.Spec)
	}
}

func TestConvergeRequirementGapGoesToTheForeman(t *testing.T) {
	req := `{"id":"C1-G1","type":"contradicts","layer":"requirement","cites":"c1","evidence":"round.go:12","fix":"allow floats for display"}`
	j := &fakeJudge{over: map[string]float64{"gap_valid_C1-G1": 0.9}}

	r := convergeRig(t, j, gapsReport(req))
	r.builtPlan(t)
	r.h.foreman = `{"decision":"ANSWER","answer":"Display may use floats.","reason":"the brief allows it"}`
	wo := r.evaluate(t)
	if len(r.h.prompts["foreman"]) != 1 || !strings.Contains(r.h.prompts["foreman"][0], "C1-G1") || !strings.Contains(r.h.prompts["foreman"][0], "allow floats for display") {
		t.Fatalf("the Foreman is asked about the gap: %v", r.h.prompts["foreman"])
	}
	if !strings.Contains(wo.Spec, "Display may use floats.") || wo.State != model.StateSpecialistReview || r.q.enqueued[len(r.q.enqueued)-1] != "inspect:" {
		t.Fatalf("the answer amends the spec and converge runs again: state %s spec %q enqueued %v", wo.State, wo.Spec, r.q.enqueued)
	}

	// an escalation parks the order for a human, whose answer re-verifies
	r2 := convergeRig(t, &fakeJudge{over: map[string]float64{"gap_valid_C1-G1": 0.9}}, gapsReport(req))
	r2.builtPlan(t)
	r2.h.foreman = `{"decision":"ESCALATE","reason":"a product decision"}`
	wo2 := r2.evaluate(t)
	if wo2.State != model.StateWaitingForHuman || wo2.ClarifTask != "converge" || !strings.Contains(wo2.ClarifQuestion, "C1-G1") || wo2.ArchitectReport != "" {
		t.Fatalf("state %s task %q question %q", wo2.State, wo2.ClarifTask, wo2.ClarifQuestion)
	}
	if len(r2.events(t, "clarification_requested")) != 1 || len(r2.events(t, "converge_append")) != 1 {
		t.Fatal("the human is asked once and the round is counted")
	}
	if err := r2.f.AnswerClarification(context.Background(), r2.wo.ID, "Keep floats out."); err != nil {
		t.Fatal(err)
	}
	after := r2.reload(t)
	if after.State != model.StateVerifying || !strings.Contains(after.Spec, "Keep floats out.") || r2.q.enqueued[len(r2.q.enqueued)-1] != "verify:" {
		t.Fatalf("state %s spec %q enqueued %v", after.State, after.Spec, r2.q.enqueued)
	}
}

func TestConvergeAppendBudgetEscalates(t *testing.T) {
	j := &fakeJudge{over: map[string]float64{"gap_valid_C1-G1": 0.9}}
	r := convergeRig(t, j, gapsReport(codeGapJSON))
	r.builtPlan(t)
	r.markAppends(t, MaxConvergeAppends)
	wo := r.evaluate(t)
	if wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	plan, _ := r.wos.Plan(context.Background(), r.wo.ID)
	if len(plan) != 1 {
		t.Fatalf("no task is appended past the budget: %+v", plan)
	}
	if ev := eventByKind(t, r, "converge_gate"); ev.Payload["outcome"] != GateEscalate || ev.Payload["row"] != float64(3) {
		t.Fatalf("event %+v", ev.Payload)
	}
	ready := eventByKind(t, r, "readiness_needs_human_review")
	if !strings.Contains(ready.Message, "converge gate needs a human decision") || !strings.Contains(ready.Message, "C1-G1") {
		t.Fatalf("readiness: %+v", ready)
	}
}

func TestConvergeUnsettledAnswers(t *testing.T) {
	// an unsettled gap_valid never appends: with nothing else wrong the order converges
	r := convergeRig(t, &fakeJudge{over: map[string]float64{"gap_valid_C1-G1": 0.5}}, gapsReport(codeGapJSON))
	r.builtPlan(t)
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview || len(r.events(t, "converge_append")) != 0 {
		t.Fatalf("state %s", wo.State)
	}
	if ev := eventByKind(t, r, "converge_gate"); ev.Payload["outcome"] != GateConverged {
		t.Fatalf("event %+v", ev.Payload)
	}

	// next to a real problem it travels to the Builder as a note
	r2 := convergeRig(t, &fakeJudge{over: map[string]float64{"gap_valid_C1-G1": 0.5, "met_c1": 0.1}}, gapsReport(codeGapJSON))
	r2.builtPlan(t)
	wo := r2.evaluate(t)
	var note string
	for _, c := range wo.Clarifications {
		if c.By == builderNotesBy {
			note += c.Answer
		}
	}
	if wo.State != model.StateImplementing || !strings.Contains(note, "unclear whether this gap is real") {
		t.Fatalf("state %s notes %q", wo.State, note)
	}

	// an unsettled criterion is a human call
	r3 := convergeRig(t, &fakeJudge{over: map[string]float64{"met_c1": 0.5}}, cleanReport)
	if wo := r3.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	if ev := eventByKind(t, r3, "converge_gate"); ev.Payload["outcome"] != GateEscalate || ev.Payload["row"] != float64(4) {
		t.Fatalf("event %+v", ev.Payload)
	}
}

func TestConvergeGateEscalatesOnJevFailureOrMissingReport(t *testing.T) {
	r := convergeRig(t, &fakeJudge{err: errors.New("boom")}, cleanReport)
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	if ev := eventByKind(t, r, "converge_gate"); ev.Payload["outcome"] != GateEscalate || !strings.Contains(ev.Payload["error"].(string), "boom") {
		t.Fatalf("event %+v", ev.Payload)
	}

	j := &fakeJudge{}
	stale := convergeRig(t, j, strings.Replace(cleanReport, `"revision":0`, `"revision":4`, 1))
	if wo := stale.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	if len(j.calls) != 0 {
		t.Fatal("without a converge report of this revision nothing is sent to Jev")
	}
	if ev := eventByKind(t, stale, "converge_gate"); ev.Payload["outcome"] != GateEscalate || !strings.Contains(ev.Payload["note"].(string), "converge report is missing") {
		t.Fatalf("event %+v", ev.Payload)
	}
}

func TestConvergeIsSkippedWithoutJev(t *testing.T) {
	r := convergeRig(t, nil, cleanReport)
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	if len(r.events(t, "converge_gate")) != 0 || len(r.h.prompts["foreman"]) != 0 || len(r.h.prompts["architect"]) != 0 {
		t.Fatal("no gate and no converge run without Jev")
	}
	if len(r.events(t, "readiness_ready")) != 1 {
		t.Fatal("today's deterministic readiness still decides")
	}
}

func TestConvergeGateWithoutCriteriaEscalates(t *testing.T) {
	r := convergeRig(t, &fakeJudge{}, cleanReport)
	if err := r.wos.UpdateFields(context.Background(), r.wo.ID, map[string]any{"acceptance": ""}); err != nil {
		t.Fatal(err)
	}
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	if ev := eventByKind(t, r, "converge_gate"); !strings.Contains(ev.Payload["note"].(string), "no acceptance criteria") {
		t.Fatalf("event %+v", ev.Payload)
	}
}

// ---- converge run ----

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
	r := convergeRig(t, &fakeJudge{}, "")
	r.f.Resolve = func(model.AgentRole, *model.WorkOrder) Harness { return h }
	if err := r.wos.ReplacePlan(context.Background(), r.wo.ID, []model.Task{{ID: "wo1_t1", WOID: r.wo.ID, Title: "Round money", Objective: "o", Role: "builder", Scope: []string{"round.go"}}}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConvergeRunsAlongsideTheSpecialistsAndIsStored(t *testing.T) {
	h := &barrierHarness{both: make(chan struct{})}
	h.converge = `{"gaps":[{"id":"G1","type":"missing","layer":"code","cites":"c1","evidence":"round.go:1","fix":"f1"},{"id":"G1","type":"partial","layer":"contract","cites":"spec §2","evidence":"t","fix":"f2"}],"converged":false}`
	r := inspectRig(t, h)
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	wo := r.reload(t)
	var rep architectReport
	if err := json.Unmarshal([]byte(wo.ArchitectReport), &rep); err != nil || rep.Revision != 0 || rep.Round != 1 || len(rep.Gaps) != 2 || rep.Converged {
		t.Fatalf("stored report %q: %v", wo.ArchitectReport, err)
	}
	if rep.Gaps[0].ID != "C1-G1" || rep.Gaps[1].ID != "C1-G2" {
		t.Fatalf("ids are assigned by code, not taken from the model: %+v", rep.Gaps)
	}
	if len(r.events(t, "converge_failed")) != 0 {
		t.Fatalf("converge failed: %+v", r.events(t, "converge_failed"))
	}
	ev := eventByKind(t, r, "converge_report")
	if ev.Actor != "architect" || !strings.Contains(ev.Message, "2 gap(s)") {
		t.Fatalf("event %+v", ev)
	}
	prompt := h.prompts["architect"][0]
	for _, want := range []string{"c1: no floats", "Round in integer rupiah", "--- round.go (+3 -1)", "+rupiah", "go-test (go test ./...) → PASS", "Round money", "Do not trust any earlier claim"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("converge prompt lacks %q:\n%s", want, prompt)
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
		t.Fatalf("converge must be one accounted run, got %d", runs)
	}
}

func TestConvergeRoundFollowsTheAppendCount(t *testing.T) {
	h := &replyHarness{converge: `{"gaps":[{"type":"missing","layer":"code","cites":"c1","evidence":"e","fix":"f"}],"converged":false}`}
	r := inspectRig(t, h)
	r.markAppends(t, 1)
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	var rep architectReport
	_ = json.Unmarshal([]byte(r.reload(t).ArchitectReport), &rep)
	if rep.Round != 2 || len(rep.Gaps) != 1 || rep.Gaps[0].ID != "C2-G1" {
		t.Fatalf("report %+v", rep)
	}
}

func TestConvergeFailureDoesNotFailTheInspection(t *testing.T) {
	h := &replyHarness{converge: `{"gaps":[{"type":"maybe","layer":"code","evidence":"e"}]}`}
	r := inspectRig(t, h)
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatalf("the inspection must survive a bad converge reply: %v", err)
	}
	if wo := r.reload(t); wo.ArchitectReport != "" {
		t.Fatalf("nothing valid to store: %q", wo.ArchitectReport)
	}
	if len(r.events(t, "converge_failed")) != 1 || r.q.enqueued[len(r.q.enqueued)-1] != "evaluate:" {
		t.Fatalf("failure must be visible and evaluation still follow: %v", r.q.enqueued)
	}
}

func TestNoConvergeRunWithoutJev(t *testing.T) {
	r := inspectRig(t, &replyHarness{})
	r.f.Judge = nil
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if len(r.events(t, "converge_report")) != 0 || len(r.events(t, "converge_failed")) != 0 || len(r.h.prompts["architect"]) != 0 {
		t.Fatal("nothing reads the gaps without Jev, so the Architect must not run")
	}
	if r.q.enqueued[len(r.q.enqueued)-1] != "evaluate:" {
		t.Fatalf("enqueued %v", r.q.enqueued)
	}
}

func TestTransientArchitectErrorRetriesOnlyConverge(t *testing.T) {
	delay := architectRetryDelay
	architectRetryDelay = 0
	t.Cleanup(func() { architectRetryDelay = delay })
	h := &replyHarness{archErr: errors.New("model offline")}
	r := inspectRig(t, h)
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID, MaxAttempts: 3, Attempts: 0}); err != nil {
		t.Fatalf("the inspection must not be requeued: %v", err)
	}
	if got := len(h.prompts["architect"]); got != convergeAttempts {
		t.Fatalf("converge attempts %d, want %d", got, convergeAttempts)
	}
	if got := len(h.prompts["inspector"]); got != len(h.specials) || got != len(r.events(t, "inspection_specialists")[0].Payload["specialists"].([]any)) {
		t.Fatalf("each specialist runs once, got %d prompts for %v", got, h.specials)
	}
	if len(r.events(t, "converge_failed")) != 1 || r.q.enqueued[len(r.q.enqueued)-1] != "evaluate:" {
		t.Fatalf("failure is reported once and evaluation follows: %v", r.q.enqueued)
	}
}

func TestArchitectErrorIsIgnoredWhenBlockersSendTheOrderBack(t *testing.T) {
	delay := architectRetryDelay
	architectRetryDelay = 0
	t.Cleanup(func() { architectRetryDelay = delay })
	h := &replyHarness{archErr: errors.New("model offline"),
		inspect: `{"findings":[{"severity":"blocker","title":"Float money","detail":"d","location":"round.go:1"}]}`}
	r := inspectRig(t, h)
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if got := len(h.prompts["architect"]); got != 1 {
		t.Fatalf("no converge retry when the Builder is next anyway, got %d", got)
	}
	if wo := r.reload(t); wo.State != model.StateImplementing || wo.Revision != 1 {
		t.Fatalf("state %s revision %d", wo.State, wo.Revision)
	}
	if len(r.events(t, "converge_failed")) != 1 {
		t.Fatal("the failure is still recorded")
	}
}

func TestConvergePanicBecomesAFailureEvent(t *testing.T) {
	r := inspectRig(t, &replyHarness{archBoom: true})
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if ev := eventByKind(t, r, "converge_failed"); !strings.Contains(ev.Message, "panicked") {
		t.Fatalf("event %+v", ev)
	}
}

// ---- inspector findings ----

func TestNonBlockingFindingsAreNotesForTheNextBuilder(t *testing.T) {
	ctx := context.Background()
	j := &fakeJudge{}
	h := &replyHarness{inspect: `{"findings":[{"severity":"minor","title":"Unused import","detail":"round.go imports fmt","location":"round.go:3"}]}`}
	r := inspectRig(t, h)
	r.f.Judge = j
	if err := r.f.handleInspect(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	wo := r.reload(t)
	if wo.State != model.StateSpecialistReview || wo.Revision != 0 || r.q.enqueued[len(r.q.enqueued)-1] != "evaluate:" {
		t.Fatalf("a minor finding sends nothing back: state %s revision %d %v", wo.State, wo.Revision, r.q.enqueued)
	}
	var notes []model.Clarification
	for _, c := range wo.Clarifications {
		if c.By == builderNotesBy {
			notes = append(notes, c)
		}
	}
	if len(notes) == 0 || notes[0].Question != "Note from inspector" || !strings.Contains(notes[0].Answer, "Unused import") {
		t.Fatalf("notes %+v", notes)
	}
	if got := answered(wo.Clarifications); len(got) != 0 {
		t.Fatalf("a note is never an answer: %+v", got)
	}
	if prompt := implementPrompt(wo, model.Job{TaskID: "wo1_t1"}, nil); !strings.Contains(prompt, "Unused import") || !strings.Contains(prompt, "Review notes") {
		t.Fatalf("Builder prompt lacks the note:\n%s", prompt)
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

func TestFindingThatCitesACriterionBlocks(t *testing.T) {
	h := &replyHarness{inspect: `{"findings":[{"severity":"minor","title":"Floats used","detail":"round.go uses float64","location":"round.go:3","cites":"c1"}]}`}
	r := inspectRig(t, h)
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	wo := r.reload(t)
	if wo.State != model.StateImplementing || wo.Revision != 1 || wo.ReviewFixes != 1 {
		t.Fatalf("a finding that cites an acceptance criterion runs the existing remediation loop: state %s revision %d", wo.State, wo.Revision)
	}
	if last := r.q.payloads[len(r.q.payloads)-1]; last["reason"] != "fix_findings" || !strings.Contains(last["brief"].(string), "Floats used") {
		t.Fatalf("payload %v", last)
	}
	fds, _ := r.f.Findings.List(context.Background(), r.wo.ID)
	if len(fds) == 0 || fds[0].Severity != model.SeverityBlocker {
		t.Fatalf("findings %+v", fds)
	}
}

func TestFindingCitingNoKnownCriterionStaysANote(t *testing.T) {
	h := &replyHarness{inspect: `{"findings":[{"severity":"major","title":"Naming","detail":"d","location":"round.go:3","cites":"c9, spec §1"}]}`}
	r := inspectRig(t, h)
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if wo := r.reload(t); wo.State != model.StateSpecialistReview || r.q.enqueued[len(r.q.enqueued)-1] != "evaluate:" {
		t.Fatalf("state %s %v", wo.State, r.q.enqueued)
	}
}

// ---- resume ----

func TestEvaluateIsIdempotentAndResumesAHalfDoneAppend(t *testing.T) {
	ctx := context.Background()
	r := convergeRig(t, &fakeJudge{}, cleanReport)
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateNeedsHumanReview); err != nil {
		t.Fatal(err)
	}
	if err := r.f.handleEvaluate(ctx, model.Job{WOID: r.wo.ID}); err != nil || len(r.q.enqueued) != 0 {
		t.Fatalf("a superseded evaluate job is a no-op: %v %v", err, r.q.enqueued)
	}
	// the order moved to IMPLEMENTING with a new task but the Builder job was lost
	id := r.builtPlan(t)
	if err := r.wos.AppendPlan(ctx, r.wo.ID, []model.Task{{ID: r.wo.ID + "_t2", WOID: r.wo.ID, Title: "Close gap C1-G1: x", Objective: "o", Role: "builder"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.wos.UpdateStateAndFields(ctx, r.wo.ID, model.StateImplementing, map[string]any{"revision": 1, "commit_sha": ""}); err != nil {
		t.Fatal(err)
	}
	if err := r.f.handleEvaluate(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if len(r.q.enqueued) != 1 || r.q.enqueued[0] != "implement:"+r.wo.ID+"_t2" || id == "" {
		t.Fatalf("only the unbuilt task is queued: %v", r.q.enqueued)
	}
}

type failStateStore struct {
	WorkOrderStore
	fail bool
}

func (s *failStateStore) UpdateStateAndFields(ctx context.Context, id string, st model.State, fields map[string]any) error {
	if s.fail {
		s.fail = false
		return errors.New("db busy")
	}
	return s.WorkOrderStore.UpdateStateAndFields(ctx, id, st, fields)
}

func TestConvergeAppendBumpsTheRevisionAndAppendsOnceAcrossARetry(t *testing.T) {
	ctx := context.Background()
	r := convergeRig(t, &fakeJudge{over: map[string]float64{"gap_valid_C1-G1": 0.9}}, gapsReport(codeGapJSON))
	r.builtPlan(t)
	r.f.WO = &failStateStore{WorkOrderStore: r.f.WO, fail: true}
	if err := r.f.handleEvaluate(ctx, model.Job{WOID: r.wo.ID}); err == nil {
		t.Fatal("the failed write must fail the job")
	}
	if wo := r.reload(t); wo.State != model.StateSpecialistReview || wo.Revision != 0 {
		t.Fatalf("the state change is not half-applied: %s rev %d", wo.State, wo.Revision)
	}
	wo := r.evaluate(t)
	plan, _ := r.wos.Plan(ctx, r.wo.ID)
	if wo.State != model.StateImplementing || wo.Revision != 1 || len(plan) != 2 || r.q.enqueued[len(r.q.enqueued)-1] != "implement:"+plan[1].ID {
		t.Fatalf("state %s rev %d plan %d enqueued %v", wo.State, wo.Revision, len(plan), r.q.enqueued)
	}
}
