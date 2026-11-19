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

func TestIntegrationsConnectionsAndJiraImport(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ghSrv := (&fakeGitHub{}).server(t)
	jiraSrv := fakeJira(t)
	dataDir := t.TempDir()
	ta := bootAt(t, dataDir)
	eng := ta.loginAs(t, "eng@example.com", "Eng")

	// connections are all personal; credentials never returned
	status, body := ta.do(t, "POST", "/api/v1/connections", fmt.Sprintf(
		`{"provider":"github","name":"Acme GitHub","scope":"personal","base_url":%q,"token":%q}`, ghSrv.URL, ghToken))
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
		`{"provider":"github","name":"x","scope":"shared","token":"t"}`); status != 400 {
		t.Fatalf("shared connections must be rejected, got %d", status)
	}
	if status, body := ta.doAs(t, eng, "GET", "/api/v1/connections", ""); status != 200 || strings.Contains(body, jiraConn.ID) || strings.Contains(body, ghConn.ID) {
		t.Fatalf("engineer must not see others' connections: %d %s", status, body)
	}
	if status, _ := ta.doAs(t, eng, "POST", "/api/v1/integrations/jira/lookup",
		fmt.Sprintf(`{"connection_id":%q,"key":"PAY-7"}`, jiraConn.ID)); status != 404 {
		t.Fatalf("someone else's personal connection must be unusable, got %d", status)
	}
	if status, _ := ta.doAs(t, eng, "POST", "/api/v1/connections/"+ghConn.ID+"/test", ""); status != 404 {
		t.Fatalf("engineer must not manage others' connections, got %d", status)
	}
	if status, _ := ta.do(t, "POST", "/api/v1/connections",
		`{"provider":"github","name":"leak","scope":"personal","base_url":"http://example.com","token":"t"}`); status != 400 {
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
}
