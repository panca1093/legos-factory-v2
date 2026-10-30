package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"legos-factory/factory/internal/harness/harnesstest"
	"legos-factory/factory/internal/sandbox"
)

// The test binary doubles as a fake ACP agent when ACP_FAKE_AGENT is set.
func TestMain(m *testing.M) {
	if harnesstest.RunIfFakeAgent() {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func startFake(t *testing.T, scenario string, spec SessionSpec, env ...string) (Session, error) {
	t.Helper()
	rt := sandbox.Local{Root: t.TempDir()}
	ws, err := rt.CreateWorkspace(context.Background(), "wo1")
	if err != nil {
		t.Fatal(err)
	}
	sb, err := rt.Start(context.Background(), ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec.Sandbox = sb
	spec.AgentCommand = []string{os.Args[0]}
	if spec.Dir == "" {
		spec.Dir = t.TempDir()
	}
	spec.Env = map[string]string{"ACP_FAKE_AGENT": scenario}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		spec.Env[k] = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	s, err := ACP{}.Start(ctx, spec)
	if err == nil {
		t.Cleanup(func() { _ = s.Close() })
	}
	return s, err
}

func prompt(t *testing.T, s Session) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.Prompt(ctx, "go")
}

func drain(s Session) []Event {
	_ = s.Close()
	var evs []Event
	for e := range s.Events() {
		evs = append(evs, e)
	}
	return evs
}

type echo struct {
	Log    []string
	Env    string
	Extra  string
	Claude []string
}

func echoOf(t *testing.T, evs []Event) echo {
	t.Helper()
	for _, ev := range evs {
		if ev.Kind == EventStep {
			var e echo
			if err := json.Unmarshal([]byte(ev.Text), &e); err != nil {
				t.Fatalf("echo %q: %v", ev.Text, err)
			}
			return e
		}
	}
	t.Fatalf("no echo step in %+v", evs)
	return echo{}
}

func TestNewSessionSetsModelAndEnv(t *testing.T) {
	dir := t.TempDir()
	s, err := startFake(t, "echo", SessionSpec{
		Role: RoleInspector, Dir: dir, Profile: Profile{Agent: "opencode", Provider: "opencode-go", Model: "kimi"},
	}, "EXTRA=1")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID() != "ses_1" {
		t.Fatalf("id = %q", s.ID())
	}
	if stop, err := prompt(t, s); err != nil || stop != "end_turn" {
		t.Fatalf("prompt = %q, %v", stop, err)
	}
	e := echoOf(t, drain(s))
	joined := strings.Join(e.Log, "\n")
	for _, want := range []string{`session/new {"cwd":"` + dir + `"`, `session/set_config_option {"configId":"model"`, `"value":"opencode-go/kimi"`, "session/prompt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log missing %q:\n%s", want, joined)
		}
	}
	for _, tool := range []string{"edit", "write", "patch", "apply_patch", "bash", "shell", "execute", "external_directory"} {
		if !strings.Contains(e.Env, `"`+tool+`":"deny"`) {
			t.Errorf("read-only config must deny %s: %s", tool, e.Env)
		}
	}
	if e.Extra != "1" {
		t.Errorf("spec.Env not passed: %q", e.Extra)
	}
}

func TestOpencodeConfigBuilderCanEdit(t *testing.T) {
	c := opencodeConfig(RoleBuilder, []string{"webfetch"})
	if !strings.Contains(c, `"external_directory":"deny"`) || !strings.Contains(c, `"webfetch":"deny"`) || strings.Contains(c, `"edit":"deny"`) {
		t.Fatalf("builder config = %s", c)
	}
}

