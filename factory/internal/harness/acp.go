package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"legos-factory/factory/internal/sandbox"
)

// ACP drives an Agent Client Protocol agent (opencode acp, claude-agent-acp, ...) over
// stdio JSON-RPC, one subprocess per session.
type ACP struct{}

// ErrNoLoad means the agent cannot resume spec.SessionID; the caller decides whether to start fresh.
var ErrNoLoad = errors.New("harness: agent cannot load session")

// ProviderError is a failure reported by the model provider through the agent.
type ProviderError struct {
	IsTransient bool // a field cannot share the method name Transient
	Status      int
	Message     string
}

func (e *ProviderError) Transient() bool { return e.IsTransient }

func (e *ProviderError) Error() string {
	kind := "permanent"
	if e.IsTransient {
		kind = "transient"
	}
	return fmt.Sprintf("provider error (%s): %s", kind, e.Message)
}

var (
	statusRe       = regexp.MustCompile(`\b([45]\d\d)\b`)
	nonAlnum       = regexp.MustCompile(`[^a-z0-9]`)
	permanentTexts = []string{"usage limit", "quota", "invalid request", "trains on request data", "insufficient", "funds", "authentication", "budget",
		"context window", "context length", "maximum context", "unauthorized", "bad request", "policy", "model not found", "invalid params"}
)

// Structured error tags per agent, normalized by lowering case and dropping separators; true means permanent.
// TODO: uncertain — the tables list the kinds seen in captures and the codex protocol enum, not each agent's full set; unlisted kinds fall back to text
var (
	claudeKinds   = map[string]bool{"usagelimit": true, "budget": true, "context": true, "ratelimit": false, "overloaded": false}
	opencodeNames = map[string]bool{"providerfreelimit": false, "providerauth": true, "providerautherror": true, "contextoverflowerror": true}
	codexInfos    = map[string]bool{"usagelimitexceeded": true, "contextwindowexceeded": true, "unauthorized": true, "badrequest": true, "sandboxerror": true,
		"serveroverloaded": false, "httpconnectionfailed": false, "responsestreamconnectionfailed": false, "internalservererror": false,
		"responsestreamdisconnected": false, "responsetoomanyfailedattempts": false}
)

// errorTags are the structured error fields an agent may attach to a JSON-RPC error.
type errorTags struct{ Claude, Opencode, Codex string }

func (t errorTags) permanent() (perm, known bool) {
	for _, c := range []struct {
		v     string
		table map[string]bool
	}{{t.Claude, claudeKinds}, {t.Opencode, opencodeNames}, {t.Codex, codexInfos}} {
		if p, ok := c.table[nonAlnum.ReplaceAllString(strings.ToLower(c.v), "")]; ok {
			return p, true
		}
	}
	return false, false
}

// classifyError keeps quota, funds, auth and invalid-request failures permanent so they are not retried (lesson #2).
// A structured kind decides first; message text is only the fallback, and 429 or overloaded text stays transient.
func classifyError(msg string, tags errorTags) *ProviderError {
	pe := &ProviderError{IsTransient: true, Message: msg}
	if m := statusRe.FindStringSubmatch(msg); m != nil {
		fmt.Sscan(m[1], &pe.Status)
	}
	if perm, ok := tags.permanent(); ok {
		pe.IsTransient = !perm
		return pe
	}
	low := strings.ToLower(msg)
	if strings.Contains(low, "429") || strings.Contains(low, "overloaded") {
		return pe
	}
	for _, s := range permanentTexts {
		if strings.Contains(low, s) {
			pe.IsTransient = false
		}
	}
	return pe
}

func NewACP() *ACP { return &ACP{} }

var readOnlyDeny = []string{"edit", "write", "patch", "apply_patch", "bash", "shell", "execute", "task", "subagent", "webfetch", "websearch"}

