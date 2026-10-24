// Package integration demonstrates a complete local Work Order lifecycle
// using the mock harness and REAL deterministic verification (go test).
package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/app"
	"legos-factory/backend/internal/config"
	"legos-factory/backend/internal/identity"
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
	*a     app.App
	base    string
	client  *http.Client
	usertok string
}

func boot(t *testing.T) *testApp {
	t.Helper()
	dataDir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	cfg := &config.Config{
		Addr: fmt.Sprintf("127.0.0.1:%d", port),
		DataDir: dataDir,
		AuthMode: "dev",
		DevUserID: "usr-test", DevUserName: "Test User",
		SessionTTL: 3600,
		MaxConcurrentRuns: 2, RunTimeoutSec: 60,
		SandboxMode: "local", FrontendOrigin: "http://localhost:5173",
	}
	log := discardLogger()
	a, err := app.Build(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		a.Sched.Stop()
		shctx, c2 := context.WithTimeout(context.Background(), 2*time.Second)
		defer c2()
		_ = a.Server.Echo.Shutdown(shctx)
		_ = a.DBCloser()
	})
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	go func() { _ = a.Server.Echo.Start(cfg.Addr) }()
	if err := waitReady(cfg.Addr); err != nil {
		t.Fatal(err)
	}

	// issue a session for the dev user via identity service directly
	u, err := a.Server.Auth.EnsureDevUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sid, _, err := a.Server.Auth.Login(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	_ = sid
	_ = identity.RoleOwner
	return &testApp{
		a: a, base: "http://" + cfg.Addr,
		client:  &http.Client{Timeout: 10 * time.Second},
		usertok: sid,
	}
}

func sel(s string) string { return s }

func discardLogger() interface{ Warn(string, ...any) } {
	return quiet{}
}

type quiet struct{}

func (quiet) Warn(string, ...any) {}

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
	req, err := http.NewRequest(method, ta.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "factory_session", Value: ta.usertok})
	resp, err := ta.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

// awaitState polls the work order endpoint until the state matches.
func (ta *testApp) awaitState(t *testing.T, woID string, want string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, body := ta.do(t, "GET", "/api/v1/work-orders/"+woID, "")
		if status != 200 {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if strings.Contains(body, `"state":"`+want+`"`) {
			return parseJSON(body)
		}
		time.Sleep(150 * time.Millisecond)
	}
	status, body := ta.do(t, "GET", "/api/v1/work-orders/"+woID, "")
	t.Fatalf("timed out waiting for state %s (last status %d): %s", want, status, tail(body, 2000))
	return nil
}

func parseJSON(s string) map[string]any { return map[string]any{} }
func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func TestFullLifecycleWithMockHarnessAndRealChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := fixtureRepo(t)
	ta := boot(t)

	// use the structured client created in TestMain-like helpers
	tc, _ := newLifecycle(t, ta, repo)
	_ = sel
	_ = tc
}

// newLifecycle runs the entire journey and returns collected evidence.
func newLifecycle(t *testing.T, ta *testApp, repo string) (*lifecycleResult, error) {
	return runLifecycleExpectations(t, ta, repo)
}
