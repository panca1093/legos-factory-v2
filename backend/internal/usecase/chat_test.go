package usecase

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type chatPlain struct {
	mu     sync.Mutex
	starts []model.AgentRequest
	reply  string
	err    error // Result fails after streaming the reply, reporting errRes
	errRes model.AgentResult
	gate   chan struct{} // when set, Result waits for it
	inRun  chan struct{} // when set, Result signals that it has been reached
	n      int
}

func (*chatPlain) Name() string                         { return "fake" }
func (*chatPlain) Cancel(context.Context, string) error { return nil }
func (h *chatPlain) Start(_ context.Context, req model.AgentRequest) (model.AgentSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.starts = append(h.starts, req)
	h.n++
	return h.stream(req, fmt.Sprintf("s%d", h.n)), nil
}
func (h *chatPlain) stream(req model.AgentRequest, id string) model.AgentSession {
	for _, w := range strings.SplitAfter(h.reply, " ") {
		req.OnText(w)
	}
	return idSession(id)
}
func (h *chatPlain) Result(context.Context, string) (model.AgentResult, error) {
	if h.inRun != nil {
		h.inRun <- struct{}{}
	}
	if h.gate != nil {
		<-h.gate
	}
	if h.err != nil {
		return h.errRes, h.err
	}
	return model.AgentResult{Text: h.reply, TokensIn: 10, TokensOut: 5, CostMicros: 7, CostSource: "measured"}, nil
}

type chatKeeper struct {
	*chatPlain
	conts  []model.AgentRequest
	closed []string
	gone   bool
}

func (h *chatKeeper) Continue(_ context.Context, id string, req model.AgentRequest) (model.AgentSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gone {
		return nil, model.ErrAgentSessionNotFound
	}
	h.conts = append(h.conts, req)
	return h.stream(req, id), nil
}
func (h *chatKeeper) Close(id string) { h.closed = append(h.closed, id) }

type usageSink struct {
	mu       sync.Mutex
	recs     []model.UsageRecord
	deny     bool
	reserved int
	released int
}

func (u *usageSink) Reserve(int64) (func(int64), bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.deny {
		return nil, false
	}
	u.reserved++
	return func(int64) { u.mu.Lock(); u.released++; u.mu.Unlock() }, true
}
func (u *usageSink) Record(_ context.Context, r model.UsageRecord) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.recs = append(u.recs, r)
	return nil
}

type fakeDocs struct{ docs map[string]*KBDetail }

func (f fakeDocs) Detail(_ context.Context, _ *model.User, id string) (*KBDetail, error) {
	if d, ok := f.docs[id]; ok {
		return d, nil
	}
	return nil, model.ErrKBDocNotFound
}

type chatRig struct {
	chat  *Chat
	usage *usageSink
	ana   *model.User
	bob   *model.User
}

func newChatRig(t *testing.T, h Harness) *chatRig {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	usage := &usageSink{}
	projRepo, members := &store.ProjectRepo{DB: db}, &store.MemberRepo{DB: db}
	users := &store.UserRepo{DB: db}
	if err := projRepo.Create(context.Background(), &model.Project{ID: "p1", Name: "p1"}); err != nil {
		t.Fatal(err)
	}
	ana := &model.User{ID: "ana", Email: "ana@t", Name: "ana", Role: model.RoleEngineer}
	bob := &model.User{ID: "bob", Email: "bob@t", Name: "bob", Role: model.RoleEngineer}
	for _, u := range []*model.User{ana, bob} {
		if err := users.Create(context.Background(), *u); err != nil {
			t.Fatal(err)
		}
	}
	if err := members.Add(context.Background(), "p1", "ana"); err != nil {
		t.Fatal(err)
	}
	return &chatRig{usage: usage, ana: ana, bob: bob, chat: &Chat{
		Store:    &store.ChatRepo{DB: db},
		Projects: &Projects{Store: projRepo, Members: members, Users: users},
		Docs: fakeDocs{docs: map[string]*KBDetail{
			"d1": {Doc: model.KBDoc{ID: "d1", Title: "Money rules"}, Body: "Money is integer rupiah."},
		}},
		Settings: &store.SettingsRepo{DB: db},
		Usage:    usage, Harness: h, Dir: filepath.Join(t.TempDir(), "chat"),
		Models: func(context.Context) ([]model.ModelInfo, error) {
			return []model.ModelInfo{
				{ProviderID: "anthropic", ModelID: "opus", Name: "Opus", Harness: "fake"},
				{ProviderID: "anthropic", ModelID: "haiku", Name: "Haiku", Harness: "fake"},
			}, nil
		},
		Default: func(context.Context) string { return "anthropic/opus" },
		Gen: func(context.Context, string, string) (model.AgentResult, error) {
			return model.AgentResult{}, errors.New("no titler")
		},
	}}
}

