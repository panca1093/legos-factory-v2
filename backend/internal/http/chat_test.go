package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
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

func bootChat(t *testing.T) *Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	users := &store.UserRepo{DB: db}
	for _, id := range []string{"ana", "bob"} {
		if err := users.Create(context.Background(), model.User{ID: id, Email: id + "@t", Name: id, Role: model.RoleEngineer}); err != nil {
			t.Fatal(err)
		}
		if err := users.CreateSession(context.Background(), "sess-"+id, id, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(testDiscard{}, nil))
	srv := New(&model.Config{FrontendOrigin: "http://localhost:5173"}, log)
	srv.Auth = &usecase.Identity{Users: users, Mode: "dev"}
	srv.Chat = &usecase.Chat{
		Store: &store.ChatRepo{DB: db}, Harness: agent.NewMock(), Dir: filepath.Join(t.TempDir(), "chat"),
		Models: func(context.Context) ([]model.ModelInfo, error) {
			return []model.ModelInfo{{ProviderID: "mock", ModelID: "mock-local", Name: "Mock", Harness: "mock"}}, nil
		},
		Default: func(context.Context) string { return "mock/mock-local" },
	}
	return srv
}

func chatDo(srv *Server, as, method, url, body string) (int, string) {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if as != "" {
		req.AddCookie(&http.Cookie{Name: "factory_session", Value: "sess-" + as})
	}
	rec := httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestChatAPI(t *testing.T) {
	srv := bootChat(t)

	if code, _ := chatDo(srv, "", "GET", "/api/v1/chats", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}

	code, body := chatDo(srv, "ana", "POST", "/api/v1/chats", `{"model":"mock/mock-local"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var created struct{ Chat model.Chat }
	_ = json.Unmarshal([]byte(body), &created)
	id := created.Chat.ID
	if id == "" || created.Chat.Title != "New chat" || created.Chat.Model != "mock/mock-local" {
		t.Fatalf("created: %s", body)
	}
	if code, body := chatDo(srv, "ana", "POST", "/api/v1/chats", `{"model":"nope/x"}`); code != http.StatusBadRequest {
		t.Fatalf("unknown model: %d %s", code, body)
	}

	code, body = chatDo(srv, "ana", "GET", "/api/v1/chat/models", "")
	if code != 200 || !strings.Contains(body, `"default":"mock/mock-local"`) || !strings.Contains(body, `"name":"Auto (default)"`) || !strings.Contains(body, `"id":"mock/mock-local"`) {
		t.Fatalf("models: %d %s", code, body)
	}

	code, body = chatDo(srv, "ana", "POST", "/api/v1/chats/"+id+"/messages", `{"content":"hello there","files":[{"name":"n.txt","content":"abc"}]}`)
	if code != 200 {
		t.Fatalf("send: %d %s", code, body)
	}
	events := parseSSE(body)
	if len(events) < 3 || events[0].name != "user_message" || events[len(events)-1].name != "done" {
		t.Fatalf("stream: %s", body)
	}
	var text string
	for _, e := range events[1 : len(events)-1] {
		var d struct{ Text string }
		_ = json.Unmarshal([]byte(e.data), &d)
		if e.name != "delta" {
			t.Fatalf("unexpected %s in the middle: %s", e.name, body)
		}
		text += d.Text
	}
	var done struct{ Message model.ChatMessage }
	_ = json.Unmarshal([]byte(events[len(events)-1].data), &done)
	if text == "" || done.Message.Content != text || done.Message.Role != "assistant" || done.Message.ID == 0 {
		t.Fatalf("done %+v, streamed %q", done.Message, text)
	}

	code, body = chatDo(srv, "ana", "GET", "/api/v1/chats/"+id+"/messages", "")
	var msgs struct{ Messages []model.ChatMessage }
	_ = json.Unmarshal([]byte(body), &msgs)
	if code != 200 || len(msgs.Messages) != 2 || msgs.Messages[0].Attachments[0].Name != "n.txt" || msgs.Messages[1].Content != text {
		t.Fatalf("messages: %d %s", code, body)
	}

	if code, body := chatDo(srv, "ana", "POST", "/api/v1/chats/"+id+"/messages", `{"content":"x","files":[{"name":"a.bin","content":"a\u0000b"}]}`); code != http.StatusBadRequest || strings.Contains(body, "event:") {
		t.Fatalf("binary file: %d %s", code, body)
	}

	code, body = chatDo(srv, "ana", "PATCH", "/api/v1/chats/"+id, `{"title":"Renamed"}`)
	if code != 200 || !strings.Contains(body, `"title":"Renamed"`) {
		t.Fatalf("rename: %d %s", code, body)
	}

	for _, c := range []struct{ method, url, body string }{
		{"GET", "/api/v1/chats/" + id, ""},
		{"PATCH", "/api/v1/chats/" + id, `{"title":"x"}`},
		{"GET", "/api/v1/chats/" + id + "/messages", ""},
		{"POST", "/api/v1/chats/" + id + "/messages", `{"content":"hi"}`},
		{"DELETE", "/api/v1/chats/" + id, ""},
	} {
		if code, _ := chatDo(srv, "bob", c.method, c.url, c.body); code != http.StatusNotFound {
			t.Errorf("bob %s %s: %d", c.method, c.url, code)
		}
	}
	if _, body := chatDo(srv, "bob", "GET", "/api/v1/chats", ""); !strings.Contains(body, `"chats":[]`) {
		t.Fatalf("bob's list: %s", body)
	}

	if code, _ := chatDo(srv, "ana", "DELETE", "/api/v1/chats/"+id, ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := chatDo(srv, "ana", "GET", "/api/v1/chats/"+id, ""); code != http.StatusNotFound {
		t.Fatalf("after delete: %d", code)
	}
}

type sseEvent struct{ name, data string }

func parseSSE(body string) []sseEvent {
	var out []sseEvent
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		var e sseEvent
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				e.name = v
			}
			if v, ok := strings.CutPrefix(line, "data: "); ok {
				e.data = v
			}
		}
		out = append(out, e)
	}
	return out
}
