package agent

// ACPHarness drives an Agent Client Protocol agent (e.g. `opencode acp`,
// `claude-agent-acp`) over stdio JSON-RPC, one subprocess per run. Verified
// against OpenCode 2.1.3 and claude-agent-acp 0.100.0:
//
//	initialize → session/new {cwd} → session/set_config_option (category model)
//	→ session/prompt; session/update streams tool calls, text and usage;
//	the prompt result carries stopReason and token usage.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"legos-factory/backend/internal/model"
)

type ACPHarness struct {
	Command  []string // agent command, e.g. ["opencode", "acp"]
	Env      []string // extra environment for the agent process
	Provider string   // provider for model values the agent lists without one, e.g. claude's "opus"
	probe    string   // directory for model discovery sessions
	mu       sync.Mutex
	runs     map[string]*acpRun
	kept     map[string]*keptConn // idle sessions kept open after a Keep run, by session id
	// IdleTTL closes a kept session nobody continued; 0 means defaultIdleTTL.
	IdleTTL time.Duration
}

const defaultIdleTTL = 20 * time.Minute

type keptConn struct {
	conn  *acpConn
	timer *time.Timer
}

func NewACPHarness(command []string, probeDir string) *ACPHarness {
	return &ACPHarness{Command: command, Provider: agentProvider(command), probe: probeDir, runs: map[string]*acpRun{}, kept: map[string]*keptConn{}}
}

func agentProvider(command []string) string {
	if len(command) == 0 {
		return "acp"
	}
	// wrappers like `npx @agentclientprotocol/claude-agent-acp` must still get claude's read-only lockdown
	for _, arg := range command {
		if strings.HasPrefix(filepath.Base(arg), "claude") {
			return "claude"
		}
	}
	return filepath.Base(command[0])
}

func (h *ACPHarness) Name() string {
	if h.Provider == "claude" {
		return "claude-acp"
	}
	return "opencode-acp"
}

// readOnly roles review and plan; they never change the workspace.
var acpReadOnlyDeny = []string{"edit", "write", "patch", "apply_patch", "bash", "shell", "execute"}

// acpChatDeny is what the chat role loses on top of the read-only denials: it gets no tools at all.
var acpChatDeny = []string{"read", "grep", "glob", "list", "webfetch", "websearch", "task", "skill", "todowrite", "todoread"}

// acpRoleConfig is the OpenCode config for a role: every role is kept inside
// its workspace, and every role but the builder is read-only. Deny (not ask)
// keeps turns alive: a rejected permission request cancels the whole turn.
func acpRoleConfig(role model.AgentRole) string {
	perm := map[string]string{"external_directory": "deny"}
	if role != model.AgentBuilder {
		for _, t := range acpReadOnlyDeny {
			perm[t] = "deny"
		}
	}
	if role == model.AgentChat {
		for _, t := range acpChatDeny {
			perm[t] = "deny"
		}
	}
	b, _ := json.Marshal(map[string]any{"permission": perm})
	return string(b)
}

// claudeReadOnlyMeta is session/new `_meta` for read-only roles on claude-agent-acp:
// `tools` makes the write and exec tools absent (so no allow rule can grant them),
// "user" settings keep the user's ~/.claude but drop project and local settings,
// and strictMcpConfig drops MCP servers from .mcp.json and settings.
var claudeReadOnlyMeta = map[string]any{"claudeCode": map[string]any{"options": map[string]any{
	"tools":           []string{"Read", "Grep", "Glob"},
	"disallowedTools": []string{"Bash", "Edit", "Write", "MultiEdit", "NotebookEdit", "Agent", "Task"},
	"settingSources":  []string{"user"},
	"strictMcpConfig": true,
}}}

// claudeChatMeta gives the chat role no tools at all.
var claudeChatMeta = map[string]any{"claudeCode": map[string]any{"options": map[string]any{
	"tools":           []string{},
	"disallowedTools": []string{"Bash", "Edit", "Write", "MultiEdit", "NotebookEdit", "Agent", "Task", "Read", "Grep", "Glob", "WebFetch", "WebSearch", "TodoWrite"},
	"settingSources":  []string{"user"},
	"strictMcpConfig": true,
}}}