func (r *chatRig) send(t *testing.T, u *model.User, id string, in ChatSend) ([]ChatEvent, error) {
	t.Helper()
	var evs []ChatEvent
	err := r.chat.Send(context.Background(), u, id, in, func(e ChatEvent) { evs = append(evs, e) })
	return evs, err
}

func kinds(evs []ChatEvent) string {
	var k []string
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return strings.Join(k, ",")
}

func TestChatLifecycleAndOwnership(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{reply: "hi"})
	ch, err := r.chat.Create(ctx, r.ana, "", "", "")
	if err != nil || ch.Title != "New chat" {
		t.Fatalf("create: %v %+v", err, ch)
	}
	if _, err := r.chat.Create(ctx, r.ana, "scoped", "p1", "anthropic/haiku"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.chat.Create(ctx, r.bob, "x", "p1", ""); !errors.Is(err, model.ErrProjectNotFound) {
		t.Fatalf("project outsider: %v", err)
	}
	if _, err := r.chat.Create(ctx, r.ana, "x", "", "anthropic/nope"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown model: %v", err)
	}
	title := "Renamed"
	if got, err := r.chat.Update(ctx, r.ana, ch.ID, &title, nil, nil); err != nil || got.Title != "Renamed" {
		t.Fatalf("rename: %v %+v", err, got)
	}
	empty := " "
	if _, err := r.chat.Update(ctx, r.ana, ch.ID, &empty, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank title: %v", err)
	}
	if list, _ := r.chat.List(ctx, r.ana); len(list) != 2 {
		t.Fatalf("ana lists %d", len(list))
	}
	if list, _ := r.chat.List(ctx, r.bob); len(list) != 0 {
		t.Fatalf("bob sees %d chats", len(list))
	}

	notFound := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, model.ErrChatNotFound) {
			t.Errorf("%s on another user's chat: %v", name, err)
		}
	}
	_, err = r.chat.Get(ctx, r.bob, ch.ID)
	notFound("get", err)
	_, err = r.chat.Update(ctx, r.bob, ch.ID, &title, nil, nil)
	notFound("update", err)
	_, err = r.chat.Messages(ctx, r.bob, ch.ID)
	notFound("messages", err)
	_, err = r.send(t, r.bob, ch.ID, ChatSend{Content: "hi"})
	notFound("send", err)
	notFound("delete", r.chat.Delete(ctx, r.bob, ch.ID))

	if err := r.chat.Delete(ctx, r.ana, ch.ID); err != nil {
		t.Fatal(err)
	}
	_, err = r.chat.Get(ctx, r.ana, ch.ID)
	notFound("get after delete", err)
}

