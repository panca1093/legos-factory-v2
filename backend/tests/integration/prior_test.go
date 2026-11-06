package integration

import (
	"strings"
	"testing"
	"time"
)

func (ta *testApp) retrievedRefs(t *testing.T, woID string) []string {
	t.Helper()
	var refs []string
	for _, e := range ta.activity(t, woID) {
		if e.Kind == "knowledge_retrieved" {
			for _, r := range e.Payload["refs"].([]any) {
				refs = append(refs, r.(string))
			}
		}
	}
	return refs
}

func TestArchitectReceivesPriorWorkOrderSummaryFromSameProjectOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := fixtureRepo(t)
	ta := boot(t)

	p1 := ta.createProject(t, "Greeter", repo)
	p2 := ta.createProject(t, "Elsewhere", repo)
	const objective = "Make the greeter greeting function available so the fixture contract test passes."
	wo1 := ta.createWO(t, p1, "Greeter greeting", objective)
	ta.plan(t, wo1)
	if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+wo1+"/plan/decisions", `{"decision":"approve"}`); status != 202 {
		t.Fatalf("approve: %d %s", status, body)
	}
	ta.awaitState(t, wo1, "NEEDS_HUMAN_REVIEW", 60*time.Second)
	if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+wo1+"/pr-decision", `{"decision":"approve","reason":"evidence complete"}`); status != 202 {
		t.Fatalf("pr decision: %d %s", status, body)
	}
	ta.awaitState(t, wo1, "READY", 5*time.Second)
	awaitLogLine(t, ta, p1, "`"+wo1+"` **work_order_completed**")

	if refs := ta.retrievedRefs(t, wo1); len(refs) != 0 {
		t.Fatalf("the first work order has no prior knowledge, got %v", refs)
	}

	wo2 := ta.createWO(t, p1, "Greeter greeting again", objective)
	ta.plan(t, wo2)
	found := false
	for _, r := range ta.retrievedRefs(t, wo2) {
		if !strings.HasPrefix(r, "kb:factory:"+p1+":projects/"+p1+"/") {
			t.Fatalf("reference outside the project: %q", r)
		}
		found = found || strings.HasSuffix(r, "-summary.md")
	}
	if !found {
		t.Fatalf("work order 2 must receive work order 1's summary, got %v", ta.retrievedRefs(t, wo2))
	}

	wo3 := ta.createWO(t, p2, "Greeter greeting again", objective)
	ta.plan(t, wo3)
	if refs := ta.retrievedRefs(t, wo3); len(refs) != 0 {
		t.Fatalf("another project must not receive %s's documents: %v", p1, refs)
	}
}
