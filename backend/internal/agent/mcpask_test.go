package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func askReq(role model.AgentRole, ws string, onPerm func(context.Context, model.PermissionRequest) bool) model.AgentRequest {
	return model.AgentRequest{Role: role, WorkspaceDir: ws, Prompt: "go", MCPAllow: []string{"mcp__docs__search", "mcp__docs__write"}, MCPAsk: []string{"mcp__docs__write"}, OnPermission: onPerm}
}

func TestACPAskToolConsultsCallback(t *testing.T) {
	ws := t.TempDir()
	for _, c := range []struct {
		name string
		tool string
		cb   func(context.Context, model.PermissionRequest) bool
		want string
	}{
		{"allow", "mcp__docs__write", func(context.Context, model.PermissionRequest) bool { return true }, "yes"},
		{"deny", "mcp__docs__write", func(context.Context, model.PermissionRequest) bool { return false }, "no"},
		{"nil callback rejects", "mcp__docs__write", nil, "no"},
		{"read-only tool skips the callback", "mcp__docs__search", func(context.Context, model.PermissionRequest) bool { t.Error("callback must not run"); return false }, "yes"},
	} {
		res, err := runFake(t, claudeFake(t, "permission", "ACP_FAKE_KIND=other", "ACP_FAKE_TOOLNAME="+c.tool), askReq(model.AgentChat, ws, c.cb))
		if err != nil || res.Text != c.want {
			t.Fatalf("%s: answered %q (%v), want %q", c.name, res.Text, err, c.want)
		}
	}
}

func TestACPAskCallbackSeesServerToolAndInput(t *testing.T) {
	var got model.PermissionRequest
	_, err := runFake(t, claudeFake(t, "permission", "ACP_FAKE_KIND=other", "ACP_FAKE_TOOLNAME=mcp__docs__write", `ACP_FAKE_INPUT={"id":7}`),
		askReq(model.AgentChat, t.TempDir(), func(_ context.Context, p model.PermissionRequest) bool { got = p; return true }))
	if err != nil || got.Server != "docs" || got.Tool != "write" || got.Input["id"] != float64(7) || got.ID == "" {
		t.Fatalf("callback got %+v (%v)", got, err)
	}
}

func TestACPAskToolOutsideAllowIsRejectedWithoutCallback(t *testing.T) {
	req := askReq(model.AgentChat, t.TempDir(), func(context.Context, model.PermissionRequest) bool { t.Error("callback must not run"); return true })
	req.MCPAllow = []string{"mcp__docs__search"}
	res, err := runFake(t, claudeFake(t, "permission", "ACP_FAKE_KIND=other", "ACP_FAKE_TOOLNAME=mcp__docs__write"), req)
	if err != nil || res.Text != "no" {
		t.Fatalf("answered %q (%v)", res.Text, err)
	}
}

func TestACPAskDoesNotBlockReadLoop(t *testing.T) {
	var mu sync.Mutex
	var text strings.Builder
	req := askReq(model.AgentChat, t.TempDir(), func(ctx context.Context, _ model.PermissionRequest) bool {
		deadline := time.After(5 * time.Second)
		for {
			mu.Lock()
			seen := strings.Contains(text.String(), "while-asking;")
			mu.Unlock()
			if seen {
				return true
			}
			select {
			case <-deadline:
				return false
			case <-time.After(10 * time.Millisecond):
			}
		}
	})
	req.OnText = func(s string) { mu.Lock(); text.WriteString(s); mu.Unlock() }
	res, err := runFake(t, claudeFake(t, "permission-live", "ACP_FAKE_KIND=other", "ACP_FAKE_TOOLNAME=mcp__docs__write"), req)
	if err != nil || !strings.HasSuffix(res.Text, "yes") {
		t.Fatalf("an update must be processed while the callback waits: %q (%v)", res.Text, err)
	}
}

func TestACPAskCallbackIsSwappedOnContinueAndClearedWhenParked(t *testing.T) {
	ws := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := claudeFake(t, "permission", "ACP_FAKE_KIND=other", "ACP_FAKE_TOOLNAME=mcp__docs__write")
	var first, second int
	req := askReq(model.AgentChat, ws, func(context.Context, model.PermissionRequest) bool { first++; return true })
	req.Keep = true
	ses, err := h.Start(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := h.Result(ctx, ses.ID()); err != nil || res.Text != "yes" {
		t.Fatalf("%q %v", res.Text, err)
	}
	h.mu.Lock()
	parked := h.kept[ses.ID()].conn
	h.mu.Unlock()
	parked.mu.Lock()
	cleared := parked.onPerm == nil
	parked.mu.Unlock()
	if !cleared {
		t.Fatal("a parked session must not keep the finished turn's callback")
	}
	next := askReq(model.AgentChat, ws, func(context.Context, model.PermissionRequest) bool { second++; return false })
	next.MCPAsk = nil
	cont, err := h.Continue(ctx, ses.ID(), next)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := h.Result(ctx, cont.ID()); err != nil || res.Text != "no" || first != 1 || second != 1 {
		t.Fatalf("continued turn: %q %v, callbacks %d/%d; Continue must use the new callback and keep the Start-time ask set", res.Text, err, first, second)
	}
	h.Close(ses.ID())
}

func TestACPReadOnlyRolesRunAllowedWritingMCPToolWithoutAsking(t *testing.T) {
	ws := t.TempDir()
	for _, role := range []model.AgentRole{model.AgentForeman, model.AgentArchitect, model.AgentInspector, model.AgentBuilder} {
		res, err := runFake(t, claudeFake(t, "permission", "ACP_FAKE_KIND=other", "ACP_FAKE_TOOLNAME=mcp__docs__write"),
			model.AgentRequest{Role: role, WorkspaceDir: ws, Prompt: "go", MCPAllow: []string{"mcp__docs__write"}})
		if err != nil || res.Text != "yes" {
			t.Fatalf("%s: answered %q (%v)", role, res.Text, err)
		}
	}
}

func TestACPReadOnlyMetaWhitelistsNoToolThatHidesMCP(t *testing.T) {
	servers := []model.MCPServer{{Name: "docs", Transport: model.MCPHTTP, URL: "https://x"}}
	for _, role := range []model.AgentRole{model.AgentArchitect, model.AgentInspector, model.AgentForeman} {
		res, err := runFake(t, claudeFake(t, "ok"), model.AgentRequest{Role: role, Prompt: "go", ProviderID: "claude", ModelID: "sonnet", MCP: servers, MCPAllow: []string{"mcp__docs__write"}})
		if err != nil {
			t.Fatal(err)
		}
		var s struct{ Meta, MCP string }
		if err := json.Unmarshal([]byte(res.Text), &s); err != nil {
			t.Fatal(err)
		}
		var meta struct {
			ClaudeCode struct {
				Options struct{ Tools, DisallowedTools []string }
			}
		}
		_ = json.Unmarshal([]byte(s.Meta), &meta)
		o := meta.ClaudeCode.Options
		if !strings.Contains(s.MCP, "docs") || slices.Contains(o.DisallowedTools, "mcp__docs__write") {
			t.Fatalf("%s: the allowed MCP tool must reach the session: mcp %s, disallowed %v", role, s.MCP, o.DisallowedTools)
		}
		for _, name := range o.Tools {
			if strings.HasPrefix(name, "mcp__") {
				t.Fatalf("%s: tools whitelist lists MCP entries: %v", role, o.Tools)
			}
		}
	}
}
