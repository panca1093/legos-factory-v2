package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"legos-factory/backend/internal/model"
)

// fakeJudge answers every asked question with a settled "good" probability
// (covers_*, verified_*, critical_resolved and met_* yes, everything else no) unless over says otherwise.
type fakeJudge struct {
	mu    sync.Mutex
	over  map[string]float64
	err   error
	calls []judgeCall
}

type judgeCall struct {
	state any
	qs    map[string]Noul
}

func (j *fakeJudge) Ask(_ context.Context, state any, qs map[string]Noul) (JudgeResult, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.calls = append(j.calls, judgeCall{state, qs})
	req, _ := json.Marshal(map[string]any{"state": state, "questions": qs})
	res := JudgeResult{Request: req, Model: "jev-test", RequestedModel: "jev-test", Usage: Usage{InputTokens: 10, OutputTokens: 1}}
	if j.err != nil {
		return res, j.err
	}
	res.Answers = map[string]float64{}
	for id := range qs {
		p := 0.05
		if strings.HasPrefix(id, "covers_") || strings.HasPrefix(id, "met_") || strings.HasPrefix(id, "verified_") || id == "critical_resolved" {
			p = 0.95
		}
		if v, ok := j.over[id]; ok {
			p = v
		}
		res.Answers[id] = p
	}
	return res, nil
}

type liveBus struct {
	mu     sync.Mutex
	events []model.LiveEvent
}

func (b *liveBus) Publish(e model.LiveEvent) {
	b.mu.Lock()
	b.events = append(b.events, e)
	b.mu.Unlock()
}

func eventByKind(t *testing.T, r *rig, kind string) model.Event {
	t.Helper()
	evs := r.events(t, kind)
	if len(evs) == 0 {
		t.Fatalf("no %s event", kind)
	}
	return evs[len(evs)-1]
}

func stateChangedTo(t *testing.T, r *rig, to model.State) model.Event {
	t.Helper()
	for _, e := range r.events(t, "state_changed") {
		if e.Payload["to"] == string(to) {
			return e
		}
	}
	t.Fatalf("no state change to %s", to)
	return model.Event{}
}

// reviewRig drafts a plan and leaves the order waiting for its Foreman review.
func reviewRig(t *testing.T, j *fakeJudge) *rig {
	t.Helper()
	r := sessionRig(t, &sessHarness{answer: answerJSON})
	if j != nil {
		r.f.Judge = j
	}
	r.f.Bus = &liveBus{}
	r.plan(t)
	if wo := r.reload(t); wo.State != model.StateArchitectReview || wo.PlanReview != "pending" {
		t.Fatalf("state %s review %q", wo.State, wo.PlanReview)
	}
	return r
}

