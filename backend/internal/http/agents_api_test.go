package http

import (
	"net/http"
	"strings"
	"testing"

	"legos-factory/backend/internal/store"
)

func TestAgentAccountAPIEndpoints(t *testing.T) {
	srv, _ := bootRevamp(t)
	const base = "/api/v1/agents/claude/accounts"
	body := `{"label":"DS","api":{"provider":"deepseek","base_url":"https://api.deepseek.com/anthropic","api_key":"sk-http-secret","models":{"opus":"deepseek-flash","sonnet":"deepseek-pro"}}}`
	if code, _ := chatDo(srv, "eng", "POST", base, body); code != http.StatusBadRequest {
		t.Fatalf("no sealer: %d", code)
	}
	box, _ := store.NewSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	srv.Agents.Secrets = box

	code, out := chatDo(srv, "eng", "POST", base, body)
	if code != http.StatusCreated || strings.Contains(out, "sk-http-secret") || !strings.Contains(out, `"has_key":true`) || !strings.Contains(out, `"login_command":""`) {
		t.Fatalf("create: %d %s", code, out)
	}
	if code, _ := chatDo(srv, "eng", "POST", "/api/v1/agents/codex/accounts", body); code != http.StatusBadRequest {
		t.Fatalf("api on codex: %d", code)
	}
	if code, _ := chatDo(srv, "eng", "POST", base, strings.Replace(body, "https://", "http://", 1)); code != http.StatusBadRequest {
		t.Fatalf("http url: %d", code)
	}
	aid := out[strings.Index(out, `"id":"`)+6:]
	aid = aid[:strings.Index(aid, `"`)]
	put := `{"provider":"deepseek","base_url":"https://api.deepseek.com/anthropic","api_key":"","models":{"opus":"deepseek-next"}}`
	code, out = chatDo(srv, "eng", "PUT", base+"/"+aid+"/api", put)
	if code != http.StatusOK || !strings.Contains(out, "deepseek-next") || !strings.Contains(out, `"has_key":true`) || strings.Contains(out, "sk-http-secret") {
		t.Fatalf("put: %d %s", code, out)
	}
	if code, _ := chatDo(srv, "eng2", "PUT", base+"/"+aid+"/api", put); code != http.StatusNotFound {
		t.Fatalf("another user: %d", code)
	}
	if code, _ := chatDo(srv, "eng", "PUT", base+"/"+aid+"/api", `{"base_url":"nope"}`); code != http.StatusBadRequest {
		t.Fatalf("invalid: %d", code)
	}
}
