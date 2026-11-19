package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

// Close codes the LSP socket uses (SPEC §WebSocket protocol); CloseSlowClient is the standard policy-violation code.
const (
	CloseTakenOver  = 4001
	CloseNotFound   = 4002
	CloseUntrusted  = 4003
	CloseTooMany    = 4004
	CloseCrashed    = 4005
	CloseSlowClient = 1008
)

var (
	ErrLSPUnknown  = errors.New("unknown language server")
	ErrLSPNotFound = errors.New("language server is not installed")
	ErrLSPClosed   = errors.New("language server client is closed")
)

// LSPCrashedError carries the stderr tail of a server that crashed too often; it is reported once per crash.
type LSPCrashedError struct{ Tail string }

func (e *LSPCrashedError) Error() string { return "language server crashed" }

type LSPKey struct{ User, Folder, Server string }

type LSPStateEvent struct {
	Server, State, Error string
}

// LSPRegistry owns the language server processes: one per (user, folder, server), one attached client each.
// The zero durations and counts fall back to the SPEC values.
type LSPRegistry struct {
	Host         LSPHost
	MaxPerUser   int
	IdleTimeout  time.Duration
	KillGrace    time.Duration
	Backoff      []time.Duration
	CrashWindow  time.Duration
	CrashLimit   int
	SlowClient   time.Duration
	ClientBuffer int

	mu      sync.Mutex
	entries map[LSPKey]*lspEntry
	subs    map[[2]string]map[chan LSPStateEvent]struct{}
	closed  bool
}

func (r *LSPRegistry) maxPerUser() int { return orDefault(r.MaxPerUser, 4) }
func (r *LSPRegistry) crashLimit() int { return orDefault(r.CrashLimit, 3) }
func (r *LSPRegistry) idle() time.Duration {
	return orDefaultDur(r.IdleTimeout, 10*time.Minute)
}
func (r *LSPRegistry) grace() time.Duration       { return orDefaultDur(r.KillGrace, 5*time.Second) }
func (r *LSPRegistry) crashWindow() time.Duration { return orDefaultDur(r.CrashWindow, 5*time.Minute) }
func (r *LSPRegistry) slow() time.Duration        { return orDefaultDur(r.SlowClient, 30*time.Second) }
func (r *LSPRegistry) backoff(n int) time.Duration {
	b := r.Backoff
	if len(b) == 0 {
		b = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second}
	}
	if n < 1 {
		n = 1
	}
	if n > len(b) {
		n = len(b)
	}
	return b[n-1]
}