type acpRun struct {
	h         *ACPHarness
	conn      *acpConn
	sessionID string
	keep      atomic.Bool // the session stays open for another prompt once this run succeeds
	costBase  float64     // session cost before this prompt; the agent reports it cumulatively
	done      chan struct{}
	result    model.AgentResult
	err       error
}

func (h *ACPHarness) Start(ctx context.Context, req model.AgentRequest) (model.AgentSession, error) {
	conn, err := h.spawn(req.Role, req.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	conn.role, conn.workspace, conn.onStep, conn.onText = req.Role, req.WorkspaceDir, req.OnStep, req.OnText
	claudeReadOnly := h.Provider == "claude" && req.Role != model.AgentBuilder
	var meta map[string]any
	if claudeReadOnly {
		meta = claudeReadOnlyMeta
		if req.Role == model.AgentChat {
			meta = claudeChatMeta
		}
	}
	ns, err := conn.newSession(ctx, req.WorkspaceDir, meta)
	if err != nil {
		conn.close()
		return nil, err
	}
	if req.ProviderID != "" || req.ModelID != "" {
		if err := conn.setModel(ctx, ns, req.ProviderID+"/"+req.ModelID, h.Provider); err != nil {
			conn.close()
			return nil, err
		}
	}
	if claudeReadOnly {
		if err := conn.setMode(ctx, ns, "plan"); err != nil {
			conn.close()
			return nil, err
		}
	}
	run := &acpRun{h: h, conn: conn, sessionID: ns.SessionID, done: make(chan struct{})}
	run.keep.Store(req.Keep)
	h.mu.Lock()
	h.runs[run.sessionID] = run
	h.mu.Unlock()
	return h.launch(run, req.Prompt)
}

// launch sends text on run's session, which is already registered in h.runs,
// and starts collecting the reply.
func (h *ACPHarness) launch(run *acpRun, text string) (model.AgentSession, error) {
	// sent before returning, so a Cancel right after Start always follows the prompt
	wait, err := run.conn.begin("session/prompt", map[string]any{
		"sessionId": run.sessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": text}},
	})
	if err != nil {
		run.conn.close()
		h.forgetRun(run)
		return nil, err
	}
	go run.finish(wait)
	return &sessionHandle{id: run.sessionID}, nil
}

// Continue sends another prompt into a session kept open by a Keep run. The
// model chosen at Start stays; req.ModelID is not applied.
func (h *ACPHarness) Continue(_ context.Context, sessionID string, req model.AgentRequest) (model.AgentSession, error) {
	run := &acpRun{h: h, sessionID: sessionID, done: make(chan struct{})}
	run.keep.Store(true)
	// taking the session and registering the run are one step, so Close sees either the kept session or the run
	h.mu.Lock()
	k, ok := h.kept[sessionID]
	if ok {
		delete(h.kept, sessionID)
		k.timer.Stop()
		run.conn = k.conn
		h.runs[sessionID] = run
	}
	h.mu.Unlock()
	if !ok {
		return nil, model.ErrAgentSessionNotFound
	}
	conn := k.conn
	if conn.gone() {
		conn.close()
		h.forgetRun(run)
		return nil, model.ErrAgentSessionNotFound
	}
	conn.mu.Lock()
	conn.text.Reset()
	conn.steps, conn.tools = 0, nil
	conn.costSeen = conn.cost != conn.costAttr // cost reported between turns belongs to this run
	conn.onStep, conn.onText = req.OnStep, req.OnText
	run.costBase = conn.costAttr
	conn.mu.Unlock()
	return h.launch(run, req.Prompt)
}

// Close ends a kept session; a prompt still running in it is the last one.
func (h *ACPHarness) Close(sessionID string) {
	h.mu.Lock()
	k, ok := h.kept[sessionID]
	delete(h.kept, sessionID)
	if ok {
		k.timer.Stop()
	}
	if run := h.runs[sessionID]; run != nil {
		run.keep.Store(false)
	}
	h.mu.Unlock()
	if ok {
		k.conn.close()
	}
}

// expire closes a kept session that sat idle; a session continued meanwhile is left alone.
func (h *ACPHarness) expire(sessionID string, k *keptConn) {
	h.mu.Lock()
	cur, ok := h.kept[sessionID]
	if ok && cur == k {
		delete(h.kept, sessionID)
	}
	h.mu.Unlock()
	if ok && cur == k {
		k.conn.close()
	}
}

// park keeps run's session for a later Continue unless Close ended it first.
func (h *ACPHarness) park(run *acpRun) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !run.keep.Load() {
		return false
	}
	ttl := h.IdleTTL
	if ttl <= 0 {
		ttl = defaultIdleTTL
	}
	k := &keptConn{conn: run.conn}
	k.timer = time.AfterFunc(ttl, func() { h.expire(run.sessionID, k) })
	h.kept[run.sessionID] = k
	return true
}

