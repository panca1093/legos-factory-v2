package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type knowledgeRecord struct {
	ID         string   `json:"id"`
	ProjectID  string   `json:"project_id"`
	WOID       string   `json:"work_order_id"`
	CommitSHA  string   `json:"commit_sha"`
	RepoURL    string   `json:"repo_url"`
	Kind       string   `json:"kind"`
	Trust      string   `json:"trust"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	SourceRefs []string `json:"source_refs"`
}

var sha40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (ta *testApp) createProject(t *testing.T, name, repo string) string {
	t.Helper()
	status, body := ta.do(t, "POST", "/api/v1/projects", fmt.Sprintf(`{"name":%q,"repo_url":%q,"language":"go"}`, name, repo))
	if status != 201 {
		t.Fatalf("create project: %d %s", status, body)
	}
	var p struct{ ID string }
	json.Unmarshal([]byte(body), &p)
	return p.ID
}

func (ta *testApp) createWO(t *testing.T, projectID, title, objective string) string {
	t.Helper()
	status, body := ta.do(t, "POST", "/api/v1/work-orders", fmt.Sprintf(
		`{"project_id":%q,"title":%q,"objective":%q,"acceptance":"TestGreet passes with go test ./...","autonomy":"assisted"}`,
		projectID, title, objective))
	if status != 201 {
		t.Fatalf("create work order: %d %s", status, body)
	}
	var wo struct{ ID string }
	json.Unmarshal([]byte(body), &wo)
	return wo.ID
}

func (ta *testApp) plan(t *testing.T, woID string) {
	t.Helper()
	if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+woID+"/plan/generate", ""); status != 202 {
		t.Fatalf("plan generate: %d %s", status, body)
	}
	ta.awaitReviewed(t, woID)
}

func (ta *testApp) knowledge(t *testing.T, session, query string) (int, []knowledgeRecord) {
	t.Helper()
	status, body := ta.doAs(t, session, "GET", "/api/v1/knowledge"+query, "")
	var out struct {
		Records []knowledgeRecord `json:"records"`
	}
	if status == 200 {
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("knowledge json: %v %s", err, body)
		}
	}
	return status, out.Records
}

func byKind(records []knowledgeRecord) map[string][]knowledgeRecord {
	m := map[string][]knowledgeRecord{}
	for _, k := range records {
		m[k.Kind] = append(m[k.Kind], k)
	}
	return m
}

func TestCatalogerKnowledgeLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := fixtureRepo(t)
	dataDir := t.TempDir()
	ta := bootAt(t, dataDir)

	if status, body := ta.do(t, "PUT", "/api/v1/settings", `{"cataloger_summaries":true}`); status != 200 {
		t.Fatalf("enable summaries: %d %s", status, body)
	}

	p1 := ta.createProject(t, "Greeter", repo)
	wo1 := ta.createWO(t, p1, "Greeter greeting", "Make the greeter greeting function available so the fixture contract test passes.")
	ta.plan(t, wo1)
	if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+wo1+"/plan/decisions", `{"decision":"approve"}`); status != 202 {
		t.Fatalf("approve: %d %s", status, body)
	}
	ta.awaitState(t, wo1, "NEEDS_HUMAN_REVIEW", 60*time.Second)
	if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+wo1+"/pr-decision", `{"decision":"approve","reason":"evidence complete"}`); status != 202 {
		t.Fatalf("pr decision: %d %s", status, body)
	}
	doc := ta.awaitState(t, wo1, "READY", 5*time.Second)
	woSHA, _ := doc["work_order"].(map[string]any)["commit_sha"].(string)
	if !sha40.MatchString(woSHA) {
		t.Fatalf("work order must carry the verified local commit, got %q", woSHA)
	}

	// the commit exists in the isolated workspace and was never pushed to the source repo
	ws := filepath.Join(dataDir, "workspaces", wo1)
	if head := gitOut(t, ws, "rev-parse", "HEAD"); head != woSHA {
		t.Fatalf("workspace HEAD %s != recorded %s", head, woSHA)
	}
	if origin := gitOut(t, repo, "rev-parse", "HEAD"); origin == woSHA {
		t.Fatal("verified commit must not reach the source repository")
	}

	// DoD 1 + 2: durable records linked to the work order, commit and decisions
	_, recs := ta.knowledge(t, ta.session, "?project_id="+p1+"&work_order_id="+wo1)
	kinds := byKind(recs)
	for kind, trust := range map[string]string{
		"architect_review": "ai_interpretation", "plan_approved": "human_decision", "implementation": "ai_interpretation",
		"verification": "verified", "findings": "ai_interpretation", "readiness": "verified",
		"pr_decision": "human_decision", "work_order_completed": "verified",
	} {
		if len(kinds[kind]) == 0 {
			t.Fatalf("missing %s record; have %v", kind, keys(kinds))
		}
		if kinds[kind][0].Trust != trust {
			t.Fatalf("%s trust = %s, want %s", kind, kinds[kind][0].Trust, trust)
		}
	}
	for _, k := range recs {
		if k.WOID != wo1 || k.ProjectID != p1 || k.RepoURL != repo {
			t.Fatalf("record not linked to its origin: %+v", k)
		}
	}
	done := kinds["work_order_completed"][0]
	if done.CommitSHA != woSHA || len(done.SourceRefs) < 7 {
		t.Fatalf("completion must link the commit and prior records: %+v", done)
	}
	if pr := kinds["pr_decision"][0]; pr.CommitSHA != woSHA || !contains(pr.SourceRefs, "pr:local:"+wo1+"@"+woSHA) {
		t.Fatalf("PR decision must reference the local PR and commit: %+v", pr)
	}
	passed := false
	for _, v := range kinds["verification"] {
		if v.CommitSHA == woSHA && strings.Contains(v.Body, "PASS") {
			passed = true
		}
		if strings.Contains(v.Body, "--- FAIL") || len(v.Body) > 2100 {
			t.Fatalf("verification record must not store raw execution logs: %q", v.Body)
		}
	}
	if !passed {
		t.Fatalf("expected a verified PASS record at %s: %+v", woSHA, kinds["verification"])
	}
	if status, body := ta.do(t, "GET", "/api/v1/work-orders/"+wo1+"/quality", ""); status != 200 || !strings.Contains(body, `"commit_sha":"`+woSHA+`"`) {
		t.Fatalf("final-revision checks must be stamped with the commit: %d %s", status, tail(body, 600))
	}

	// optional AI summary: one run, non-authoritative, citing its sources
	summary := awaitKnowledge(t, ta, "?work_order_id="+wo1+"&trust=ai_interpretation", "summary")
	if !strings.Contains(summary.Title, "AI-generated") || !hasPrefix(summary.SourceRefs, "knowledge:") || !hasPrefix(summary.SourceRefs, "run:") {
		t.Fatalf("summary must be labelled and cite sources: %+v", summary)
	}

	// DoD 3: a later work order in the same project retrieves that knowledge during planning
	wo2 := ta.createWO(t, p1, "Greeter greeting follow-up", "Extend the greeter greeting function for the fixture contract test.")
	ta.plan(t, wo2)
	refs := retrievedRefs(t, ta, wo2)
	ids := map[string]bool{}
	for _, k := range recs {
		ids["knowledge:"+k.ID] = true
	}
	hit := false
	for _, r := range refs {
		hit = hit || ids[r]
	}
	if !hit {
		t.Fatalf("second work order should retrieve work order 1 knowledge, got refs %v", refs)
	}

	// a different project never sees it
	p2 := ta.createProject(t, "Other", fixtureRepo(t))
	wo3 := ta.createWO(t, p2, "Greeter greeting elsewhere", "Make the greeter greeting function available so the fixture contract test passes.")
	ta.plan(t, wo3)
	if refs := retrievedRefs(t, ta, wo3); len(refs) != 0 {
		t.Fatalf("knowledge leaked across projects: %v", refs)
	}

	// DoD 4: project and workspace boundaries
	eng := ta.loginAs(t, "eng@example.com", "Eng")
	if status, recs := ta.knowledge(t, eng, ""); status != 200 || len(recs) != 0 {
		t.Fatalf("non-member must see no knowledge: %d %d", status, len(recs))
	}
	for _, path := range []string{
		"/api/v1/knowledge?project_id=" + p1, "/api/v1/knowledge/" + done.ID,
		"/api/v1/projects/" + p1, "/api/v1/work-orders/" + wo1, "/api/v1/work-orders/" + wo1 + "/activity",
	} {
		if status, body := ta.doAs(t, eng, "GET", path, ""); status != 404 {
			t.Fatalf("non-member GET %s: want 404, got %d %s", path, status, body)
		}
	}
	if status, body := ta.doAs(t, eng, "GET", "/api/v1/work-orders", ""); status != 200 || strings.Contains(body, wo1) {
		t.Fatalf("non-member work order list must be empty: %d %s", status, body)
	}
	if status, _ := ta.doAs(t, eng, "POST", "/api/v1/work-orders", fmt.Sprintf(`{"project_id":%q,"title":"x","objective":"y"}`, p1)); status != 400 {
		t.Fatalf("non-member must not create work in p1, got %d", status)
	}
	if status, _ := ta.doAs(t, eng, "POST", "/api/v1/projects/"+p1+"/members", `{"email":"eng@example.com"}`); status != 403 {
		t.Fatalf("engineer must not manage membership, got %d", status)
	}
	if status, body := ta.do(t, "POST", "/api/v1/projects/"+p1+"/members", `{"email":"eng@example.com"}`); status != 200 {
		t.Fatalf("owner adds member: %d %s", status, body)
	}
	_, visible := ta.knowledge(t, eng, "")
	if len(visible) == 0 {
		t.Fatal("member should now see p1 knowledge")
	}
	for _, k := range visible {
		if k.ProjectID != p1 {
			t.Fatalf("member of p1 saw %s knowledge", k.ProjectID)
		}
	}
	if status, _ := ta.doAs(t, eng, "GET", "/api/v1/knowledge?project_id="+p2, ""); status != 404 {
		t.Fatalf("p2 knowledge must stay hidden, got %d", status)
	}

	// DoD 5: knowledge survives a restart
	_, before := ta.knowledge(t, ta.session, "?project_id="+p1)
	ta.stop()
	ta2 := bootAt(t, dataDir)
	_, after := ta2.knowledge(t, ta2.session, "?project_id="+p1)
	if len(after) == 0 || len(after) != len(before) {
		t.Fatalf("knowledge lost across restart: before %d after %d", len(before), len(after))
	}
	if _, found := ta2.knowledge(t, ta2.session, "?project_id="+p1+"&q=greeter"); len(found) == 0 {
		t.Fatal("keyword search must work after restart")
	}
}

func (ta *testApp) loginAs(t *testing.T, email, name string) string {
	t.Helper()
	ctx := context.Background()
	u, err := ta.a.Server.Auth.UpsertUser(ctx, email, name)
	if err != nil {
		t.Fatal(err)
	}
	sid, _, err := ta.a.Server.Auth.Login(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

func retrievedRefs(t *testing.T, ta *testApp, woID string) []string {
	t.Helper()
	status, body := ta.do(t, "GET", "/api/v1/work-orders/"+woID+"/activity", "")
	if status != 200 {
		t.Fatalf("activity: %d %s", status, body)
	}
	var out struct {
		Events []struct {
			Kind    string         `json:"kind"`
			Payload map[string]any `json:"payload"`
		} `json:"events"`
	}
	json.Unmarshal([]byte(body), &out)
	var refs []string
	for _, e := range out.Events {
		if e.Kind != "knowledge_retrieved" {
			continue
		}
		list, _ := e.Payload["refs"].([]any)
		for _, r := range list {
			refs = append(refs, fmt.Sprint(r))
		}
	}
	return refs
}

func awaitKnowledge(t *testing.T, ta *testApp, query, kind string) knowledgeRecord {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, recs := ta.knowledge(t, ta.session, query)
		for _, k := range recs {
			if k.Kind == kind {
				return k
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("no %s knowledge record for %s", kind, query)
	return knowledgeRecord{}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func hasPrefix(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func keys(m map[string][]knowledgeRecord) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