func TestChatSendStreamsAndPersists(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "Four is the answer"}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	evs, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "  what is 2+2?  "})
	if err != nil {
		t.Fatal(err)
	}
	if k := kinds(evs); !strings.HasPrefix(k, "user_message,delta") || !strings.HasSuffix(k, ",done") {
		t.Fatalf("events: %s", k)
	}
	var streamed string
	for _, e := range evs {
		if e.Kind == "delta" {
			streamed += e.Text
		}
	}
	last := evs[len(evs)-1].Message
	if streamed != "Four is the answer" || last.Content != streamed || last.Usage.TokensOut != 5 || last.Usage.Model != "anthropic/opus" {
		t.Fatalf("streamed %q, done %+v", streamed, last)
	}
	msgs, _ := r.chat.Messages(ctx, r.ana, ch.ID)
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[0].Content != "what is 2+2?" || msgs[1].Role != "assistant" || msgs[1].Content != streamed {
		t.Fatalf("persisted: %+v", msgs)
	}
	got, _ := r.chat.Get(ctx, r.ana, ch.ID)
	if got.Title != "what is 2+2" {
		t.Fatalf("title: %q", got.Title)
	}
	req := h.starts[0]
	if req.Role != model.AgentChat || req.ProviderID != "anthropic" || req.ModelID != "opus" || req.WorkspaceDir == "" {
		t.Fatalf("request: %+v", req)
	}
	if len(r.usage.recs) != 1 || r.usage.recs[0].Role != "chat" || r.usage.recs[0].UserID != "ana" || r.usage.recs[0].TokensIn != 10 || r.usage.recs[0].Model != "opus" {
		t.Fatalf("usage: %+v", r.usage.recs)
	}

	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "and 3+3?"}); err != nil {
		t.Fatal(err)
	}
	second := h.starts[1].Prompt
	if !strings.Contains(second, "User: what is 2+2?") || !strings.Contains(second, "Assistant: Four is the answer") || !strings.HasSuffix(second, "and 3+3?") {
		t.Fatalf("a harness without sessions gets history in the prompt: %q", second)
	}
	if got, _ := r.chat.Get(ctx, r.ana, ch.ID); got.Title != "what is 2+2" {
		t.Fatalf("title changed to %q", got.Title)
	}
}

func TestChatKeepsOneSessionPerConversation(t *testing.T) {
	ctx := context.Background()
	h := &chatKeeper{chatPlain: &chatPlain{reply: "ok"}}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	r.send(t, r.ana, ch.ID, ChatSend{Content: "first"})
	r.send(t, r.ana, ch.ID, ChatSend{Content: "second"})
	if len(h.starts) != 1 || !h.starts[0].Keep || len(h.conts) != 1 || h.conts[0].Prompt != "second" {
		t.Fatalf("starts %d keep=%v conts %+v", len(h.starts), h.starts[0].Keep, h.conts)
	}

	h.gone = true
	evs, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "third"})
	if err != nil || evs[len(evs)-1].Kind != "done" || len(h.starts) != 2 {
		t.Fatalf("lost session should start over: %v %s starts=%d", err, kinds(evs), len(h.starts))
	}
	if p := h.starts[1].Prompt; !strings.Contains(p, "User: first") || !strings.Contains(p, "User: second") {
		t.Fatalf("restart replays history: %q", p)
	}

	h.gone = false
	model2 := "anthropic/haiku"
	if _, err := r.chat.Update(ctx, r.ana, ch.ID, nil, &model2, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.closed) != 2 || h.closed[1] != "s2" {
		t.Fatalf("changing the model should end the session: %v", h.closed)
	}
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "fourth"}); err != nil {
		t.Fatal(err)
	}
	if len(h.starts) != 3 || h.starts[2].ModelID != "haiku" {
		t.Fatalf("new session on the new model: %d starts, %d conts, closed %v", len(h.starts), len(h.conts), h.closed)
	}
	if err := r.chat.Delete(ctx, r.ana, ch.ID); err != nil || len(h.closed) != 3 {
		t.Fatalf("delete closes the session: %v %v", err, h.closed)
	}
}

