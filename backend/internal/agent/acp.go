package agent

// ACPHarness drives an Agent Client Protocol agent (e.g. `opencode acp`) over
// stdio JSON-RPC, one subprocess per run. Verified against OpenCode 2.1.3:
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
	Command []string // agent command, e.g. ["opencode", "acp"]
	Env     []string // extra environment for the agent process
	probe   string   // directory for model discovery sessions
	mu      sync.Mutex
	runs    map[string]*acpRun
}

func NewACPHarness(command []string, probeDir string) *ACPHarness {
	return &ACPHarness{Command: command, probe: probeDir, runs: map[string]*acpRun{}}
}

func (h *ACPHarness) Name() string { return "opencode-acp" }

// readOnly roles review and plan; they never change the workspace.
var acpReadOnlyDeny = []string{"edit", "write", "patch", "apply_patch", "bash", "shell", "execute"}

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
	b, _ := json.Marshal(map[string]any{"permission": perm})
	return string(b)
}

type acpRun struct {
	conn      *acpConn
	sessionID string
	done      chan struct{}
	result    model.AgentResult
	err       error
}

func (h *ACPHarness) Start(ctx context.Context, req model.AgentRequest) (model.AgentSession, error) {
	conn, err := h.spawn(req.Role, req.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	conn.role, conn.workspace, conn.onStep = req.Role, req.WorkspaceDir, req.OnStep
	ns, err := conn.newSession(ctx, req.WorkspaceDir)
	if err != nil {
		conn.close()
		return nil, err
	}
	if req.ProviderID != "" || req.ModelID != "" {
		if err := conn.setModel(ctx, ns, req.ProviderID+"/"+req.ModelID); err != nil {
			conn.close()
			return nil, err
		}
	}
	run := &acpRun{conn: conn, sessionID: ns.SessionID, done: make(chan struct{})}
	// sent before returning, so a Cancel right after Start always follows the prompt
	wait, err := conn.begin("session/prompt", map[string]any{
		"sessionId": ns.SessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": req.Prompt}},
	})
	if err != nil {
		conn.close()
		return nil, err
	}
	h.mu.Lock()
	h.runs[ns.SessionID] = run
	h.mu.Unlock()
	go run.finish(wait)
	return &sessionHandle{id: ns.SessionID}, nil
}

func (h *ACPHarness) Result(ctx context.Context, sessionID string) (model.AgentResult, error) {
	run, ok := h.run(sessionID)
	if !ok {
		return model.AgentResult{}, model.ErrAgentSessionNotFound
	}
	defer h.forget(sessionID)
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
	ns, err := conn.newSession(ctx, h.probe)
	if err != nil {
		return nil, err
	}
	out := []Model{}
	if opt := ns.modelOption(); opt != nil {
		for _, o := range opt.Options {
			provider, id, _ := strings.Cut(o.Value, "/")
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

func (h *ACPHarness) forget(id string) {
	h.mu.Lock()
	delete(h.runs, id)
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
	if _, err := c.call(context.Background(), "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]any{"name": "legos-factory", "version": "1"},
	}); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (r *acpRun) finish(wait func(context.Context) (json.RawMessage, error)) {
	defer close(r.done)
	defer r.conn.close()
	raw, err := wait(context.Background())
	if err != nil {
		r.err = err
		return
	}
	var res struct {
		StopReason string `json:"stopReason"`
		Usage      struct {
			InputTokens  int64 `json:"inputTokens"`
			OutputTokens int64 `json:"outputTokens"`
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
		TokensIn: res.Usage.InputTokens, TokensOut: res.Usage.OutputTokens,
		CostMicros: int64(c.cost * 1_000_000), CostSource: "unknown",
	}
	if c.costSeen {
		r.result.CostSource = "measured"
	}
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

	role      model.AgentRole
	workspace string
	onStep    func(model.AgentStep)

	mu       sync.Mutex
	text     strings.Builder
	cost     float64
	costSeen bool
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

func (c *acpConn) newSession(ctx context.Context, cwd string) (acpNewSession, error) {
	var ns acpNewSession
	raw, err := c.call(ctx, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
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
func (c *acpConn) setModel(ctx context.Context, ns acpNewSession, ref string) error {
	opt := ns.modelOption()
	if opt == nil {
		return fmt.Errorf("%w: agent offers no model selection", model.ErrPermanent)
	}
	for _, o := range opt.Options {
		if o.Value == ref {
			_, err := c.call(ctx, "session/set_config_option", map[string]any{"sessionId": ns.SessionID, "configId": opt.ID, "value": ref})
			return err
		}
	}
	return fmt.Errorf("%w: model %s is not available to the agent; log in to its provider (opencode auth login) or pick another model in settings", model.ErrPermanent, ref)
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
	for _, s := range []string{"usage limit", "quota", "429", "invalid-request", "trains on request data"} {
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

func (c *acpConn) close() {
	c.once.Do(func() {
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
			Kind       string         `json:"sessionUpdate"`
			ToolCallID string         `json:"toolCallId"`
			Title      string         `json:"title"`
			Status     string         `json:"status"`
			RawInput   map[string]any `json:"rawInput"`
			Content    struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Cost *struct {
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
	switch u.Kind {
	case "agent_message_chunk":
		if u.Content.Type == "text" {
			c.text.WriteString(u.Content.Text)
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
	c.mu.Unlock()
	if step != nil && c.onStep != nil {
		c.onStep(*step)
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
	allow := c.role == model.AgentBuilder || p.ToolCall.Kind == "read" || p.ToolCall.Kind == "search" || p.ToolCall.Kind == "think"
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
