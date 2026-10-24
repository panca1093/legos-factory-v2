// Package integration demonstrates a complete local Work Order lifecycle
// using the mock harness and REAL deterministic verification (go test).
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/app"
	"legos-factory/backend/internal/config"
)

const fixtureGoMod = "module factory-fixture\n\ngo 1.25\n"

const fixtureTest = `package greeter_test

import (
	"testing"

	"factory-fixture/greeter"
)

func TestGreet(t *testing.T) {
	if got, want := greeter.Greet("Ada"), "Hello, Ada!"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := greeter.Greet(""); got != "Hello, stranger!" {
		t.Fatalf("empty name: %q", got)
	}
}
`

func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(fixtureGoMod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "greeter_test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "greeter_test", "greeter_test.go"), []byte(fixtureTest), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "init", "-q")
	run(t, dir, "git", "add", "-A")
	run(t, dir, "git", "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-qm", "fixture")
	return dir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", args, out)
	}
}

type testApp struct {
	a       *app.App
	base    string
	client  *http.Client
	session string
	stop    func()
}

func boot(t *testing.T) *testApp {
	t.Helper()
	return bootAt(t, t.TempDir())
}

// bootAt starts the app on dataDir; stop is idempotent so a test can
// restart on the same directory.
func bootAt(t *testing.T, dataDir string) *testApp {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	cfg := &config.Config{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		DataDir:           dataDir,
		AuthMode:          "dev",
		DevUserID:         "usr-test",
		DevUserName:       "Test User",
		SessionTTL:        3600,
		MaxConcurrentRuns: 2,
		RunTimeoutSec:     60,
		SandboxMode:       "local",
		FrontendOrigin:    "http://localhost:5173",
	}
	a, err := app.Build(cfg, slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			a.Sched.Stop()
			shctx, c2 := context.WithTimeout(context.Background(), 2*time.Second)
			defer c2()
			_ = a.Server.Echo.Shutdown(shctx)
			_ = a.DBCloser()
		})
	}
	t.Cleanup(stop)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { _ = a.Server.Echo.Start(cfg.Addr) }()
	if err := waitReady(cfg.Addr); err != nil {
		t.Fatal(err)
	}

	u, err := a.Server.Auth.EnsureDevUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sid, _, err := a.Server.Auth.Login(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	return &testApp{
		a:       a,
		base:    "http://" + cfg.Addr,
		client:  &http.Client{Timeout: 10 * time.Second},
		session: sid,
		stop:    stop,
	}
}

func waitReady(addr string) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("server never became ready")
}

func (ta *testApp) do(t *testing.T, method, path string, body string) (int, string) {
	t.Helper()
	return ta.doAs(t, ta.session, method, path, body)
}

func (ta *testApp) doAs(t *testing.T, session, method, path string, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, ta.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "factory_session", Value: session})
	resp, err := ta.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