func (h *ACPHarness) Result(ctx context.Context, sessionID string) (model.AgentResult, error) {
	run, ok := h.run(sessionID)
	if !ok {
		return model.AgentResult{}, model.ErrAgentSessionNotFound
	}
	defer h.forgetRun(run)
	select {
	case <-run.done:
		return run.result, run.err
	case <-ctx.Done():
		run.conn.close()
		return model.AgentResult{}, ctx.Err()
	}
}

func (h *ACPHarness) Cancel(_ context.Context, sessionID string) error {
	run, ok := h.run(sessionID)
	if !ok {
		return model.ErrAgentSessionNotFound
	}
	run.conn.notify("session/cancel", map[string]any{"sessionId": sessionID})
	select {
	case <-run.done:
	case <-time.After(5 * time.Second):
		run.conn.close()
	}
	return nil
}

// Models lists the models the agent offers, from a throwaway session's model option.
func (h *ACPHarness) Models(ctx context.Context) ([]Model, error) {
	if err := os.MkdirAll(h.probe, 0o755); err != nil {
		return nil, err
	}
	conn, err := h.spawn(model.AgentForeman, h.probe)
	if err != nil {
		return nil, err
	}
	defer conn.close()
	ns, err := conn.newSession(ctx, h.probe, nil)
	if err != nil {
		return nil, err
	}
	out := []Model{}
	if opt := ns.modelOption(); opt != nil {
		for _, o := range opt.Options {
			provider, id, _ := strings.Cut(qualify(o.Value, h.Provider), "/")
			out = append(out, Model{ProviderID: provider, ModelID: id, Name: o.Name})
		}
	}
	return out, nil
}

func (h *ACPHarness) run(id string) (*acpRun, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.runs[id]
	return r, ok
}

// forgetRun drops run unless a later prompt on the same session replaced it.
func (h *ACPHarness) forgetRun(run *acpRun) {
	h.mu.Lock()
	if h.runs[run.sessionID] == run {
		delete(h.runs, run.sessionID)
	}
	h.mu.Unlock()
}