func TestUnknownOpencodeModelIsPermanent(t *testing.T) {
	_, err := startFake(t, "echo", SessionSpec{Role: RoleBuilder, Profile: Profile{Agent: "opencode", Provider: "anthropic", Model: "claude"}})
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.IsTransient || !strings.Contains(err.Error(), "anthropic/claude") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetModelViaAdvertisedModels(t *testing.T) {
	s, err := startFake(t, "echo", SessionSpec{Role: RoleBuilder, Profile: Profile{Agent: "codex-acp", Model: "haiku"}}, "ACP_FAKE_MODELS=1")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = prompt(t, s)
	if log := strings.Join(echoOf(t, drain(s)).Log, "\n"); !strings.Contains(log, `session/set_model {"modelId":"haiku"`) {
		t.Fatalf("log = %s", log)
	}
}

func TestSetModelSkippedWithoutAdvertisedModels(t *testing.T) {
	s, err := startFake(t, "echo", SessionSpec{Role: RoleBuilder, Profile: Profile{Agent: "codex-acp", Model: "haiku"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = prompt(t, s)
	if log := strings.Join(echoOf(t, drain(s)).Log, "\n"); strings.Contains(log, "set_model") || strings.Contains(log, "set_config_option") {
		t.Fatalf("log = %s", log)
	}
}

func TestLoadSession(t *testing.T) {
	s, err := startFake(t, "echo", SessionSpec{Role: RoleBuilder, SessionID: "ses_old", Profile: Profile{Agent: "opencode", Provider: "opencode-go", Model: "kimi"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID() != "ses_old" {
		t.Fatalf("id = %q", s.ID())
	}
	_, _ = prompt(t, s)
	evs := drain(s)
	log := strings.Join(echoOf(t, evs).Log, "\n")
	if !strings.Contains(log, `session/load {"cwd"`) || strings.Contains(log, "session/new") || !strings.Contains(log, `"sessionId":"ses_old"`) || !strings.Contains(log, "set_config_option") {
		t.Fatalf("log = %s", log)
	}
	for _, e := range evs {
		if e.Kind == EventToolCall {
			t.Fatalf("replayed history leaked as event: %+v", e)
		}
	}
}

func TestLoadSessionErrNoLoad(t *testing.T) {
	cases := map[string]string{
		"agent lacks loadSession": "ACP_FAKE_NOLOAD=1",
		"load fails":              "ACP_FAKE_LOADFAIL=1",
	}
	for name, env := range cases {
		_, err := startFake(t, "echo", SessionSpec{Role: RoleBuilder, SessionID: "ses_old"}, env)
		if !errors.Is(err, ErrNoLoad) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestReplayedOpencodeStream(t *testing.T) {
	fixture, err := filepath.Abs("testdata/opencode_prompt.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	s, err := startFake(t, "replay", SessionSpec{Role: RoleBuilder}, "ACP_FAKE_FIXTURE="+fixture)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := prompt(t, s)
	if err != nil || stop != "end_turn" {
		t.Fatalf("prompt = %q, %v", stop, err)
	}
	var got []string
	for _, e := range drain(s) {
		line := string(e.Kind)
		switch e.Kind {
		case EventStep:
			line += ":" + e.Text
		case EventToolCall:
			line += ":" + e.ToolCallID + ":" + e.Tool
		case EventToolUpdate:
			line += ":" + e.ToolCallID + ":" + e.Tool + ":" + e.Status + ":" + e.Text
		case EventUsage:
			line += fmt.Sprintf(":%+v", e.Usage)
		case EventStop:
			line += ":" + e.StopReason
		}
		got = append(got, line)
	}
	want := []string{
		"step:I will read main.go.",
		"tool_call:call_1:read",
		"tool_update:call_1:read:in_progress:",
		"tool_update:call_1:read:completed:package main\nfunc main() {}",
		"tool_call:call_2:shell",
		"tool_update:call_2:shell:failed:",
		"usage:{InputTokens:0 OutputTokens:0 CacheReadTokens:0 CacheCreateTokens:0 CostUSD:0.0123}",
		"step:Done.",
		"usage:{InputTokens:100 OutputTokens:20 CacheReadTokens:7 CacheCreateTokens:0 CostUSD:0}",
		"stop:end_turn",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPermissionAlwaysAllows(t *testing.T) {
	ws := t.TempDir()
	cases := []struct {
		role       Role
		kind, path string
		wantErr    string
	}{
		{RoleBuilder, "edit", ws + "/main.go", ""},
		{RoleBuilder, "read", "/etc/passwd", "outside workspace"},
		{RoleInspector, "read", ws + "/main.go", ""},
		{RoleInspector, "edit", ws + "/main.go", "read-only role"},
	}
	for _, c := range cases {
		s, err := startFake(t, "permission", SessionSpec{Role: c.role, Dir: ws}, "ACP_FAKE_KIND="+c.kind, "ACP_FAKE_PATH="+c.path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := prompt(t, s); err != nil {
			t.Fatal(err)
		}
		var answered, errText string
		for _, e := range drain(s) {
			if e.Kind == EventStep {
				answered = e.Text
			}
			if e.Kind == EventError {
				errText += e.Err.Error()
			}
		}
		if answered != "yes" {
			t.Errorf("%s %s: answered %q, want allow", c.role, c.kind, answered)
		}
		if (c.wantErr == "") != (errText == "") || !strings.Contains(errText, c.wantErr) {
			t.Errorf("%s %s %s: error events %q, want %q", c.role, c.kind, c.path, errText, c.wantErr)
		}
	}
}

func TestProviderErrors(t *testing.T) {
	cases := []struct {
		scenario  string
		msg       string
		transient bool
		status    int
	}{
		{"quota", "Go usage limit exceeded", false, 0},
		{"funds", "Insufficient account funds", false, 0},
		{"overloaded", "503 overloaded", true, 503},
	}
	for _, c := range cases {
		s, err := startFake(t, c.scenario, SessionSpec{Role: RoleBuilder})
		if err != nil {
			t.Fatal(err)
		}
		_, err = prompt(t, s)
		var pe *ProviderError
		if !errors.As(err, &pe) || !strings.Contains(pe.Message, c.msg) || pe.IsTransient != c.transient || pe.Status != c.status {
			t.Fatalf("%s: err = %#v", c.scenario, err)
		}
		var evErr *ProviderError
		for _, e := range drain(s) {
			if e.Kind == EventError {
				errors.As(e.Err, &evErr)
			}
		}
		if evErr == nil || evErr.IsTransient != c.transient {
			t.Errorf("%s: no matching EventError", c.scenario)
		}
	}
}

func TestRefusalStopReason(t *testing.T) {
	s, err := startFake(t, "refusal", SessionSpec{Role: RoleBuilder})
	if err != nil {
		t.Fatal(err)
	}
	if stop, err := prompt(t, s); err != nil || stop != "refusal" {
		t.Fatalf("prompt = %q, %v", stop, err)
	}
}

func TestAgentCrashSurfacesStderr(t *testing.T) {
	s, err := startFake(t, "crash", SessionSpec{Role: RoleBuilder})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prompt(t, s); err == nil || !strings.Contains(err.Error(), "provider exploded") {
		t.Fatalf("err = %v", err)
	}
}

func TestPromptCancel(t *testing.T) {
	s, err := startFake(t, "hang", SessionSpec{Role: RoleBuilder})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	stop, err := s.Prompt(ctx, "go")
	if stop != "cancelled" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("prompt = %q, %v", stop, err)
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestCloseKillsProcessTree(t *testing.T) {
	pidFile := t.TempDir() + "/pids"
	s, err := startFake(t, "hang", SessionSpec{Role: RoleBuilder}, "ACP_FAKE_PIDFILE="+pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for deadline := time.Now().Add(5 * time.Second); len(pids) != 2 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		pids = nil
		if b, err := os.ReadFile(pidFile); err == nil {
			for _, f := range strings.Fields(string(b)) {
				n, _ := strconv.Atoi(f)
				pids = append(pids, n)
			}
		}
	}
	if len(pids) != 2 || !alive(pids[0]) || !alive(pids[1]) {
		t.Fatalf("pids = %v", pids)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		for deadline := time.Now().Add(3 * time.Second); alive(pid) && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		}
		if alive(pid) {
			t.Errorf("pid %d survived Close", pid)
		}
	}
	if _, ok := <-s.Events(); ok {
		t.Error("Events not closed")
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestOpencodeConfigMergedWithSpecEnv(t *testing.T) {
	s, err := startFake(t, "echo", SessionSpec{Role: RoleInspector, Profile: Profile{Agent: "opencode", Provider: "opencode-go", Model: "kimi"}},
		`OPENCODE_CONFIG_CONTENT={"provider":{"acme":{"name":"acme"}},"permission":{"edit":"allow","custom":"ask"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prompt(t, s); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Provider   map[string]any    `json:"provider"`
		Permission map[string]string `json:"permission"`
	}
	if err := json.Unmarshal([]byte(echoOf(t, drain(s)).Env), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Provider["acme"] == nil || cfg.Permission["custom"] != "ask" || cfg.Permission["edit"] != "deny" || cfg.Permission["external_directory"] != "deny" {
		t.Fatalf("merged config = %+v", cfg)
	}
}

func TestSessionFilesWrittenToHome(t *testing.T) {
	rt := sandbox.Local{Root: t.TempDir()}
	ws, _ := rt.CreateWorkspace(context.Background(), "wo1")
	sb, _ := rt.Start(context.Background(), ws, nil)
	s, err := ACP{}.Start(context.Background(), SessionSpec{
		Role: RoleBuilder, Profile: Profile{Agent: "codex-acp", Model: "haiku"}, AgentCommand: []string{os.Args[0]},
		Dir: t.TempDir(), Env: map[string]string{"ACP_FAKE_AGENT": "echo"}, Sandbox: sb,
		Files: map[string][]byte{".codex/auth.json": []byte(`{"a":1}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	p := filepath.Join(ws.HomeDir(), ".codex", "auth.json")
	b, err := os.ReadFile(p)
	fi, _ := os.Stat(p)
	if err != nil || string(b) != `{"a":1}` || fi.Mode().Perm() != 0o600 {
		t.Fatalf("auth file = %q %v %v", b, err, fi)
	}
}
