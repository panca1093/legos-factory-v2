package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/agent"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

func tinyMCP(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		var m struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		if r.Header.Get("Authorization") != "Bearer sekret-token" {
			nethttp.Error(w, "no", nethttp.StatusUnauthorized)
			return
		}
		if m.ID == nil {
			w.WriteHeader(nethttp.StatusAccepted)
			return
		}
		res := map[string]any{"protocolVersion": "2025-03-26"}
		if m.Method == "tools/list" {
			res = map[string]any{"tools": []any{
				map[string]any{"name": "search", "description": "find", "inputSchema": map[string]any{}, "annotations": map[string]any{"readOnlyHint": true}},
				map[string]any{"name": "write", "description": "change", "inputSchema": map[string]any{}},
			}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": res})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func bootMCP(t *testing.T) *Server { return bootMCPLog(t, testDiscard{}) }

func bootMCPLog(t *testing.T, logs io.Writer) *Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := New(&model.Config{FrontendOrigin: "http://localhost:5173"}, slog.New(slog.NewTextHandler(logs, nil)))
	srv.Auth = &usecase.Identity{Users: &store.UserRepo{DB: db}, Mode: "dev", DevUser: model.User{ID: "u1", Email: "a@b", Name: "A", Role: model.RoleOwner}}
	box, _ := store.NewSecretBox(bytes.Repeat([]byte{9}, 32))
	srv.MCP = &usecase.MCPServers{Store: &store.MCPRepo{DB: db}, Secrets: box, Lister: agent.ListMCPTools}
	for _, u := range []struct {
		id   string
		role model.Role
	}{{"eng", model.RoleEngineer}, {"adm", model.RoleAdmin}, {"adm2", model.RoleAdmin}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, role, created_at) VALUES (?,?,?,?,?)`, u.id, u.id+"@t", u.id, string(u.role), time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO sessions (id, user_id, expires_at, created_at) VALUES (?,?,?,?)`, "sess-"+u.id, u.id, time.Now().Add(time.Hour).Unix(), time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	return srv
}

func mcpDo(srv *Server, as, method, path, body string) (int, string) {
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&nethttp.Cookie{Name: "factory_session", Value: "sess-" + as})
	rec := httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestMCPAPIIsAdminOnly(t *testing.T) {
	srv := bootMCP(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/mcp-servers", ""},
		{"POST", "/mcp-servers", `{"name":"x","transport":"http","url":"https://x"}`},
		{"PATCH", "/mcp-servers/mcp_1", `{}`},
		{"DELETE", "/mcp-servers/mcp_1", ""},
		{"POST", "/mcp-servers/mcp_1/discover", ""},
		{"PUT", "/mcp-servers/mcp_1/roles", `{"roles":[]}`},
		{"PUT", "/mcp-servers/mcp_1/tools", `[]`},
	} {
		if code, body := mcpDo(srv, "eng", c.method, c.path, c.body); code != nethttp.StatusForbidden {
			t.Fatalf("engineer %s %s: %d %s; want 403", c.method, c.path, code, body)
		}
	}
}

func TestMCPAPIFlowAgainstARealServer(t *testing.T) {
	srv := bootMCP(t)
	mcp := tinyMCP(t)
	code, body := mcpDo(srv, "adm", "POST", "/mcp-servers", fmt.Sprintf(`{"name":"docs","transport":"http","url":%q,"secret":{"Authorization":"Bearer sekret-token"}}`, mcp.URL))
	var v model.MCPServerView
	if code != 201 || json.Unmarshal([]byte(body), &v) != nil || v.ID == "" || v.Status != "active" || strings.Join(v.SecretKeys, ",") != "Authorization" {
		t.Fatalf("create: %d %s", code, body)
	}
	if strings.Contains(body, "sekret-token") {
		t.Fatalf("create leaked the secret: %s", body)
	}
	if code, body = mcpDo(srv, "adm", "PUT", "/mcp-servers/"+v.ID+"/tools", `[{"name":"search","enabled":true}]`); code != 400 {
		t.Fatalf("enabling before discovery: %d %s", code, body)
	}
	if code, body = mcpDo(srv, "adm", "POST", "/mcp-servers/"+v.ID+"/discover", ""); code != 200 || !strings.Contains(body, `"name":"search"`) || !strings.Contains(body, `"read_only":true`) || !strings.Contains(body, `"last_error":""`) {
		t.Fatalf("discover: %d %s", code, body)
	}
	if code, body = mcpDo(srv, "adm", "PUT", "/mcp-servers/"+v.ID+"/roles", `{"roles":["inspector","chat"]}`); code != 200 {
		t.Fatalf("roles: %d %s", code, body)
	}
	if code, body = mcpDo(srv, "adm", "PUT", "/mcp-servers/"+v.ID+"/tools", `[{"name":"write","enabled":true}]`); code != 200 || !strings.Contains(body, `"read_only":false,"enabled":true`) {
		t.Fatalf("a writing tool on any roles: %d %s", code, body)
	}
	if code, body = mcpDo(srv, "adm", "PUT", "/mcp-servers/"+v.ID+"/tools", `[{"name":"search","enabled":true}]`); code != 200 || !strings.Contains(body, `"approx_tokens"`) {
		t.Fatalf("tools: %d %s", code, body)
	}
	if code, body = mcpDo(srv, "adm", "PATCH", "/mcp-servers/"+v.ID, `{"status":"disabled"}`); code != 200 || !strings.Contains(body, `"status":"disabled"`) {
		t.Fatalf("patch: %d %s", code, body)
	}
	if code, body = mcpDo(srv, "adm", "GET", "/mcp-servers", ""); code != 200 || strings.Contains(body, "sekret-token") || !strings.Contains(body, `"servers":[`) {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, body = mcpDo(srv, "adm", "PATCH", "/mcp-servers/"+v.ID, `{"secret":{"Authorization":"Bearer wrong"}}`); code != 200 {
		t.Fatalf("rotate: %d %s", code, body)
	}
	if code, body = mcpDo(srv, "adm", "POST", "/mcp-servers/"+v.ID+"/discover", ""); code != 200 || !strings.Contains(body, "401") {
		t.Fatalf("a failed discovery reports last_error: %d %s", code, body)
	}
	if code, _ = mcpDo(srv, "adm", "POST", "/mcp-servers", `{"name":"BAD","transport":"http","url":"https://x"}`); code != 400 {
		t.Fatalf("bad name: %d", code)
	}
	if code, _ = mcpDo(srv, "adm", "PUT", "/mcp-servers/nope/roles", `{"roles":[]}`); code != 404 {
		t.Fatalf("unknown id: %d", code)
	}
	if code, _ = mcpDo(srv, "adm", "DELETE", "/mcp-servers/"+v.ID, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
}
