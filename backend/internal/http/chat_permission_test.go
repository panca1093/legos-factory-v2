package http

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

type askHarness struct{ answer chan string }

func (askHarness) Name() string                         { return "ask" }
func (askHarness) Cancel(context.Context, string) error { return nil }
func (h askHarness) Start(ctx context.Context, req model.AgentRequest) (model.AgentSession, error) {
	if req.OnPermission == nil {
		return titleSession{}, nil
	}
	go func() {
		ok := req.OnPermission(ctx, model.PermissionRequest{Server: "docs", Tool: "write", Input: map[string]any{"id": 7}})
		if ok {
			h.answer <- "ran"
		} else {
			h.answer <- "refused"
		}
	}()
	return stepSession{}, nil
}
type titleSession struct{}

func (titleSession) ID() string { return "title" }

func (h askHarness) Result(ctx context.Context, id string) (model.AgentResult, error) {
	if id == "title" {
		return model.AgentResult{Text: "T"}, nil
	}
	select {
	case a := <-h.answer:
		return model.AgentResult{Text: a}, nil
	case <-ctx.Done():
		return model.AgentResult{}, ctx.Err()
	}
}

func TestChatPermissionRoute(t *testing.T) {
	srv := bootChat(t)
	srv.Chat.Harness = askHarness{answer: make(chan string, 1)}
	ts := httptest.NewServer(srv.Echo)
	defer ts.Close()
	do := func(as, method, path, body string) (*http.Response, error) {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "factory_session", Value: "sess-" + as})
		return http.DefaultClient.Do(req)
	}
	_, body := chatDo(srv, "ana", "POST", "/api/v1/chats", `{}`)
	var created struct{ Chat model.Chat }
	_ = json.Unmarshal([]byte(body), &created)
	id := created.Chat.ID

	for _, c := range []struct {
		decision, wantOutcome, wantText string
	}{{"allow", "allowed", "ran"}, {"deny", "denied", "refused"}} {
		res, err := do("ana", "POST", "/api/v1/chats/"+id+"/messages", `{"content":"go"}`)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(res.Body)
		var event, reqID string
		var seen []string
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				event = v
				seen = append(seen, v)
			}
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			switch event {
			case "permission":
				var p struct{ ID, Server, Tool, Input string }
				_ = json.Unmarshal([]byte(data), &p)
				if p.ID == "" || p.Server != "docs" || p.Tool != "write" || p.Input != `{"id":7}` {
					t.Fatalf("permission frame: %s", data)
				}
				reqID = p.ID
				if r, _ := do("bob", "POST", "/api/v1/chats/"+id+"/permissions/"+reqID, `{"decision":"allow"}`); r.StatusCode != http.StatusNotFound {
					t.Fatalf("another user's answer: %d", r.StatusCode)
				}
				if r, _ := do("ana", "POST", "/api/v1/chats/"+id+"/permissions/"+reqID, `{"decision":"maybe"}`); r.StatusCode != http.StatusBadRequest {
					t.Fatalf("bad decision: %d", r.StatusCode)
				}
				if r, _ := do("ana", "POST", "/api/v1/chats/"+id+"/permissions/"+reqID, `{"decision":"`+c.decision+`"}`); r.StatusCode != http.StatusNoContent {
					t.Fatalf("answer: %d", r.StatusCode)
				}
				if r, _ := do("ana", "POST", "/api/v1/chats/"+id+"/permissions/"+reqID, `{"decision":"allow"}`); r.StatusCode != http.StatusNotFound {
					t.Fatalf("answering twice: %d", r.StatusCode)
				}
			case "permission_resolved":
				if want := `{"id":"` + reqID + `","outcome":"` + c.wantOutcome + `"}`; data != want {
					t.Fatalf("resolved frame: %s, want %s", data, want)
				}
			}
		}
		res.Body.Close()
		if got := strings.Join(seen, ","); !strings.HasPrefix(got, "user_message,permission,permission_resolved,") || !strings.HasSuffix(got, ",done") {
			t.Fatalf("events: %s", got)
		}
		_, msgs := chatDo(srv, "ana", "GET", "/api/v1/chats/"+id+"/messages", "")
		if !strings.Contains(msgs, `"content":"`+c.wantText+`"`) {
			t.Fatalf("reply for %s: %s", c.decision, msgs)
		}
	}
	if r, _ := do("vic", "POST", "/api/v1/chats/"+id+"/permissions/perm_x", `{"decision":"allow"}`); r.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer: %d", r.StatusCode)
	}
}
