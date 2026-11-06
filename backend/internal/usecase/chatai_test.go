package usecase

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestFallbackTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Fix the login bug. It crashes on submit":            "Fix the login bug",
		"how do I add a retry to the billing worker please?": "how do I add a retry to the billing",
		"short":              "short",
		"line one\nline two": "line one",
		"supercalifragilisticexpialidocious_and_more_and_more_xx": "supercalifragilisticexpialidocious_and_m",
		"   ": "",
	} {
		if got := fallbackTitle(in); got != want {
			t.Errorf("fallbackTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanTitle(t *testing.T) {
	if got := cleanTitle("\"Retry logic for the billing worker today.\"\nextra"); got != "Retry logic for the billing worker" {
		t.Fatalf("got %q", got)
	}
}

func TestTitleGeneratedInParallelAndSentBeforeDone(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "ok", gate: make(chan struct{}), inRun: make(chan struct{}, 4)}
	r := newChatRig(t, h)
	started := make(chan string, 1)
	r.chat.Gen = func(_ context.Context, modelRef, prompt string) (model.AgentResult, error) {
		started <- modelRef
		return model.AgentResult{Text: "Bug login saat submit", TokensIn: 4, TokensOut: 2, CostMicros: 3}, nil
	}
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	var evs []ChatEvent
	done := make(chan struct{})
	go func() {
		evs, _ = r.send(t, r.ana, ch.ID, ChatSend{Content: "login saya error waktu submit"})
		close(done)
	}()
	<-h.inRun // the reply is still running
	select {
	case m := <-started:
		if m != "anthropic/haiku" {
			t.Fatalf("title model %q, want the cheap one", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("title generation did not start while the reply was running")
	}
	close(h.gate)
	<-done
	k := kinds(evs)
	if !strings.HasSuffix(k, "title,done") {
		t.Fatalf("events: %s", k)
	}
	if got, _ := r.chat.Get(ctx, r.ana, ch.ID); got.Title != "Bug login saat submit" {
		t.Fatalf("persisted title %q", got.Title)
	}
	var titleUsage bool
	for _, u := range r.usage.recs {
		titleUsage = titleUsage || (u.Model == "haiku" && u.TokensIn == 4)
	}
	if !titleUsage {
		t.Fatalf("title cost not recorded: %+v", r.usage.recs)
	}
}

func TestUserSetTitleIsNotRegenerated(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "ok"})
	calls := 0
	r.chat.Gen = func(context.Context, string, string) (model.AgentResult, error) {
		calls++
		return model.AgentResult{Text: "Generated"}, nil
	}
	ch, _ := r.chat.Create(ctx, r.ana, "My own title", "", "")
	evs, _ := r.send(t, r.ana, ch.ID, ChatSend{Content: "hello"})
	if strings.Contains(kinds(evs), "title") || calls != 0 {
		t.Fatalf("events %s, gen calls %d", kinds(evs), calls)
	}
	if got, _ := r.chat.Get(ctx, r.ana, ch.ID); got.Title != "My own title" {
		t.Fatalf("title %q", got.Title)
	}
	// a second turn of an auto-titled chat does not retitle either
	ch2, _ := r.chat.Create(ctx, r.ana, "", "", "")
	r.send(t, r.ana, ch2.ID, ChatSend{Content: "first"})
	calls = 0
	evs, _ = r.send(t, r.ana, ch2.ID, ChatSend{Content: "second"})
	if strings.Contains(kinds(evs), "title") || calls != 0 {
		t.Fatalf("second turn: %s, %d", kinds(evs), calls)
	}
}

func draftRig(t *testing.T, replies ...string) (*chatRig, *model.Chat, *[]string) {
	t.Helper()
	r := newChatRig(t, &chatPlain{reply: "Sure, a retry with backoff."})
	var prompts []string
	var mu sync.Mutex
	r.chat.Gen = func(_ context.Context, _, prompt string) (model.AgentResult, error) {
		mu.Lock()
		defer mu.Unlock()
		prompts = append(prompts, prompt)
		text := replies[len(replies)-1]
		if len(prompts) <= len(replies) {
			text = replies[len(prompts)-1]
		}
		return model.AgentResult{Text: text, TokensIn: 2, TokensOut: 1}, nil
	}
	ch, _ := r.chat.Create(context.Background(), r.ana, "t", "", "")
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "add retries to the billing worker"}); err != nil {
		t.Fatal(err)
	}
	return r, ch, &prompts
}

