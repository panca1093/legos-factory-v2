package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/usecase"
)

func (ta *testApp) createWOWith(t *testing.T, projectID, objective string) string {
	t.Helper()
	status, body := ta.do(t, "POST", "/api/v1/work-orders", fmt.Sprintf(
		`{"project_id":%q,"title":"Greeter","objective":%q,"acceptance":"TestGreet passes with go test ./..."}`,
		projectID, objective))
	if status != 201 {
		t.Fatalf("create work order: %d %s", status, body)
	}
	return decode[struct{ ID string }](t, body).ID
}

func (ta *testApp) workOrder(t *testing.T, woID string) map[string]any {
	t.Helper()
	status, body := ta.do(t, "GET", "/api/v1/work-orders/"+woID, "")
	if status != 200 {
		t.Fatalf("get work order: %d %s", status, body)
	}
	var doc map[string]any
	json.Unmarshal([]byte(body), &doc)
	return doc["work_order"].(map[string]any)
}

type activityEvent struct {
	Actor   string         `json:"actor"`
	Kind    string         `json:"kind"`
	Message string         `json:"message"`
	Payload map[string]any `json:"payload"`
}

func (ta *testApp) activity(t *testing.T, woID string) []activityEvent {
	t.Helper()
	_, body := ta.do(t, "GET", "/api/v1/work-orders/"+woID+"/activity", "")
	var out struct {
		Events []activityEvent `json:"events"`
	}
	json.Unmarshal([]byte(body), &out)
	return out.Events
}

func (ta *testApp) eventKinds(t *testing.T, woID string) []string {
	t.Helper()
	evs := ta.activity(t, woID)
	kinds := make([]string, 0, len(evs))
	for _, e := range evs {
		kinds = append(kinds, e.Kind+": "+e.Message)
	}
	return kinds
}

func hasEvent(events []string, kind string) bool {
	for _, e := range events {
		if strings.HasPrefix(e, kind+":") {
			return true
		}
	}
	return false
}

// scriptedJudge answers like a Jev that finds nothing wrong, optionally
// sending the first plan back or asking for one append at the converge gate. It is a
// fake: the real TypeSafe API is never called from tests.
type scriptedJudge struct {
	mu          sync.Mutex
	revisePlan  bool
	fixDone     bool
	gapValid    bool
	planCalls   int
	doneCalls   int
	lastRequest []byte
}

func (j *scriptedJudge) Ask(_ context.Context, state any, qs map[string]usecase.Noul) (usecase.JudgeResult, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	answers := map[string]float64{}
	for id := range qs {
		answers[id] = 0.05
		if strings.HasPrefix(id, "covers_") || strings.HasPrefix(id, "met_") || strings.HasPrefix(id, "verified_") || id == "critical_resolved" {
			answers[id] = 0.95
		}
	}
	if _, plan := qs["scope_creep"]; plan {
		j.planCalls++
		if j.revisePlan && j.planCalls == 1 {
			answers["scope_creep"] = 0.9
		}
	} else {
		j.doneCalls++
		if j.fixDone && j.doneCalls == 1 {
			answers["met_c1"] = 0.1
		}
		for id := range qs {
			if j.gapValid && j.doneCalls == 1 && strings.HasPrefix(id, "gap_valid_") {
				answers[id] = 0.9
			}
		}
	}
	raw, _ := json.Marshal(map[string]any{"state": state, "questions": qs})
	j.lastRequest = raw
	return usecase.JudgeResult{Answers: answers, Model: "jev-fake", RequestedModel: "jev-fake", Request: raw}, nil
}

func TestForemanBriefsAndAnswersTheArchitect(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ta := boot(t)
	proj := ta.createProject(t, "Greeter", fixtureRepo(t))
	id := ta.createWOWith(t, proj, "[foreman-answer] Which greeting? Make the greeter greeting function available.")
	ta.plan(t, id)

	w := ta.workOrder(t, id)
	if w["state"] != "ARCHITECT_REVIEW" {
		t.Fatalf("the Foreman answer must not park the order: %v", w["state"])
	}
	if b, _ := w["brief"].(string); !strings.Contains(b, "Scripted brief") {
		t.Fatalf("brief not stored: %v", w["brief"])
	}
	if s, _ := w["spec"].(string); !strings.Contains(s, "Scripted spec") {
		t.Fatalf("spec not stored: %v", w["spec"])
	}
	if w["clarif_rounds"].(float64) != 1 || len(w["clarifications"].([]any)) != 1 {
		t.Fatalf("Foreman answer not recorded: %v", w)
	}
	ev := ta.eventKinds(t, id)
	for _, kind := range []string{"brief_ready", "clarification_asked", "clarification_answered", "plan_ready", "plan_reviewed", "plan_escalated"} {
		if !hasEvent(ev, kind) {
			t.Fatalf("missing %s event: %v", kind, ev)
		}
	}
	if hasEvent(ev, "clarification_requested") {
		t.Fatalf("a question the Foreman answered must not reach a human: %v", ev)
	}
	if hasEvent(ev, "plan_gate") {
		t.Fatalf("without Jev nothing is sent to the gate: %v", ev)
	}
}

