package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
		if mode == "roots" {
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "method": "roots/list"})
		}
		if mode == "spawn" && m.Method == "initialize" {
			child := exec.Command("sleep", "60")
			_ = child.Start()
			_ = os.WriteFile(os.Getenv("MCP_PIDFILE"), []byte(fmt.Sprint(child.Process.Pid)), 0o600)
		}
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

func TestMCPClientHTTPDoesNotFollowRedirects(t *testing.T) {
	var hit bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer srv.Close()
	_, err := ListMCPTools(context.Background(), model.MCPServer{Transport: model.MCPHTTP, URL: srv.URL, Secret: map[string]string{"Authorization": "Bearer abc"}})
	if err == nil || !strings.Contains(err.Error(), "302") || hit {
		t.Fatalf("a redirect must be an error naming 302 and must not be followed: %v, followed=%v", err, hit)
	}
}

func TestMCPClientSSEJoinsMultiLineData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		if m.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := any(map[string]any{"protocolVersion": "2025-03-26"})
		if m.Method == "tools/list" {
			result = mcpToolsPage("p2")
		}
		body, _ := json.MarshalIndent(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result}, "", " ")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message\n")
		for _, line := range strings.Split(string(body), "\n") {
			fmt.Fprintf(w, "data: %s\n", line)
		}
		fmt.Fprint(w, "\n")
	}))
	defer srv.Close()
	tools, err := ListMCPTools(context.Background(), model.MCPServer{Transport: model.MCPHTTP, URL: srv.URL})
	if err != nil || len(tools) != 1 || tools[0].Name != "write" {
		t.Fatalf("a reply split over several data lines must be read: %+v %v", tools, err)
	}
}

func TestMCPClientStdioIgnoresServerRequestsWithCollidingIDs(t *testing.T) {
	tools, err := ListMCPTools(context.Background(), model.MCPServer{Name: "x", Transport: model.MCPStdio, Command: os.Args[0],
		Secret: map[string]string{"MCP_FAKE_SERVER": "roots", "MCP_TOKEN": "t0k"}})
	checkTools(t, tools, err)
}

func TestMCPClientStdioKillsTheWholeProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	_, err := ListMCPTools(context.Background(), model.MCPServer{Name: "x", Transport: model.MCPStdio, Command: os.Args[0],
		Secret: map[string]string{"MCP_FAKE_SERVER": "spawn", "MCP_PIDFILE": pidFile, "MCP_TOKEN": "t0k"}})
	if err != nil {
		t.Fatal(err)
	}
	b, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatal(rerr)
	}
	pid, _ := strconv.Atoi(string(b))
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the server's child %d outlived the call", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
