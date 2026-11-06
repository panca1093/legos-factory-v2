package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

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
		`{"project_id":%q,"title":%q,"objective":%q,"acceptance":"TestGreet passes with go test ./..."}`,
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

func (ta *testApp) projectLog(projectID string) string {
	data, _ := os.ReadFile(filepath.Join(ta.a.Server.Catalog.Bundle.Root(), "projects", projectID, "log.md"))
	return string(data)
}

func awaitLogLine(t *testing.T, ta *testApp, projectID, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, l := range strings.Split(ta.projectLog(projectID), "\n") {
			if strings.Contains(l, want) {
				return l
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("no log line containing %q in:\n%s", want, ta.projectLog(projectID))
	return ""
}

func TestCatalogerJournalIsAProjectLog(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := fixtureRepo(t)
	dataDir := t.TempDir()
	ta := bootAt(t, dataDir)

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

	// the journal is one line per record in projects/<pid>/log.md
	awaitLogLine(t, ta, p1, "`"+wo1+"` **work_order_completed** (cataloger)")
	log := ta.projectLog(p1)
	if !strings.HasPrefix(log, "# Log\n\n## ") {
		t.Fatalf("project log must be an OKF log:\n%s", log)
	}
	for kind, actor := range map[string]string{
		"architect_review": "architect", "plan_approved": "human", "findings": "inspector",
		"readiness": "factory", "pr_decision": "human", "work_order_completed": "cataloger",
	} {
		if !strings.Contains(log, "`"+wo1+"` **"+kind+"** ("+actor+")") {
			t.Fatalf("missing %s line:\n%s", kind, log)
		}
	}
	if n := strings.Count(log, "**work_order_completed**"); n != 1 {
		t.Fatalf("want exactly one task summary line, got %d:\n%s", n, log)
	}
	for _, kind := range []string{"verification", "implementation"} {
		if strings.Contains(log, "**"+kind+"**") {
			t.Fatalf("no per-revision %s lines expected:\n%s", kind, log)
		}
	}
	if !strings.Contains(log, "Task summary: Greeter greeting (READY) — Goal: Greeter greeting") {
		t.Fatalf("completion line must carry the summary:\n%s", log)
	}
	projDir := filepath.Join(ta.a.Server.Catalog.Bundle.Root(), "projects", p1)
	if kn, _ := filepath.Glob(filepath.Join(projDir, "*", "kn_*.md")); len(kn) != 0 {
		t.Fatalf("no kn_ record files may be written: %v", kn)
	}
	if _, err := os.Stat(filepath.Join(projDir, "journal")); !os.IsNotExist(err) {
		t.Fatalf("no journal directory may be created: %v", err)
	}
	if root, _ := os.ReadFile(filepath.Join(ta.a.Server.Catalog.Bundle.Root(), "log.md")); strings.Contains(string(root), "pr_decision") {
		t.Fatalf("project log lines must not reach the root log:\n%s", root)
	}

	// decisions the summary reads are events
	evs := ta.eventKinds(t, wo1)
	for _, kind := range []string{"plan_approved", "pr_approved", "verification_started", "verification_passed"} {
		if !hasEvent(evs, kind) {
			t.Fatalf("run log must keep %s; have %v", kind, evs)
		}
	}
	if !hasEvent(evs, "check_pass") && !hasEvent(evs, "check_passed") {
		t.Fatalf("run log must keep per-check events; have %v", evs)
	}
	if status, body := ta.do(t, "GET", "/api/v1/work-orders/"+wo1+"/quality", ""); status != 200 || !strings.Contains(body, `"commit_sha":"`+woSHA+`"`) {
		t.Fatalf("final-revision checks must be stamped with the commit: %d %s", status, tail(body, 600))
	}

	// the record endpoints are gone
	for _, path := range []string{"/api/v1/knowledge", "/api/v1/knowledge/kn_x"} {
		if status, _ := ta.do(t, "GET", path, ""); status != 404 {
			t.Fatalf("GET %s: want 404, got %d", path, status)
		}
	}

	// project and workspace boundaries
	eng := ta.loginAs(t, "eng@example.com", "Eng")
	for _, path := range []string{"/api/v1/projects/" + p1, "/api/v1/work-orders/" + wo1, "/api/v1/work-orders/" + wo1 + "/activity"} {
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

	// the log survives a restart
	before := ta.projectLog(p1)
	ta.stop()
	ta2 := bootAt(t, dataDir)
	if after := ta2.projectLog(p1); after != before {
		t.Fatalf("project log changed across restart:\n%s\n---\n%s", before, after)
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

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}
