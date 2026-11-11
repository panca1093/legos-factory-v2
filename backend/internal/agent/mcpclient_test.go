package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func mcpToolsPage(cursor string) map[string]any {
	if cursor == "" {
		return map[string]any{"tools": []any{
			map[string]any{"name": "search", "description": "find things", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}},
		}, "nextCursor": "p2"}
	}
	return map[string]any{"tools": []any{
		map[string]any{"name": "write", "description": "change things", "inputSchema": map[string]any{"type": "object"}},
	}}
}

func fakeMCPStdio(mode string) {
	in := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		var m struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		_ = json.Unmarshal(in.Bytes(), &m)
		if mode == "hang" || m.ID == nil {
			continue
		}
		fmt.Fprintln(os.Stderr, "log line on stderr")
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{}})
		switch m.Method {
		case "initialize":
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}}})
		case "tools/list":
			res := mcpToolsPage(m.Params.Cursor)
			if os.Getenv("MCP_TOKEN") != "t0k" {
				res = map[string]any{"tools": []any{}}
			}
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": res})
		}
	}
}

func fakeMCPServer(t *testing.T, sse bool) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		seen = append(seen, fmt.Sprintf("%s auth=%s sid=%s accept=%s", m.Method, r.Header.Get("Authorization"), r.Header.Get("Mcp-Session-Id"), r.Header.Get("Accept")))
		if m.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch m.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			result = map[string]any{"protocolVersion": "2025-03-26"}
		default:
			result = mcpToolsPage(m.Params.Cursor)
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\nevent: message\ndata: %s\n\n", body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func checkTools(t *testing.T, tools []model.MCPTool, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "search" || !tools[0].ReadOnly || tools[1].Name != "write" || tools[1].ReadOnly || tools[0].Enabled || tools[0].Size == 0 {
		t.Fatalf("tools = %+v; want search (read-only) then write across two pages, all disabled", tools)
	}
}

func TestMCPClientStdioListsToolsAcrossPagesWithEnv(t *testing.T) {
	tools, err := ListMCPTools(context.Background(), model.MCPServer{Name: "x", Transport: model.MCPStdio, Command: os.Args[0],
		Secret: map[string]string{"MCP_FAKE_SERVER": "ok", "MCP_TOKEN": "t0k"}})
	checkTools(t, tools, err)
}

func TestMCPClientStdioDoesNotInheritBackendEnv(t *testing.T) {
	t.Setenv("MCP_TOKEN", "t0k")
	tools, err := ListMCPTools(context.Background(), model.MCPServer{Name: "x", Transport: model.MCPStdio, Command: os.Args[0],
		Secret: map[string]string{"MCP_FAKE_SERVER": "ok"}})
	if err != nil || len(tools) != 0 {
		t.Fatalf("the backend's own environment must not reach the server: %+v %v", tools, err)
	}
}

func TestMCPClientStdioBadCommandAndTimeout(t *testing.T) {
	if _, err := ListMCPTools(context.Background(), model.MCPServer{Transport: model.MCPStdio, Command: "/nonexistent/mcp"}); err == nil {
		t.Fatal("a missing command must fail")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ListMCPTools(ctx, model.MCPServer{Transport: model.MCPStdio, Command: os.Args[0], Secret: map[string]string{"MCP_FAKE_SERVER": "hang"}})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("a silent server must time out: %v after %v", err, time.Since(start))
	}
}

func TestMCPClientHTTPJSONAndSSE(t *testing.T) {
	for _, sse := range []bool{false, true} {
		srv, seen := fakeMCPServer(t, sse)
		tools, err := ListMCPTools(context.Background(), model.MCPServer{Name: "x", Transport: model.MCPHTTP, URL: srv.URL,
			Secret: map[string]string{"Authorization": "Bearer abc"}})
		checkTools(t, tools, err)
		got := strings.Join(*seen, "\n")
		for _, want := range []string{
			"initialize auth=Bearer abc sid= accept=application/json, text/event-stream",
			"notifications/initialized auth=Bearer abc sid=sess-1",
			"tools/list auth=Bearer abc sid=sess-1",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("sse=%v: requests\n%s\nmissing %q", sse, got, want)
			}
		}
		if strings.Count(got, "tools/list") != 2 {
			t.Fatalf("sse=%v: tools/list must follow nextCursor:\n%s", sse, got)
		}
	}
}

func TestMCPClientHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "bad token", http.StatusUnauthorized) }))
	defer srv.Close()
	if _, err := ListMCPTools(context.Background(), model.MCPServer{Transport: model.MCPHTTP, URL: srv.URL}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}