func (r *rig) review(t *testing.T) *model.WorkOrder {
	t.Helper()
	if err := r.f.handleReview(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	return r.reload(t)
}

func TestPlanGateApprovalStartsTheBuildWithoutAHuman(t *testing.T) {
	j := &fakeJudge{}
	r := reviewRig(t, j)
	wo := r.review(t)
	if wo.State != model.StateQueued || wo.PlanReview != "approved" {
		t.Fatalf("state %s review %q", wo.State, wo.PlanReview)
	}
	if got := r.q.enqueued[len(r.q.enqueued)-1]; got != "implement:wo1_t1" {
		t.Fatalf("the root task must be queued: %v", r.q.enqueued)
	}
	if ev := stateChangedTo(t, r, model.StateQueued); ev.Actor != "factory" {
		t.Fatalf("code approved the plan, actor %q", ev.Actor)
	}
	ev := eventByKind(t, r, "plan_gate")
	if ev.Actor != "jev" || ev.Payload["outcome"] != GateApprove || ev.Payload["gate"] != "plan" || ev.Payload["row"] != float64(5) {
		t.Fatalf("gate event: %+v", ev)
	}
	if _, ok := ev.Payload["request"]; !ok {
		t.Fatal("the stored gate event must carry the exact Jev request")
	}
	if len(r.events(t, "plan_escalated")) != 0 {
		t.Fatal("an approved plan is not escalated")
	}
	if len(j.calls) != 1 {
		t.Fatalf("one Jev request per gate, got %d", len(j.calls))
	}
	state := j.calls[0].state.(map[string]any)
	if state["analysis"] == nil || state["request"] != "t\n\no" {
		t.Fatalf("gate state: %+v", state)
	}
	if _, ok := j.calls[0].qs["covers_c1"]; !ok {
		t.Fatalf("criteria are numbered c1..: %v", j.calls[0].qs)
	}
}

func TestGateEventsStoreTheRequestButStreamASlimCopy(t *testing.T) {
	r := reviewRig(t, &fakeJudge{})
	r.review(t)
	var live model.LiveEvent
	for _, e := range r.f.Bus.(*liveBus).events {
		if e.Kind == "plan_gate" {
			live = e
		}
	}
	payload, _ := live.Payload["payload"].(map[string]any)
	if live.Actor != "jev" || payload["outcome"] != GateApprove || payload["answers"] == nil {
		t.Fatalf("live gate event: %+v", live)
	}
	for _, heavy := range []string{"request", "unsent_request"} {
		if _, ok := payload[heavy]; ok {
			t.Fatalf("live gate event still ships %s", heavy)
		}
	}
}

func TestPlanGateRevisionsAreCountedAndBounded(t *testing.T) {
	ctx := context.Background()
	j := &fakeJudge{over: map[string]float64{"scope_creep": 0.9}}
	r := reviewRig(t, j)
	for round := 1; round <= maxPlanRevisions; round++ {
		wo := r.review(t)
		if wo.State != model.StatePlanning || wo.PlanRevisions != round || wo.PlanReview != "" {
			t.Fatalf("round %d: state %s revisions %d review %q", round, wo.State, wo.PlanRevisions, wo.PlanReview)
		}
		last := r.q.payloads[len(r.q.payloads)-1]
		notes, _ := last["notes"].(string)
		if r.q.enqueued[len(r.q.enqueued)-1] != "plan:" || !strings.Contains(notes, "the plan includes work the request does not ask for") {
			t.Fatalf("the Architect gets the code-built note: %v %q", r.q.enqueued, notes)
		}
		if ev := stateChangedTo(t, r, model.StatePlanning); ev.Actor != "factory" {
			t.Fatalf("actor %q", ev.Actor)
		}
		r.plan(t)
	}
	wo := r.review(t)
	if wo.State != model.StateArchitectReview || wo.PlanReview != "approved" || wo.PlanRevisions != maxPlanRevisions {
		t.Fatalf("budget spent: state %s review %q revisions %d", wo.State, wo.PlanReview, wo.PlanRevisions)
	}
	esc := eventByKind(t, r, "plan_escalated")
	if esc.Actor != "factory" || !strings.Contains(esc.Message, "work the request does not ask for") {
		t.Fatalf("escalation: %+v", esc)
	}
	var outcomes []any
	for _, e := range r.events(t, "plan_gate") {
		outcomes = append(outcomes, e.Payload["outcome"])
	}
	if len(outcomes) != 3 || outcomes[0] != GateRevise || outcomes[1] != GateRevise || outcomes[2] != GateEscalate {
		t.Fatalf("outcomes %v", outcomes)
	}

	// the human decision after an escalation works and starts a fresh budget
	if err := r.f.RevisePlan(ctx, r.wo.ID, "split it differently"); err != nil {
		t.Fatal(err)
	}
	if got := r.reload(t); got.PlanRevisions != 0 || got.State != model.StatePlanning {
		t.Fatalf("a human revision resets the counter: %d %s", got.PlanRevisions, got.State)
	}
	if ev := r.events(t, "plan_revision_human"); len(ev) != 1 || ev[0].Actor != "human" || ev[0].Message != "split it differently" {
		t.Fatalf("a human plan revision must leave its own event: %+v", ev)
	}
}

func TestPlanGateEscalatesRiskyPlansForAHumanDecision(t *testing.T) {
	j := &fakeJudge{over: map[string]float64{"touches_schema": 0.9}}
	r := reviewRig(t, j)
	wo := r.review(t)
	if wo.State != model.StateArchitectReview || wo.PlanReview != "approved" || wo.PlanRevisions != 0 {
		t.Fatalf("state %s review %q revisions %d", wo.State, wo.PlanReview, wo.PlanRevisions)
	}
	if esc := eventByKind(t, r, "plan_escalated"); !strings.Contains(esc.Message, "database schema") || esc.Payload["row"] != float64(2) {
		t.Fatalf("escalation: %+v", esc)
	}
	if err := r.f.ApprovePlan(context.Background(), r.wo.ID); err != nil {
		t.Fatalf("the escalated plan must be approvable by a human: %v", err)
	}
	if ev := stateChangedTo(t, r, model.StateQueued); ev.Actor != "human" {
		t.Fatalf("actor %q", ev.Actor)
	}
	if ev := r.events(t, "plan_approved"); len(ev) != 1 || ev[0].Actor != "human" || !strings.Contains(ev[0].Message, "task(s)") && !strings.Contains(ev[0].Message, "tasks)") {
		t.Fatalf("plan approval must leave a plan_approved event: %+v", ev)
	}
}

func TestPlanGateWithoutJevLeavesTheHumanApproval(t *testing.T) {
	r := reviewRig(t, nil)
	wo := r.review(t)
	if wo.State != model.StateArchitectReview || wo.PlanReview != "approved" {
		t.Fatalf("state %s review %q", wo.State, wo.PlanReview)
	}
	if len(r.events(t, "plan_gate")) != 0 {
		t.Fatal("nothing was sent to Jev, so no gate event")
	}
	if esc := eventByKind(t, r, "plan_escalated"); !strings.Contains(esc.Message, "Jev is not configured") {
		t.Fatalf("escalation: %+v", esc)
	}
	if err := r.f.ApprovePlan(context.Background(), r.wo.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPlanGateFailureAndOversizeEscalateWithoutGuessing(t *testing.T) {
	j := &fakeJudge{err: errors.New("typesafe 503")}
	r := reviewRig(t, j)
	if wo := r.review(t); wo.State != model.StateArchitectReview || wo.PlanReview != "approved" {
		t.Fatalf("a failed call must escalate: %s %q", wo.State, wo.PlanReview)
	}
	ev := eventByKind(t, r, "plan_gate")
	if ev.Payload["outcome"] != GateEscalate || !strings.Contains(ev.Payload["error"].(string), "typesafe 503") {
		t.Fatalf("event: %+v", ev.Payload)
	}

	j2 := &fakeJudge{}
	r2 := reviewRig(t, j2)
	if err := r2.wos.UpdateFields(context.Background(), r2.wo.ID, map[string]any{"spec": strings.Repeat("x", 4*maxRequestTokens)}); err != nil {
		t.Fatal(err)
	}
	if wo := r2.review(t); wo.State != model.StateArchitectReview {
		t.Fatalf("state %s", wo.State)
	}
	if len(j2.calls) != 0 {
		t.Fatal("an oversize state must never be sent")
	}
	ev = eventByKind(t, r2, "plan_gate")
	if _, ok := ev.Payload["unsent_request"]; !ok || ev.Payload["outcome"] != GateEscalate {
		t.Fatalf("event: %+v", ev.Payload)
	}
}

func TestOnlyOneApprovalStartsTheBuild(t *testing.T) {
	r := reviewRig(t, &fakeJudge{})
	r.review(t)
	queued := len(r.q.enqueued)
	if err := r.f.ApprovePlan(context.Background(), r.wo.ID); !errors.Is(err, model.ErrStateConflict) {
		t.Fatalf("a human approval after the gate already queued the build must conflict: %v", err)
	}
	if len(r.q.enqueued) != queued {
		t.Fatalf("no second set of jobs: %v", r.q.enqueued)
	}
}

func TestInterruptedPlanRevisionIsFinishedOnRetry(t *testing.T) {
	r := reviewRig(t, &fakeJudge{})
	// the revision moved the order to PLANNING and then failed before queueing the Architect
	if err := r.wos.UpdateState(context.Background(), r.wo.ID, model.StatePlanning); err != nil {
		t.Fatal(err)
	}
	if err := r.f.handleReview(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if r.q.enqueued[len(r.q.enqueued)-1] != "plan:" || r.reload(t).PlanReview != "" {
		t.Fatalf("enqueued %v review %q", r.q.enqueued, r.reload(t).PlanReview)
	}
}

func TestFailedPlanRevisionStepDoesNotCountTwice(t *testing.T) {
	r := reviewRig(t, &fakeJudge{over: map[string]float64{"scope_creep": 0.9}})
	r.f.WO = &failStateStore{WorkOrderStore: r.f.WO, fail: true}
	if err := r.f.handleReview(context.Background(), model.Job{WOID: r.wo.ID}); err == nil {
		t.Fatal("the failed write must fail the job")
	}
	if wo := r.reload(t); wo.State != model.StateArchitectReview || wo.PlanRevisions != 0 {
		t.Fatalf("nothing half-applied: %s revisions %d", wo.State, wo.PlanRevisions)
	}
	if wo := r.review(t); wo.State != model.StatePlanning || wo.PlanRevisions != 1 {
		t.Fatalf("state %s revisions %d", wo.State, wo.PlanRevisions)
	}
}

type failOnceQueue struct {
	*fakeQueue
	fail bool
}

func (q *failOnceQueue) Enqueue(ctx context.Context, kind, woID, taskID string, payload map[string]any) (string, error) {
	if q.fail {
		q.fail = false
		return "", errors.New("queue down")
	}
	return q.fakeQueue.Enqueue(ctx, kind, woID, taskID, payload)
}

func TestLostPlanEnqueueAfterARevisionIsRecoveredOnRetry(t *testing.T) {
	r := reviewRig(t, &fakeJudge{over: map[string]float64{"scope_creep": 0.9}})
	r.f.Sched = &failOnceQueue{fakeQueue: r.q, fail: true}
	before := len(r.q.enqueued)
	if err := r.f.handleReview(context.Background(), model.Job{WOID: r.wo.ID}); err == nil {
		t.Fatal("the lost enqueue must fail the job")
	}
	if wo := r.reload(t); wo.State != model.StatePlanning || wo.PlanReview != "" || wo.PlanRevisions != 1 {
		t.Fatalf("state %s review %q revisions %d", wo.State, wo.PlanReview, wo.PlanRevisions)
	}
	if err := r.f.handleReview(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if len(r.q.enqueued) != before+1 || r.q.enqueued[before] != "plan:" {
		t.Fatalf("the retry must re-queue the Architect: %v", r.q.enqueued)
	}
	r.q.active = true
	if err := r.f.handleReview(context.Background(), model.Job{WOID: r.wo.ID}); err != nil || len(r.q.enqueued) != before+1 {
		t.Fatalf("no duplicate while a plan job is active: %v %v", err, r.q.enqueued)
	}
}

const (
	criticalContract    = `{"findings":[{"id":"F1","severity":"critical","layer":"contract","cites":"c1","issue":"no error path","fix":"add a rule"}],"coverage":[{"criterion":"c1","verification":"missing"}]}`
	criticalRequirement = `{"findings":[{"id":"F1","severity":"critical","layer":"requirement","cites":"brief: constraint 1","issue":"silent on limits","fix":"cap uploads at 5MB"}],"coverage":[]}`
)

func harnessRig(t *testing.T, h *sessHarness, j *fakeJudge) *rig {
	t.Helper()
	r := sessionRig(t, h)
	r.f.Judge, r.f.Bus = j, &liveBus{}
	r.plan(t)
	return r
}

func TestSecondRoundReviewIsScopedToThePreviousCriticals(t *testing.T) {
	h := &sessHarness{answer: answerJSON, review: criticalContract}
	j := &fakeJudge{over: map[string]float64{"critical_resolved": 0.1}}
	r := harnessRig(t, h, j)
	if wo := r.review(t); wo.State != model.StatePlanning || wo.PlanRevisions != 1 {
		t.Fatalf("state %s revisions %d", wo.State, wo.PlanRevisions)
	}
	notes, _ := lastPayload(r)["notes"].(string)
	if !strings.Contains(notes, "F1 [critical, contract] c1: no error path") || strings.Contains(notes, "Coverage") {
		t.Fatalf("the revise note holds the unresolved critical finding only: %q", notes)
	}
	if strings.Contains(h.starts[len(h.starts)-1].Prompt, "re-review") {
		t.Fatal("round 1 is a full review")
	}

	h.review = `{"resolved":[{"id":"F1","status":"resolved"}],"findings":[{"id":"F2","severity":"high","layer":"contract","cites":"c1","issue":"error text unspecified","fix":"name the message"}],"coverage":[{"criterion":"c1","verification":"test"}]}`
	j.over = nil
	r.plan(t)
	if wo := r.review(t); wo.State != model.StateQueued {
		t.Fatalf("state %s", wo.State)
	}
	prompt := h.starts[len(h.starts)-1].Prompt
	if !strings.Contains(prompt, "Do NOT run a fresh full review") || !strings.Contains(prompt, "F1 [critical, contract]") {
		t.Fatalf("round 2 prompt must be scoped to F1:\n%s", prompt)
	}
	state := j.calls[len(j.calls)-1].state.(map[string]any)["analysis"].(map[string]any)
	fs, _ := state["findings"].([]reviewFinding)
	if len(fs) != 1 || fs[0].ID != "F2" {
		t.Fatalf("the resolved critical must leave the analysis: %+v", fs)
	}
	wo := r.reload(t)
	if len(wo.Clarifications) != 1 || wo.Clarifications[0].By != builderNotesBy || !strings.Contains(wo.Clarifications[0].Answer, "name the message") {
		t.Fatalf("the high finding must reach the Builder as a note: %+v", wo.Clarifications)
	}
	if p := implementPrompt(wo, model.Job{TaskID: "wo1_t1"}, nil); !strings.Contains(p, "error text unspecified") {
		t.Fatalf("builder prompt lacks the note:\n%s", p)
	}
}

func TestUnresolvedCriticalStaysInTheAnalysis(t *testing.T) {
	h := &sessHarness{answer: answerJSON, review: criticalContract}
	j := &fakeJudge{over: map[string]float64{"critical_resolved": 0.1}}
	r := harnessRig(t, h, j)
	r.review(t)
	h.review = `{"findings":[],"coverage":[]}` // no status for F1: it must stay unresolved
	r.plan(t)
	r.review(t)
	state := j.calls[len(j.calls)-1].state.(map[string]any)["analysis"].(map[string]any)
	if fs, _ := state["findings"].([]reviewFinding); len(fs) != 1 || fs[0].ID != "F1" {
		t.Fatalf("F1 must carry over: %+v", fs)
	}
}

func TestRequirementCriticalAmendsTheBriefThenRevises(t *testing.T) {
	h := &sessHarness{answer: answerJSON, review: criticalRequirement}
	r := harnessRig(t, h, &fakeJudge{over: map[string]float64{"critical_resolved": 0.1}})
	wo := r.review(t)
	if wo.State != model.StatePlanning || !strings.Contains(wo.Brief, "Amendments from plan review:\n- cap uploads at 5MB") {
		t.Fatalf("state %s brief %q", wo.State, wo.Brief)
	}
	if ev := r.events(t, "brief_amended"); len(ev) != 1 || ev[0].Actor != "foreman" {
		t.Fatalf("brief_amended: %+v", ev)
	}
}

func TestRequirementCriticalWithAQuestionAsksTheHuman(t *testing.T) {
	q := strings.Replace(criticalRequirement, `"fix":"cap uploads at 5MB"`, `"fix":"","question":"Which upload cap?"`, 1)
	r := harnessRig(t, &sessHarness{answer: answerJSON, review: q}, &fakeJudge{})
	wo := r.review(t)
	if wo.State != model.StateWaitingForHuman || wo.ClarifQuestion != "Which upload cap?" {
		t.Fatalf("state %s question %q", wo.State, wo.ClarifQuestion)
	}
	if len(r.events(t, "plan_gate")) != 0 {
		t.Fatal("the gate must wait for the human answer")
	}
}

func TestUnsettledScopeCreepApprovesWithANoteForTheBuilder(t *testing.T) {
	r := reviewRig(t, &fakeJudge{over: map[string]float64{"scope_creep": 0.5}})
	wo := r.review(t)
	if wo.State != model.StateQueued {
		t.Fatalf("state %s", wo.State)
	}
	if len(wo.Clarifications) != 1 || !strings.Contains(wo.Clarifications[0].Answer, "it is unclear whether the plan includes work") {
		t.Fatalf("notes: %+v", wo.Clarifications)
	}
}
