package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type mcpRig struct {
	m     *MCPServers
	tools []model.MCPTool
	err   error
	got   []model.MCPServer
}

func newMCPRig(t *testing.T) *mcpRig {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, _ := store.NewSecretBox(bytes.Repeat([]byte{5}, 32))
	r := &mcpRig{tools: []model.MCPTool{
		{Name: "search", Description: "find", ReadOnly: true, Size: 400},
		{Name: "write", Description: "change", Size: 800},
	}}
	r.m = &MCPServers{Store: &store.MCPRepo{DB: db}, Secrets: box, Lister: func(_ context.Context, s model.MCPServer) ([]model.MCPTool, error) {
		r.got = append(r.got, s)
		return slices.Clone(r.tools), r.err
	}}
	return r
}

func (r *mcpRig) server(t *testing.T, name string) model.MCPServerView {
	t.Helper()
	v, err := r.m.Create(context.Background(), model.CreateMCPServer{Name: name, Transport: model.MCPHTTP, URL: "https://mcp.example/" + name,
		Secret: map[string]string{"Authorization": "Bearer sekret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (r *mcpRig) discovered(t *testing.T, name string, roles ...string) model.MCPServerView {
	t.Helper()
	ctx := context.Background()
	v := r.server(t, name)
	if _, err := r.m.Discover(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	v, err := r.m.SetRoles(ctx, v.ID, roles)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMCPCreateValidatesAndNeverReturnsSecrets(t *testing.T) {
	r := newMCPRig(t)
	ctx := context.Background()
	for _, bad := range []model.CreateMCPServer{
		{Name: "Bad-Name", Transport: model.MCPHTTP, URL: "https://x"},
		{Name: "a", Transport: "ws", URL: "https://x"},
		{Name: "a", Transport: model.MCPStdio, Command: "relative/bin"},
		{Name: "a", Transport: model.MCPHTTP, URL: "ftp://x"},
		{Name: "a", Transport: model.MCPHTTP, URL: "https://x", Secret: map[string]string{"bad header": "v"}},
		{Name: "a", Transport: model.MCPStdio, Command: "/bin/x", Secret: map[string]string{"1BAD": "v"}},
	} {
		if _, err := r.m.Create(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: err = %v; want ErrInvalid", bad, err)
		}
	}
	v := r.server(t, "docs")
	if v.Auth != model.MCPAuthToken || strings.Join(v.SecretKeys, ",") != "Authorization" {
		t.Fatalf("view: %+v", v)
	}
	if _, err := r.m.Create(ctx, model.CreateMCPServer{Name: "docs", Transport: model.MCPHTTP, URL: "https://y"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate name: %v", err)
	}
	all, _ := r.m.List(ctx)
	b, _ := json.Marshal(all)
	if strings.Contains(string(b), "sekret-token") {
		t.Fatalf("a secret leaked into the API shape: %s", b)
	}
	noBox := &MCPServers{Store: r.m.Store}
	if _, err := noBox.Create(ctx, model.CreateMCPServer{Name: "c", Transport: model.MCPHTTP, URL: "https://x", Secret: map[string]string{"A": "b"}}); !errors.Is(err, model.ErrSecretsUnavailable) {
		t.Fatalf("without a secret key a credential is refused: %v", err)
	}
	if _, err := noBox.Create(ctx, model.CreateMCPServer{Name: "d", Transport: model.MCPHTTP, URL: "https://x"}); err != nil {
		t.Fatalf("a server without credentials needs no key: %v", err)
	}
}

func TestMCPUpdateSecretReplaceAndDefinitionChangeDropsTools(t *testing.T) {
	r := newMCPRig(t)
	ctx := context.Background()
	v := r.discovered(t, "docs", "inspector")
	if _, err := r.m.SetTools(ctx, v.ID, []model.MCPToolToggle{{Name: "search", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	tok := map[string]string{"X-Api-Key": "k2"}
	v, err := r.m.Update(ctx, v.ID, model.UpdateMCPServer{Secret: &tok})
	if err != nil || strings.Join(v.SecretKeys, ",") != "X-Api-Key" || !v.Tools[0].Enabled {
		t.Fatalf("a secret replace keeps the tools: %+v %v", v, err)
	}
	if sel, _ := r.m.Selection(ctx, model.AgentInspector); sel.Servers[0].Secret["X-Api-Key"] != "k2" || len(sel.Servers[0].Secret) != 1 {
		t.Fatalf("selection must carry the replaced secret: %+v", sel.Servers)
	}
	url := "https://other.example/mcp"
	v, err = r.m.Update(ctx, v.ID, model.UpdateMCPServer{URL: &url})
	if err != nil || len(v.Tools) != 0 || v.DiscoveredAt != nil {
		t.Fatalf("pointing at another server must drop the tools: %+v %v", v, err)
	}
	off := "disabled"
	if v, err = r.m.Update(ctx, v.ID, model.UpdateMCPServer{Status: &off}); err != nil || v.Status != model.MCPDisabled {
		t.Fatalf("%+v %v", v, err)
	}
	bad := "paused"
	if _, err = r.m.Update(ctx, v.ID, model.UpdateMCPServer{Status: &bad}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad status: %v", err)
	}
}

func TestMCPDiscoverKeepsEnabledDropsVanishedStartsNewDisabled(t *testing.T) {
	r := newMCPRig(t)
	ctx := context.Background()
	v := r.server(t, "docs")
	if v, _ = r.m.Discover(ctx, v.ID); v.DiscoveredAt == nil || len(v.Tools) != 2 || v.Tools[0].Enabled || v.Tools[1].Enabled {
		t.Fatalf("first discovery: %+v", v)
	}
	if len(r.got) != 1 || r.got[0].Secret["Authorization"] != "Bearer sekret-token" {
		t.Fatalf("discovery must connect with the stored credentials: %+v", r.got)
	}
	if _, err := r.m.SetTools(ctx, v.ID, []model.MCPToolToggle{{Name: "search", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	r.tools = []model.MCPTool{{Name: "search", ReadOnly: true, Size: 500}, {Name: "fresh", ReadOnly: true, Size: 100}}
	v, _ = r.m.Discover(ctx, v.ID)
	byName := map[string]model.MCPTool{}
	for _, tl := range v.Tools {
		byName[tl.Name] = tl
	}
	if len(v.Tools) != 2 || !byName["search"].Enabled || byName["search"].Size != 500 || byName["fresh"].Enabled {
		t.Fatalf("after rediscovery: %+v", v.Tools)
	}
	if v.ApproxTokens != 125 {
		t.Fatalf("approx_tokens = %d, want enabled bytes/4 = 125", v.ApproxTokens)
	}
	r.tools = []model.MCPTool{{Name: "search", ReadOnly: false, Size: 500}}
	if v, _ = r.m.Discover(ctx, v.ID); v.Tools[0].Enabled {
		t.Fatal("a tool that turned into a writing tool must not stay enabled on a server that is not builder-only")
	}
	r.err = errors.New("dial tcp: refused")
	v, err := r.m.Discover(ctx, v.ID)
	if err != nil || v.LastError == "" || len(v.Tools) != 1 {
		t.Fatalf("a failed discovery records last_error and keeps the tools: %+v %v", v, err)
	}
}

func TestMCPWritingToolNeedsBuilderOnlyServer(t *testing.T) {
	r := newMCPRig(t)
	ctx := context.Background()
	if _, err := r.m.SetTools(ctx, r.server(t, "early").ID, []model.MCPToolToggle{{Name: "search", Enabled: true}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("enabling before discovery: %v", err)
	}
	v := r.discovered(t, "docs", "inspector", "builder")
	if _, err := r.m.SetTools(ctx, v.ID, []model.MCPToolToggle{{Name: "write", Enabled: true}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a writing tool on a shared server: %v", err)
	}
	if _, err := r.m.SetTools(ctx, v.ID, []model.MCPToolToggle{{Name: "nope", Enabled: true}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown tool: %v", err)
	}
	if _, err := r.m.SetTools(ctx, v.ID, []model.MCPToolToggle{{Name: "search", Enabled: true}}); err != nil {
		t.Fatalf("a read-only tool is fine anywhere: %v", err)
	}
	if _, err := r.m.SetRoles(ctx, v.ID, []string{"builder"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.SetTools(ctx, v.ID, []model.MCPToolToggle{{Name: "write", Enabled: true}}); err != nil {
		t.Fatalf("builder-only server may enable a writing tool: %v", err)
	}
	if _, err := r.m.SetRoles(ctx, v.ID, []string{"builder", "chat"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("widening roles while a writing tool is enabled: %v", err)
	}
	if _, err := r.m.SetRoles(ctx, v.ID, []string{"builder", "wizard"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown role: %v", err)
	}
}

func TestMCPSelectionPerRole(t *testing.T) {
	r := newMCPRig(t)
	ctx := context.Background()
	docs := r.discovered(t, "docs", "inspector", "chat")
	if _, err := r.m.SetTools(ctx, docs.ID, []model.MCPToolToggle{{Name: "search", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	ops := r.discovered(t, "ops", "builder")
	if _, err := r.m.SetTools(ctx, ops.ID, []model.MCPToolToggle{{Name: "write", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	idle := r.discovered(t, "idle", "inspector")
	_ = idle

	sel, _ := r.m.Selection(ctx, model.AgentInspector)
	if len(sel.Servers) != 1 || sel.Servers[0].Name != "docs" || !slices.Equal(sel.Allow, []string{"mcp__docs__search"}) || !slices.Equal(sel.Deny, []string{"mcp__docs__write"}) {
		t.Fatalf("inspector: %+v", sel)
	}
	if sel.Servers[0].Secret["Authorization"] != "Bearer sekret-token" {
		t.Fatalf("selection carries the decrypted header: %+v", sel.Servers[0])
	}
	sel, _ = r.m.Selection(ctx, model.AgentBuilder)
	if len(sel.Servers) != 1 || sel.Servers[0].Name != "ops" || !slices.Equal(sel.Allow, []string{"mcp__ops__write"}) || !slices.Equal(sel.Deny, []string{"mcp__ops__search"}) {
		t.Fatalf("builder: %+v", sel)
	}
	for _, role := range []model.AgentRole{model.AgentArchitect, model.AgentForeman, model.AgentCataloger} {
		if sel, _ = r.m.Selection(ctx, role); len(sel.Servers) != 0 {
			t.Fatalf("%s must get nothing: %+v", role, sel)
		}
	}
	off := model.MCPDisabled
	if _, err := r.m.Update(ctx, docs.ID, model.UpdateMCPServer{Status: &off}); err != nil {
		t.Fatal(err)
	}
	if sel, _ = r.m.Selection(ctx, model.AgentChat); len(sel.Servers) != 0 {
		t.Fatalf("a disabled server is not passed: %+v", sel)
	}
}

func TestMCPSelectionDropsWritingToolOnSharedServerEvenIfFlagged(t *testing.T) {
	r := newMCPRig(t)
	ctx := context.Background()
	v := r.discovered(t, "docs", "inspector")
	rec, _ := r.m.Store.Get(ctx, v.ID)
	rec.Tools[0].Enabled, rec.Tools[1].Enabled = true, true
	if err := r.m.Store.Update(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	sel, _ := r.m.Selection(ctx, model.AgentInspector)
	if !slices.Equal(sel.Allow, []string{"mcp__docs__search"}) || !slices.Contains(sel.Deny, "mcp__docs__write") {
		t.Fatalf("a writing tool must never reach a read-only role: %+v", sel)
	}
}

type fakeSelector struct {
	sel   model.MCPSelection
	err   error
	roles []model.AgentRole
}

func (f *fakeSelector) Selection(_ context.Context, role model.AgentRole) (model.MCPSelection, error) {
	f.roles = append(f.roles, role)
	return f.sel, f.err
}

var testSelection = model.MCPSelection{
	Servers: []model.MCPServer{{Name: "docs", Transport: model.MCPHTTP, URL: "https://x", Secret: map[string]string{"Authorization": "Bearer sekret-token"}}},
	Allow:   []string{"mcp__docs__search"}, Deny: []string{"mcp__docs__write"},
}

func TestRunAgentPassesMCPSelectionAndAuditsNamesOnly(t *testing.T) {
	r := newRig(t)
	sel := &fakeSelector{sel: testSelection}
	r.f.MCP = sel
	if _, _, err := r.f.runAgent(context.Background(), r.wo, model.AgentInspector, "p", "m", "look", nil, "", runOpts{}); err != nil {
		t.Fatal(err)
	}
	var got model.AgentRequest
	for _, q := range r.h.reqs {
		got = q
	}
	if got.Role != model.AgentInspector || len(got.MCP) != 1 || got.MCP[0].Name != "docs" || !slices.Equal(got.MCPAllow, testSelection.Allow) || !slices.Equal(got.MCPDeny, testSelection.Deny) {
		t.Fatalf("request: %+v", got)
	}
	if !slices.Equal(sel.roles, []model.AgentRole{model.AgentInspector}) {
		t.Fatalf("selection asked for %v", sel.roles)
	}
	evs := r.events(t, "agent_started")
	if len(evs) != 1 {
		t.Fatalf("events: %+v", evs)
	}
	b, _ := json.Marshal(evs[0].Payload)
	if !strings.Contains(string(b), `"mcp_servers":["docs"]`) || !strings.Contains(string(b), `"mcp_tools":["mcp__docs__search"]`) || strings.Contains(string(b), "sekret-token") {
		t.Fatalf("agent_started payload: %s", b)
	}
}

func TestRunAgentWithoutMCPPortOrSelectionIsUnchanged(t *testing.T) {
	for _, sel := range []MCPSelector{nil, &fakeSelector{}, &fakeSelector{err: errors.New("db down")}} {
		r := newRig(t)
		r.f.MCP = sel
		if _, _, err := r.f.runAgent(context.Background(), r.wo, model.AgentBuilder, "p", "m", "go", nil, "", runOpts{}); err != nil {
			t.Fatal(err)
		}
		for _, q := range r.h.reqs {
			if q.MCP != nil || q.MCPAllow != nil || q.MCPDeny != nil {
				t.Fatalf("%T: request carries MCP: %+v", sel, q)
			}
		}
		if b, _ := json.Marshal(r.events(t, "agent_started")[0].Payload); strings.Contains(string(b), "mcp_") {
			t.Fatalf("%T: payload %s", sel, b)
		}
	}
}

func TestChatStartsWithMCPSelectionForChatRole(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "ok"}
	r := newChatRig(t, h)
	sel := &fakeSelector{sel: testSelection}
	r.chat.MCP = sel
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	got := h.starts[0]
	if len(got.MCP) != 1 || !slices.Equal(got.MCPAllow, testSelection.Allow) || !slices.Equal(got.MCPDeny, testSelection.Deny) || !slices.Equal(sel.roles, []model.AgentRole{model.AgentChat}) {
		t.Fatalf("chat request: %+v, asked %v", got, sel.roles)
	}

	h2 := &chatPlain{reply: "ok"}
	r2 := newChatRig(t, h2)
	ch, _ = r2.chat.Create(ctx, r2.ana, "", "", "")
	if _, err := r2.send(t, r2.ana, ch.ID, ChatSend{Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if q := h2.starts[0]; q.MCP != nil || q.MCPAllow != nil || q.MCPDeny != nil {
		t.Fatalf("a nil port leaves the request unchanged: %+v", q)
	}
}

func TestMCPSelectionAllowsOnlyReadToolsForEveryRoleButBuilder(t *testing.T) {
	r := newMCPRig(t)
	ctx := context.Background()
	v := r.discovered(t, "docs", "foreman", "architect", "builder", "inspector", "chat")
	rec, _ := r.m.Store.Get(ctx, v.ID)
	rec.Tools[0].Enabled, rec.Tools[1].Enabled = true, true
	if err := r.m.Store.Update(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	for _, role := range []model.AgentRole{model.AgentForeman, model.AgentArchitect, model.AgentBuilder, model.AgentInspector, model.AgentChat} {
		sel, _ := r.m.Selection(ctx, role)
		if !slices.Equal(sel.Allow, []string{"mcp__docs__search"}) || !slices.Contains(sel.Deny, "mcp__docs__write") {
			t.Fatalf("%s: %+v", role, sel)
		}
	}
}
