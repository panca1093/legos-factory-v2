package usecase

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

// permHarness asks for each tool in calls, one after another, and replies with the answers.
type permHarness struct {
	calls []string
	ctx   func() (context.Context, context.CancelFunc)
	done  chan string
}

func (*permHarness) Name() string                         { return "perm" }
func (*permHarness) Cancel(context.Context, string) error { return nil }
func (h *permHarness) Start(_ context.Context, req model.AgentRequest) (model.AgentSession, error) {
	h.done = make(chan string, 1)
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		if h.ctx != nil {
			ctx, cancel = h.ctx()
		}
		defer cancel()
		var out []string
		for _, tool := range h.calls {
			if req.OnPermission(ctx, model.PermissionRequest{Server: "docs", Tool: tool, Input: map[string]any{"n": 1}}) {
				out = append(out, "ran")
			} else {
				out = append(out, "refused")
			}
		}
		h.done <- strings.Join(out, ",")
	}()
	return idSession("s1"), nil
}
func (h *permHarness) Result(ctx context.Context, _ string) (model.AgentResult, error) {
	select {
	case s := <-h.done:
		return model.AgentResult{Text: s}, nil
	case <-ctx.Done():
		return model.AgentResult{}, ctx.Err()
	}
}

type permTurn struct {
	r      *chatRig
	chatID string
	mu     sync.Mutex
	evs    []ChatEvent
	next   int
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func startPermTurn(t *testing.T, h *permHarness) *permTurn {
	t.Helper()
	old := permissionWait
	permissionWait = 2 * time.Second
	t.Cleanup(func() { permissionWait = old })
	r := newChatRig(t, h)
	ch, err := r.chat.Create(context.Background(), r.ana, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &permTurn{r: r, chatID: ch.ID, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

func (p *permTurn) send(t *testing.T) {
	t.Helper()
	go func() {
		defer close(p.done)
		_ = p.r.chat.Send(p.ctx, p.r.ana, p.chatID, ChatSend{Content: "go"}, func(e ChatEvent) {
			p.mu.Lock()
			p.evs = append(p.evs, e)
			p.mu.Unlock()
		})
	}()
}

func (p *permTurn) waitEvent(t *testing.T, kind string) ChatEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		for p.next < len(p.evs) {
			e := p.evs[p.next]
			p.next++
			if e.Kind == kind {
				p.mu.Unlock()
				return e
			}
		}
		p.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no %s event; got %s", kind, p.kinds())
	return ChatEvent{}
}

func (p *permTurn) kinds() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return kinds(p.evs)
}

func (p *permTurn) finish(t *testing.T) string {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("turn did not finish; got %s", p.kinds())
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	last := p.evs[len(p.evs)-1]
	if last.Kind != "done" {
		t.Fatalf("last event %+v", last)
	}
	return last.Message.Content
}

func TestChatPermissionAllowAndDeny(t *testing.T) {
	for _, c := range []struct{ decision, outcome, reply string }{
		{"allow", "allowed", "ran"}, {"deny", "denied", "refused"},
	} {
		p := startPermTurn(t, &permHarness{calls: []string{"write"}})
		p.send(t)
		ask := p.waitEvent(t, "permission")
		if ask.ReqID == "" || ask.Server != "docs" || ask.Tool != "write" || ask.Input != `{"n":1}` {
			t.Fatalf("permission event: %+v", ask)
		}
		if err := p.r.chat.AnswerPermission(context.Background(), p.r.ana, p.chatID, ask.ReqID, c.decision); err != nil {
			t.Fatal(err)
		}
		if res := p.waitEvent(t, "permission_resolved"); res.ReqID != ask.ReqID || res.Outcome != c.outcome {
			t.Fatalf("resolved: %+v", res)
		}
		if got := p.finish(t); got != c.reply {
			t.Fatalf("%s: reply %q, want %q", c.decision, got, c.reply)
		}
		if err := p.r.chat.AnswerPermission(context.Background(), p.r.ana, p.chatID, ask.ReqID, "allow"); !errors.Is(err, ErrPermissionNotFound) {
			t.Fatalf("answering twice: %v", err)
		}
	}
}

func TestChatPermissionAllowAllSkipsLaterAsks(t *testing.T) {
	p := startPermTurn(t, &permHarness{calls: []string{"write", "write", "other"}})
	p.send(t)
	ask := p.waitEvent(t, "permission")
	if err := p.r.chat.AnswerPermission(context.Background(), p.r.ana, p.chatID, ask.ReqID, "allow_all"); err != nil {
		t.Fatal(err)
	}
	p.waitEvent(t, "permission_resolved")
	second := p.waitEvent(t, "permission")
	if second.Tool != "other" {
		t.Fatalf("the allowed-for-chat tool must not ask again, got %+v (%s)", second, p.kinds())
	}
	if err := p.r.chat.AnswerPermission(context.Background(), p.r.ana, p.chatID, second.ReqID, "deny"); err != nil {
		t.Fatal(err)
	}
	if got := p.finish(t); got != "ran,ran,refused" {
		t.Fatalf("reply %q", got)
	}
	if n := strings.Count(p.kinds(), "permission,"); n != 2 {
		t.Fatalf("events %s", p.kinds())
	}

	ctx := context.Background()
	if err := p.r.chat.Delete(ctx, p.r.ana, p.chatID); err != nil {
		t.Fatal(err)
	}
	p.r.chat.mu.Lock()
	left := len(p.r.chat.allowAll)
	p.r.chat.mu.Unlock()
	if left != 0 {
		t.Fatal("deleting the chat must drop its allow-all set")
	}
}

func TestChatPermissionTimesOutAsExpired(t *testing.T) {
	p := startPermTurn(t, &permHarness{calls: []string{"write"}})
	permissionWait = 30 * time.Millisecond
	p.send(t)
	ask := p.waitEvent(t, "permission")
	if res := p.waitEvent(t, "permission_resolved"); res.Outcome != "expired" {
		t.Fatalf("resolved: %+v", res)
	}
	if got := p.finish(t); got != "refused" {
		t.Fatalf("reply %q", got)
	}
	if err := p.r.chat.AnswerPermission(context.Background(), p.r.ana, p.chatID, ask.ReqID, "allow"); !errors.Is(err, ErrPermissionNotFound) {
		t.Fatalf("answer after expiry: %v", err)
	}
	p.r.chat.mu.Lock()
	left := len(p.r.chat.perms)
	p.r.chat.mu.Unlock()
	if left != 0 {
		t.Fatal("the pending entry must be removed")
	}
}

func TestChatPermissionEndsWhenHarnessContextIsCancelled(t *testing.T) {
	cancelled := make(chan context.CancelFunc, 1)
	h := &permHarness{calls: []string{"write"}, ctx: func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancelled <- cancel
		return ctx, cancel
	}}
	p := startPermTurn(t, h)
	p.send(t)
	p.waitEvent(t, "permission")
	(<-cancelled)()
	if res := p.waitEvent(t, "permission_resolved"); res.Outcome != "expired" {
		t.Fatalf("resolved: %+v", res)
	}
	if got := p.finish(t); got != "refused" {
		t.Fatalf("reply %q", got)
	}
}

func TestChatPermissionOnlyTheOwnerCanAnswer(t *testing.T) {
	p := startPermTurn(t, &permHarness{calls: []string{"write"}})
	p.send(t)
	ask := p.waitEvent(t, "permission")
	ctx := context.Background()
	if err := p.r.chat.AnswerPermission(ctx, p.r.bob, p.chatID, ask.ReqID, "allow"); !errors.Is(err, model.ErrChatNotFound) {
		t.Fatalf("another user: %v", err)
	}
	other, err := p.r.chat.Create(ctx, p.r.ana, "other", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.r.chat.AnswerPermission(ctx, p.r.ana, other.ID, ask.ReqID, "allow"); !errors.Is(err, ErrPermissionNotFound) {
		t.Fatalf("another chat's request: %v", err)
	}
	if err := p.r.chat.AnswerPermission(ctx, p.r.ana, p.chatID, ask.ReqID, "sure"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown decision: %v", err)
	}
	if err := p.r.chat.AnswerPermission(ctx, p.r.ana, p.chatID, ask.ReqID, "deny"); err != nil {
		t.Fatalf("the request must survive the refused attempts: %v", err)
	}
	p.finish(t)
}

func TestChatFingerprintIncludesAskSet(t *testing.T) {
	base := model.AgentRequest{MCP: []model.MCPServer{{Name: "docs", Transport: model.MCPHTTP, URL: "https://x"}}, MCPAllow: []string{"mcp__docs__write"}}
	asked := base
	asked.MCPAsk = []string{"mcp__docs__write"}
	if mcpFingerprint(base) == mcpFingerprint(asked) {
		t.Fatal("a changed ask set must start a fresh session")
	}
}

func TestChatPermissionEndsWhenTheCallerLeavesAndTheChatIsFreed(t *testing.T) {
	p := startPermTurn(t, &permHarness{calls: []string{"write"}})
	p.send(t)
	p.waitEvent(t, "permission")
	p.cancel()
	if res := p.waitEvent(t, "permission_resolved"); res.Outcome != "expired" {
		t.Fatalf("resolved: %+v", res)
	}
	if got := p.finish(t); got != "refused" {
		t.Fatalf("reply %q", got)
	}
	title := "x"
	if _, err := p.r.chat.Update(context.Background(), p.r.ana, p.chatID, &title, nil, nil); err != nil {
		t.Fatalf("the chat must not stay busy: %v", err)
	}
}

func TestChatAllowAllDropsWhenTheMCPSelectionChanges(t *testing.T) {
	h := &permKeeper{permHarness: &permHarness{calls: []string{"write"}}}
	p := startPermTurn(t, h.permHarness)
	p.r.chat.Harness = h
	sel := &fakeSelector{sel: selWith("Bearer one", "mcp__docs__write")}
	p.r.chat.MCP = sel
	p.send(t)
	ask := p.waitEvent(t, "permission")
	if err := p.r.chat.AnswerPermission(context.Background(), p.r.ana, p.chatID, ask.ReqID, "allow_all"); err != nil {
		t.Fatal(err)
	}
	p.finish(t)
	has := func() bool {
		p.r.chat.mu.Lock()
		defer p.r.chat.mu.Unlock()
		return len(p.r.chat.allowAll[p.chatID]) > 0
	}
	if !has() {
		t.Fatal("allow-all must be recorded")
	}
	sel.sel = selWith("Bearer two", "mcp__docs__write")
	p2 := &permTurn{r: p.r, chatID: p.chatID, ctx: p.ctx, cancel: p.cancel, done: make(chan struct{})}
	p2.send(t)
	p2.waitEvent(t, "permission")
	if has() {
		t.Fatal("a changed selection starts a fresh session, so allow-all must not carry over")
	}
}

// permKeeper lets the chat keep a session so a changed fingerprint is noticed.
type permKeeper struct{ *permHarness }

func (*permKeeper) Continue(context.Context, string, model.AgentRequest) (model.AgentSession, error) {
	return nil, model.ErrAgentSessionNotFound
}
func (*permKeeper) Close(string) {}
