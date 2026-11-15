package http

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"mime/multipart"
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
		Store: &store.ChatRepo{DB: db}, Harness: agent.NewMock(), Projects: &usecase.Projects{Store: projRepo, Members: members, Users: users}, Dir: filepath.Join(t.TempDir(), "chat"), DataDir: t.TempDir(),
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

func chatUpload(srv *Server, as, chatID, name string, data []byte) (int, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/v1/chats/"+chatID+"/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "factory_session", Value: "sess-" + as})
	rec := httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func chatGet(srv *Server, as, url string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", url, nil)
	req.AddCookie(&http.Cookie{Name: "factory_session", Value: "sess-" + as})
	rec := httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	return rec
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

	code, body = chatUpload(srv, "ana", id, "n.txt", []byte("abc"))
	var up model.ChatFileRec
	_ = json.Unmarshal([]byte(body), &up)
	if code != http.StatusCreated || up.ID == "" {
		t.Fatalf("upload: %d %s", code, body)
	}
	code, body = chatDo(srv, "ana", "POST", "/api/v1/chats/"+id+"/messages", `{"content":"hello there","file_ids":["`+up.ID+`"]}`)
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

	if code, body := chatUpload(srv, "ana", id, "a.bin", []byte("a\x00b")); code != http.StatusBadRequest || strings.Contains(body, "event:") {
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

type sessionMCPList struct{}

func (sessionMCPList) List(context.Context) ([]model.MCPServerRec, error) {
	return []model.MCPServerRec{{ID: "m1", Name: "docs", Transport: model.MCPHTTP, Status: model.MCPActive, Roles: []string{"chat"}, Tools: []model.MCPTool{{Name: "search", Enabled: true}}}}, nil
}

func TestChatSessionAPI(t *testing.T) {
	srv := bootChat(t)
	srv.Chat.MCPServers = sessionMCPList{}
	mk := func(as, body string) string {
		code, out := chatDo(srv, as, "POST", "/api/v1/chats", body)
		if code != http.StatusCreated {
			t.Fatalf("create: %d %s", code, out)
		}
		var c struct{ Chat struct{ ID string } }
		_ = json.Unmarshal([]byte(out), &c)
		return c.Chat.ID
	}
	plain, bound := mk("ana", `{}`), mk("ana", `{"project_id":"p1"}`)

	code, body := chatDo(srv, "ana", "GET", "/api/v1/chats/"+plain+"/session", "")
	if code != 200 || !strings.Contains(body, `"project":null`) || !strings.Contains(body, `"parts":[]`) || !strings.Contains(body, `"calls":{}`) || !strings.Contains(body, `"cache":null`) {
		t.Fatalf("unbound: %d %s", code, body)
	}
	code, body = chatDo(srv, "ana", "GET", "/api/v1/chats/"+bound+"/session", "")
	for _, want := range []string{`"worktrees":[]`, `"running":[]`, `"parts":[]`, `"calls":{}`, `"local":null`, `"checkout":null`} {
		if code != 200 || !strings.Contains(body, want) {
			t.Fatalf("bound: want %s in %d %s", want, code, body)
		}
	}
	if strings.Contains(body, "null,") && strings.Contains(body, `"mcp":null`) {
		t.Fatalf("mcp must be a list: %s", body)
	}

	if code, _ := chatDo(srv, "bob", "GET", "/api/v1/chats/"+plain+"/session", ""); code != http.StatusNotFound {
		t.Fatalf("other user: %d", code)
	}
	if code, _ := chatDo(srv, "", "GET", "/api/v1/chats/"+plain+"/session", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
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

func TestChatBodyLimits(t *testing.T) {
	srv := bootChat(t)
	_, body := chatDo(srv, "ana", "POST", "/api/v1/chats", `{"model":"mock/mock-local"}`)
	var created struct{ Chat model.Chat }
	_ = json.Unmarshal([]byte(body), &created)
	id := created.Chat.ID

	if code, _ := chatDo(srv, "ana", "POST", "/api/v1/chats/"+id+"/messages", `{"content":"`+strings.Repeat("a", 12<<20)+`"}`); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over 12 MB on the message route: %d", code)
	}
	if code, _ := chatDo(srv, "ana", "POST", "/api/v1/chats", `{"title":"`+strings.Repeat("a", 2<<20)+`"}`); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over 1 MB on another route: %d", code)
	}
	if code, _ := chatDo(srv, "ana", "PATCH", "/api/v1/chats/"+id, `{"title":"`+strings.Repeat("a", 2<<20)+`"}`); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over 1 MB on the same path with another method: %d", code)
	}

	pdf := append([]byte("%PDF-1.7\n"), make([]byte, 20<<20-9)...)
	if code, body := chatUpload(srv, "ana", id, "max.pdf", pdf); code != http.StatusCreated {
		t.Fatalf("a 20 MB PDF must pass the upload limit: %d %s", code, body[:min(len(body), 200)])
	}
	if code, _ := chatUpload(srv, "ana", id, "huge.pdf", append(pdf, make([]byte, 2<<20)...)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over 21 MB on the upload route: %d", code)
	}
	if code, body := chatUpload(srv, "ana", id, "p.pdf", append(pdf, 'x')); code != http.StatusBadRequest || !strings.Contains(body, "p.pdf is over 20 MB.") {
		t.Fatalf("20 MB + 1: %d %s", code, body[:min(len(body), 200)])
	}
}

func TestChatFilesOverHTTP(t *testing.T) {
	srv := bootChat(t)
	mk := func(as string) string {
		_, body := chatDo(srv, as, "POST", "/api/v1/chats", `{"model":"mock/mock-local"}`)
		var c struct{ Chat model.Chat }
		_ = json.Unmarshal([]byte(body), &c)
		return c.Chat.ID
	}
	id, bobChat := mk("ana"), mk("bob")
	png := []byte("\x89PNG\r\n\x1a\n0123")

	code, body := chatUpload(srv, "ana", id, "naïve shot.png", png)
	var up model.ChatFileRec
	_ = json.Unmarshal([]byte(body), &up)
	if code != http.StatusCreated || up.Kind != "image" || up.MediaType != "image/png" || up.Size != 12 || up.Name != "naïve shot.png" ||
		!strings.Contains(body, `"media_type":"image/png"`) || strings.Contains(body, "chat_id") {
		t.Fatalf("upload: %d %s", code, body)
	}
	if code, body := chatUpload(srv, "ana", id, "a.docx", []byte("PK\x03\x04")); code != http.StatusBadRequest || !strings.Contains(body, "VALIDATION_FAILED") {
		t.Fatalf("bad type: %d %s", code, body)
	}
	if code, _ := chatUpload(srv, "vic", id, "a.png", png); code != http.StatusForbidden {
		t.Fatalf("viewer upload: %d", code)
	}

	url := "/api/v1/chats/" + id + "/files/" + up.ID
	rec := chatGet(srv, "ana", url)
	h := rec.Header()
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), png) || h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Content-Security-Policy") != "sandbox" || h.Get("Cache-Control") != "private, max-age=3600" ||
		h.Get("Content-Disposition") != "inline; filename*=UTF-8''na%C3%AFve%20shot.png" {
		t.Fatalf("get: %d %v", rec.Code, h)
	}
	_, pdfBody := chatUpload(srv, "ana", id, "r.pdf", []byte("%PDF-1.4"))
	var pdf struct{ ID string }
	_ = json.Unmarshal([]byte(pdfBody), &pdf)
	if p := chatGet(srv, "ana", "/api/v1/chats/"+id+"/files/"+pdf.ID); p.Header().Get("Content-Type") != "application/pdf" || p.Header().Get("Content-Security-Policy") != "" {
		t.Fatalf("pdf headers: %v", p.Header())
	}

	for label, u := range map[string]string{"other user's chat": url, "own chat, foreign file": "/api/v1/chats/" + bobChat + "/files/" + up.ID} {
		if rec := chatGet(srv, "bob", u); rec.Code != http.StatusNotFound {
			t.Errorf("get %s: %d", label, rec.Code)
		}
		if code, _ := chatDo(srv, "bob", "DELETE", u, ""); code != http.StatusNotFound {
			t.Errorf("delete %s: %d", label, code)
		}
	}
	if code, _ := chatUpload(srv, "bob", id, "a.png", png); code != http.StatusNotFound {
		t.Errorf("upload into another's chat: %d", code)
	}
	if code, _ := chatDo(srv, "bob", "POST", "/api/v1/chats/"+bobChat+"/messages", `{"content":"x","file_ids":["`+up.ID+`"]}`); code != http.StatusBadRequest {
		t.Errorf("send with a foreign file id: %d", code)
	}

	if code, body := chatDo(srv, "ana", "POST", "/api/v1/chats/"+id+"/messages", `{"content":"look","file_ids":["`+up.ID+`"]}`); code != 200 || !strings.Contains(body, `"kind":"image"`) || !strings.Contains(body, `"file_id":"`+up.ID+`"`) {
		t.Fatalf("send: %d %s", code, body)
	}
	if code, _ := chatDo(srv, "ana", "DELETE", url, ""); code != http.StatusConflict {
		t.Fatalf("delete attached: %d", code)
	}
	if code, _ := chatDo(srv, "ana", "DELETE", "/api/v1/chats/"+id+"/files/"+pdf.ID, ""); code != http.StatusNoContent {
		t.Fatalf("delete free: %d", code)
	}
	if rec := chatGet(srv, "ana", "/api/v1/chats/"+id+"/files/"+pdf.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("get deleted: %d", rec.Code)
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