func TestChatFailedReplyIsStoredWithErrorMarker(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "partial answer", err: errors.New("agent exited")}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	evs, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q"})
	if err != nil {
		t.Fatal(err)
	}
	end := evs[len(evs)-1]
	if end.Kind != "error" || end.Error != "agent exited" || end.Message.Content != "partial answer" {
		t.Fatalf("end: %+v", end)
	}
	msgs, _ := r.chat.Messages(ctx, r.ana, ch.ID)
	if len(msgs) != 2 || msgs[1].Error != "agent exited" || msgs[1].Content != "partial answer" {
		t.Fatalf("persisted: %+v", msgs)
	}
	if len(r.usage.recs) != 0 {
		t.Fatalf("no usage for a failed reply: %+v", r.usage.recs)
	}
	h.err = nil
	if evs, _ := r.send(t, r.ana, ch.ID, ChatSend{Content: "again"}); evs[len(evs)-1].Kind != "done" {
		t.Fatal("chat should recover after a failed reply")
	}
}

func TestChatRejectsConcurrentSend(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "x", gate: make(chan struct{}), inRun: make(chan struct{}, 16)}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	done := make(chan struct{})
	go func() { r.send(t, r.ana, ch.ID, ChatSend{Content: "one"}); close(done) }()
	<-h.inRun
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "two"}); !errors.Is(err, ErrChatBusy) {
		t.Fatalf("second send while answering: %v", err)
	}
	if err := r.chat.Delete(ctx, r.ana, ch.ID); !errors.Is(err, ErrChatBusy) {
		t.Fatalf("delete while answering: %v", err)
	}
	close(h.gate)
	<-done
}

func TestChatAttachmentLimits(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "ok"}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	big := strings.Repeat("a", maxChatFile+1)
	many := make([]ChatFile, maxChatFiles+1)
	for i := range many {
		many[i] = ChatFile{Name: "f.txt", Content: "x"}
	}
	chunk := strings.Repeat("b", maxChatFile)
	cases := map[string]ChatSend{
		"empty message": {Content: "  "},
		"binary":        {Content: "q", Files: []ChatFile{{Name: "a.bin", Content: "ab\x00cd"}}},
		"invalid utf8":  {Content: "q", Files: []ChatFile{{Name: "a.bin", Content: "\xff\xfe"}}},
		"too large":     {Content: "q", Files: []ChatFile{{Name: "big.txt", Content: big}}},
		"too many":      {Content: "q", Files: many},
		"total":         {Content: "q", Files: []ChatFile{{Name: "1", Content: chunk}, {Name: "2", Content: chunk}, {Name: "3", Content: chunk}, {Name: "4", Content: chunk}}},
		"no name":       {Content: "q", Files: []ChatFile{{Name: "", Content: "x"}}},
		"long quote":    {Content: "q", KB: &ChatKBRef{ID: "d1", Quote: strings.Repeat("q", maxChatQuote+1)}},
	}
	for name, in := range cases {
		evs, err := r.send(t, r.ana, ch.ID, in)
		if !errors.Is(err, ErrInvalid) || len(evs) != 0 {
			t.Errorf("%s: err=%v events=%d", name, err, len(evs))
		}
	}
	if msgs, _ := r.chat.Messages(ctx, r.ana, ch.ID); len(msgs) != 0 || len(h.starts) != 0 {
		t.Fatalf("rejected sends must store and run nothing: %d msgs, %d runs", len(msgs), len(h.starts))
	}

	evs, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "summarise", Files: []ChatFile{{Name: "dir/notes.txt", Content: "line one\nline two"}}})
	if err != nil {
		t.Fatal(err)
	}
	p := h.starts[0].Prompt
	if !strings.Contains(p, "Attached file notes.txt") || !strings.Contains(p, "line two") || !strings.HasSuffix(p, "summarise") {
		t.Fatalf("prompt: %q", p)
	}
	att := evs[0].Message.Attachments
	if len(att) != 1 || att[0].Kind != "file" || att[0].Name != "notes.txt" || att[0].Size != 17 {
		t.Fatalf("attachment meta: %+v", att)
	}
	if strings.Contains(fmt.Sprint(att), "line two") {
		t.Fatal("file content must not be stored")
	}
}