func TestDraftWorkOrder(t *testing.T) {
	ctx := context.Background()
	good := "```json\n{\"title\":\"Add retries\",\"objective\":\"Retry billing jobs.\",\"acceptance\":[\"Retries 3 times\",\"Backoff is exponential\"]}\n```"
	r, ch, prompts := draftRig(t, "not json at all", good)
	d, err := r.chat.DraftWorkOrder(ctx, r.ana, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Add retries" || d.Objective != "Retry billing jobs." || d.Acceptance != "Retries 3 times\nBackoff is exponential" {
		t.Fatalf("draft %+v", d)
	}
	if len(*prompts) != 2 || !strings.Contains((*prompts)[0], "add retries to the billing worker") {
		t.Fatalf("expected one retry over the transcript: %d prompts", len(*prompts))
	}
	if r.usage.released == 0 {
		t.Fatal("budget reservation not released")
	}

	r2, ch2, p2 := draftRig(t, "{\"title\":\"\",\"objective\":\"x\"}")
	if _, err := r2.chat.DraftWorkOrder(ctx, r2.ana, ch2.ID); !errors.Is(err, ErrDraftFailed) || len(*p2) != 2 {
		t.Fatalf("invalid twice: %v after %d calls", err, len(*p2))
	}

	r3, ch3, _ := draftRig(t, good)
	r3.usage.deny = true
	if _, err := r3.chat.DraftWorkOrder(ctx, r3.ana, ch3.ID); !errors.Is(err, ErrChatBudget) {
		t.Fatalf("budget: %v", err)
	}
	r3.usage.deny = false
	if !r3.chat.lock(ch3.ID) {
		t.Fatal("lock")
	}
	if _, err := r3.chat.DraftWorkOrder(ctx, r3.ana, ch3.ID); !errors.Is(err, ErrChatBusy) {
		t.Fatalf("busy: %v", err)
	}
	r3.chat.unlock(ch3.ID)
	if _, err := r3.chat.DraftWorkOrder(ctx, r3.bob, ch3.ID); !errors.Is(err, model.ErrChatNotFound) {
		t.Fatalf("other user: %v", err)
	}
}

func TestBoundedTranscriptKeepsNewest(t *testing.T) {
	big := strings.Repeat("x", 4000)
	var msgs []model.ChatMessage
	msgs = append(msgs, model.ChatMessage{Role: "user", Content: "OLDEST"})
	for i := 0; i < 20; i++ {
		msgs = append(msgs, model.ChatMessage{Role: "assistant", Content: big})
	}
	msgs = append(msgs, model.ChatMessage{Role: "user", Content: "NEWEST"})
	got := boundedTranscript(msgs)
	if strings.Contains(got, "OLDEST") || !strings.HasSuffix(got, "User: NEWEST\n") || len([]rune(got)) > draftTranscript {
		t.Fatalf("transcript len %d", len(got))
	}
}

func TestChatProjectUpdate(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "ok"})
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	p := "p1"
	got, err := r.chat.Update(ctx, r.ana, ch.ID, nil, nil, &p)
	if err != nil || got.ProjectID != "p1" {
		t.Fatalf("set: %v %+v", err, got)
	}
	empty := ""
	if got, err = r.chat.Update(ctx, r.ana, ch.ID, nil, nil, &empty); err != nil || got.ProjectID != "" {
		t.Fatalf("clear: %v %+v", err, got)
	}
	bobChat, _ := r.chat.Create(ctx, r.bob, "", "", "")
	if _, err := r.chat.Update(ctx, r.bob, bobChat.ID, nil, nil, &p); !errors.Is(err, model.ErrProjectNotFound) {
		t.Fatalf("non-member: %v", err)
	}
	unknown := "nope"
	if _, err := r.chat.Update(ctx, r.ana, ch.ID, nil, nil, &unknown); !errors.Is(err, model.ErrProjectNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
}

type relatedSpy struct {
	project, text string
	docs          []model.KBDoc
}

func (s *relatedSpy) Related(_ context.Context, project, text string, limit int) ([]model.KBDoc, error) {
	s.project, s.text = project, text
	return s.docs, nil
}

func TestRelatedKnowledge(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "Billing retries use exponential backoff"})
	spy := &relatedSpy{docs: []model.KBDoc{{ID: "d1", Title: "Retry policy", Type: "Playbook", Description: "How retries work"}, {ID: "d2", Title: "Billing", Source: "repo"}}}
	r.chat.Related = spy

	ch, _ := r.chat.Create(ctx, r.ana, "", "p1", "")
	empty, err := r.chat.RelatedKnowledge(ctx, r.ana, ch.ID)
	if err != nil || empty == nil || len(empty) != 0 || spy.text != "" {
		t.Fatalf("empty chat: %v %#v %q", err, empty, spy.text)
	}
	r.send(t, r.ana, ch.ID, ChatSend{Content: "How do we handle the retries in the billing worker?"})
	docs, err := r.chat.RelatedKnowledge(ctx, r.ana, ch.ID)
	if err != nil || len(docs) != 2 || docs[0] != (RelatedDoc{ID: "d1", Title: "Retry policy", Kind: "Playbook", Snippet: "How retries work"}) || docs[1].Kind != "repo" {
		t.Fatalf("docs %v %+v", err, docs)
	}
	if spy.project != "p1" || strings.Contains(" "+spy.text+" ", " the ") || strings.Contains(" "+spy.text+" ", " we ") || !strings.Contains(spy.text, "retries") || !strings.Contains(spy.text, "billing") {
		t.Fatalf("scope %q query %q", spy.project, spy.text)
	}
	if _, err := r.chat.RelatedKnowledge(ctx, r.bob, ch.ID); !errors.Is(err, model.ErrChatNotFound) {
		t.Fatalf("other user: %v", err)
	}

	// a chat whose project the user can no longer reach falls back to factory-wide docs
	bobChat, _ := r.chat.Create(ctx, r.bob, "", "", "")
	r.send(t, r.bob, bobChat.ID, ChatSend{Content: "billing retries"})
	_ = r.chat.Store.SetProject(ctx, bobChat.ID, "p1")
	if _, err := r.chat.RelatedKnowledge(ctx, r.bob, bobChat.ID); err != nil || spy.project != "" {
		t.Fatalf("revoked project: %v %q", err, spy.project)
	}
}

