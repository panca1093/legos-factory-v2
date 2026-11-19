package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

type lspFakeProc struct {
	mu      sync.Mutex
	out     chan []byte
	exit    chan struct{}
	once    sync.Once
	written [][]byte
	killed  bool
	tail    string
	n       int
}

func (p *lspFakeProc) ReadMessage() ([]byte, error) {
	select {
	case m := <-p.out:
		return m, nil
	case <-p.exit:
		return nil, io.EOF
	}
}

func (p *lspFakeProc) WriteMessage(msg []byte) error {
	p.mu.Lock()
	p.written = append(p.written, append([]byte(nil), msg...))
	p.mu.Unlock()
	var h rpcHead
	_ = json.Unmarshal(msg, &h)
	if h.Method == "initialize" {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": h.ID, "result": map[string]any{"capabilities": map[string]any{}, "start": p.n}})
		p.out <- b
	}
	return nil
}

func (p *lspFakeProc) Wait() error        { <-p.exit; return nil }
func (p *lspFakeProc) Kill() error        { p.mu.Lock(); p.killed = true; p.mu.Unlock(); p.die(); return nil }
func (p *lspFakeProc) StderrTail() string { return p.tail }
func (p *lspFakeProc) die()               { p.once.Do(func() { close(p.exit) }) }
func (p *lspFakeProc) wasKilled() bool    { p.mu.Lock(); defer p.mu.Unlock(); return p.killed }
func (p *lspFakeProc) writes() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]byte(nil), p.written...)
}
func (p *lspFakeProc) methods() (ms []string) {
	for _, w := range p.writes() {
		var h rpcHead
		_ = json.Unmarshal(w, &h)
		ms = append(ms, h.Method)
	}
	return
}

type lspFakeHost struct {
	mu    sync.Mutex
	procs []*lspFakeProc
	fail  error
}

func (h *lspFakeHost) Discover(context.Context, []model.LSPServerSpec, map[string]string) []model.LSPServerState {
	return nil
}

func (h *lspFakeHost) Start(_ context.Context, _ model.LSPServerSpec, _, _ string) (model.LSPProcess, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fail != nil {
		return nil, h.fail
	}
	p := &lspFakeProc{out: make(chan []byte, 64), exit: make(chan struct{}), n: len(h.procs) + 1, tail: "stderr line"}
	h.procs = append(h.procs, p)
	return p, nil
}

func (h *lspFakeHost) started() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.procs) }
func (h *lspFakeHost) proc(i int) *lspFakeProc {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.procs[i]
}

func lspNewReg(h *lspFakeHost) *LSPRegistry {
	return &LSPRegistry{Host: h, IdleTimeout: time.Hour, KillGrace: 50 * time.Millisecond, Backoff: []time.Duration{20 * time.Millisecond, 40 * time.Millisecond, 60 * time.Millisecond}}
}

var lspSpec = model.LSPServerSpec{ID: "gopls"}

func lspKey(user, folder string) LSPKey { return LSPKey{User: user, Folder: folder, Server: "gopls"} }

func lspRecv(t *testing.T, c *LSPClient) []byte {
	t.Helper()
	select {
	case m := <-c.Messages():
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("no message")
		return nil
	}
}

func lspWaitDone(t *testing.T, c *LSPClient) int {
	t.Helper()
	select {
	case <-c.Done():
		return c.CloseCode()
	case <-time.After(2 * time.Second):
		t.Fatal("client not closed")
		return -1
	}
}

func lspEventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func lspAttach(t *testing.T, r *LSPRegistry, k LSPKey, root string) *LSPClient {
	t.Helper()
	c, err := r.Attach(context.Background(), k, lspSpec, "/bin/gopls", root)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	return c
}