func TestChatKnowledgeContext(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "ok"}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	evs, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "explain this", KB: &ChatKBRef{ID: "d1", Quote: "integer rupiah"}})
	if err != nil {
		t.Fatal(err)
	}
	p := h.starts[0].Prompt
	for _, want := range []string{`"Money rules" (d1)`, "Money is integer rupiah.", "<selection>\ninteger rupiah\n</selection>", "explain this"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q: %s", want, p)
		}
	}
	att := evs[0].Message.Attachments
	if len(att) != 1 || att[0].Kind != "kb" || att[0].DocID != "d1" || att[0].Name != "Money rules" || att[0].Quote != "integer rupiah" {
		t.Fatalf("attachment meta: %+v", att)
	}
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", KB: &ChatKBRef{ID: "missing"}}); !errors.Is(err, model.ErrKBDocNotFound) {
		t.Fatalf("unknown document: %v", err)
	}
}

func TestChatModelList(t *testing.T) {
	ctx := context.Background()
	r := newChatRig(t, &chatPlain{})
	list, err := r.chat.ModelList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if list.Default != "anthropic/opus" || len(list.Models) != 3 || list.Models[0].ID != "" || list.Models[0].Name != "Auto (default)" || list.Models[1].ID != "anthropic/opus" {
		t.Fatalf("list: %+v", list)
	}
	r.chat.Settings.(*store.SettingsRepo).Put(ctx, model.Settings{AllowedModels: []string{"anthropic/haiku"}})
	list, _ = r.chat.ModelList(ctx)
	if len(list.Models) != 2 || list.Models[1].ID != "anthropic/haiku" {
		t.Fatalf("allowed-models filter: %+v", list)
	}
	if _, err := r.chat.Create(ctx, r.ana, "x", "", "anthropic/opus"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a model outside the allowed list: %v", err)
	}
}

func TestChatBudgetReservedReleasedAndEnforced(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "ok"}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "hi"}); err != nil || r.usage.reserved != 1 || r.usage.released != 1 {
		t.Fatalf("reserve/release: %v %+v", err, r.usage)
	}
	r.usage.deny = true
	evs, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "again"})
	if !errors.Is(err, ErrChatBudget) || len(evs) != 0 {
		t.Fatalf("over budget: %v, %d events", err, len(evs))
	}
	if msgs, _ := r.chat.Messages(ctx, r.ana, ch.ID); len(msgs) != 2 || len(h.starts) != 1 {
		t.Fatalf("a refused turn stores and runs nothing: %d msgs, %d runs", len(msgs), len(h.starts))
	}
}

func TestChatRecordsUsageOfFailedTurn(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "part", err: errors.New("boom"), errRes: model.AgentResult{TokensIn: 3, TokensOut: 1}}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	r.send(t, r.ana, ch.ID, ChatSend{Content: "q"})
	if len(r.usage.recs) != 1 || r.usage.recs[0].TokensIn != 3 || r.usage.released != 1 {
		t.Fatalf("usage %+v", r.usage)
	}
}

func TestChatUpdateWaitsOutATurnAndSendRechecksAllowedModel(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "x", gate: make(chan struct{}), inRun: make(chan struct{}, 16)}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "anthropic/haiku")
	done := make(chan struct{})
	go func() { r.send(t, r.ana, ch.ID, ChatSend{Content: "one"}); close(done) }()
	<-h.inRun
	title := "t"
	if _, err := r.chat.Update(ctx, r.ana, ch.ID, &title, nil, nil); !errors.Is(err, ErrChatBusy) {
		t.Fatalf("update while answering: %v", err)
	}
	close(h.gate)
	<-done
	r.chat.Settings.(*store.SettingsRepo).Put(ctx, model.Settings{AllowedModels: []string{"anthropic/opus"}})
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "two"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("model no longer allowed: %v", err)
	}
}

