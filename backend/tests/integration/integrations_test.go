package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const ghToken = "ghp_e2e_secret_token_value"

// fakeGitHub serves the GitHub REST endpoints the connector uses and records
// pull-request creations.
type fakeGitHub struct {
	mu      sync.Mutex
	created []map[string]any
}

func (f *fakeGitHub) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+ghToken {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/user":
			json.NewEncoder(w).Encode(map[string]string{"login": "factory-bot"})
		case r.Method == "GET" && r.URL.Path == "/repos/acme/greeter/pulls":
			if len(f.created) == 0 {
				w.Write([]byte(`[]`))
				return
			}
			json.NewEncoder(w).Encode([]map[string]any{{"number": 5, "html_url": "https://github.example/acme/greeter/pull/5"}})
		case r.Method == "POST" && r.URL.Path == "/repos/acme/greeter/pulls":
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			json.Unmarshal(raw, &body)
			f.created = append(f.created, body)
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"number": 5, "html_url": "https://github.example/acme/greeter/pull/5"})
		default:
			t.Errorf("unexpected GitHub call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeGitHub) prs() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.created...)
}

func fakeJira(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "pm@acme.io" || pass != "jira_secret" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/rest/api/3/myself":
			json.NewEncoder(w).Encode(map[string]string{"emailAddress": "pm@acme.io"})
		case "/rest/api/3/issue/PAY-7":
			json.NewEncoder(w).Encode(map[string]any{"key": "PAY-7", "fields": map[string]any{
				"summary":     "Greeter greeting",
				"description": "Make the greeter greeting function available so the fixture contract test passes.\nAcceptance criteria:\nTestGreet passes with go test ./...",
			}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func decode[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %T: %v: %s", v, err, body)
	}
	return v
}

func TestIntegrationsPullRequestAndJiraImport(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	gh := &fakeGitHub{}
	ghSrv := gh.server(t)
	jiraSrv := fakeJira(t)
	repo := fixtureRepo(t)
	dataDir := t.TempDir()
	ta := bootAt(t, dataDir)
	eng := ta.loginAs(t, "eng@example.com", "Eng")

	// connections: shared GitHub (admin), personal Jira; credentials never returned
	status, body := ta.do(t, "POST", "/api/v1/connections", fmt.Sprintf(
		`{"provider":"github","name":"Acme GitHub","scope":"shared","base_url":%q,"token":%q}`, ghSrv.URL, ghToken))
	if status != 201 || strings.Contains(body, ghToken) {
		t.Fatalf("create github connection: %d %s", status, body)
	}
	ghConn := decode[struct {
		ID            string `json:"id"`
		HasCredential bool   `json:"has_credential"`
	}](t, body)
	if !ghConn.HasCredential {
		t.Fatal("connection must report a stored credential")
	}
	status, body = ta.do(t, "POST", "/api/v1/connections/"+ghConn.ID+"/test", "")
	if status != 200 || !strings.Contains(body, `"account":"factory-bot"`) || !strings.Contains(body, `"verified_at"`) {
		t.Fatalf("test connection: %d %s", status, body)
	}
	status, body = ta.do(t, "POST", "/api/v1/connections", fmt.Sprintf(
		`{"provider":"jira","name":"My Jira","scope":"personal","base_url":%q,"username":"pm@acme.io","token":"jira_secret"}`, jiraSrv.URL))
	if status != 201 {
		t.Fatalf("create jira connection: %d %s", status, body)
	}
	jiraConn := decode[struct{ ID string }](t, body)

	// role and ownership rules
	if status, _ := ta.doAs(t, eng, "POST", "/api/v1/connections",
		`{"provider":"github","name":"x","scope":"shared","token":"t"}`); status != 403 {
		t.Fatalf("engineer must not create shared connections, got %d", status)
	}
	if status, body := ta.doAs(t, eng, "GET", "/api/v1/connections", ""); status != 200 || strings.Contains(body, jiraConn.ID) || !strings.Contains(body, ghConn.ID) {
		t.Fatalf("engineer sees shared but not others' personal connections: %d %s", status, body)
	}
	if status, _ := ta.doAs(t, eng, "POST", "/api/v1/integrations/jira/lookup",
		fmt.Sprintf(`{"connection_id":%q,"key":"PAY-7"}`, jiraConn.ID)); status != 404 {
		t.Fatalf("someone else's personal connection must be unusable, got %d", status)
	}
	if status, _ := ta.doAs(t, eng, "POST", "/api/v1/connections/"+ghConn.ID+"/test", ""); status != 403 {
		t.Fatalf("engineer must not manage shared connections, got %d", status)
	}
	if status, _ := ta.do(t, "POST", "/api/v1/connections",
		`{"provider":"github","name":"leak","scope":"shared","base_url":"http://example.com","token":"t"}`); status != 400 {
		t.Fatalf("non-loopback http base URL must be rejected, got %d", status)
	}

	// credentials are sealed at rest
	for _, name := range []string{"factory.db", "factory.db-wal"} {
		raw, _ := os.ReadFile(filepath.Join(dataDir, name))
		if strings.Contains(string(raw), ghToken) || strings.Contains(string(raw), "jira_secret") {
			t.Fatalf("plaintext credential found in %s", name)
		}
	}

	// Jira import → draft with an acceptance section and a source reference
	status, body = ta.do(t, "POST", "/api/v1/integrations/jira/lookup", fmt.Sprintf(`{"connection_id":%q,"key":"pay-7"}`, jiraConn.ID))
	if status != 200 {
		t.Fatalf("jira lookup: %d %s", status, body)
	}
	draft := decode[struct {
		SourceRef  string `json:"source_ref"`
		Title      string `json:"title"`
		Objective  string `json:"objective"`
		Acceptance string `json:"acceptance"`
	}](t, body)
	if draft.SourceRef != "jira:PAY-7" || draft.Acceptance != "TestGreet passes with go test ./..." || strings.Contains(draft.Objective, "Acceptance") {
		t.Fatalf("bad draft: %+v", draft)
	}

	// project bound to the shared GitHub connection
	proj := ta.createProject(t, "Greeter", repo)
	if status, body := ta.do(t, "PUT", "/api/v1/projects/"+proj+"/binding",
		fmt.Sprintf(`{"git_connection_id":%q,"remote_repo":"acme/greeter"}`, jiraConn.ID)); status != 400 {
		t.Fatalf("personal/non-git connections must not bind: %d %s", status, body)
	}
	if status, body := ta.do(t, "PUT", "/api/v1/projects/"+proj+"/binding",
		fmt.Sprintf(`{"git_connection_id":%q,"remote_repo":"acme/greeter"}`, ghConn.ID)); status != 200 || !strings.Contains(body, `"remote_repo":"acme/greeter"`) {
		t.Fatalf("bind: %d %s", status, body)
	}

	// work order from the Jira draft, through to human approval
	status, body = ta.do(t, "POST", "/api/v1/work-orders", fmt.Sprintf(
		`{"project_id":%q,"title":%q,"objective":%q,"acceptance":%q,"autonomy":"assisted","source_ref":%q}`,
		proj, draft.Title, draft.Objective, draft.Acceptance, draft.SourceRef))
	if status != 201 || !strings.Contains(body, `"source_ref":"jira:PAY-7"`) {
		t.Fatalf("create work order: %d %s", status, body)
	}
	wo := decode[struct{ ID string }](t, body).ID
	ta.plan(t, wo)
	if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+wo+"/plan/decisions", `{"decision":"approve"}`); status != 202 {
		t.Fatalf("approve plan: %d %s", status, body)
	}
	ta.awaitState(t, wo, "NEEDS_HUMAN_REVIEW", 60*time.Second)
	if len(gh.prs()) != 0 {
		t.Fatal("nothing may be published before the human PR decision")
	}
	if status, body := ta.do(t, "POST", "/api/v1/work-orders/"+wo+"/pr-decision", `{"decision":"approve","reason":"ship it"}`); status != 202 {
		t.Fatalf("pr decision: %d %s", status, body)
	}

	// the approval pushes the verified commit and opens exactly one PR
	var doc map[string]any
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		doc = ta.awaitState(t, wo, "READY", 5*time.Second)
		if doc["work_order"].(map[string]any)["pr_url"] != nil {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	w := doc["work_order"].(map[string]any)
	if w["pr_url"] != "https://github.example/acme/greeter/pull/5" || w["pr_ref"] != "github:acme/greeter#5" {
		t.Fatalf("PR not recorded on the work order: %v", w)
	}
	sha, _ := w["commit_sha"].(string)
	if got := gitOut(t, repo, "rev-parse", "refs/heads/legos-factory/"+wo); got != sha {
		t.Fatalf("remote branch %s != verified commit %s", got, sha)
	}
	base := gitOut(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	created := gh.prs()
	if len(created) != 1 || created[0]["head"] != "legos-factory/"+wo || created[0]["base"] != base ||
		!strings.Contains(fmt.Sprint(created[0]["body"]), "jira:PAY-7") {
		t.Fatalf("unexpected PR creation: %+v (base %s)", created, base)
	}
	if got := gitOut(t, repo, "rev-parse", base); got == sha {
		t.Fatal("the default branch must never be pushed")
	}
	pr := awaitKnowledge(t, ta, "?work_order_id="+wo, "pr_opened")
	if pr.Trust != "verified" || !contains(pr.SourceRefs, "pr:github:acme/greeter#5") {
		t.Fatalf("PR knowledge record: %+v", pr)
	}

	// a disabled connection blocks new work on the bound project
	if status, body := ta.do(t, "PATCH", "/api/v1/connections/"+ghConn.ID, `{"status":"disabled"}`); status != 200 {
		t.Fatalf("disable: %d %s", status, body)
	}
	status, body = ta.do(t, "POST", "/api/v1/work-orders", fmt.Sprintf(`{"project_id":%q,"title":"next","objective":"more"}`, proj))
	if status != 500 || !strings.Contains(body, "WORKSPACE_FAILED") || !strings.Contains(body, "disabled") {
		t.Fatalf("disabled connection must block new work: %d %s", status, body)
	}
}