func orDefault(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

func orDefaultDur(v, d time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return d
}

type initWaiter struct {
	id json.RawMessage
	c  *LSPClient
}

type lspEntry struct {
	key        LSPKey
	spec       model.LSPServerSpec
	bin, root  string
	proc       model.LSPProcess
	gen        int
	starting   chan struct{} // non-nil while a spawn is in flight
	client     *LSPClient
	lastActive time.Time
	idleTimer  *time.Timer
	crashes    []time.Time
	notBefore  time.Time
	crashed    bool
	crashTail  string
	stopping   bool

	initSent        bool
	initResult      json.RawMessage
	initWaiters     []initWaiter
	initializedSent bool
}

func (e *lspEntry) live() bool { return e.proc != nil || e.starting != nil }

// LSPClient is the single attached client of a server process. Messages are whole JSON-RPC messages.
type LSPClient struct {
	r    *LSPRegistry
	e    *lspEntry
	out  chan []byte
	done chan struct{}
	once sync.Once
	code int
}

func (c *LSPClient) Messages() <-chan []byte { return c.out }
func (c *LSPClient) Done() <-chan struct{}   { return c.done }
func (c *LSPClient) closeWith(code int)      { c.once.Do(func() { c.code = code; close(c.done) }) }
func (c *LSPClient) CloseCode() int          { <-c.done; return c.code }
func (c *LSPClient) Close()                  { c.r.detach(c) }
func (c *LSPClient) Send(msg []byte) error   { return c.r.fromClient(c, msg) }
func (c *LSPClient) deliver(msg []byte) {
	select {
	case c.out <- msg:
		return
	case <-c.done:
		return
	default:
	}
	t := time.NewTimer(c.r.slow())
	defer t.Stop()
	select {
	case c.out <- msg:
	case <-c.done:
	case <-t.C:
		c.closeWith(CloseSlowClient)
		c.r.detach(c)
	}
}

// Attach connects the caller to the server for key, spawning it when needed. An earlier client is closed with
// CloseTakenOver. A server that crashed too often fails once with *LSPCrashedError; attaching again restarts it.
func (r *LSPRegistry) Attach(ctx context.Context, key LSPKey, spec model.LSPServerSpec, bin, root string) (*LSPClient, error) {
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, ErrLSPClosed
		}
		if r.entries == nil {
			r.entries = map[LSPKey]*lspEntry{}
		}
		e := r.entries[key]
		if e != nil && e.crashed {
			tail := e.crashTail
			delete(r.entries, key)
			r.mu.Unlock()
			return nil, &LSPCrashedError{Tail: tail}
		}
		if e != nil && e.starting != nil {
			wait := e.starting
			r.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if e == nil || e.proc == nil {
			if err := r.makeRoomLocked(key); err != nil {
				r.mu.Unlock()
				return nil, err
			}
			if e == nil {
				e = &lspEntry{key: key}
				r.entries[key] = e
			}
			e.spec, e.bin, e.root = spec, bin, root
			e.starting = make(chan struct{})
			delay := time.Until(e.notBefore)
			r.mu.Unlock()
			if err := r.spawn(ctx, e, delay); err != nil {
				return nil, err
			}
			continue
		}
		if e.idleTimer != nil {
			e.idleTimer.Stop()
		}
		if old := e.client; old != nil {
			old.closeWith(CloseTakenOver)
		}
		buf := r.ClientBuffer
		if buf <= 0 {
			buf = 256
		}
		c := &LSPClient{r: r, e: e, out: make(chan []byte, buf), done: make(chan struct{})}
		e.client = c
		r.mu.Unlock()
		return c, nil
	}
}

func (r *LSPRegistry) spawn(ctx context.Context, e *lspEntry, delay time.Duration) error {
	fail := func(err error) error {
		r.mu.Lock()
		close(e.starting)
		e.starting = nil
		if e.proc == nil && len(e.crashes) == 0 {
			delete(r.entries, e.key)
		}
		r.mu.Unlock()
		return err
	}
	if delay > 0 {
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return fail(ctx.Err())
		}
	}
	proc, err := r.Host.Start(context.WithoutCancel(ctx), e.spec, e.bin, e.root)
	if err != nil {
		return fail(err)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = proc.Kill()
		return fail(ErrLSPClosed)
	}
	e.gen++
	gen := e.gen
	e.proc = proc
	e.initSent, e.initResult, e.initWaiters, e.initializedSent = false, nil, nil, false
	close(e.starting)
	e.starting = nil
	e.lastActive = time.Now()
	r.mu.Unlock()
	r.notify(e.key, LSPStateEvent{Server: e.key.Server, State: model.LSPRunning})
	go r.readLoop(e, proc, gen)
	go func() {
		_ = proc.Wait()
		r.onExit(e, proc, gen)
	}()
	return nil
}

// makeRoomLocked frees a slot for key's user by stopping the least recently used idle server.
func (r *LSPRegistry) makeRoomLocked(key LSPKey) error {
	live := 0
	var lru *lspEntry
	for k, e := range r.entries {
		if k.User != key.User || !e.live() {
			continue
		}
		live++
		if e.client == nil && e.proc != nil && !e.stopping && (lru == nil || e.lastActive.Before(lru.lastActive)) {
			lru = e
		}
	}
	if live < r.maxPerUser() {
		return nil
	}
	if lru == nil {
		return model.ErrTooManyLSP
	}
	r.stopLocked(lru)
	return nil
}

// stopLocked removes e from the registry and shuts its process down in the background.
func (r *LSPRegistry) stopLocked(e *lspEntry) {
	e.stopping = true
	if e.idleTimer != nil {
		e.idleTimer.Stop()
	}
	if r.entries[e.key] == e {
		delete(r.entries, e.key)
	}
	if proc := e.proc; proc != nil {
		go r.graceful(proc)
	}
	r.notifyLocked(e.key, LSPStateEvent{Server: e.key.Server, State: model.LSPFound})
}