// opencodeConfig denies by default: a rejected permission request cancels the whole turn (lesson #3).
// question is denied everywhere: no human answers it in a headless run.
func opencodeConfig(role Role, policy ToolPolicy) string {
	perm := map[string]string{"external_directory": "deny", "question": "deny"}
	if policy.AllowNone {
		perm["*"] = "deny"
	}
	if role.ReadOnly() {
		for _, t := range readOnlyDeny {
			perm[t] = "deny"
		}
	}
	for _, t := range policy.Deny {
		perm[t] = "deny"
	}
	b, _ := json.Marshal(map[string]any{"permission": perm})
	return string(b)
}

// mergeJSON deep-merges two JSON objects; over (the role config) wins on conflicts, and an unparsable base yields over.
func mergeJSON(base, over string) string {
	var b, o map[string]any
	if json.Unmarshal([]byte(base), &b) != nil || json.Unmarshal([]byte(over), &o) != nil {
		return over
	}
	out, _ := json.Marshal(deepMerge(b, o))
	return string(out)
}

func deepMerge(a, b map[string]any) map[string]any {
	for k, v := range b {
		if bm, ok := v.(map[string]any); ok {
			if am, ok := a[k].(map[string]any); ok {
				v = deepMerge(am, bm)
			}
		}
		a[k] = v
	}
	return a
}

const (
	agentClaude   = "claude-agent-acp"
	agentOpencode = "opencode"
	agentCodex    = "codex-acp"
)

// agentOf names the agent kind that selects the model, tool-deny, permission and usage behaviour; "" is a generic ACP agent.
func agentOf(s SessionSpec) string {
	for _, name := range []string{agentClaude, agentOpencode, agentCodex} {
		if s.Profile.Agent == name || (len(s.AgentCommand) > 0 && filepath.Base(s.AgentCommand[0]) == name) {
			return name
		}
	}
	return ""
}

var claudeReadOnlyDeny = []string{"Edit", "Write", "MultiEdit", "NotebookEdit", "Bash"}

// claudeMeta carries model and inline tool denies; claude-agent-acp reads them from _meta at session creation.
func claudeMeta(spec SessionSpec) map[string]any {
	opts := map[string]any{}
	if spec.Profile.Model != "" {
		opts["model"] = spec.Profile.Model
	}
	var deny []string
	if spec.Role.ReadOnly() {
		deny = append(deny, claudeReadOnlyDeny...)
	}
	deny = append(deny, spec.ToolPolicy.Deny...)
	if spec.ToolPolicy.AllowNone {
		opts["tools"] = []string{}
	}
	if len(deny) > 0 {
		opts["settings"] = map[string]any{"permissions": map[string]any{"deny": deny}}
	}
	return map[string]any{"claudeCode": map[string]any{"options": opts}}
}