func (ta *testApp) awaitState(t *testing.T, woID string, want string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastStatus int
	var lastBody string
	for time.Now().Before(deadline) {
		lastStatus, lastBody = ta.do(t, "GET", "/api/v1/work-orders/"+woID, "")
		if lastStatus == 200 {
			var doc map[string]any
			if err := json.Unmarshal([]byte(lastBody), &doc); err == nil {
				if wo := doc["work_order"].(map[string]any); wo["state"] == want {
					return doc
				}
				continue
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for state %s (last status %d): %s", want, lastStatus, tail(lastBody, 1200))
	return nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func TestFullLifecycleMockHarnessRealChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := fixtureRepo(t)
	ta := boot(t)

	// 1. project
	status, body := ta.do(t, "POST", "/api/v1/projects", fmt.Sprintf(
		`{"name":"Greeter","repo_url":%q,"language":"go"}`, repo))
	if status != 201 {
		t.Fatalf("create project: %d %s", status, body)
	}
	var proj struct {
		ID string `json:"id"`
	}
	json.Unmarshal([]byte(body), &proj)

	// 2. work order (objective has no "?" → architect approves the plan)
	status, body = ta.do(t, "POST", "/api/v1/work-orders", fmt.Sprintf(
		`{"project_id":%q,"title":"Greeter fixture","objective":"Make the greeter greeting function available so the fixture contract test passes.","acceptance":"TestGreet passes with go test ./...","autonomy":"assisted"}`, proj.ID))
	if status != 201 {
		t.Fatalf("create work order: %d %s", status, body)
	}
	var wo struct {
		ID string `json:"id"`
	}
	json.Unmarshal([]byte(body), &wo)

	// 3. planning
	status, body = ta.do(t, "POST", fmt.Sprintf("/api/v1/work-orders/%s/plan/generate", wo.ID), "")
	if status != 202 {
		t.Fatalf("plan generate: %d %s", status, body)
	}
	doc := ta.awaitState(t, wo.ID, "ARCHITECT_REVIEW", 30*time.Second)
	plan := doc["plan"].([]any)
	if len(plan) != 4 {
		t.Fatalf("expected 4 planned tasks, got %d", len(plan))
	}

	// 4. human approves → queued → full build/verify/inspect cycle
	status, body = ta.do(t, "POST", fmt.Sprintf("/api/v1/work-orders/%s/plan/decisions", wo.ID), `{"decision":"approve"}`)
	if status != 202 {
		t.Fatalf("approve plan: %d %s", status, body)
	}
	doc = ta.awaitState(t, wo.ID, "NEEDS_HUMAN_REVIEW", 60*time.Second)

	// revision identity: scripted inspector regression → fix on revision 1
	if rev := int(doc["work_order"].(map[string]any)["revision"].(float64)); rev != 1 {
		t.Fatalf("expected revision 1 after remediation loop, got %d", rev)
	}

	// quality evidence attached to the current revision
	status, body = ta.do(t, "GET", fmt.Sprintf("/api/v1/work-orders/%s/quality", wo.ID), "")
	if status != 200 {
		t.Fatalf("quality: %d %s", status, body)
	}
	if !strings.Contains(body, `"outcome":"PASS"`) {
		t.Fatalf("expected PASS evidence on current revision: %s", tail(body, 800))
	}
	if !strings.Contains(body, `"outcome":"FAIL"`) {
		// baseline failure evidence from revision 0 must exist too
		t.Fatalf("expected recorded baseline FAIL at revision 0: %s", tail(body, 800))
	}

	// findings: one blocker, later resolved by clean re-inspection
	status, body = ta.do(t, "GET", fmt.Sprintf("/api/v1/work-orders/%s/findings", wo.ID), "")
	if status != 200 || !strings.Contains(body, `"severity":"blocker"`) {
		t.Fatalf("expected recorded blocker finding, got %d %s", status, body)
	}
	if !strings.Contains(body, `"status":"resolved"`) {
		t.Fatalf("expected earlier finding resolved after clean re-inspection: %s", body)
	}

	// usage attribution recorded for agents
	status, body = ta.do(t, "GET", "/api/v1/usage", "")
	if status != 200 || strings.Contains(body, `"cost_micros":0,"tokens_in":0,"tokens_out":0`) {
		t.Fatalf("expected usage records, got %d %s", status, body)
	}

	// 5. human PR decision → READY (no publish/merge happens)
	status, body = ta.do(t, "POST", fmt.Sprintf("/api/v1/work-orders/%s/pr-decision", wo.ID), `{"decision":"approve","reason":"evidence complete"}`)
	if status != 202 {
		t.Fatalf("pr decision: %d %s", status, body)
	}
	ta.awaitState(t, wo.ID, "READY", 5*time.Second)

	// workspace produced the real implementation file
	ta.assertWorkspaceImpl(t, wo.ID, "greeter/greeter.go")
}

func TestClarificationPath(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := fixtureRepo(t)
	ta := boot(t)

	var proj struct {
		ID string `json:"id"`
	}
	status, body := ta.do(t, "POST", "/api/v1/projects", fmt.Sprintf(`{"name":"Clarify","repo_url":%q,"language":"go"}`, repo))
	if status != 201 {
		t.Fatal(body)
	}
	json.Unmarshal([]byte(body), &proj)

	var wo struct {
		ID string `json:"id"`
	}
	status, body = ta.do(t, "POST", "/api/v1/work-orders", fmt.Sprintf(
		`{"project_id":%q,"title":"Ambiguous order","objective":"Add retries? Confirm the idempotency window first.","acceptance":"Tests pass","autonomy":"assisted"}`, proj.ID))
	if status != 201 {
		t.Fatal(body)
	}
	json.Unmarshal([]byte(body), &wo)

	if status, _ := ta.do(t, "POST", fmt.Sprintf("/api/v1/work-orders/%s/plan/generate", wo.ID), ""); status != 202 {
		t.Fatal("plan generate failed")
	}
	// architect detects the open question and escalates to a human
	doc := ta.awaitState(t, wo.ID, "WAITING_FOR_HUMAN", 30*time.Second)
	question := doc["work_order"].(map[string]any)["clarif_question"].(string)
	if question == "" {
		t.Fatalf("expected a clarification question, got: %s", body)
	}

	// human answers → architect re-review runs → plan approved again
	status, body = ta.do(t, "POST", fmt.Sprintf("/api/v1/work-orders/%s/clarifications", wo.ID),
		`{"answer":"Idempotency window is 24 hours."}`)
	if status != 202 {
		t.Fatalf("clarify: %d %s", status, body)
	}
	ta.awaitState(t, wo.ID, "ARCHITECT_REVIEW", 30*time.Second)

	// answering again must fail: not waiting anymore
	status, body = ta.do(t, "POST", fmt.Sprintf("/api/v1/work-orders/%s/clarifications", wo.ID),
		`{"answer":"second answer"}`)
	if status != 409 {
		t.Fatalf("second clarify should be a state conflict, got %d %s", status, body)
	}
}

func (ta *testApp) assertWorkspaceImpl(t *testing.T, woID, relPath string) {
	t.Helper()
	dir := filepath.Join(ta.a.Cfg.DataDir, "workspaces", woID, filepath.FromSlash(relPath))
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("workspace implementation missing: %v", err)
	}
}
