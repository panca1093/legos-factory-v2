package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func (ta *testApp) createWOWith(t *testing.T, projectID, objective, autonomy string) string {
	t.Helper()
	status, body := ta.do(t, "POST", "/api/v1/work-orders", fmt.Sprintf(
		`{"project_id":%q,"title":"Greeter","objective":%q,"acceptance":"TestGreet passes with go test ./...","autonomy":%q}`,
		projectID, objective, autonomy))
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

func (ta *testApp) eventKinds(t *testing.T, woID string) []string {
	t.Helper()
	_, body := ta.do(t, "GET", "/api/v1/work-orders/"+woID+"/activity", "")
	var out struct {
		Events []struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		} `json:"events"`
	}
	json.Unmarshal([]byte(body), &out)
	kinds := make([]string, 0, len(out.Events))
	for _, e := range out.Events {
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

func TestForemanReviewRevisesClarifiesAndRetriesInvalidReplies(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ta := boot(t)
	proj := ta.createProject(t, "Greeter", fixtureRepo(t))

	// REVISE once: the Architect re-drafts, the Foreman then approves
	revise := ta.createWOWith(t, proj, "[foreman-revise] Make the greeter greeting function available.", "assisted")
	ta.plan(t, revise)
	if w := ta.workOrder(t, revise); w["review_rounds"].(float64) != 1 {
		t.Fatalf("expected one Foreman revision round, got %v", w["review_rounds"])
	}
	if ev := ta.eventKinds(t, revise); !hasEvent(ev, "plan_revision_requested") || !hasEvent(ev, "plan_reviewed") {
		t.Fatalf("revision loop not visible in activity: %v", ev)
	}

	// CLARIFY: ambiguity goes to the human, the answer produces a reviewed plan
	clarify := ta.createWOWith(t, proj, "[foreman-clarify] Make the greeter greeting function available.", "assisted")
	if status, _ := ta.do(t, "POST", "/api/v1/work-orders/"+clarify+"/plan/generate", ""); status != 202 {
		t.Fatal("plan generate")
	}
	doc := ta.awaitState(t, clarify, "WAITING_FOR_HUMAN", 30*time.Second)
	if q := doc["work_order"].(map[string]any)["clarif_question"]; q != "Which retry window should apply?" {
		t.Fatalf("Foreman question not surfaced: %v", q)
	}
	if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+clarify+"/plan/decisions", `{"decision":"approve"}`); status != 409 {
		t.Fatalf("a plan waiting for clarification must not be approvable: %d %s", status, body)
	}
	if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+clarify+"/clarifications", `{"answer":"24 hours"}`); status != 202 {
		t.Fatalf("clarify: %d %s", status, body)
	}
	if plan := ta.awaitReviewed(t, clarify)["plan"].([]any); len(plan) == 0 {
		t.Fatal("clarified order must have a plan")
	}

	// an invalid Architect reply is rejected and retried once with the schema error
	invalid := ta.createWOWith(t, proj, "[invalid-reply] Make the greeter greeting function available.", "assisted")
	ta.plan(t, invalid)
	if ev := ta.eventKinds(t, invalid); !hasEvent(ev, "agent_reply_invalid") {
		t.Fatalf("invalid reply should be rejected visibly: %v", ev)
	}
}