func TestKBDocsRelatedScopesAndDropsSeeAlso(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/factory.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, id := range []string{"p1", "p2"} {
		if err := (&store.ProjectRepo{DB: db}).Create(ctx, &model.Project{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	repo := &store.KBRepo{DB: db}
	put := func(source, project, path, title, body string, links ...string) {
		d := model.KBParsed{KBDoc: model.KBDoc{
			ID: source + ":" + project + ":" + path, Source: source, ProjectID: project, Path: path, CommitSHA: "c", ContentHash: path,
			Type: "Doc", Status: model.KBStatusStable, Trust: model.KBTrustUnverified, Title: title, Links: links, Meta: map[string]any{},
		}, Body: body}
		if err := repo.Upsert(ctx, d, nil); err != nil {
			t.Fatal(err)
		}
	}
	put("factory", "", "a.md", "Factory retries", "retries billing backoff")
	put("repo", "p1", "b.md", "P1 retries", "retries worker", "repo:p1:linked.md")
	put("repo", "p1", "linked.md", "Linked only", "nothing relevant zzz")
	put("repo", "p2", "c.md", "P2 retries", "retries billing")
	s := &KBDocs{KB: &KB{Idx: kb.Index(repo)}}

	docs, err := s.Related(ctx, "p1", "retries billing", 5)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	got := strings.Join(ids, ",")
	if !strings.Contains(got, "factory::a.md") || !strings.Contains(got, "repo:p1:b.md") || strings.Contains(got, "p2") || strings.Contains(got, "linked") {
		t.Fatalf("docs %s", got)
	}
	docs, _ = s.Related(ctx, "", "retries billing", 5)
	if len(docs) != 1 || docs[0].ID != "factory::a.md" {
		t.Fatalf("factory-wide only: %+v", docs)
	}
}

func TestCreateWorkOrderFromChat(t *testing.T) {
	ctx := context.Background()
	f, _, wos := monitorFactory(t, nil)
	f.DataDir = t.TempDir()
	box := &cloneSandbox{dataDir: f.DataDir, files: map[string]string{"go.mod": "module x"}}
	chats := &store.ChatRepo{DB: wos.DB}
	w := &WorkOrders{Store: wos, Projects: &Projects{Store: &store.ProjectRepo{DB: wos.DB}}, Sandbox: box, Settings: fixedSettings{}, Chats: chats}
	u := &model.User{ID: "u1", Role: model.RoleAdmin}
	now := time.Now()
	mine := &model.Chat{ID: "chat_mine", UserID: "u1", Title: "t", CreatedAt: now, UpdatedAt: now}
	theirs := &model.Chat{ID: "chat_theirs", UserID: "u2", Title: "t", CreatedAt: now, UpdatedAt: now}
	for _, c := range []*model.Chat{mine, theirs} {
		if err := chats.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	req := model.CreateWorkOrderRequest{ProjectID: "p1", Title: "t", Objective: "o", SourceChatID: "chat_mine"}
	wo, err := w.Create(ctx, u, req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := wos.Get(ctx, wo.ID)
	if got.SourceChatID != "chat_mine" {
		t.Fatalf("stored source chat %q", got.SourceChatID)
	}
	ev, _ := wos.LatestEvent(ctx, wo.ID, "created_from_chat")
	if ev == nil || ev.Payload["chat_id"] != "chat_mine" {
		t.Fatalf("event %+v", ev)
	}
	for _, id := range []string{"chat_theirs", "chat_missing"} {
		req.SourceChatID = id
		if _, err := w.Create(ctx, u, req); !errors.Is(err, model.ErrChatNotFound) {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if len(box.destroyed) != 0 {
		t.Fatal("a rejected chat link must fail before any workspace is prepared")
	}
	req.SourceChatID = ""
	plain, err := w.Create(ctx, u, req)
	if err != nil || plain.SourceChatID != "" {
		t.Fatalf("no link: %v %+v", err, plain)
	}
	if err := chats.Delete(ctx, "chat_mine"); err != nil {
		t.Fatal(err)
	}
	if got, _ := wos.Get(ctx, wo.ID); got.SourceChatID != "" {
		t.Fatalf("deleting the chat should clear the link, got %q", got.SourceChatID)
	}
}