// graceful asks the server to shut down, then kills its group after the grace period.
func (r *LSPRegistry) graceful(proc model.LSPProcess) {
	_ = proc.WriteMessage([]byte(`{"jsonrpc":"2.0","id":"legos-shutdown","method":"shutdown"}`))
	_ = proc.WriteMessage([]byte(`{"jsonrpc":"2.0","method":"exit"}`))
	done := make(chan struct{})
	go func() { _ = proc.Wait(); close(done) }()
	t := time.NewTimer(r.grace())
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		_ = proc.Kill()
	}
}

func (r *LSPRegistry) detach(c *LSPClient) {
	r.mu.Lock()
	e := c.e
	if e.client == c {
		e.client = nil
		e.lastActive = time.Now()
		if e.proc != nil && !e.stopping {
			if e.idleTimer != nil {
				e.idleTimer.Stop()
			}
			e.idleTimer = time.AfterFunc(r.idle(), func() { r.idleStop(e) })
		}
	}
	r.mu.Unlock()
	c.closeWith(0)
}

func (r *LSPRegistry) idleStop(e *lspEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.client != nil || e.proc == nil || e.stopping {
		return
	}
	r.stopLocked(e)
}

func (r *LSPRegistry) readLoop(e *lspEntry, proc model.LSPProcess, gen int) {
	for {
		msg, err := proc.ReadMessage()
		if err != nil {
			return
		}
		r.fromServer(e, gen, msg)
	}
}

type rpcHead struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func (r *LSPRegistry) fromServer(e *lspEntry, gen int, msg []byte) {
	var h rpcHead
	_ = json.Unmarshal(msg, &h)
	r.mu.Lock()
	if e.gen != gen {
		r.mu.Unlock()
		return
	}
	c := e.client
	if h.Method == "" && len(h.ID) > 0 && len(e.initWaiters) > 0 && bytes.Equal(h.ID, e.initWaiters[0].id) {
		waiters := e.initWaiters
		e.initWaiters = nil
		if len(h.Error) == 0 {
			e.initResult = append(json.RawMessage(nil), h.Result...)
		} else {
			e.initSent = false
		}
		r.mu.Unlock()
		for _, w := range waiters {
			if w.c == c && c != nil {
				c.deliver(rpcResponse(w.id, h.Result, h.Error))
			}
		}
		return
	}
	r.mu.Unlock()
	if c != nil {
		c.deliver(msg)
	}
}

func rpcResponse(id, result, errObj json.RawMessage) []byte {
	m := map[string]json.RawMessage{"jsonrpc": json.RawMessage(`"2.0"`), "id": id}
	if len(errObj) > 0 {
		m["error"] = errObj
	} else {
		if len(result) == 0 {
			result = json.RawMessage("null")
		}
		m["result"] = result
	}
	b, _ := json.Marshal(m)
	return b
}

func (r *LSPRegistry) onExit(e *lspEntry, proc model.LSPProcess, gen int) {
	r.mu.Lock()
	if e.gen != gen || e.proc != proc {
		r.mu.Unlock()
		return
	}
	e.proc = nil
	c := e.client
	e.client = nil
	if e.stopping || r.closed {
		r.mu.Unlock()
		if c != nil {
			c.closeWith(CloseCrashed)
		}
		return
	}
	now := time.Now()
	if e.idleTimer != nil {
		e.idleTimer.Stop()
	}
	kept := e.crashes[:0]
	for _, t := range e.crashes {
		if now.Sub(t) <= r.crashWindow() {
			kept = append(kept, t)
		}
	}
	e.crashes = append(kept, now)
	ev := LSPStateEvent{Server: e.key.Server, State: model.LSPFound}
	if len(e.crashes) > r.crashLimit() {
		e.crashed, e.crashTail = true, proc.StderrTail()
		ev = LSPStateEvent{Server: e.key.Server, State: model.LSPCrashed, Error: e.crashTail}
	} else {
		e.notBefore = now.Add(r.backoff(len(e.crashes)))
	}
	r.mu.Unlock()
	if c != nil {
		c.closeWith(CloseCrashed)
	}
	r.notify(e.key, ev)
}