func (ACP) Start(ctx context.Context, spec SessionSpec) (Session, error) {
	if len(spec.AgentCommand) == 0 {
		return nil, errors.New("acp: no agent command configured")
	}
	if spec.Sandbox == nil {
		return nil, errors.New("acp: no sandbox")
	}
	env := map[string]string{}
	kind := agentOf(spec)
	switch kind {
	case agentOpencode:
		env["OPENCODE_DISABLE_AUTOUPDATE"] = "1"
		env["OPENCODE_CONFIG_CONTENT"] = opencodeConfig(spec.Role, spec.ToolPolicy)
	case agentCodex:
		env["NO_BROWSER"] = "1"
	case agentClaude:
		// fallback only: callers (the engine) own the per-session dir; a private one keeps the owner's settings and CLAUDE.md out of the prompt
		env["CLAUDE_CONFIG_DIR"] = filepath.Join(spec.Sandbox.Workspace().HomeDir(), string(spec.Role)+"-session")
		env["IS_SANDBOX"] = "1"
		if spec.Profile.Model != "" {
			env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = spec.Profile.Model
		}
	}
	for k, v := range spec.Env {
		if k == "OPENCODE_CONFIG_CONTENT" && env[k] != "" {
			v = mergeJSON(v, env[k])
		}
		env[k] = v
	}
	sessDir := env["CLAUDE_CONFIG_DIR"]
	if sessDir == "" {
		sessDir = filepath.Join(spec.Sandbox.Workspace().HomeDir(), string(spec.Role)+"-session")
	}
	// login files live in the per-session dir, not the shared home, so read-only roles never see them
	if _, ok := env["XDG_DATA_HOME"]; !ok && kind == agentOpencode {
		env["XDG_DATA_HOME"] = filepath.Join(sessDir, ".local", "share")
	}
	if _, ok := env["CODEX_HOME"]; !ok && kind == agentCodex {
		env["CODEX_HOME"] = filepath.Join(sessDir, ".codex")
	}
	var written []string
	for rel, data := range spec.Files {
		p := filepath.Join(sessDir, rel)
		if err := spec.Sandbox.WriteFile(ctx, p, data, 0o600); err != nil {
			removeFiles(spec.Sandbox, written)
			return nil, fmt.Errorf("acp: write %s: %w", rel, err)
		}
		written = append(written, p)
	}
	cmd := spec.Sandbox.Command(spec.Dir, env, spec.AgentCommand...).Exec(context.Background())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	s := &acpSession{
		cmd: cmd, w: stdin, spec: spec, kind: kind, apiKeyAuth: env["CODEX_API_KEY"] != "" || env["OPENAI_API_KEY"] != "", stderr: &tailBuffer{max: 2048},
		events: make(chan Event, 1024), quit: make(chan struct{}), exited: make(chan struct{}),
		tools: map[string]string{}, toolKinds: map[string]string{}, files: written,
	}
	cmd.Stderr = s.stderr
	if err := cmd.Start(); err != nil {
		removeFiles(spec.Sandbox, written)
		return nil, fmt.Errorf("acp: start %s: %w", strings.Join(spec.AgentCommand, " "), err)
	}
	go s.readLoop(stdout)
	if err := s.handshake(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

type acpSession struct {
	cmd     *exec.Cmd
	w       io.WriteCloser
	wmu     sync.Mutex
	spec    SessionSpec
	kind    string // agentOf(spec)
	id      string
	next    atomic.Int64
	pending sync.Map // request id → chan acpMsg
	stderr  *tailBuffer
	exited  chan struct{}

	events chan Event
	quit   chan struct{}
	emu    sync.RWMutex
	closed bool
	once   sync.Once

	promptMu  sync.Mutex
	mu        sync.Mutex
	muted     bool // session/load replays history as updates
	text      strings.Builder
	tools     map[string]string // tool call id → title
	toolKinds map[string]string // tool call id → ACP tool kind
	files     []string          // login files to delete on Close

	apiKeyAuth bool // codex-acp rejects session/new until authenticate selects the API-key method
}

func removeFiles(sb sandbox.Sandbox, paths []string) {
	if len(paths) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, _, _ = sb.Exec(ctx, "", nil, append([]string{"rm", "-f"}, paths...)...)
}

type acpMsg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error,omitempty"`
}

type sessionResult struct {
	SessionID     string `json:"sessionId"`
	ConfigOptions []struct {
		ID       string `json:"id"`
		Category string `json:"category"`
		Options  []struct {
			Value string `json:"value"`
		} `json:"options"`
	} `json:"configOptions"`
	Models *struct {
		AvailableModels []struct {
			ModelID string `json:"modelId"`
		} `json:"availableModels"`
	} `json:"models"`
}

func (s *acpSession) ID() string           { return s.id }
func (s *acpSession) Events() <-chan Event { return s.events }

func (s *acpSession) handshake(ctx context.Context) error {
	raw, err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]any{"name": "legos-factory", "version": "1"},
	})
	if err != nil {
		return err
	}
	var init struct {
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	_ = json.Unmarshal(raw, &init)

	if s.kind == agentCodex && s.apiKeyAuth {
		if _, err := s.call(ctx, "authenticate", map[string]any{"methodId": "api-key"}); err != nil {
			return err
		}
	}
	params := map[string]any{"cwd": s.spec.Dir, "mcpServers": []any{}}
	if s.kind == agentClaude {
		params["_meta"] = claudeMeta(s.spec)
	}
	var res sessionResult
	if s.spec.SessionID != "" {
		if !init.AgentCapabilities.LoadSession {
			return fmt.Errorf("%w: agent does not advertise loadSession", ErrNoLoad)
		}
		s.setMuted(true)
		params["sessionId"] = s.spec.SessionID
		raw, err = s.call(ctx, "session/load", params)
		s.setMuted(false)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrNoLoad, err)
		}
		_ = json.Unmarshal(raw, &res)
		s.id = s.spec.SessionID
	} else {
		raw, err = s.call(ctx, "session/new", params)
		if err != nil {
			return err
		}
		if json.Unmarshal(raw, &res) != nil || res.SessionID == "" {
			return errors.New("acp: session/new returned no session id")
		}
		s.id = res.SessionID
	}
	return s.setModel(ctx, res)
}