func TestInitializeIsRewrittenAndCachedForReconnect(t *testing.T) {
	h := &lspFakeHost{}
	r := lspNewReg(h)
	defer r.Shutdown()
	root := t.TempDir()
	c1 := lspAttach(t, r, lspKey("u", "f"), root)
	if err := c1.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"processId":1,"rootUri":"file:///evil","rootPath":"/evil","workspaceFolders":[{"uri":"file:///evil","name":"evil"}],"capabilities":{"x":1}}}`)); err != nil {
		t.Fatal(err)
	}
	resp := lspRecv(t, c1)
	var got struct {
		ID     int `json:"id"`
		Result struct{ Start int }
	}
	_ = json.Unmarshal(resp, &got)
	if got.ID != 1 || got.Result.Start != 1 {
		t.Fatalf("initialize response: %s", resp)
	}
	var sent struct {
		Params struct {
			ProcessID  int                    `json:"processId"`
			RootURI    string                 `json:"rootUri"`
			RootPath   string                 `json:"rootPath"`
			Workspaces []struct{ URI string } `json:"workspaceFolders"`
			Caps       map[string]int         `json:"capabilities"`
		}
	}
	_ = json.Unmarshal(h.proc(0).writes()[0], &sent)
	p := sent.Params
	if p.ProcessID != os.Getpid() || p.RootURI != "file://"+root || p.RootPath != root || len(p.Workspaces) != 1 || p.Workspaces[0].URI != "file://"+root || p.Caps["x"] != 1 {
		t.Fatalf("initialize not rewritten: %s", h.proc(0).writes()[0])
	}
	_ = c1.Send([]byte(`{"jsonrpc":"2.0","method":"initialized","params":{}}`))
	_ = c1.Send([]byte(`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{}}`))
	c1.Close()

	c2 := lspAttach(t, r, lspKey("u", "f"), root)
	_ = c2.Send([]byte(`{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"processId":2}}`))
	resp = lspRecv(t, c2)
	_ = json.Unmarshal(resp, &got)
	if got.ID != 7 || got.Result.Start != 1 {
		t.Fatalf("cached initialize response: %s", resp)
	}
	_ = c2.Send([]byte(`{"jsonrpc":"2.0","method":"initialized","params":{}}`))
	_ = c2.Send([]byte(`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{}}`))
	if h.started() != 1 {
		t.Fatalf("server restarted on reconnect: %d starts", h.started())
	}
	if m := strings.Join(h.proc(0).methods(), ","); m != "initialize,initialized,textDocument/didOpen,textDocument/didOpen" {
		t.Fatalf("server saw %s: initialize must not repeat and initialized must be forwarded once", m)
	}
}

func TestShutdownAndExitFromClientNeverReachTheSharedServer(t *testing.T) {
	h := &lspFakeHost{}
	r := lspNewReg(h)
	defer r.Shutdown()
	c := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	_ = c.Send([]byte(`{"jsonrpc":"2.0","id":3,"method":"shutdown"}`))
	if got := lspRecv(t, c); !strings.Contains(string(got), `"id":3`) || !strings.Contains(string(got), `"result":null`) {
		t.Fatalf("shutdown reply: %s", got)
	}
	_ = c.Send([]byte(`{"jsonrpc":"2.0","method":"exit"}`))
	if n := len(h.proc(0).writes()); n != 0 {
		t.Fatalf("server received %d messages", n)
	}
}

func TestSecondClientTakesOverWith4001(t *testing.T) {
	h := &lspFakeHost{}
	r := lspNewReg(h)
	defer r.Shutdown()
	c1 := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	c2 := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	if code := lspWaitDone(t, c1); code != CloseTakenOver {
		t.Fatalf("close code %d", code)
	}
	if err := c1.Send([]byte(`{"jsonrpc":"2.0","method":"x"}`)); !errors.Is(err, ErrLSPClosed) {
		t.Fatalf("old client can still send: %v", err)
	}
	h.proc(0).out <- []byte(`{"jsonrpc":"2.0","method":"window/logMessage"}`)
	if got := lspRecv(t, c2); !strings.Contains(string(got), "logMessage") {
		t.Fatalf("new client got %s", got)
	}
	if h.started() != 1 {
		t.Fatal("takeover must reuse the process")
	}
}

func TestIdleServerIsShutDownThenRestartedOnNextAttach(t *testing.T) {
	h := &lspFakeHost{}
	r := lspNewReg(h)
	r.IdleTimeout = 40 * time.Millisecond
	defer r.Shutdown()
	c := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	c.Close()
	p := h.proc(0)
	lspEventually(t, "kill after grace", p.wasKilled)
	if ms := p.methods(); len(ms) != 2 || ms[0] != "shutdown" || ms[1] != "exit" {
		t.Fatalf("graceful sequence: %v", ms)
	}
	c2 := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	defer c2.Close()
	if h.started() != 2 {
		t.Fatalf("starts %d", h.started())
	}
}

func TestAttachWithinIdleWindowCancelsShutdown(t *testing.T) {
	h := &lspFakeHost{}
	r := lspNewReg(h)
	r.IdleTimeout = 60 * time.Millisecond
	defer r.Shutdown()
	c := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	c.Close()
	c2 := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	defer c2.Close()
	time.Sleep(150 * time.Millisecond)
	if h.proc(0).wasKilled() || h.started() != 1 {
		t.Fatal("an attached server was stopped as idle")
	}
}

func TestCrashClosesClientWith4005AndRestartWaitsForBackoff(t *testing.T) {
	h := &lspFakeHost{}
	r := lspNewReg(h)
	defer r.Shutdown()
	c := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	crashAt := time.Now()
	h.proc(0).die()
	if code := lspWaitDone(t, c); code != CloseCrashed {
		t.Fatalf("code %d", code)
	}
	if !h.proc(0).wasKilled() {
		t.Fatal("a crashed server's process group was not killed")
	}
	c2 := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	defer c2.Close()
	if d := time.Since(crashAt); d < 18*time.Millisecond {
		t.Fatalf("restart ignored the 1st backoff step: %v", d)
	}
	if h.started() != 2 {
		t.Fatalf("starts %d", h.started())
	}
}

func TestMoreThanThreeCrashesInWindowMarksCrashedUntilUserReattaches(t *testing.T) {
	h := &lspFakeHost{}
	r := lspNewReg(h)
	defer r.Shutdown()
	events, cancel := r.Watch("u", "f")
	defer cancel()
	for i := 0; i < 4; i++ {
		c := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
		h.proc(i).die()
		lspWaitDone(t, c)
	}
	_, err := r.Attach(context.Background(), lspKey("u", "f"), lspSpec, "/bin/gopls", t.TempDir())
	var crashed *LSPCrashedError
	if !errors.As(err, &crashed) || crashed.Tail != "stderr line" {
		t.Fatalf("want crashed with the stderr tail, got %v", err)
	}
	var last LSPStateEvent
	for len(events) > 0 {
		last = <-events
		if last.State == model.LSPCrashed {
			break
		}
	}
	if last.State != model.LSPCrashed || last.Error != "stderr line" {
		t.Fatalf("crashed state event: %+v", last)
	}
	c := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	defer c.Close()
	if h.started() != 5 {
		t.Fatalf("reattach after crashed must restart: %d starts", h.started())
	}
}

func TestCrashesOutsideTheWindowDoNotAccumulate(t *testing.T) {
	h := &lspFakeHost{}
	r := lspNewReg(h)
	r.CrashWindow = 30 * time.Millisecond
	r.Backoff = []time.Duration{time.Millisecond}
	defer r.Shutdown()
	for i := 0; i < 6; i++ {
		c := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
		h.proc(i).die()
		lspWaitDone(t, c)
		time.Sleep(40 * time.Millisecond)
	}
	c := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	c.Close()
}

func TestMaxServersPerUserEvictsLeastRecentlyUsedIdleOne(t *testing.T) {
	h := &lspFakeHost{}
	r := lspNewReg(h)
	r.MaxPerUser = 2
	defer r.Shutdown()
	a := lspAttach(t, r, lspKey("u", "a"), t.TempDir())
	b := lspAttach(t, r, lspKey("u", "b"), t.TempDir())
	if _, err := r.Attach(context.Background(), lspKey("u", "c"), lspSpec, "/bin/gopls", t.TempDir()); !errors.Is(err, model.ErrTooManyLSP) {
		t.Fatalf("all attached must refuse: %v", err)
	}
	if _, err := r.Attach(context.Background(), lspKey("other", "c"), lspSpec, "/bin/gopls", t.TempDir()); err != nil {
		t.Fatalf("limit is per user: %v", err)
	}
	a.Close()
	time.Sleep(5 * time.Millisecond)
	b.Close()
	c := lspAttach(t, r, lspKey("u", "c"), t.TempDir())
	defer c.Close()
	lspEventually(t, "LRU eviction", h.proc(0).wasKilled)
	if h.proc(1).wasKilled() {
		t.Fatal("evicted the more recently used server")
	}
}

func TestShutdownKillsEverythingAndRefusesAttach(t *testing.T) {
	h := &lspFakeHost{}
	r := lspNewReg(h)
	c1 := lspAttach(t, r, lspKey("u", "a"), t.TempDir())
	c2 := lspAttach(t, r, lspKey("v", "b"), t.TempDir())
	r.Shutdown()
	for i := 0; i < 2; i++ {
		if !h.proc(i).wasKilled() {
			t.Fatalf("proc %d survived shutdown", i)
		}
	}
	lspWaitDone(t, c1)
	lspWaitDone(t, c2)
	if _, err := r.Attach(context.Background(), lspKey("u", "a"), lspSpec, "/bin/gopls", t.TempDir()); !errors.Is(err, ErrLSPClosed) {
		t.Fatalf("attach after shutdown: %v", err)
	}
}

func TestSlowClientIsDisconnectedButServerKeepsRunning(t *testing.T) {
	h := &lspFakeHost{}
	r := lspNewReg(h)
	r.ClientBuffer = 1
	r.SlowClient = 30 * time.Millisecond
	defer r.Shutdown()
	c := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	for i := 0; i < 3; i++ {
		h.proc(0).out <- []byte(`{"jsonrpc":"2.0","method":"x"}`)
	}
	if code := lspWaitDone(t, c); code != CloseSlowClient {
		t.Fatalf("code %d", code)
	}
	if h.proc(0).wasKilled() {
		t.Fatal("server killed with a slow client")
	}
	c2 := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	c2.Close()
	if h.started() != 1 {
		t.Fatal("server restarted")
	}
}

func TestStartFailureLeavesNoEntryBehind(t *testing.T) {
	h := &lspFakeHost{fail: errors.New("boom")}
	r := lspNewReg(h)
	defer r.Shutdown()
	if _, err := r.Attach(context.Background(), lspKey("u", "f"), lspSpec, "/bin/gopls", t.TempDir()); err == nil {
		t.Fatal("start failure swallowed")
	}
	h.fail = nil
	c := lspAttach(t, r, lspKey("u", "f"), t.TempDir())
	c.Close()
}