func TestPlanGateRevisesOnceThenStartsTheBuildAndInvalidRepliesAreRetried(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ta := boot(t)
	judge := &scriptedJudge{revisePlan: true}
	ta.a.Server.Factory.Judge = judge
	proj := ta.createProject(t, "Greeter", fixtureRepo(t))

	// the gate sends the first plan back; the second is approved and built with no human click
	revise := ta.createWOWith(t, proj, "Make the greeter greeting function available.")
	if status, _ := ta.do(t, "POST", "/api/v1/work-orders/"+revise+"/plan/generate", ""); status != 202 {
		t.Fatal("plan generate")
	}
	ta.awaitState(t, revise, "NEEDS_HUMAN_REVIEW", 90*time.Second)
	if w := ta.workOrder(t, revise); w["plan_revisions"].(float64) != 1 {
		t.Fatalf("expected one gate revision, got %v", w["plan_revisions"])
	}
	var outcomes []string
	for _, e := range ta.activity(t, revise) {
		if e.Kind == "plan_gate" {
			if e.Actor != "jev" {
				t.Fatalf("gate actor %q", e.Actor)
			}
			outcomes = append(outcomes, fmt.Sprint(e.Payload["outcome"]))
		}
	}
	if strings.Join(outcomes, ",") != "revise,approve" {
		t.Fatalf("gate outcomes %v", outcomes)
	}
	if ev := ta.eventKinds(t, revise); !hasEvent(ev, "plan_revision_requested") || hasEvent(ev, "plan_escalated") {
		t.Fatalf("revision loop not visible in activity: %v", ev)
	}

	// an invalid Architect reply is rejected and retried once with the schema error
	invalid := ta.createWOWith(t, proj, "[invalid-reply] Make the greeter greeting function available.")
	if status, _ := ta.do(t, "POST", "/api/v1/work-orders/"+invalid+"/plan/generate", ""); status != 202 {
		t.Fatal("plan generate")
	}
	ta.awaitState(t, invalid, "NEEDS_HUMAN_REVIEW", 90*time.Second)
	if ev := ta.eventKinds(t, invalid); !hasEvent(ev, "agent_reply_invalid") {
		t.Fatalf("invalid reply should be rejected visibly: %v", ev)
	}
}

func TestJevGatedLifecycleNeedsNoHumanUntilThePRDecision(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ta := boot(t)
	judge := &scriptedJudge{fixDone: true}
	ta.a.Server.Factory.Judge = judge
	proj := ta.createProject(t, "Greeter", fixtureRepo(t))

	// governance snapshot: later default changes do not alter existing orders
	if status, body := ta.do(t, "PATCH", "/api/v1/settings", `{"foreman_model":"openai/gpt-5","architect_model":"openai/gpt-5-mini"}`); status != 200 {
		t.Fatalf("settings: %d %s", status, body)
	}
	// the autonomy field of older clients is accepted and ignored
	status, body := ta.do(t, "POST", "/api/v1/work-orders", fmt.Sprintf(
		`{"project_id":%q,"title":"Greeter","objective":"Make the greeter greeting function available.","acceptance":"TestGreet passes with go test ./...","autonomy":"guided"}`, proj))
	if status != 201 {
		t.Fatalf("create: %d %s", status, body)
	}
	id := decode[struct{ ID string }](t, body).ID
	ta.do(t, "PATCH", "/api/v1/settings", `{"foreman_model":"openai/other","architect_model":"openai/other"}`)
	if w := ta.workOrder(t, id); w["foreman_model"] != "openai/gpt-5" || w["architect_model"] != "openai/gpt-5-mini" {
		t.Fatalf("governance snapshot: %v / %v", w["foreman_model"], w["architect_model"])
	}
	if _, has := ta.workOrder(t, id)["autonomy"]; has {
		t.Fatal("autonomy levels are gone from the work order")
	}

	if status, _ := ta.do(t, "POST", "/api/v1/work-orders/"+id+"/plan/generate", ""); status != 202 {
		t.Fatal("plan generate")
	}
	ta.awaitState(t, id, "NEEDS_HUMAN_REVIEW", 120*time.Second)

	evs := ta.activity(t, id)
	var gates []string
	for _, e := range evs {
		switch {
		case e.Kind == "plan_gate" || e.Kind == "converge_gate":
			if e.Actor != "jev" || e.Payload["request"] == nil || e.Payload["row"] == nil {
				t.Fatalf("a gate event carries its actor, exact request and table row: %+v", e)
			}
			gates = append(gates, e.Kind+":"+fmt.Sprint(e.Payload["outcome"]))
		case e.Kind == "state_changed" && e.Payload["to"] == "QUEUED":
			if e.Actor != "factory" {
				t.Fatalf("the plan gate, not a human, queued the build: %q", e.Actor)
			}
		case e.Kind == "plan_approved" || e.Kind == "pr_approved":
			t.Fatalf("no human decision was needed: %+v", e)
		}
	}
	if strings.Join(gates, ",") != "plan_gate:approve,converge_gate:append,converge_gate:converged" {
		t.Fatalf("gate sequence %v", gates)
	}
	kinds := ta.eventKinds(t, id)
	for _, kind := range []string{"converge_report", "converge_append", "fix_requested", "readiness_ready", "plan_reviewed"} {
		if !hasEvent(kinds, kind) {
			t.Fatalf("missing %s: %v", kind, kinds)
		}
	}
	if w := ta.workOrder(t, id); w["revision"].(float64) != 2 {
		t.Fatalf("blocker fix + converge append = revision 2, got %v", w["revision"])
	}
	if !strings.Contains(string(judge.lastRequest), "greeter/greeter.go") {
		t.Fatalf("the converge gate must send the diff: %.300s", judge.lastRequest)
	}

	// specialists are chosen from the changed files and tag their findings
	picked := ""
	for _, e := range kinds {
		if strings.HasPrefix(e, "inspection_specialists:") {
			picked = e
		}
	}
	if !strings.Contains(picked, "testing") || !strings.Contains(picked, "backend") {
		t.Fatalf("expected testing + backend specialists for a Go change: %q", picked)
	}
	_, body = ta.do(t, "GET", "/api/v1/work-orders/"+id+"/findings", "")
	findings := decode[struct {
		Findings []struct {
			ID        string `json:"id"`
			Specialty string `json:"specialty"`
		} `json:"findings"`
	}](t, body).Findings
	if len(findings) == 0 || findings[0].Specialty != "testing" {
		t.Fatalf("findings must carry their specialty: %+v", findings)
	}

	// dispositions: a dispute needs a reason, and is recorded
	fid := findings[0].ID
	if status, _ := ta.do(t, "POST", "/api/v1/work-orders/"+id+"/findings/"+fid+"/decision", `{"action":"dispute"}`); status != 400 {
		t.Fatalf("dispute without a note must be rejected, got %d", status)
	}
	status, body = ta.do(t, "POST", "/api/v1/work-orders/"+id+"/findings/"+fid+"/decision", `{"action":"dispute","note":"scripted mock regression"}`)
	if status != 200 || !strings.Contains(body, `"status":"disputed"`) || !strings.Contains(body, `"decided_by":"usr-test"`) {
		t.Fatalf("dispute: %d %s", status, body)
	}
	if status, _ := ta.do(t, "POST", "/api/v1/work-orders/"+id+"/findings/nope/decision", `{"action":"confirm"}`); status != 404 {
		t.Fatalf("unknown finding: %d", status)
	}

	// the settings page learns that Jev is on
	if _, body := ta.do(t, "GET", "/api/v1/settings", ""); !strings.Contains(body, `"jev_enabled":false`) {
		t.Fatalf("jev_enabled mirrors TYPESAFE_API_KEY, unset in tests: %s", body)
	}
}

func TestManualPauseHoldsAnOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ta := boot(t)
	proj := ta.createProject(t, "Greeter", fixtureRepo(t))

	manual := ta.createWOWith(t, proj, "Make the greeter greeting function available.")
	ta.plan(t, manual)
	if status, _ := ta.do(t, "POST", "/api/v1/work-orders/"+manual+"/resume", ""); status != 409 {
		t.Fatalf("resume of a running order must conflict, got %d", status)
	}
	if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+manual+"/pause", `{"reason":"checking scope"}`); status != 202 {
		t.Fatalf("pause: %d %s", status, body)
	}
	if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+manual+"/plan/decisions", `{"decision":"approve"}`); status != 202 {
		t.Fatalf("approving while paused is a human decision and stays allowed: %d %s", status, body)
	}
	time.Sleep(1500 * time.Millisecond)
	if w := ta.workOrder(t, manual); w["state"] != "QUEUED" || w["pause_reason"] != "checking scope" {
		t.Fatalf("paused order must not progress: state=%v reason=%v", w["state"], w["pause_reason"])
	}
	if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+manual+"/resume", ""); status != 202 {
		t.Fatalf("resume: %d %s", status, body)
	}
	ta.awaitState(t, manual, "NEEDS_HUMAN_REVIEW", 60*time.Second)
}

func TestConvergeGapAppendsATaskEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ta := boot(t)
	ta.a.Server.Factory.Judge = &scriptedJudge{gapValid: true}
	proj := ta.createProject(t, "Greeter", fixtureRepo(t))
	id := ta.createWOWith(t, proj, "[converge-gap] Make the greeter greeting function available.")
	if status, _ := ta.do(t, "POST", "/api/v1/work-orders/"+id+"/plan/generate", ""); status != 202 {
		t.Fatal("plan generate")
	}
	ta.awaitState(t, id, "NEEDS_HUMAN_REVIEW", 120*time.Second)
	var gates []string
	for _, e := range ta.activity(t, id) {
		if e.Kind == "converge_gate" {
			gates = append(gates, fmt.Sprint(e.Payload["outcome"]))
		}
	}
	if strings.Join(gates, ",") != "append,converged" {
		t.Fatalf("converge gates %v", gates)
	}
	if kinds := ta.eventKinds(t, id); !hasEvent(kinds, "converge_append") {
		t.Fatalf("missing converge_append: %v", kinds)
	}
}