// setModel selects the profile's model before the first prompt; OpenCode ignores a model sent with the prompt (lesson #4).
// claude-agent-acp takes the model from _meta at creation, so only a loaded session needs set_config_option;
// its builder also leaves permission prompts behind via bypassPermissions mode.
func (s *acpSession) setModel(ctx context.Context, res sessionResult) error {
	p := s.spec.Profile
	switch s.kind {
	case agentClaude:
		if !s.spec.Role.ReadOnly() {
			if _, err := s.call(ctx, "session/set_config_option", map[string]any{"sessionId": s.id, "configId": "mode", "value": "bypassPermissions"}); err != nil {
				return err
			}
		}
		if p.Model != "" && s.spec.SessionID != "" {
			for _, o := range res.ConfigOptions {
				if o.Category == "model" {
					_, err := s.call(ctx, "session/set_config_option", map[string]any{"sessionId": s.id, "configId": o.ID, "value": p.Model})
					return err
				}
			}
		}
		return nil
	case agentCodex:
		if err := s.setCodexMode(ctx, res); err != nil {
			return err
		}
	}
	if p.Model == "" {
		return nil
	}
	switch s.kind {
	case agentOpencode:
		ref := p.Model
		if p.Provider != "" {
			ref = p.Provider + "/" + p.Model
		}
		return s.setOption(ctx, res, "model", ref, ref, "log in to its provider or pick another model")
	case agentCodex:
		// codex-acp advertises "<model>[<effort>]" in models, and the model and thought_level options separately
		base, effort, _ := strings.Cut(strings.TrimSuffix(p.Model, "]"), "[")
		if hasOption(res, "model") {
			if err := s.setOption(ctx, res, "model", base, base, "pick another model"); err != nil {
				return err
			}
			if effort != "" {
				return s.setOption(ctx, res, "thought_level", effort, base+" with effort "+effort, "pick another reasoning effort")
			}
			return nil
		}
	}
	if res.Models == nil {
		return nil
	}
	for _, m := range res.Models.AvailableModels {
		if m.ModelID == p.Model {
			_, err := s.call(ctx, "session/set_model", map[string]any{"sessionId": s.id, "modelId": p.Model})
			return err
		}
	}
	return &ProviderError{Message: fmt.Sprintf("model %s is not offered by the agent", p.Model)}
}

func hasOption(res sessionResult, category string) bool {
	for _, o := range res.ConfigOptions {
		if o.Category == category {
			return true
		}
	}
	return false
}

// setOption sets the first config option of a category to value; a value the agent does not offer is permanent.
func (s *acpSession) setOption(ctx context.Context, res sessionResult, category, value, label, hint string) error {
	for _, o := range res.ConfigOptions {
		if o.Category != category {
			continue
		}
		for _, v := range o.Options {
			if v.Value == value {
				_, err := s.call(ctx, "session/set_config_option", map[string]any{"sessionId": s.id, "configId": o.ID, "value": value})
				return err
			}
		}
		return &ProviderError{Message: fmt.Sprintf("model %s is not available to the agent; %s", label, hint)}
	}
	return &ProviderError{Message: "agent offers no " + category + " selection"}
}

// setCodexMode picks the approval and sandbox preset: read-only for every role but the builder, which runs
// with full access because the container is the boundary and codex's own sandbox needs kernel features containers may lack.
func (s *acpSession) setCodexMode(ctx context.Context, res sessionResult) error {
	mode := "agent-full-access"
	if s.spec.Role.ReadOnly() || s.spec.ToolPolicy.AllowNone {
		mode = "read-only"
	}
	for _, o := range res.ConfigOptions {
		if o.Category == "mode" {
			_, err := s.call(ctx, "session/set_config_option", map[string]any{"sessionId": s.id, "configId": o.ID, "value": mode})
			return err
		}
	}
	return nil
}

