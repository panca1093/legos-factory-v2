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
// (covers_* and met_* yes, everything else no) unless over says otherwise.
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
		if strings.HasPrefix(id, "covers_") || strings.HasPrefix(id, "met_") {
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
	if state["foreman_review"] != "ok" || state["request"] != "t\n\no" {
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
		if r.q.enqueued[len(r.q.enqueued)-1] != "plan:" || !strings.Contains(notes, "the plan includes work the request does not ask for") || !strings.Contains(notes, "Foreman review: ok") {
			t.Fatalf("the Architect gets the code-built note and the Foreman's notes: %v %q", r.q.enqueued, notes)
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