func TestChatLimitsConcurrentTurns(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "x", gate: make(chan struct{}), inRun: make(chan struct{}, 16)}
	r := newChatRig(t, h)
	var wg sync.WaitGroup
	var ids []string
	for i := 0; i < maxChatTurns; i++ {
		ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
		ids = append(ids, ch.ID)
		wg.Add(1)
		go func() { defer wg.Done(); r.send(t, r.ana, ch.ID, ChatSend{Content: "go"}) }()
		<-h.inRun
	}
	extra, _ := r.chat.Create(ctx, r.ana, "", "", "")
	if _, err := r.send(t, r.ana, extra.ID, ChatSend{Content: "go"}); !errors.Is(err, ErrChatSaturated) {
		t.Fatalf("beyond the limit: %v", err)
	}
	close(h.gate)
	wg.Wait()
	if _, err := r.send(t, r.ana, extra.ID, ChatSend{Content: "go"}); err != nil {
		t.Fatalf("a slot is free again: %v", err)
	}
}

func TestChatUnavailableWithoutHarness(t *testing.T) {
	r := newChatRig(t, nil)
	ch, _ := r.chat.Create(context.Background(), r.ana, "", "", "")
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "hi"}); !errors.Is(err, ErrChatUnavailable) {
		t.Fatalf("no harness: %v", err)
	}
}

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR-test-image")

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestChatImageDetection(t *testing.T) {
	webp := append(append([]byte("RIFF\x00\x00\x00\x00"), "WEBP"...), 'x')
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"png", pngBytes, "image/png"},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 1}, "image/jpeg"},
		{"gif87", []byte("GIF87a.."), "image/gif"},
		{"gif89", []byte("GIF89a.."), "image/gif"},
		{"webp", webp, "image/webp"},
		{"riff but wav", append(append([]byte("RIFF\x00\x00\x00\x00"), "WAVE"...), 'x'), ""},
		{"text", []byte("hello"), ""},
	}
	ctx := context.Background()
	h := &chatPlain{reply: "ok"}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	for _, c := range cases {
		// a mislabelled extension must not matter: the signature decides
		_, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", Files: []ChatFile{{Name: "pic.txt", Content: b64(c.data), Encoding: "base64"}}})
		if c.want == "" {
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "pic.txt is not a supported image (PNG, JPEG, GIF or WebP).") {
				t.Errorf("%s: err=%v", c.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := h.starts[len(h.starts)-1].Images; len(got) != 1 || got[0].MediaType != c.want || string(got[0].Data) != string(c.data) {
			t.Errorf("%s: images %+v", c.name, got)
		}
	}

	n := len(h.starts)
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", Files: []ChatFile{{Name: "x.png", Content: "plain text"}}}); err != nil {
		t.Errorf("unencoded stays text: %v", err)
	}
	if len(h.starts) != n+1 || len(h.starts[n].Images) != 0 || !strings.Contains(h.starts[n].Prompt, "Attached file x.png") {
		t.Fatalf("unencoded file must stay text: %+v", h.starts[n:])
	}
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", Files: []ChatFile{{Name: "x.png", Content: "!!!", Encoding: "base64"}}}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "x.png") {
		t.Errorf("malformed base64 should name the file: %v", err)
	}
}

