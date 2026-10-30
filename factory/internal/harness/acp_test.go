package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"legos-factory/factory/internal/sandbox"
)

// The test binary doubles as a fake ACP agent when ACP_FAKE_AGENT is set.
func TestMain(m *testing.M) {
	if scenario := os.Getenv("ACP_FAKE_AGENT"); scenario != "" {
		fakeAgent(scenario)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeAgent(scenario string) {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	out := json.NewEncoder(os.Stdout)
	send := func(v map[string]any) { v["jsonrpc"] = "2.0"; _ = out.Encode(v) }
	reply := func(id any, result any) { send(map[string]any{"id": id, "result": result}) }
	chunk := func(text string) {
		send(map[string]any{"method": "session/update", "params": map[string]any{"update": map[string]any{
			"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}}})
	}
	var log []string // requests seen, echoed back in the "echo" scenario
	var promptID any
	for in.Scan() {
		var m struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(in.Bytes(), &m)
		if m.Method != "initialize" {
			log = append(log, m.Method+" "+string(m.Params))
		}
		switch m.Method {
		case "initialize":
			reply(m.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": os.Getenv("ACP_FAKE_NOLOAD") == ""}})
		case "session/new":
			res := map[string]any{"sessionId": "ses_1"}
			if os.Getenv("ACP_FAKE_MODELS") != "" {
				res["models"] = map[string]any{"availableModels": []any{map[string]any{"modelId": "sonnet"}, map[string]any{"modelId": "haiku"}}}
			} else {
				res["configOptions"] = []any{
					map[string]any{"id": "mode", "category": "mode", "options": []any{map[string]any{"value": "build"}}},
					map[string]any{"id": "model", "category": "model", "options": []any{map[string]any{"value": "opencode/free-a"}, map[string]any{"value": "opencode-go/kimi"}}},
				}
			}
			reply(m.ID, res)
			if pidFile := os.Getenv("ACP_FAKE_PIDFILE"); pidFile != "" {
				child := exec.Command("sleep", "60")
				_ = child.Start()
				_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())+" "+strconv.Itoa(child.Process.Pid)), 0o644)
			}
		case "session/load":
			// history replay must not surface as events
			send(map[string]any{"method": "session/update", "params": map[string]any{"update": map[string]any{
				"sessionUpdate": "tool_call", "toolCallId": "old", "title": "old-tool"}}})
			if os.Getenv("ACP_FAKE_LOADFAIL") != "" {
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32602, "message": "session not found"}})
				continue
			}
			reply(m.ID, map[string]any{"configOptions": []any{
				map[string]any{"id": "model", "category": "model", "options": []any{map[string]any{"value": "opencode-go/kimi"}}},
			}})
		case "session/set_config_option", "session/set_model":
			reply(m.ID, map[string]any{})
		case "session/cancel":
			if promptID != nil {
				reply(promptID, map[string]any{"stopReason": "cancelled"})
			}
		case "session/prompt":
			promptID = m.ID
			switch scenario {
			case "echo":
				b, _ := json.Marshal(map[string]any{"log": log, "env": os.Getenv("OPENCODE_CONFIG_CONTENT"), "extra": os.Getenv("EXTRA"),
					"claude": []string{os.Getenv("CLAUDE_CONFIG_DIR"), os.Getenv("IS_SANDBOX"), os.Getenv("ANTHROPIC_DEFAULT_HAIKU_MODEL")}})
				chunk(string(b))
				reply(m.ID, map[string]any{"stopReason": "end_turn"})
			case "replay":
				f, err := os.Open(os.Getenv("ACP_FAKE_FIXTURE"))
				if err != nil {
					panic(err)
				}
				sc := bufio.NewScanner(f)
				sc.Buffer(make([]byte, 1<<20), 1<<20)
				started := false // captures begin with the handshake; replay only what follows the prompt
				for sc.Scan() {
					// fixtures are either bare agent lines or {"dir":..,"msg":..} captures
					var line struct {
						Dir string          `json:"dir"`
						Msg json.RawMessage `json:"msg"`
					}
					raw := json.RawMessage(sc.Bytes())
					if json.Unmarshal(sc.Bytes(), &line) != nil || line.Dir == "" {
						started = true
					} else {
						if strings.HasPrefix(line.Dir, "client") && strings.Contains(string(line.Msg), "session/prompt") {
							started = true
						}
						if !started || !strings.HasPrefix(line.Dir, "agent") {
							continue
						}
						raw = line.Msg
					}
					var msg map[string]any
					_ = json.Unmarshal(raw, &msg)
					_, isCall := msg["method"]
					if _, hasID := msg["id"]; hasID && !isCall {
						msg["id"] = m.ID
					}
					_ = out.Encode(msg)
					if isCall && msg["id"] != nil {
						in.Scan()
						if p := os.Getenv("ACP_FAKE_REPLYLOG"); p != "" {
							f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
							fmt.Fprintln(f, in.Text())
							f.Close()
						}
					}
				}
			case "permission":
				send(map[string]any{"id": "perm-1", "method": "session/request_permission", "params": map[string]any{
					"toolCall": map[string]any{"toolCallId": "t9", "title": "x", "kind": os.Getenv("ACP_FAKE_KIND"), "locations": []any{map[string]any{"path": os.Getenv("ACP_FAKE_PATH")}}},
					"options": []any{
						map[string]any{"optionId": "no", "name": "Reject", "kind": "reject_once"},
						map[string]any{"optionId": "yes", "name": "Allow", "kind": "allow_once"},
					},
				}})
				in.Scan()
				var r struct {
					Result struct {
						Outcome struct{ OptionID string } `json:"outcome"`
					} `json:"result"`
				}
				_ = json.Unmarshal(in.Bytes(), &r)
				chunk(r.Result.Outcome.OptionID)
				reply(m.ID, map[string]any{"stopReason": "end_turn"})
			case "quota":
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32603, "message": "Go usage limit exceeded"}})
			case "funds":
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32603, "message": "Internal error: Upstream request failed: Insufficient account funds"}})
			case "overloaded":
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32603, "message": "upstream returned 503 overloaded"}})
			case "refusal":
				reply(m.ID, map[string]any{"stopReason": "refusal"})
			case "crash":
				fmt.Fprintln(os.Stderr, "fatal: provider exploded")
				os.Exit(3)
			case "hang":
			}
		}
	}
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
