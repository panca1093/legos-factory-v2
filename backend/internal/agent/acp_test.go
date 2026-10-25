package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
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
	var cwd, chosenModel string
	var promptID any
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
			reply(m.ID, map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "Fake", "version": "0"}})
		case "session/new":
			var p struct{ Cwd string }
			_ = json.Unmarshal(m.Params, &p)
			cwd = p.Cwd
			reply(m.ID, map[string]any{"sessionId": "fake-ses", "configOptions": []any{
				map[string]any{"id": "mode", "category": "mode", "currentValue": "build", "options": []any{map[string]any{"value": "build", "name": "Build"}}},
				map[string]any{"id": "model", "category": "model", "currentValue": "opencode/free-a", "options": []any{
					map[string]any{"value": "opencode/free-a", "name": "opencode/Free A"},
					map[string]any{"value": "opencode-go/kimi", "name": "opencode-go/Kimi"},
				}},
			}})
		case "session/set_config_option":
			var p struct{ ConfigID, Value string }
			_ = json.Unmarshal(m.Params, &p)
			chosenModel = p.ConfigID + "=" + p.Value
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
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": `"model":"` + chosenModel + `","config":` + fmt.Sprintf("%q", os.Getenv("OPENCODE_CONFIG_CONTENT")) + `}`}})
				update(map[string]any{"sessionUpdate": "usage_update", "cost": map[string]any{"amount": 0.0123, "currency": "USD"}})
				reply(m.ID, map[string]any{"stopReason": "end_turn", "usage": map[string]any{"inputTokens": 100, "outputTokens": 20}})
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
	ws := t.TempDir()
	res, err := runFake(t, fakeHarness(t, "ok"), model.AgentRequest{
		Role: model.AgentInspector, ProviderID: "opencode-go", ModelID: "kimi", WorkspaceDir: ws, Prompt: "go",
		OnStep: func(s model.AgentStep) { steps = append(steps, s) },
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
	if res.TokensIn != 100 || res.TokensOut != 20 || res.CostMicros != 12300 || res.CostSource != "measured" || res.SessionRef != "fake-ses" {
		t.Fatalf("result = %+v", res)
	}
}

func TestACPRoleConfig(t *testing.T) {
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
	if err == nil || !errors.Is(err, model.ErrPermanent) || !strings.Contains(err.Error(), "anthropic/claude") || !strings.Contains(err.Error(), "opencode auth login") {
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
	if len(models) != 2 || models[1].ProviderID != "opencode-go" || models[1].ModelID != "kimi" || models[1].Name != "opencode-go/Kimi" {
		t.Fatalf("models = %+v", models)
	}
}