func (h *ACPHarness) spawn(role model.AgentRole, dir string) (*acpConn, error) {
	if len(h.Command) == 0 {
		return nil, fmt.Errorf("acp: no agent command configured")
	}
	cmd := exec.Command(h.Command[0], h.Command[1:]...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_CONFIG_CONTENT="+acpRoleConfig(role)), h.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c := &acpConn{cmd: cmd, w: stdin, stderr: &tailBuffer{max: 2048}, exited: make(chan struct{})}
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("acp: start %s: %w", strings.Join(h.Command, " "), err)
	}
	go c.readLoop(stdout)
	raw, err := c.call(context.Background(), "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]any{"name": "legos-factory", "version": "1"},
	})
	if err != nil {
		c.close()
		return nil, err
	}
	var init struct {
		Meta struct {
			Steering struct {
				Supported bool `json:"supported"`
			} `json:"steering"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(raw, &init)
	c.steering = init.Meta.Steering.Supported
	return c, nil
}

// Steer injects a message into the run's in-flight turn; the agent reads it
// before its next step and the turn ends only after it has acted on it.
func (h *ACPHarness) Steer(ctx context.Context, sessionID, text string) error {
	run, ok := h.run(sessionID)
	if !ok {
		return model.ErrAgentSessionNotFound
	}
	if !run.conn.steering {
		return model.ErrSteeringUnsupported
	}
	raw, err := run.conn.call(ctx, "_session/steering", map[string]any{
		"sessionId": sessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": text}},
		// the default starts a fresh turn when idle, which nobody would wait for
		"_meta": map[string]any{"steering": map[string]any{"idleBehavior": "promptRequired"}},
	})
	if err != nil {
		if run.conn.gone() {
			return model.ErrAgentSessionNotFound
		}
		return err
	}
	var res struct {
		Outcome string `json:"outcome"`
	}
	if json.Unmarshal(raw, &res) == nil && res.Outcome == "promptRequired" {
		return model.ErrAgentSessionNotFound
	}
	return nil
}

func (r *acpRun) finish(wait func(context.Context) (json.RawMessage, error)) {
	r.collect(wait)
	r.conn.mu.Lock()
	r.conn.onStep, r.conn.onText = nil, nil // a parked session must not call back into a finished caller
	r.conn.mu.Unlock()
	// parked before done closes, so the caller's next Continue always finds it
	if r.err != nil || !r.h.park(r) {
		r.conn.close()
	}
	close(r.done)
}

func (r *acpRun) collect(wait func(context.Context) (json.RawMessage, error)) {
	raw, err := wait(context.Background())
	if err != nil {
		r.err = err
		return
	}
	var res struct {
		StopReason string `json:"stopReason"`
		Usage      struct {
			InputTokens       int64 `json:"inputTokens"`
			OutputTokens      int64 `json:"outputTokens"`
			CachedReadTokens  int64 `json:"cachedReadTokens"`
			CachedWriteTokens int64 `json:"cachedWriteTokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		r.err = fmt.Errorf("acp: bad prompt result: %w", err)
		return
	}
	switch res.StopReason {
	case "end_turn":
	case "cancelled":
		r.err = errors.New("cancelled")
		return
	case "refusal":
		r.err = fmt.Errorf("%w: agent stopped: refusal", model.ErrPermanent)
		return
	default:
		r.err = fmt.Errorf("agent stopped: %s", res.StopReason)
		return
	}
	c := r.conn
	c.mu.Lock()
	defer c.mu.Unlock()
	r.result = model.AgentResult{
		Text: c.text.String(), SessionRef: r.sessionID,
		// inputTokens excludes cache reads/writes, which carry nearly all of a cached prompt
		TokensIn:   res.Usage.InputTokens + res.Usage.CachedReadTokens + res.Usage.CachedWriteTokens,
		TokensOut:  res.Usage.OutputTokens,
		CacheRead:  res.Usage.CachedReadTokens,
		CacheWrite: res.Usage.CachedWriteTokens,
		CostMicros: int64((c.cost - r.costBase) * 1_000_000), CostSource: "unknown",
	}
	if c.costSeen {
		r.result.CostSource = "measured"
	}
	c.costAttr = c.cost
}

// ---- connection ----

type acpConn struct {
	cmd     *exec.Cmd
	w       io.WriteCloser
	wmu     sync.Mutex
	next    atomic.Int64
	pending sync.Map // request id → chan acpMsg
	stderr  *tailBuffer
	exited  chan struct{}
	once    sync.Once
	closed  atomic.Bool

	steering bool // the agent accepts _session/steering

	role      model.AgentRole
	workspace string
	onStep    func(model.AgentStep)
	onText    func(string)

	mu       sync.Mutex
	text     strings.Builder
	cost     float64
	costSeen bool
	costAttr float64 // cost already attributed to finished runs
	tools    map[string]*acpTool
	steps    int
}

type acpTool struct {
	name  string
	input map[string]any
}

type acpMsg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type acpNewSession struct {
	SessionID     string      `json:"sessionId"`
	ConfigOptions []acpOption `json:"configOptions"`
}

type acpOption struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Options  []struct {
		Value string `json:"value"`
		Name  string `json:"name"`
	} `json:"options"`
}

func (ns acpNewSession) modelOption() *acpOption {
	for i := range ns.ConfigOptions {
		if ns.ConfigOptions[i].Category == "model" {
			return &ns.ConfigOptions[i]
		}
	}
	return nil
}

