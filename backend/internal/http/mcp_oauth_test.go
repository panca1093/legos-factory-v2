package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"legos-factory/backend/internal/mcpfake"
	"legos-factory/backend/internal/model"
)

func TestMCPOAuthRoutesAreAdminOnly(t *testing.T) {
	srv := bootMCP(t)
	for _, c := range []struct{ method, path string }{
		{"POST", "/mcp-servers/mcp_1/oauth/start"},
		{"GET", "/mcp/oauth/callback?code=c&state=s"},
	} {
		if code, body := mcpDo(srv, "eng", c.method, c.path, ""); code != nethttp.StatusForbidden {
			t.Fatalf("engineer %s %s: %d %s; want 403", c.method, c.path, code, body)
		}
	}
}

func TestMCPOAuthFlowOverHTTPLeaksNothing(t *testing.T) {
	var logs bytes.Buffer
	srv := bootMCPLog(t, &logs)
	fp := mcpfake.New(t)
	srv.MCP.HTTP, srv.MCP.CallbackURL = fp.Server.Client(), "http://localhost:5173/api/v1/mcp/oauth/callback"

	code, body := mcpDo(srv, "adm", "POST", "/mcp-servers", fmt.Sprintf(`{"name":"docs","transport":"http","url":%q,"auth":"oauth","client_id":"","scopes":"read"}`, fp.MCPURL()))
	var v model.MCPServerView
	if code != 201 || json.Unmarshal([]byte(body), &v) != nil || v.Auth != "oauth" || v.SignedIn || v.Status != "needs_sign_in" {
		t.Fatalf("create: %d %s", code, body)
	}
	var all []string
	all = append(all, body)

	code, body = mcpDo(srv, "adm", "POST", "/mcp-servers/"+v.ID+"/oauth/start", "")
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &start) != nil || !strings.HasPrefix(start.AuthorizeURL, fp.Server.URL+"/authorize?") {
		t.Fatalf("start: %d %s", code, body)
	}
	authCode, state, err := fp.Browser(start.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	cb := "/mcp/oauth/callback?code=" + url.QueryEscape(authCode) + "&state=" + url.QueryEscape(state)

	if code, _ = mcpDo(srv, "adm2", "GET", cb, ""); code != nethttp.StatusFound {
		t.Fatalf("other admin: %d", code)
	}
	if _, got := mcpDo(srv, "adm", "GET", "/mcp-servers", ""); strings.Contains(got, `"signed_in":true`) {
		t.Fatal("another admin's callback must not sign the server in")
	}
	req := httptest.NewRequest("GET", "/api/v1"+cb, nil)
	req.AddCookie(&nethttp.Cookie{Name: "factory_session", Value: "sess-adm"})
	rec := httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	if rec.Code != nethttp.StatusFound || rec.Header().Get("Location") != "http://localhost:5173/settings?mcp_oauth=error#connections" {
		t.Fatalf("a state another admin spent: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	code, body = mcpDo(srv, "adm", "POST", "/mcp-servers/"+v.ID+"/oauth/start", "")
	_ = json.Unmarshal([]byte(body), &start)
	authCode, state, _ = fp.Browser(start.AuthorizeURL)
	cb = "/mcp/oauth/callback?code=" + url.QueryEscape(authCode) + "&state=" + url.QueryEscape(state)
	req = httptest.NewRequest("GET", "/api/v1"+cb, nil)
	req.AddCookie(&nethttp.Cookie{Name: "factory_session", Value: "sess-adm"})
	rec = httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	if rec.Code != nethttp.StatusFound || rec.Header().Get("Location") != "http://localhost:5173/settings?mcp_oauth=ok#connections" {
		t.Fatalf("callback: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	all = append(all, rec.Body.String(), rec.Header().Get("Location"))

	code, body = mcpDo(srv, "adm", "POST", "/mcp-servers/"+v.ID+"/discover", "")
	if code != 200 || !strings.Contains(body, `"name":"search"`) {
		t.Fatalf("discover with the bearer: %d %s", code, body)
	}
	all = append(all, body)
	code, body = mcpDo(srv, "adm", "GET", "/mcp-servers", "")
	if code != 200 || !strings.Contains(body, `"signed_in":true`) || !strings.Contains(body, `"oauth_client":"dynamic"`) || !strings.Contains(body, `"expires_at":"`) {
		t.Fatalf("list: %d %s", code, body)
	}
	all = append(all, body, logs.String())

	secrets := []string{"access-", "refresh-", authCode, state, "dyn-1"}
	for _, f := range fp.Forms() {
		secrets = append(secrets, f.Get("code_verifier"))
	}
	for i, txt := range all {
		for _, s := range secrets {
			if s != "" && strings.Contains(txt, s) {
				t.Fatalf("output %d leaks %q: %s", i, s, txt)
			}
		}
	}
}

func TestMCPOAuthStartErrorsAreReadable(t *testing.T) {
	srv := bootMCP(t)
	fp := mcpfake.New(t)
	fp.NoDCR = true
	srv.MCP.HTTP, srv.MCP.CallbackURL = fp.Server.Client(), "http://localhost:5173/api/v1/mcp/oauth/callback"
	_, body := mcpDo(srv, "adm", "POST", "/mcp-servers", fmt.Sprintf(`{"name":"docs","transport":"http","url":%q,"auth":"oauth"}`, fp.MCPURL()))
	var v model.MCPServerView
	_ = json.Unmarshal([]byte(body), &v)
	code, body := mcpDo(srv, "adm", "POST", "/mcp-servers/"+v.ID+"/oauth/start", "")
	if code != 400 || !strings.Contains(body, "enter a client ID") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ = mcpDo(srv, "adm", "POST", "/mcp-servers/nope/oauth/start", ""); code != 404 {
		t.Fatalf("unknown server: %d", code)
	}
}