func (s *acpSession) Prompt(ctx context.Context, text string) (string, error) {
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	wait, err := s.begin("session/prompt", map[string]any{
		"sessionId": s.id,
		"prompt":    []any{map[string]any{"type": "text", "text": text}},
	})
	if err != nil {
		return "", err
	}
	// after ctx cancel, give the agent a few seconds to answer session/cancel with its stop reason
	pctx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() {
		select {
		case <-ctx.Done():
			s.notify("session/cancel", map[string]any{"sessionId": s.id})
			select {
			case <-pctx.Done():
			case <-time.After(5 * time.Second):
				stop()
			}
		case <-pctx.Done():
		}
	}()
	raw, err := wait(pctx)
	s.flushText()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var pe *ProviderError
		if errors.As(err, &pe) {
			s.emit(Event{Kind: EventError, Err: pe, Text: pe.Message})
		}
		return "", err
	}
	var res struct {
		StopReason string `json:"stopReason"`
		Usage      *struct {
			InputTokens       int `json:"inputTokens"`
			OutputTokens      int `json:"outputTokens"`
			CachedReadTokens  int `json:"cachedReadTokens"`
			CachedWriteTokens int `json:"cachedWriteTokens"`
			ThoughtTokens     int `json:"thoughtTokens"`
		} `json:"usage"`
		Meta struct {
			Quota struct {
				ModelUsage []struct {
					Model      string `json:"model"`
					TokenCount struct {
						InputTokens       int `json:"inputTokens"`
						CachedInputTokens int `json:"cachedInputTokens"`
						CachedWriteTokens int `json:"cachedWriteTokens"`
						OutputTokens      int `json:"outputTokens"`
					} `json:"token_count"`
				} `json:"model_usage"`
			} `json:"quota"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("acp: bad prompt result: %w", err)
	}
	var models []ModelUsage
	for _, m := range res.Meta.Quota.ModelUsage {
		t := m.TokenCount
		models = append(models, ModelUsage{Model: m.Model, InputTokens: t.InputTokens, OutputTokens: t.OutputTokens, CacheReadTokens: t.CachedInputTokens, CacheWriteTokens: t.CachedWriteTokens})
	}
	if u := res.Usage; u != nil || len(models) > 0 {
		var ev Usage
		if u != nil {
			ev = Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CachedReadTokens, CacheCreateTokens: u.CachedWriteTokens}
			if s.kind == agentOpencode {
				ev.OutputTokens += u.ThoughtTokens // opencode reports reasoning outside outputTokens (totalTokens is the sum of all parts)
			}
		}
		ev.Models = models
		s.emit(Event{Kind: EventUsage, Usage: ev})
	}
	s.emit(Event{Kind: EventStop, StopReason: res.StopReason})
	if ctx.Err() != nil {
		return res.StopReason, ctx.Err()
	}
	return res.StopReason, nil
}

// Close kills the agent's process group and closes Events.
func (s *acpSession) Close() error {
	s.once.Do(func() {
		close(s.quit)
		_ = s.w.Close()
		if s.cmd.Process != nil {
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		}
		<-s.exited
		removeFiles(s.spec.Sandbox, s.files)
		s.emu.Lock()
		s.closed = true
		close(s.events)
		s.emu.Unlock()
	})
	return nil
}

func (s *acpSession) emit(e Event) {
	e.At = time.Now()
	s.emu.RLock()
	defer s.emu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.events <- e:
	case <-s.quit:
	}
}

func (s *acpSession) setMuted(v bool) {
	s.mu.Lock()
	s.muted = v
	s.mu.Unlock()
}

func (s *acpSession) flushText() {
	s.mu.Lock()
	t := strings.TrimSpace(s.text.String())
	s.text.Reset()
	s.mu.Unlock()
	if t != "" {
		s.emit(Event{Kind: EventStep, Text: t})
	}
}

func (s *acpSession) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	wait, err := s.begin(method, params)
	if err != nil {
		return nil, err
	}
	return wait(ctx)
}

func (s *acpSession) begin(method string, params any) (func(context.Context) (json.RawMessage, error), error) {
	key := fmt.Sprint(s.next.Add(1))
	ch := make(chan acpMsg, 1)
	s.pending.Store(key, ch)
	if err := s.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(key), "method": method, "params": params}); err != nil {
		s.pending.Delete(key)
		return nil, s.exitErr(method, err)
	}
	return func(ctx context.Context) (json.RawMessage, error) {
		defer s.pending.Delete(key)
		select {
		case m := <-ch:
			if m.Error != nil {
				var data struct {
					ErrorKind      string          `json:"errorKind"`
					ErrorName      string          `json:"errorName"`
					Message        string          `json:"message"`
					CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
				}
				_ = json.Unmarshal(m.Error.Data, &data)
				msg := m.Error.Message
				if data.Message != "" {
					msg += ": " + data.Message // codex-acp puts the cause here under a generic "Internal error"
				}
				return nil, fmt.Errorf("acp %s: %w", method, classifyError(msg, errorTags{Claude: data.ErrorKind, Opencode: data.ErrorName, Codex: codexInfoName(data.CodexErrorInfo)}))
			}
			return m.Result, nil
		case <-s.exited:
			return nil, s.exitErr(method, nil)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, nil
}

// codexInfoName reads codexErrorInfo, a bare string or a one-key object such as {"httpConnectionFailed":{...}}.
func codexInfoName(raw json.RawMessage) string {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return name
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		for k := range obj {
			return k
		}
	}
	return ""
}

// secrets lists the credential values a stderr tail must not leak: long env values and apiKeys inside the opencode config.
func (s *acpSession) secrets() []string {
	var out []string
	for _, v := range s.spec.Env {
		if len(v) >= 8 {
			out = append(out, v)
		}
	}
	var cfg any
	if json.Unmarshal([]byte(s.spec.Env["OPENCODE_CONFIG_CONTENT"]), &cfg) == nil {
		var walk func(any)
		walk = func(n any) {
			switch n := n.(type) {
			case map[string]any:
				for k, v := range n {
					if str, ok := v.(string); ok && k == "apiKey" && str != "" {
						out = append(out, str)
					}
					walk(v)
				}
			case []any:
				for _, v := range n {
					walk(v)
				}
			}
		}
		walk(cfg)
	}
	slices.SortFunc(out, func(a, b string) int { return len(b) - len(a) })
	return out
}

func (s *acpSession) exitErr(method string, cause error) error {
	tail := strings.TrimSpace(s.stderr.String())
	for _, v := range s.secrets() {
		tail = strings.ReplaceAll(tail, v, "[redacted]")
	}
	if tail != "" {
		return fmt.Errorf("acp %s: agent exited: %s", method, tail)
	}
	if cause != nil {
		return fmt.Errorf("acp %s: %w", method, cause)
	}
	return fmt.Errorf("acp %s: agent exited", method)
}

func (s *acpSession) notify(method string, params any) {
	_ = s.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (s *acpSession) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.w.Write(append(b, '\n'))
	return err
}

func (s *acpSession) readLoop(r io.Reader) {
	defer func() {
		_ = s.cmd.Wait()
		close(s.exited)
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
			if ch, ok := s.pending.Load(strings.Trim(string(m.ID), `"`)); ok {
				ch.(chan acpMsg) <- m
			}
		case m.Method == "session/update":
			s.onUpdate(m.Params)
		case m.Method == "session/request_permission":
			s.answerPermission(m)
		case len(m.ID) > 0:
			_ = s.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "method not supported by legos-factory"}})
		}
	}
}