func (c *acpConn) newSession(ctx context.Context, cwd string, meta map[string]any) (acpNewSession, error) {
	var ns acpNewSession
	params := map[string]any{"cwd": cwd, "mcpServers": []any{}}
	if meta != nil {
		params["_meta"] = meta
	}
	raw, err := c.call(ctx, "session/new", params)
	if err != nil {
		return ns, err
	}
	if err := json.Unmarshal(raw, &ns); err != nil || ns.SessionID == "" {
		return ns, fmt.Errorf("acp: session/new returned no session id")
	}
	return ns, nil
}

// setModel selects ref through the agent's model option. An unknown model is
// permanent: retrying cannot help until a provider is logged in or settings change.
func (c *acpConn) setModel(ctx context.Context, ns acpNewSession, ref, provider string) error {
	opt := ns.modelOption()
	if opt == nil {
		return fmt.Errorf("%w: agent offers no model selection", model.ErrPermanent)
	}
	for _, o := range opt.Options {
		if qualify(o.Value, provider) == ref {
			_, err := c.call(ctx, "session/set_config_option", map[string]any{"sessionId": ns.SessionID, "configId": opt.ID, "value": o.Value})
			return err
		}
	}
	return fmt.Errorf("%w: model %s is not available to the agent; log in to its provider (e.g. opencode auth login, claude login) or pick another model in settings", model.ErrPermanent, ref)
}

// setMode selects mode through the agent's mode option. Failing to is permanent:
// a read-only role must not run in a mode the agent would not confirm.
func (c *acpConn) setMode(ctx context.Context, ns acpNewSession, mode string) error {
	for _, opt := range ns.ConfigOptions {
		if opt.Category != "mode" {
			continue
		}
		for _, o := range opt.Options {
			if o.Value == mode {
				_, err := c.call(ctx, "session/set_config_option", map[string]any{"sessionId": ns.SessionID, "configId": opt.ID, "value": mode})
				return err
			}
		}
	}
	return fmt.Errorf("%w: agent offers no %q mode, which read-only roles require", model.ErrPermanent, mode)
}

// qualify gives a model value the agent lists without a provider its harness provider.
func qualify(value, provider string) string {
	if strings.Contains(value, "/") {
		return value
	}
	return provider + "/" + value
}

func (c *acpConn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	wait, err := c.begin(method, params)
	if err != nil {
		return nil, err
	}
	return wait(ctx)
}

// begin sends a request now and returns a function that waits for its response.
func (c *acpConn) begin(method string, params any) (func(context.Context) (json.RawMessage, error), error) {
	key := fmt.Sprint(c.next.Add(1))
	ch := make(chan acpMsg, 1)
	c.pending.Store(key, ch)
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(key), "method": method, "params": params}); err != nil {
		c.pending.Delete(key)
		return nil, c.exitErr(method, err)
	}
	return func(ctx context.Context) (json.RawMessage, error) {
		defer c.pending.Delete(key)
		select {
		case m := <-ch:
			if m.Error != nil {
				return nil, rpcError(method, m.Error.Message)
			}
			return m.Result, nil
		case <-c.exited:
			return nil, c.exitErr(method, nil)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, nil
}

// rpcError keeps provider quota and invalid-request failures permanent, as a
// quick scheduler retry cannot succeed.
// TODO: uncertain — matched on OpenCode 2.1.3 HTTP error texts; verify the exact ACP error text for quota and privacy-setting failures before prod
func rpcError(method, msg string) error {
	err := fmt.Errorf("acp %s: %s", method, msg)
	low := strings.ToLower(msg)
	for _, s := range []string{"usage limit", "quota", "429", "invalid-request", "trains on request data", "insufficient", "funds"} {
		if strings.Contains(low, s) {
			return fmt.Errorf("%w: %v", model.ErrPermanent, err)
		}
	}
	return err
}

func (c *acpConn) exitErr(method string, cause error) error {
	if tail := strings.TrimSpace(c.stderr.String()); tail != "" {
		return fmt.Errorf("acp %s: agent exited: %s", method, tail)
	}
	if cause != nil {
		return fmt.Errorf("acp %s: %w", method, cause)
	}
	return fmt.Errorf("acp %s: agent exited", method)
}

func (c *acpConn) notify(method string, params any) {
	_ = c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *acpConn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// gone reports that the agent process was torn down or exited.
func (c *acpConn) gone() bool {
	select {
	case <-c.exited:
		return true
	default:
		return c.closed.Load()
	}
}

func (c *acpConn) close() {
	c.once.Do(func() {
		c.closed.Store(true)
		_ = c.w.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
	})
}

func (c *acpConn) readLoop(r io.Reader) {
	defer func() {
		_ = c.cmd.Wait()
		close(c.exited)
	}()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 32<<20)
	for sc.Scan() {
		var m acpMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Method == "" && len(m.ID) > 0:
			if ch, ok := c.pending.Load(strings.Trim(string(m.ID), `"`)); ok {
				ch.(chan acpMsg) <- m
			}
		case m.Method == "session/update":
			c.onUpdate(m.Params)
		case m.Method == "session/request_permission":
			c.answerPermission(m)
		case len(m.ID) > 0:
			_ = c.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "method not supported by legos-factory"}})
		}
	}
}