func (r *LSPRegistry) fromClient(c *LSPClient, msg []byte) error {
	var h rpcHead
	if err := json.Unmarshal(msg, &h); err != nil {
		return err
	}
	r.mu.Lock()
	e := c.e
	select {
	case <-c.done:
		r.mu.Unlock()
		return ErrLSPClosed
	default:
	}
	if e.client != c || e.proc == nil {
		r.mu.Unlock()
		return ErrLSPClosed
	}
	proc := e.proc
	switch h.Method {
	case "initialize":
		switch {
		case e.initResult != nil:
			resp := rpcResponse(h.ID, e.initResult, nil)
			r.mu.Unlock()
			c.deliver(resp)
			return nil
		case e.initSent:
			e.initWaiters = append(e.initWaiters, initWaiter{h.ID, c})
			r.mu.Unlock()
			return nil
		}
		out, err := rewriteInitialize(msg, e.root)
		if err != nil {
			r.mu.Unlock()
			return err
		}
		e.initSent = true
		e.initWaiters = []initWaiter{{h.ID, c}}
		msg = out
	case "initialized":
		if e.initializedSent {
			r.mu.Unlock()
			return nil
		}
		e.initializedSent = true
	case "shutdown":
		resp := rpcResponse(h.ID, nil, nil)
		r.mu.Unlock()
		c.deliver(resp)
		return nil
	case "exit":
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	return proc.WriteMessage(msg)
}

// rewriteInitialize points the server at the folder and at the backend as its parent process.
func rewriteInitialize(msg []byte, root string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(msg, &m); err != nil {
		return nil, err
	}
	params := map[string]json.RawMessage{}
	if len(m["params"]) > 0 {
		if err := json.Unmarshal(m["params"], &params); err != nil {
			return nil, err
		}
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(root)}).String()
	set := func(k string, v any) { b, _ := json.Marshal(v); params[k] = b }
	set("processId", os.Getpid())
	set("rootUri", uri)
	set("rootPath", root)
	set("workspaceFolders", []map[string]string{{"uri": uri, "name": filepath.Base(root)}})
	b, _ := json.Marshal(params)
	m["params"] = b
	return json.Marshal(m)
}

// Running reports which servers have a live process for the user's folder.
func (r *LSPRegistry) Running(user, folder string) map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]bool{}
	for k, e := range r.entries {
		if k.User == user && k.Folder == folder && e.proc != nil && !e.stopping {
			out[k.Server] = true
		}
	}
	return out
}

// Watch streams state changes of the user's servers in one folder.
func (r *LSPRegistry) Watch(user, folder string) (<-chan LSPStateEvent, func()) {
	ch := make(chan LSPStateEvent, 16)
	k := [2]string{user, folder}
	r.mu.Lock()
	if r.subs == nil {
		r.subs = map[[2]string]map[chan LSPStateEvent]struct{}{}
	}
	if r.subs[k] == nil {
		r.subs[k] = map[chan LSPStateEvent]struct{}{}
	}
	r.subs[k][ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.subs[k], ch)
		r.mu.Unlock()
	}
}

func (r *LSPRegistry) notify(key LSPKey, ev LSPStateEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notifyLocked(key, ev)
}

func (r *LSPRegistry) notifyLocked(key LSPKey, ev LSPStateEvent) {
	for ch := range r.subs[[2]string{key.User, key.Folder}] {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Shutdown kills every server this registry started and refuses further attaches.
func (r *LSPRegistry) Shutdown() {
	r.mu.Lock()
	r.closed = true
	var procs []model.LSPProcess
	var clients []*LSPClient
	for _, e := range r.entries {
		e.stopping = true
		if e.idleTimer != nil {
			e.idleTimer.Stop()
		}
		if e.proc != nil {
			procs = append(procs, e.proc)
		}
		if e.client != nil {
			clients = append(clients, e.client)
		}
	}
	r.mu.Unlock()
	for _, p := range procs {
		_ = p.Kill()
	}
	for _, c := range clients {
		c.closeWith(CloseCrashed)
	}
}