func (s *acpSession) onUpdate(params json.RawMessage) {
	var p struct {
		Update struct {
			Kind       string `json:"sessionUpdate"`
			ToolCallID string `json:"toolCallId"`
			Title      string `json:"title"`
			ToolKind   string `json:"kind"`
			Status     string `json:"status"`
			Meta       struct {
				ClaudeCode struct {
					ToolName string `json:"toolName"`
				} `json:"claudeCode"`
			} `json:"_meta"`
			Content json.RawMessage `json:"content"` // an object on message chunks, an array on tool updates
			Cost    *struct {
				Amount float64 `json:"amount"`
			} `json:"cost"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	u := p.Update
	if n := u.Meta.ClaudeCode.ToolName; n != "" {
		u.Title = n
	}
	s.mu.Lock()
	muted := s.muted
	s.mu.Unlock()
	if muted {
		return
	}
	switch u.Kind {
	case "agent_message_chunk":
		var chunk struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(u.Content, &chunk) == nil && chunk.Type == "text" {
			s.mu.Lock()
			s.text.WriteString(chunk.Text)
			s.mu.Unlock()
		}
	case "usage_update":
		if u.Cost != nil {
			s.emit(Event{Kind: EventUsage, Usage: Usage{CostUSD: u.Cost.Amount}})
		}
	case "tool_call":
		s.flushText()
		s.mu.Lock()
		s.tools[u.ToolCallID] = u.Title
		s.toolKinds[u.ToolCallID] = u.ToolKind
		s.mu.Unlock()
		s.emit(Event{Kind: EventToolCall, ToolCallID: u.ToolCallID, Tool: u.Title, ToolKind: u.ToolKind, Status: u.Status})
	case "tool_call_update":
		s.mu.Lock()
		if u.Title != "" {
			s.tools[u.ToolCallID] = u.Title
		}
		if u.ToolKind != "" {
			s.toolKinds[u.ToolCallID] = u.ToolKind
		}
		title, kind := s.tools[u.ToolCallID], s.toolKinds[u.ToolCallID]
		s.mu.Unlock()
		s.emit(Event{Kind: EventToolUpdate, ToolCallID: u.ToolCallID, Tool: title, ToolKind: kind, Status: u.Status, Text: toolText(u.Content)})
	}
}

func toolText(raw json.RawMessage) string {
	var items []struct {
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var parts []string
	for _, it := range items {
		if it.Content.Type == "text" && it.Content.Text != "" {
			parts = append(parts, it.Content.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// answerPermission allows by default because on OpenCode a reject cancels the whole turn (lesson #3);
// containment there comes from the role config and the sandbox, and a read-only role or out-of-workspace
// path that still reaches here is surfaced as an EventError. On claude-agent-acp a reject only fails
// that tool call, so read-only roles reject anything but reads.
func (s *acpSession) answerPermission(m acpMsg) {
	var p struct {
		ToolCall struct {
			Title     string `json:"title"`
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
	k := p.ToolCall.Kind
	// codex only asks before commands and file changes, so a read-only role rejects every request whatever its kind
	harmless := (k == "read" || k == "search" || k == "think") && s.kind != agentCodex
	blocked := s.spec.Role.ReadOnly() && !harmless
	if blocked {
		s.emit(Event{Kind: EventError, Tool: p.ToolCall.Title, Err: fmt.Errorf("permission requested by read-only role %s for %s", s.spec.Role, k)})
	}
	for _, l := range p.ToolCall.Locations {
		if !within(s.spec.Dir, l.Path) {
			s.emit(Event{Kind: EventError, Tool: p.ToolCall.Title, Err: fmt.Errorf("permission requested outside workspace: %s", l.Path)})
		}
	}
	prefer := []string{"allow_once", "allow_always"}
	// a reject fails only that call on claude and codex but cancels the turn on opencode, which only the tool-less Foreman accepts
	rejects := s.kind == agentClaude || s.kind == agentCodex
	if rejects && blocked || s.spec.ToolPolicy.AllowNone {
		prefer = []string{"reject_once", "reject_always"}
	}
	outcome := map[string]any{"outcome": "cancelled"}
	for _, want := range prefer {
		for _, o := range p.Options {
			if o.Kind == want {
				outcome = map[string]any{"outcome": "selected", "optionId": o.OptionID}
				want = ""
				break
			}
		}
		if want == "" {
			break
		}
	}
	_ = s.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"outcome": outcome}})
}

func within(root, path string) bool {
	if root == "" {
		return true
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