func TestChatImageAttachment(t *testing.T) {
	ctx := context.Background()
	h := &chatKeeper{chatPlain: &chatPlain{reply: "ok"}}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	in := ChatSend{Content: "what is this?", Files: []ChatFile{
		{Name: "dir/shot.png", Content: b64(pngBytes), Encoding: "base64"},
		{Name: "n.txt", Content: "note"},
	}}
	check := func(label string, reqs []model.AgentRequest) {
		t.Helper()
		last := reqs[len(reqs)-1]
		if len(last.Images) != 1 || last.Images[0].MediaType != "image/png" || string(last.Images[0].Data) != string(pngBytes) {
			t.Fatalf("%s: images %+v", label, last.Images)
		}
		if strings.Contains(last.Prompt, b64(pngBytes)) || !strings.Contains(last.Prompt, "Attached file n.txt") {
			t.Fatalf("%s: prompt %q", label, last.Prompt)
		}
	}

	evs, err := r.send(t, r.ana, ch.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	check("new session", h.starts)
	att := evs[0].Message.Attachments
	if len(att) != 2 || att[0] != (model.ChatAttachment{Kind: "image", Name: "shot.png", Size: len(pngBytes)}) || att[1].Kind != "file" {
		t.Fatalf("attachments: %+v", att)
	}

	if _, err := r.send(t, r.ana, ch.ID, in); err != nil {
		t.Fatal(err)
	}
	check("continue", h.conts)

	h.gone = true
	if _, err := r.send(t, r.ana, ch.ID, in); err != nil {
		t.Fatal(err)
	}
	check("lost-session fallback", h.starts)
	p := h.starts[len(h.starts)-1].Prompt
	if !strings.Contains(p, "(attached image: shot.png)") || strings.Contains(p, b64(pngBytes)) {
		t.Fatalf("history replay shows the name only: %q", p)
	}

	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "plain"}); err != nil {
		t.Fatal(err)
	}
	if got := h.starts[len(h.starts)-1].Images; len(got) != 0 {
		t.Fatalf("turn without images carries none: %+v", got)
	}
	msgs, _ := r.chat.Messages(ctx, r.ana, ch.ID)
	if strings.Contains(fmt.Sprint(msgs), b64(pngBytes)) {
		t.Fatal("image bytes must not be stored")
	}
}

func TestChatZipRejected(t *testing.T) {
	r := newChatRig(t, &chatPlain{reply: "ok"})
	ch, _ := r.chat.Create(context.Background(), r.ana, "", "", "")
	_, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", Files: []ChatFile{{Name: "a.zip", Content: "PK\x03\x04\x00\x00"}}})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "a.zip looks binary; only text files can be attached") {
		t.Fatalf("zip: %v", err)
	}
}

func TestChatImageSizeCaps(t *testing.T) {
	ctx := context.Background()
	h := &chatPlain{reply: "ok"}
	r := newChatRig(t, h)
	ch, _ := r.chat.Create(ctx, r.ana, "", "", "")
	pad := func(n int) string { // valid base64 of a PNG, padded to n characters
		raw := append(append([]byte{}, pngBytes...), make([]byte, (n/4)*3)...)
		return b64(raw)[:n]
	}
	img := func(name string, n int) ChatFile { return ChatFile{Name: name, Content: pad(n), Encoding: "base64"} }

	_, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", Files: []ChatFile{img("big.png", 5242881)}})
	if !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), "big.png is over 3.75 MB.") {
		t.Fatalf("over cap: %v", err)
	}
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", Files: []ChatFile{img("ok.png", 5242880)}}); err != nil {
		t.Fatalf("at cap: %v", err)
	}
	_, err = r.send(t, r.ana, ch.ID, ChatSend{Content: "q", Files: []ChatFile{img("1.png", 5242880), img("2.png", 5242880), img("3.png", 4)}})
	if !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), "Images can total 7.5 MB at most.") {
		t.Fatalf("total: %v", err)
	}
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", Files: []ChatFile{img("1.png", 5242880), img("2.png", 5242880)}}); err != nil {
		t.Fatalf("total at cap: %v", err)
	}

	big := strings.Repeat("a", maxChatFile)
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", Files: []ChatFile{img("1.png", 5242880), img("2.png", 5242880), {Name: "1", Content: big}, {Name: "2", Content: big}, {Name: "3", Content: big}}}); err != nil {
		t.Fatalf("images and text count against separate caps: %v", err)
	}
	many := []ChatFile{img("1.png", 16), img("2.png", 16), img("3.png", 16), {Name: "a", Content: "x"}, {Name: "b", Content: "x"}, {Name: "c", Content: "x"}}
	if _, err := r.send(t, r.ana, ch.ID, ChatSend{Content: "q", Files: many}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "at most 5 files") {
		t.Fatalf("count includes images: %v", err)
	}
}