func (c *acpConn) onUpdate(params json.RawMessage) {
	var p struct {
		Update struct {
			Kind       string          `json:"sessionUpdate"`
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Status     string          `json:"status"`
			RawInput   map[string]any  `json:"rawInput"`
			Content    json.RawMessage `json:"content"` // an object on message chunks, an array on tool updates
			Cost       *struct {
				Amount float64 `json:"amount"`
			} `json:"cost"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	u := p.Update
	c.mu.Lock()
	var step *model.AgentStep
	var delta string
	switch u.Kind {
	case "agent_message_chunk":
		var chunk struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(u.Content, &chunk) == nil && chunk.Type == "text" {
			c.text.WriteString(chunk.Text)
			delta = chunk.Text
		}
	case "usage_update":
		if u.Cost != nil {
			c.cost, c.costSeen = u.Cost.Amount, true
		}
	case "tool_call", "tool_call_update":
		if c.tools == nil {
			c.tools = map[string]*acpTool{}
		}
		t := c.tools[u.ToolCallID]
		if t == nil {
			t = &acpTool{name: u.Title}
			c.tools[u.ToolCallID] = t
		}
		if len(u.RawInput) > 0 {
			t.input = u.RawInput
		}
		if u.Status == "completed" || u.Status == "failed" {
			delete(c.tools, u.ToolCallID)
			c.steps++
			step = &model.AgentStep{N: c.steps, Tools: []model.AgentTool{{Name: t.name, Target: toolTarget(t.input), Failed: u.Status == "failed"}}}
		}
	}
	onStep := c.onStep // Continue swaps it between prompts
	onText := c.onText
	c.mu.Unlock()
	if step != nil && onStep != nil {
		onStep(*step)
	}
	if delta != "" && onText != nil {
		onText(delta)
	}
}

// answerPermission is the second line of defence behind the role config: it
// allows only work inside the workspace, and only reads for read-only roles.
func (c *acpConn) answerPermission(m acpMsg) {
	var p struct {
		ToolCall struct {
			Kind      string `json:"kind"`
			Locations []struct {
				Path string `json:"path"`
			} `json:"locations"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	_ = json.Unmarshal(m.Params, &p)
	allow := c.role != model.AgentChat && (c.role == model.AgentBuilder || p.ToolCall.Kind == "read" || p.ToolCall.Kind == "search" || p.ToolCall.Kind == "think")
	for _, l := range p.ToolCall.Locations {
		if !within(c.workspace, l.Path) {
			allow = false
		}
	}
	want := "reject_once"
	if allow {
		want = "allow_once"
	}
	outcome := map[string]any{"outcome": "cancelled"}
	for _, o := range p.Options {
		if o.Kind == want {
			outcome = map[string]any{"outcome": "selected", "optionId": o.OptionID}
		}
	}
	_ = c.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"outcome": outcome}})
}

func within(root, path string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// tailBuffer keeps the last max bytes written, for agent stderr in errors.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
