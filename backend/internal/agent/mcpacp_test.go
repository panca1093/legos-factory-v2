package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func claudeFake(t *testing.T, scenario string, env ...string) *ACPHarness {
	t.Helper()
	h := fakeHarness(t, scenario, env...)
	h.Provider = "claude"
	return h
}

func sentSession(t *testing.T, h *ACPHarness, req model.AgentRequest) (mcp string, disallowed []string) {
	t.Helper()
	req.Prompt = "go"
	if req.ModelID == "" {
		req.ProviderID, req.ModelID = "claude", "sonnet"
	}
	res, err := runFake(t, h, req)
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ Meta, MCP string }
	if err := json.Unmarshal([]byte(res.Text), &s); err != nil {
		t.Fatalf("reply %q: %v", res.Text, err)
	}
	var meta struct {
		ClaudeCode struct {
			Options struct{ DisallowedTools []string }
		}
	}
	_ = json.Unmarshal([]byte(s.Meta), &meta)
	return s.MCP, meta.ClaudeCode.Options.DisallowedTools
}

func TestACPClaudeSessionCarriesMCPServersAndDeniedTools(t *testing.T) {
	servers := []model.MCPServer{
		{Name: "docs", Transport: model.MCPHTTP, URL: "https://x/mcp", Secret: map[string]string{"Authorization": "Bearer t"}},
		{Name: "files", Transport: model.MCPStdio, Command: "/bin/f", Args: []string{"-q"}, Secret: map[string]string{"K": "V"}},
	}
	deny := []string{"mcp__docs__drop", "mcp__files__rm"}
	before, _ := json.Marshal([]any{claudeReadOnlyMeta, claudeBuilderMeta, claudeChatMeta})
	for i := 0; i < 2; i++ {
		for _, role := range []model.AgentRole{model.AgentInspector, model.AgentBuilder} {
			mcp, dis := sentSession(t, claudeFake(t, "ok"), model.AgentRequest{Role: role, MCP: servers, MCPDeny: deny})
			want := `[{"headers":[{"name":"Authorization","value":"Bearer t"}],"name":"docs","type":"http","url":"https://x/mcp"},` +
				`{"args":["-q"],"command":"/bin/f","env":[{"name":"K","value":"V"}],"name":"files"}]`
			if mcp != want {
				t.Fatalf("%s: mcpServers = %s\nwant %s", role, mcp, want)
			}
			if !strings.HasSuffix(strings.Join(dis, ","), "mcp__docs__drop,mcp__files__rm") {
				t.Fatalf("%s: disallowedTools = %v", role, dis)
			}
			if role == model.AgentInspector && !slices.Contains(dis, "Bash") {
				t.Fatalf("the role's own denials must stay: %v", dis)
			}
		}
	}
	if after, _ := json.Marshal([]any{claudeReadOnlyMeta, claudeBuilderMeta, claudeChatMeta}); string(after) != string(before) {
		t.Fatalf("shared meta maps were mutated:\n%s\n%s", before, after)
	}
	if _, dis := sentSession(t, claudeFake(t, "ok"), model.AgentRequest{Role: model.AgentInspector}); slices.Contains(dis, "mcp__docs__drop") {
		t.Fatalf("a later start must not inherit an earlier deny list: %v", dis)
	}
}

func TestACPWithoutMCPSendsEmptyListAndOpenCodeNever(t *testing.T) {
	if mcp, _ := sentSession(t, claudeFake(t, "ok"), model.AgentRequest{Role: model.AgentBuilder}); mcp != "[]" {
		t.Fatalf("no selection must send []: %s", mcp)
	}
	h := fakeHarness(t, "ok")
	h.Provider = "opencode"
	mcp, _ := sentSession(t, h, model.AgentRequest{Role: model.AgentBuilder, ProviderID: "opencode", ModelID: "free-a", MCP: []model.MCPServer{{Name: "docs", Transport: model.MCPHTTP, URL: "https://x"}}, MCPDeny: []string{"mcp__docs__a"}})
	if mcp != "[]" {
		t.Fatalf("OpenCode must get no servers: %s", mcp)
	}
}

func TestACPMCPPermissionAllowsOnlyEnabledTools(t *testing.T) {
	ws := t.TempDir()
	allow := []string{"mcp__docs__search"}
	cases := []struct {
		role       model.AgentRole
		tool, kind string
		want       string
	}{
		{model.AgentInspector, "mcp__docs__search", "other", "yes"},
		{model.AgentArchitect, "mcp__docs__search", "other", "yes"},
		{model.AgentChat, "mcp__docs__search", "other", "yes"},
		{model.AgentBuilder, "mcp__docs__search", "other", "yes"},
		{model.AgentInspector, "mcp__docs__write", "other", "no"},
		{model.AgentChat, "mcp__docs__write", "other", "no"},
		{model.AgentBuilder, "mcp__docs__write", "other", "no"},
		{model.AgentBuilder, "mcp__other__search", "other", "no"},
		{model.AgentBuilder, "Bash", "execute", "yes"},
		{model.AgentInspector, "Read", "read", "yes"},
		{model.AgentInspector, "Bash", "execute", "no"},
	}
	for _, c := range cases {
		res, err := runFake(t, claudeFake(t, "permission", "ACP_FAKE_KIND="+c.kind, "ACP_FAKE_TOOLNAME="+c.tool),
			model.AgentRequest{Role: c.role, WorkspaceDir: ws, Prompt: "go", MCPAllow: allow})
		if err != nil {
			t.Fatal(err)
		}
		if res.Text != c.want {
			t.Fatalf("%s %s: answered %q, want %q", c.role, c.tool, res.Text, c.want)
		}
	}
}

func TestACPMCPAllowSetSurvivesContinue(t *testing.T) {
	ws := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, c := range []struct{ tool, widened, want string }{
		{"mcp__docs__search", "", "yes"},
		{"mcp__docs__write", "mcp__docs__write", "no"},
	} {
		h := claudeFake(t, "permission", "ACP_FAKE_KIND=other", "ACP_FAKE_TOOLNAME="+c.tool)
		ses, err := h.Start(ctx, model.AgentRequest{Role: model.AgentChat, WorkspaceDir: ws, Prompt: "one", Keep: true, MCPAllow: []string{"mcp__docs__search"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.Result(ctx, ses.ID()); err != nil {
			t.Fatal(err)
		}
		again := model.AgentRequest{Role: model.AgentChat, WorkspaceDir: ws, Prompt: "two", MCPAllow: []string{c.widened}}
		next, err := h.Continue(ctx, ses.ID(), again)
		if err != nil {
			t.Fatal(err)
		}
		if res, err := h.Result(ctx, next.ID()); err != nil || res.Text != c.want {
			t.Fatalf("%s: continued turn answered %q (%v), want %q from the Start-time set", c.tool, res.Text, err, c.want)
		}
		h.Close(ses.ID())
	}
}
