package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
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
	update := func(u map[string]any) {
		send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "fake-ses", "update": u}})
	}
	reply := func(id any, result any) { send(map[string]any{"id": id, "result": result}) }
	var cwd, chosenModel, sessionMeta string
	var setCalls []string
	var promptID any
	turns := 0
	for in.Scan() {
		var m struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		_ = json.Unmarshal(in.Bytes(), &m)
		switch m.Method {
		case "initialize":
			res := map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "Fake", "version": "0"}}
			if scenario == "steer" || scenario == "steer-exit" {
				res["_meta"] = map[string]any{"steering": map[string]any{"supported": true}}
			}
			reply(m.ID, res)
		case "_session/steering":
			var p struct {
				Prompt []struct{ Text string } `json:"prompt"`
				Meta   struct {
					Steering struct{ IdleBehavior string } `json:"steering"`
				} `json:"_meta"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if p.Meta.Steering.IdleBehavior != "promptRequired" {
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32602, "message": "want idleBehavior promptRequired"}})
				continue
			}
			if scenario == "steer-exit" {
				os.Exit(3)
			}
			if promptID == nil {
				reply(m.ID, map[string]any{"outcome": "promptRequired", "reason": "noRunningTurn"})
				continue
			}
			reply(m.ID, map[string]any{"outcome": "injected"})
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "steered: " + p.Prompt[0].Text}})
			reply(promptID, map[string]any{"stopReason": "end_turn"})
			promptID = nil
		case "session/new":
			var p struct {
				Cwd  string
				Meta json.RawMessage `json:"_meta"`
			}
			_ = json.Unmarshal(m.Params, &p)
			cwd, sessionMeta = p.Cwd, string(p.Meta)
			reply(m.ID, map[string]any{"sessionId": "fake-ses", "configOptions": []any{
				map[string]any{"id": "mode", "category": "mode", "currentValue": "build", "options": []any{map[string]any{"value": "build", "name": "Build"}, map[string]any{"value": "plan", "name": "Plan"}}},
				map[string]any{"id": "model", "category": "model", "currentValue": "opencode/free-a", "options": []any{
					map[string]any{"value": "opencode/free-a", "name": "opencode/Free A"},
					map[string]any{"value": "opencode-go/kimi", "name": "opencode-go/Kimi"},
					map[string]any{"value": "sonnet", "name": "Sonnet"},
				}},
			}})
		case "session/set_config_option":
			var p struct{ ConfigID, Value string }
			_ = json.Unmarshal(m.Params, &p)
			setCalls = append(setCalls, p.ConfigID+"="+p.Value)
			if p.ConfigID == "model" {
				chosenModel = p.ConfigID + "=" + p.Value
			}
			reply(m.ID, map[string]any{"configOptions": []any{}})
		case "session/cancel":
			if promptID != nil {
				reply(promptID, map[string]any{"stopReason": "cancelled"})
			}
		case "session/prompt":
			promptID = m.ID
			switch scenario {
			case "ok":
				update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "read", "kind": "read", "status": "pending", "rawInput": map[string]any{}})
				update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "in_progress", "rawInput": map[string]any{"path": "/data/workspaces/wo/a/b/c.go"}})
				update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t2", "title": "shell", "kind": "execute", "status": "pending"})
				update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed",
					"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "Read file"}}}})
				update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t2", "status": "in_progress", "rawInput": map[string]any{"command": "go test ./..."}})
				update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t2", "status": "failed"})
				update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "thinking"}})
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": `{"cwd":"` + cwd + `",`}})
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": `"model":"` + chosenModel + `","config":` + fmt.Sprintf("%q", os.Getenv("OPENCODE_CONFIG_CONTENT")) + `,"meta":` + fmt.Sprintf("%q", sessionMeta) + `,"set":` + fmt.Sprintf("%q", strings.Join(setCalls, ",")) + `}`}})
				update(map[string]any{"sessionUpdate": "usage_update", "cost": map[string]any{"amount": 0.0123, "currency": "USD"}})
				reply(m.ID, map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 100, "outputTokens": 20, "cachedReadTokens": 4000, "cachedWriteTokens": 900}})
			case "permission":
				send(map[string]any{"id": "perm-1", "method": "session/request_permission", "params": map[string]any{
					"sessionId": "fake-ses",
					"toolCall":  map[string]any{"toolCallId": "t9", "kind": os.Getenv("ACP_FAKE_KIND"), "locations": []any{map[string]any{"path": os.Getenv("ACP_FAKE_PATH")}}},
					"options": []any{
						map[string]any{"optionId": "yes", "name": "Allow", "kind": "allow_once"},
						map[string]any{"optionId": "no", "name": "Reject", "kind": "reject_once"},
					},
				}})
				in.Scan()
				var r struct {
					Result struct {
						Outcome struct{ OptionID string } `json:"outcome"`
					} `json:"result"`
				}
				_ = json.Unmarshal(in.Bytes(), &r)
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": r.Result.Outcome.OptionID}})
				reply(m.ID, map[string]any{"stopReason": "end_turn"})
			case "multi":
				var p struct{ Prompt []struct{ Text string } }
				_ = json.Unmarshal(m.Params, &p)
				turns++
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": fmt.Sprintf("turn %d: %s", turns, p.Prompt[0].Text)}})
				update(map[string]any{"sessionUpdate": "usage_update", "cost": map[string]any{"amount": 0.01 * float64(turns), "currency": "USD"}})
				reply(m.ID, map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 10, "outputTokens": 2}})
			case "echo":
				var p struct {
					Prompt json.RawMessage `json:"prompt"`
				}
				_ = json.Unmarshal(m.Params, &p)
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": string(p.Prompt)}})
				reply(m.ID, map[string]any{"stopReason": "end_turn"})
			case "quota":
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32603, "message": "Go usage limit exceeded"}})
			case "funds":
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32603, "message": "Internal error: Upstream request failed: Insufficient account funds"}})
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

func fakeHarness(t *testing.T, scenario string, extraEnv ...string) *ACPHarness {
	t.Helper()
	h := NewACPHarness([]string{os.Args[0]}, t.TempDir())
	h.Env = append([]string{"ACP_FAKE_AGENT=" + scenario}, extraEnv...)
	return h
}

func runFake(t *testing.T, h *ACPHarness, req model.AgentRequest) (model.AgentResult, error) {
	t.Helper()
	if req.WorkspaceDir == "" {
		req.WorkspaceDir = t.TempDir()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ses, err := h.Start(ctx, req)
	if err != nil {
		return model.AgentResult{}, err
	}
	return h.Result(ctx, ses.ID())
}

func TestACPRunReportsStepsTextAndUsage(t *testing.T) {
	var steps []model.AgentStep
	var streamed strings.Builder
	ws := t.TempDir()
	res, err := runFake(t, fakeHarness(t, "ok"), model.AgentRequest{
		Role: model.AgentInspector, ProviderID: "opencode-go", ModelID: "kimi", WorkspaceDir: ws, Prompt: "go",
		OnStep: func(s model.AgentStep) { steps = append(steps, s) },
		OnText: func(s string) { streamed.WriteString(s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	var reply struct{ Cwd, Model, Config string }
	if err := json.Unmarshal([]byte(res.Text), &reply); err != nil {
		t.Fatalf("reply text %q: %v", res.Text, err)
	}
	if reply.Cwd != ws || reply.Model != "model=opencode-go/kimi" {
		t.Fatalf("session setup = %+v", reply)
	}
	if !strings.Contains(reply.Config, `"external_directory":"deny"`) || !strings.Contains(reply.Config, `"edit":"deny"`) {
		t.Fatalf("reviewer config = %s", reply.Config)
	}
	want := []model.AgentStep{
		{N: 1, Tools: []model.AgentTool{{Name: "read", Target: "a/b/c.go"}}},
		{N: 2, Tools: []model.AgentTool{{Name: "shell", Target: "go test ./...", Failed: true}}},
	}
	if fmt.Sprint(steps) != fmt.Sprint(want) {
		t.Fatalf("steps = %+v\nwant   %+v", steps, want)
	}
	if streamed.String() != res.Text {
		t.Fatalf("OnText streamed %q, result text %q", streamed.String(), res.Text)
	}
	if res.TokensIn != 5000 || res.TokensOut != 20 || res.CacheRead != 4000 || res.CacheWrite != 900 || res.CostMicros != 12300 || res.CostSource != "measured" || res.SessionRef != "fake-ses" {
		t.Fatalf("result = %+v", res)
	}
}

func TestACPClaudeReadOnlyRolesStartInPlanModeWithoutWriteTools(t *testing.T) {
	type sent struct{ Meta, Set string }
	run := func(role model.AgentRole) sent {
		h := fakeHarness(t, "ok")
		h.Provider = "claude"
		res, err := runFake(t, h, model.AgentRequest{Role: role, ProviderID: "claude", ModelID: "sonnet", Prompt: "go"})
		if err != nil {
			t.Fatal(err)
		}
		var s sent
		if err := json.Unmarshal([]byte(res.Text), &s); err != nil {
			t.Fatalf("reply %q: %v", res.Text, err)
		}
		return s
	}
	for _, r := range []model.AgentRole{model.AgentArchitect, model.AgentForeman, model.AgentInspector, model.AgentCataloger} {
		s := run(r)
		var meta struct {
			ClaudeCode struct {
				Options struct {
					Tools           []string
					DisallowedTools []string
					SettingSources  []string
					StrictMcpConfig bool
				}
			}
		}
		if err := json.Unmarshal([]byte(s.Meta), &meta); err != nil {
			t.Fatalf("%s: meta %q: %v", r, s.Meta, err)
		}
		o := meta.ClaudeCode.Options
		if strings.Join(o.Tools, ",") != "Read,Grep,Glob" || !strings.Contains(s.Meta, `"settingSources":[]`) || !o.StrictMcpConfig ||
			!strings.Contains(strings.Join(o.DisallowedTools, ","), "Bash") || !strings.Contains(strings.Join(o.DisallowedTools, ","), "Write") {
			t.Fatalf("%s: options = %+v", r, o)
		}
		if s.Set != "model=sonnet,mode=plan" {
			t.Fatalf("%s: config calls = %q; want the model, then plan mode", r, s.Set)
		}
	}
	chat := run(model.AgentChat)
	if !strings.Contains(chat.Meta, `"tools":[]`) || !strings.Contains(chat.Meta, `"Read"`) || !strings.Contains(chat.Meta, `"WebFetch"`) || chat.Set != "model=sonnet,mode=plan" {
		t.Fatalf("chat must get no tools: meta %q, set %q", chat.Meta, chat.Set)
	}
	if b := run(model.AgentBuilder); b.Meta != `{"claudeCode":{"options":{"settingSources":["project","local"]}}}` || b.Set != "model=sonnet" {
		t.Fatalf("builder must keep its tools and the repo settings but not the user's: meta %q, set %q", b.Meta, b.Set)
	}
}

func TestACPRoleConfig(t *testing.T) {
	chatCfg := acpRoleConfig(model.AgentChat)
	for _, tool := range []string{"read", "webfetch", "bash", "edit"} {
		if !strings.Contains(chatCfg, `"`+tool+`":"deny"`) {
			t.Fatalf("chat config must deny %s: %s", tool, chatCfg)
		}
	}
	if strings.Contains(acpRoleConfig(model.AgentInspector), `"read":"deny"`) {
		t.Fatal("reviewers keep read access")
	}

	builder := acpRoleConfig(model.AgentBuilder)
	if !strings.Contains(builder, `"external_directory":"deny"`) || strings.Contains(builder, `"edit":"deny"`) {
		t.Fatalf("builder config = %s", builder)
	}
	for _, r := range []model.AgentRole{model.AgentArchitect, model.AgentForeman, model.AgentInspector, model.AgentCataloger} {
		c := acpRoleConfig(r)
		for _, tool := range []string{"edit", "write", "patch", "apply_patch", "bash", "shell", "execute", "external_directory"} {
			if !strings.Contains(c, `"`+tool+`":"deny"`) {
				t.Fatalf("%s config must deny %s: %s", r, tool, c)
			}
		}
	}
}

func TestACPUnknownModelFailsBeforePrompt(t *testing.T) {
	_, err := runFake(t, fakeHarness(t, "ok"), model.AgentRequest{Role: model.AgentBuilder, ProviderID: "anthropic", ModelID: "claude", Prompt: "go"})
	if err == nil || !errors.Is(err, model.ErrPermanent) || !strings.Contains(err.Error(), "anthropic/claude") || !strings.Contains(err.Error(), "log in to its provider") {
		t.Fatalf("err = %v", err)
	}
}

func TestACPPermissionPolicy(t *testing.T) {
	ws := t.TempDir()
	cases := []struct {
		role       model.AgentRole
		kind, path string
		want       string
	}{
		{model.AgentBuilder, "edit", ws + "/main.go", "yes"},
		{model.AgentBuilder, "read", "/etc/passwd", "no"},
		{model.AgentBuilder, "edit", ws + "/../other/x.go", "no"},
		{model.AgentInspector, "read", ws + "/main.go", "yes"},
		{model.AgentInspector, "edit", ws + "/main.go", "no"},
		{model.AgentInspector, "execute", ws, "no"},
		{model.AgentChat, "read", ws + "/main.go", "no"},
	}
	for _, c := range cases {
		res, err := runFake(t, fakeHarness(t, "permission", "ACP_FAKE_KIND="+c.kind, "ACP_FAKE_PATH="+c.path),
			model.AgentRequest{Role: c.role, WorkspaceDir: ws, Prompt: "go"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Text != c.want {
			t.Fatalf("%s %s %s: answered %q, want %q", c.role, c.kind, c.path, res.Text, c.want)
		}
	}
}

func TestACPFailures(t *testing.T) {
	_, err := runFake(t, fakeHarness(t, "quota"), model.AgentRequest{Role: model.AgentBuilder, Prompt: "go"})
	if err == nil || !strings.Contains(err.Error(), "Go usage limit exceeded") || !errors.Is(err, model.ErrPermanent) {
		t.Fatalf("quota err = %v", err)
	}
	_, err = runFake(t, fakeHarness(t, "funds"), model.AgentRequest{Role: model.AgentBuilder, Prompt: "go"})
	if err == nil || !strings.Contains(err.Error(), "Insufficient account funds") || !errors.Is(err, model.ErrPermanent) {
		t.Fatalf("funds err = %v", err)
	}
	_, err = runFake(t, fakeHarness(t, "refusal"), model.AgentRequest{Role: model.AgentBuilder, Prompt: "go"})
	if err == nil || !strings.Contains(err.Error(), "refusal") {
		t.Fatalf("refusal err = %v", err)
	}
	_, err = runFake(t, fakeHarness(t, "crash"), model.AgentRequest{Role: model.AgentBuilder, Prompt: "go"})
	if err == nil || !strings.Contains(err.Error(), "provider exploded") {
		t.Fatalf("crash err = %v", err)
	}
}

func TestACPCancel(t *testing.T) {
	h := fakeHarness(t, "hang")
	ctx := context.Background()
	ses, err := h.Start(ctx, model.AgentRequest{Role: model.AgentBuilder, WorkspaceDir: t.TempDir(), Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Cancel(ctx, ses.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Result(ctx, ses.ID()); err == nil || err.Error() != "cancelled" {
		t.Fatalf("err = %v", err)
	}
	if err := h.Cancel(ctx, "nope"); err != model.ErrAgentSessionNotFound {
		t.Fatalf("unknown session err = %v", err)
	}
}

func TestACPModels(t *testing.T) {
	models, err := fakeHarness(t, "ok").Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 || models[1].ProviderID != "opencode-go" || models[1].ModelID != "kimi" || models[1].Name != "opencode-go/Kimi" {
		t.Fatalf("models = %+v", models)
	}
}

func TestACPUnqualifiedModelsUseHarnessProvider(t *testing.T) {
	h := fakeHarness(t, "ok")
	h.Provider = "claude"
	models, err := h.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m := models[2]; m.ProviderID != "claude" || m.ModelID != "sonnet" {
		t.Fatalf("models = %+v", models)
	}
	res, err := runFake(t, h, model.AgentRequest{Role: model.AgentBuilder, ProviderID: "claude", ModelID: "sonnet", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, `"model":"model=sonnet"`) {
		t.Fatalf("text = %s", res.Text)
	}
}

func TestACPHarnessName(t *testing.T) {
	if n := NewACPHarness([]string{"/opt/homebrew/bin/claude-agent-acp"}, "").Name(); n != "claude-acp" {
		t.Fatalf("name = %s", n)
	}
	if n := NewACPHarness([]string{"npx", "@agentclientprotocol/claude-agent-acp"}, "").Name(); n != "claude-acp" {
		t.Fatalf("wrapped name = %s", n)
	}
	if n := NewACPHarness([]string{"opencode", "acp"}, "").Name(); n != "opencode-acp" {
		t.Fatalf("name = %s", n)
	}
}

func TestACPKeptSessionTakesFurtherPrompts(t *testing.T) {
	h := fakeHarness(t, "multi")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ws := t.TempDir()
	ses, err := h.Start(ctx, model.AgentRequest{Role: model.AgentForeman, Prompt: "first", WorkspaceDir: ws, Keep: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.Result(ctx, ses.ID())
	if err != nil || res.Text != "turn 1: first" || res.CostMicros != 10000 {
		t.Fatalf("first turn: %+v, %v", res, err)
	}
	var streamed string
	next, err := h.Continue(ctx, ses.ID(), model.AgentRequest{Role: model.AgentForeman, Prompt: "second", WorkspaceDir: ws, OnText: func(s string) { streamed += s }})
	if err != nil {
		t.Fatal(err)
	}
	res, err = h.Result(ctx, next.ID())
	if err != nil || res.Text != "turn 2: second" || res.CostMicros != 10000 || res.TokensOut != 2 {
		t.Fatalf("a continued turn reports its own text and cost: %+v, %v", res, err)
	}
	if streamed != "turn 2: second" {
		t.Fatalf("a continued turn streams to its own OnText: %q", streamed)
	}
	h.Close(ses.ID())
	if _, err := h.Continue(ctx, ses.ID(), model.AgentRequest{Prompt: "third"}); !errors.Is(err, model.ErrAgentSessionNotFound) {
		t.Fatalf("continue after Close: %v", err)
	}
}

func TestACPSessionWithoutKeepIsNotContinuable(t *testing.T) {
	h := fakeHarness(t, "multi")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ses, err := h.Start(ctx, model.AgentRequest{Role: model.AgentArchitect, Prompt: "one", WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Result(ctx, ses.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Continue(ctx, ses.ID(), model.AgentRequest{Prompt: "two"}); !errors.Is(err, model.ErrAgentSessionNotFound) {
		t.Fatalf("continue without Keep: %v", err)
	}
}

func TestACPIdleKeptSessionExpires(t *testing.T) {
	h := fakeHarness(t, "multi")
	h.IdleTTL = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ses, err := h.Start(ctx, model.AgentRequest{Role: model.AgentForeman, Prompt: "first", WorkspaceDir: t.TempDir(), Keep: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Result(ctx, ses.ID()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		n := len(h.kept)
		h.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := h.Continue(ctx, ses.ID(), model.AgentRequest{Prompt: "late"}); !errors.Is(err, model.ErrAgentSessionNotFound) {
		t.Fatalf("an idle session must expire: %v", err)
	}
}

func TestACPContinueRacingCloseLeavesNothingParked(t *testing.T) {
	for i := 0; i < 8; i++ {
		h := fakeHarness(t, "multi")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		ses, err := h.Start(ctx, model.AgentRequest{Role: model.AgentForeman, Prompt: "first", WorkspaceDir: t.TempDir(), Keep: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.Result(ctx, ses.ID()); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var next model.AgentSession
		wg.Add(2)
		go func() { defer wg.Done(); next, _ = h.Continue(ctx, ses.ID(), model.AgentRequest{Prompt: "second"}) }()
		go func() { defer wg.Done(); h.Close(ses.ID()) }()
		wg.Wait()
		if next != nil {
			_, _ = h.Result(ctx, next.ID())
		}
		h.mu.Lock()
		parked := len(h.kept)
		h.mu.Unlock()
		cancel()
		if parked != 0 {
			t.Fatalf("iteration %d: a closed session stayed parked", i)
		}
	}
}

func TestACPSteerInjectsIntoRunningTurn(t *testing.T) {
	h := fakeHarness(t, "steer")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ses, err := h.Start(ctx, model.AgentRequest{Role: model.AgentBuilder, Prompt: "go", WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Steer(ctx, ses.ID(), "use the helper in util.ts"); err != nil {
		t.Fatalf("steer: %v", err)
	}
	res, err := h.Result(ctx, ses.ID())
	if err != nil || res.Text != "steered: use the helper in util.ts" {
		t.Fatalf("result %q, %v", res.Text, err)
	}
	if err := h.Steer(ctx, ses.ID(), "late"); !errors.Is(err, model.ErrAgentSessionNotFound) {
		t.Fatalf("steer after the turn: %v", err)
	}
}

func TestACPSteerRacingTeardownIsSessionGone(t *testing.T) {
	h := fakeHarness(t, "steer-exit")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ses, err := h.Start(ctx, model.AgentRequest{Role: model.AgentBuilder, Prompt: "go", WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Steer(ctx, ses.ID(), "hi"); !errors.Is(err, model.ErrAgentSessionNotFound) {
		t.Fatalf("steer while the agent exits: %v; want ErrAgentSessionNotFound (a 409, not a 500)", err)
	}
}

func TestACPSteerUnsupported(t *testing.T) {
	h := fakeHarness(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ses, err := h.Start(ctx, model.AgentRequest{Role: model.AgentBuilder, Prompt: "go", WorkspaceDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Cancel(ctx, ses.ID())
	if err := h.Steer(ctx, ses.ID(), "hi"); !errors.Is(err, model.ErrSteeringUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestACPImageBlock(t *testing.T) {
	h := fakeHarness(t, "echo")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ws := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\nbytes")
	jpg := []byte("\xff\xd8\xffjpg")
	imgs := []model.AgentImage{{MediaType: "image/png", Data: png}, {MediaType: "image/jpeg", Data: jpg}}
	want := func(text string) string {
		b, _ := json.Marshal([]any{
			map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(png)},
			map[string]any{"type": "image", "mimeType": "image/jpeg", "data": base64.StdEncoding.EncodeToString(jpg)},
			map[string]any{"type": "text", "text": text},
		})
		return string(b)
	}
	ses, err := h.Start(ctx, model.AgentRequest{Role: model.AgentChat, Prompt: "look", WorkspaceDir: ws, Keep: true, Images: imgs})
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.Result(ctx, ses.ID())
	if err != nil || res.Text != want("look") {
		t.Fatalf("start prompt = %q, %v", res.Text, err)
	}
	next, err := h.Continue(ctx, ses.ID(), model.AgentRequest{Role: model.AgentChat, Prompt: "again", WorkspaceDir: ws, Images: imgs})
	if err != nil {
		t.Fatal(err)
	}
	res, err = h.Result(ctx, next.ID())
	if err != nil || res.Text != want("again") {
		t.Fatalf("continue prompt = %q, %v", res.Text, err)
	}
}

func TestACPNoImagesPayloadUnchanged(t *testing.T) {
	h := fakeHarness(t, "echo")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ws := t.TempDir()
	const want = `[{"text":"hi","type":"text"}]`
	ses, err := h.Start(ctx, model.AgentRequest{Role: model.AgentChat, Prompt: "hi", WorkspaceDir: ws, Keep: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.Result(ctx, ses.ID())
	if err != nil || res.Text != want {
		t.Fatalf("start prompt = %q, %v", res.Text, err)
	}
	next, err := h.Continue(ctx, ses.ID(), model.AgentRequest{Role: model.AgentChat, Prompt: "hi", WorkspaceDir: ws})
	if err != nil {
		t.Fatal(err)
	}
	res, err = h.Result(ctx, next.ID())
	if err != nil || res.Text != want {
		t.Fatalf("continue prompt = %q, %v", res.Text, err)
	}
}
