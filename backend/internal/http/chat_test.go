package http

import (
	"context"
	"encoding/base64"
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
	for _, id := range []string{"ana", "bob", "vic"} {
		role := model.RoleEngineer
		if id == "vic" {
			role = model.RoleViewer
		}
		if err := users.Create(context.Background(), model.User{ID: id, Email: id + "@t", Name: id, Role: role}); err != nil {
			t.Fatal(err)
		}
		if err := users.CreateSession(context.Background(), "sess-"+id, id, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	projRepo, members := &store.ProjectRepo{DB: db}, &store.MemberRepo{DB: db}
	if err := projRepo.Create(context.Background(), &model.Project{ID: "p1", Name: "p1"}); err != nil {
		t.Fatal(err)
	}
	if err := members.Add(context.Background(), "p1", "ana"); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(testDiscard{}, nil))
	srv := New(&model.Config{FrontendOrigin: "http://localhost:5173"}, log)
	srv.Auth = &usecase.Identity{Users: users, Mode: "dev"}
	srv.Chat = &usecase.Chat{
		Store: &store.ChatRepo{DB: db}, Harness: agent.NewMock(), Projects: &usecase.Projects{Store: projRepo, Members: members, Users: users}, Dir: filepath.Join(t.TempDir(), "chat"),
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

	if code, _ := chatDo(srv, "vic", "POST", "/api/v1/chats", `{}`); code != http.StatusForbidden {
		t.Fatalf("viewer create: %d", code)
	}
	if code, _ := chatDo(srv, "vic", "GET", "/api/v1/chats", ""); code != http.StatusOK {
		t.Fatalf("viewer list: %d", code)
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
		if e.name == "title" {
			continue
		}
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

func TestChatUnavailableIs503(t *testing.T) {
	srv := bootChat(t)
	_, body := chatDo(srv, "ana", "POST", "/api/v1/chats", `{}`)
	var created struct{ Chat model.Chat }
	_ = json.Unmarshal([]byte(body), &created)
	srv.Chat.Harness = nil
	if code, body := chatDo(srv, "ana", "POST", "/api/v1/chats/"+created.Chat.ID+"/messages", `{"content":"hi"}`); code != http.StatusServiceUnavailable || !strings.Contains(body, "CHAT_UNAVAILABLE") {
		t.Fatalf("no harness: %d %s", code, body)
	}
}

func TestChatTitleFrameAndProjectAndDraftAPI(t *testing.T) {
	srv := bootChat(t)
	_, body := chatDo(srv, "ana", "POST", "/api/v1/chats", `{}`)
	var created struct{ Chat model.Chat }
	_ = json.Unmarshal([]byte(body), &created)
	id := created.Chat.ID

	srv.Chat.Gen = func(context.Context, string, string) (model.AgentResult, error) {
		return model.AgentResult{Text: "Hello greeting"}, nil
	}
	_, body = chatDo(srv, "ana", "POST", "/api/v1/chats/"+id+"/messages", `{"content":"hello there"}`)
	events := parseSSE(body)
	n := len(events)
	var title struct{ Title string }
	_ = json.Unmarshal([]byte(events[n-2].data), &title)
	if events[n-2].name != "title" || title.Title != "Hello greeting" || events[n-1].name != "done" {
		t.Fatalf("stream tail: %s", body)
	}

	code, body := chatDo(srv, "ana", "PATCH", "/api/v1/chats/"+id, `{"project_id":"p1"}`)
	if code != 200 || !strings.Contains(body, `"project_id":"p1"`) {
		t.Fatalf("set project: %d %s", code, body)
	}
	if code, body := chatDo(srv, "ana", "PATCH", "/api/v1/chats/"+id, `{"project_id":"nope"}`); code != http.StatusNotFound {
		t.Fatalf("unknown project: %d %s", code, body)
	}
	code, body = chatDo(srv, "ana", "PATCH", "/api/v1/chats/"+id, `{"project_id":""}`)
	if code != 200 || strings.Contains(body, "project_id") {
		t.Fatalf("clear project: %d %s", code, body)
	}

	code, body = chatDo(srv, "ana", "GET", "/api/v1/chats/"+id+"/related-knowledge", "")
	if code != 200 || strings.TrimSpace(body) != `{"docs":[]}` {
		t.Fatalf("related: %d %s", code, body)
	}

	srv.Chat.Gen = func(context.Context, string, string) (model.AgentResult, error) {
		return model.AgentResult{Text: `{"title":"T","objective":"O","acceptance":"a\nb"}`}, nil
	}
	code, body = chatDo(srv, "ana", "POST", "/api/v1/chats/"+id+"/work-order-draft", "")
	if code != 200 || strings.TrimSpace(body) != `{"title":"T","objective":"O","acceptance":"a\nb"}` {
		t.Fatalf("draft: %d %s", code, body)
	}
	if code, _ := chatDo(srv, "vic", "POST", "/api/v1/chats/"+id+"/work-order-draft", ""); code != http.StatusForbidden {
		t.Fatalf("viewer draft: %d", code)
	}
	if code, _ := chatDo(srv, "bob", "POST", "/api/v1/chats/"+id+"/work-order-draft", ""); code != http.StatusNotFound {
		t.Fatalf("other user's draft: %d", code)
	}
	srv.Chat.Gen = func(context.Context, string, string) (model.AgentResult, error) {
		return model.AgentResult{Text: "sorry, no"}, nil
	}
	code, body = chatDo(srv, "ana", "POST", "/api/v1/chats/"+id+"/work-order-draft", "")
	if code != http.StatusBadGateway || !strings.Contains(body, "DRAFT_FAILED") {
		t.Fatalf("bad draft: %d %s", code, body)
	}
}

func TestChatSendBodyLimit(t *testing.T) {
	srv := bootChat(t)
	_, body := chatDo(srv, "ana", "POST", "/api/v1/chats", `{"model":"mock/mock-local"}`)
	var created struct{ Chat model.Chat }
	_ = json.Unmarshal([]byte(body), &created)
	url := "/api/v1/chats/" + created.Chat.ID + "/messages"

	img := strings.Repeat("A", 5242880)
	ctrl := strings.Repeat(`\u0001`, 100<<10) // JSON-escaped control characters: 6 bytes each
	worst := `{"content":"` + strings.Repeat(`\u0001`, 32<<10) + `","files":[` +
		`{"name":"1.png","encoding":"base64","content":"` + img + `"},` +
		`{"name":"2.png","encoding":"base64","content":"` + img + `"},` +
		`{"name":"a.txt","content":"` + ctrl + `"},{"name":"b.txt","content":"` + ctrl + `"},{"name":"c.txt","content":"` + ctrl + `"}]}`
	if len(worst) <= 12<<20-300<<10 || len(worst) > 12<<20 {
		t.Fatalf("worst-case body is %d bytes", len(worst))
	}
	if code, body := chatDo(srv, "ana", "POST", url, worst); code == http.StatusRequestEntityTooLarge {
		t.Fatalf("worst-case send rejected by the body limit: %s", body[:min(len(body), 200)])
	}

	if code, _ := chatDo(srv, "ana", "POST", url, `{"content":"`+strings.Repeat("a", 12<<20)+`"}`); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over 12 MB on the chat route: %d", code)
	}
	if code, _ := chatDo(srv, "ana", "POST", "/api/v1/chats", `{"title":"`+strings.Repeat("a", 2<<20)+`"}`); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over 1 MB on another route: %d", code)
	}
	if code, _ := chatDo(srv, "ana", "PATCH", "/api/v1/chats/"+created.Chat.ID, `{"title":"`+strings.Repeat("a", 2<<20)+`"}`); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over 1 MB on the same path with another method: %d", code)
	}
}

func TestChatSendImageOverHTTP(t *testing.T) {
	srv := bootChat(t)
	_, body := chatDo(srv, "ana", "POST", "/api/v1/chats", `{"model":"mock/mock-local"}`)
	var created struct{ Chat model.Chat }
	_ = json.Unmarshal([]byte(body), &created)
	url := "/api/v1/chats/" + created.Chat.ID + "/messages"
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n0123"))

	code, body := chatDo(srv, "ana", "POST", url, `{"content":"look","files":[{"name":"s.png","encoding":"base64","content":"`+png+`"}]}`)
	if code != 200 || !strings.Contains(body, `"kind":"image"`) || !strings.Contains(body, `"size":12`) {
		t.Fatalf("image send: %d %s", code, body)
	}
	code, body = chatDo(srv, "ana", "POST", url, `{"content":"x","files":[{"name":"a.zip","content":"PK\u0003\u0004\u0000"}]}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "a.zip looks binary; only text files can be attached") {
		t.Fatalf("zip: %d %s", code, body)
	}
	code, body = chatDo(srv, "ana", "POST", url, `{"content":"x","files":[{"name":"a.png","encoding":"base64","content":"AAAA"}]}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "a.png is not a supported image (PNG, JPEG, GIF or WebP).") {
		t.Fatalf("not an image: %d %s", code, body)
	}
}

type stepHarness struct{}

func (stepHarness) Name() string                         { return "steps" }
func (stepHarness) Cancel(context.Context, string) error { return nil }
func (stepHarness) Result(context.Context, string) (model.AgentResult, error) {
	return model.AgentResult{Text: "See a.go:1"}, nil
}
func (stepHarness) Start(_ context.Context, req model.AgentRequest) (model.AgentSession, error) {
	req.OnStep(model.AgentStep{N: 1, Tools: []model.AgentTool{{Name: "Read File", Target: "backend/a.go"}}})
	req.OnStep(model.AgentStep{N: 2, Tools: []model.AgentTool{{Name: "grep"}}})
	req.OnText("See a.go:1")
	return stepSession{}, nil
}

type stepSession struct{}

func (stepSession) ID() string { return "s1" }

func TestChatRepoAndStepFrames(t *testing.T) {
	srv := bootChat(t)
	srv.Chat.Harness = stepHarness{}
	srv.Chat.Repo = func(context.Context, string, bool) (string, string, error) {
		return t.TempDir(), "0123456789abcdef0123456789abcdef01234567", nil
	}
	_, body := chatDo(srv, "ana", "POST", "/api/v1/chats", `{"title":"t","project_id":"p1"}`)
	var created struct{ Chat model.Chat }
	_ = json.Unmarshal([]byte(body), &created)
	_, body = chatDo(srv, "ana", "POST", "/api/v1/chats/"+created.Chat.ID+"/messages", `{"content":"where?"}`)
	events := parseSSE(body)
	var names []string
	for _, e := range events {
		names = append(names, e.name)
	}
	if strings.Join(names, ",") != "user_message,repo,step,step,delta,done" {
		t.Fatalf("stream: %s", body)
	}
	if events[1].data != `{"project_id":"p1","sha":"0123456789abcdef0123456789abcdef01234567"}` {
		t.Fatalf("repo frame: %s", events[1].data)
	}
	if events[2].data != `{"tools":[{"name":"Read File","target":"backend/a.go","failed":false}]}` || events[3].data != `{"tools":[{"name":"grep","target":"","failed":false}]}` {
		t.Fatalf("step frames: %s | %s", events[2].data, events[3].data)
	}
}