func TestAutonomyPauseResumeSpecialistsAndGovernance(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ta := boot(t)
	proj := ta.createProject(t, "Greeter", fixtureRepo(t))

	// governance snapshot: later default changes do not alter existing orders
	if status, body := ta.do(t, "PUT", "/api/v1/settings", `{"foreman_model":"openai/gpt-5","architect_model":"openai/gpt-5-mini"}`); status != 200 {
		t.Fatalf("settings: %d %s", status, body)
	}
	delegated := ta.createWOWith(t, proj, "Make the greeter greeting function available.", "delegated")
	ta.do(t, "PUT", "/api/v1/settings", `{"foreman_model":"openai/other","architect_model":"openai/other"}`)
	if w := ta.workOrder(t, delegated); w["foreman_model"] != "openai/gpt-5" || w["architect_model"] != "openai/gpt-5-mini" {
		t.Fatalf("governance snapshot: %v / %v", w["foreman_model"], w["architect_model"])
	}

	// delegated: execution starts after the Foreman review with no human plan approval
	if status, _ := ta.do(t, "POST", "/api/v1/work-orders/"+delegated+"/plan/generate", ""); status != 202 {
		t.Fatal("plan generate")
	}
	ta.awaitState(t, delegated, "NEEDS_HUMAN_REVIEW", 60*time.Second)
	if ev := ta.eventKinds(t, delegated); !hasEvent(ev, "plan_reviewed") {
		t.Fatalf("delegated run must still be Foreman-reviewed: %v", ev)
	}

	// specialists are chosen from the changed files and tag their findings
	ev := ta.eventKinds(t, delegated)
	picked := ""
	for _, e := range ev {
		if strings.HasPrefix(e, "inspection_specialists:") {
			picked = e
		}
	}
	if !strings.Contains(picked, "testing") || !strings.Contains(picked, "backend") {
		t.Fatalf("expected testing + backend specialists for a Go change: %q", picked)
	}
	_, body := ta.do(t, "GET", "/api/v1/work-orders/"+delegated+"/findings", "")
	findings := decode[struct {
		Findings []struct {
			ID        string `json:"id"`
			Severity  string `json:"severity"`
			Specialty string `json:"specialty"`
			Status    string `json:"status"`
		} `json:"findings"`
	}](t, body).Findings
	if len(findings) == 0 || findings[0].Specialty != "testing" {
		t.Fatalf("findings must carry their specialty: %+v", findings)
	}

	// dispositions: a dispute needs a reason, and is recorded
	fid := findings[0].ID
	if status, _ := ta.do(t, "POST", "/api/v1/work-orders/"+delegated+"/findings/"+fid+"/decision", `{"action":"dispute"}`); status != 400 {
		t.Fatalf("dispute without a note must be rejected, got %d", status)
	}
	status, body := ta.do(t, "POST", "/api/v1/work-orders/"+delegated+"/findings/"+fid+"/decision", `{"action":"dispute","note":"scripted mock regression"}`)
	if status != 200 || !strings.Contains(body, `"status":"disputed"`) || !strings.Contains(body, `"decided_by":"usr-test"`) {
		t.Fatalf("dispute: %d %s", status, body)
	}
	if status, _ := ta.do(t, "POST", "/api/v1/work-orders/"+delegated+"/findings/nope/decision", `{"action":"confirm"}`); status != 404 {
		t.Fatalf("unknown finding: %d", status)
	}

	// guided: every build step pauses until a human resumes
	guided := ta.createWOWith(t, proj, "Make the greeter greeting function available.", "guided")
	ta.plan(t, guided)
	if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+guided+"/plan/decisions", `{"decision":"approve"}`); status != 202 {
		t.Fatalf("approve: %d %s", status, body)
	}
	resumes := 0
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		w := ta.workOrder(t, guided)
		if w["state"] == "NEEDS_HUMAN_REVIEW" {
			break
		}
		if w["paused_at"] != nil {
			if !strings.Contains(fmt.Sprint(w["pause_reason"]), "Guided") {
				t.Fatalf("unexpected pause reason: %v", w["pause_reason"])
			}
			if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+guided+"/resume", ""); status != 202 {
				t.Fatalf("resume: %d %s", status, body)
			}
			resumes++
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ta.workOrder(t, guided)["state"] != "NEEDS_HUMAN_REVIEW" || resumes < 4 {
		t.Fatalf("guided order should finish after a human resumed each step; resumes=%d", resumes)
	}

	// manual pause holds an assisted order until resume
	manual := ta.createWOWith(t, proj, "Make the greeter greeting function available.", "assisted")
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
