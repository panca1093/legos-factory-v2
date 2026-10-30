package harness

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type fixtureRun struct {
	events  []Event
	stop    string
	err     error
	replies []string
}

func (r fixtureRun) of(kind EventKind) (out []Event) {
	for _, e := range r.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func (r fixtureRun) tools() (names []string) {
	for _, e := range r.of(EventToolCall) {
		names = append(names, e.Tool)
	}
	return names
}

// finalStatus is the status of each tool's last update, keyed by the name its tool_call announced.
func (r fixtureRun) finalStatus() map[string]string {
	name, out := map[string]string{}, map[string]string{}
	for _, e := range r.of(EventToolCall) {
		name[e.ToolCallID] = e.Tool
	}
	for _, e := range r.of(EventToolUpdate) {
		out[name[e.ToolCallID]] = e.Status
	}
	return out
}

// tokens is the usage event the prompt result produced (usage_update frames carry cost only).
func (r fixtureRun) tokens() Usage {
	for _, e := range r.of(EventUsage) {
		if e.Usage.InputTokens+e.Usage.OutputTokens+e.Usage.CacheReadTokens > 0 {
			u := e.Usage
			u.Models = nil
			return u
		}
	}
	return Usage{}
}

type fixtureCase struct {
	name     string
	file     string
	profile  Profile
	role     Role
	policy   ToolPolicy
	env      map[string]string
	loadID   string // resume this session instead of creating one
	startsAt string // "session": the capture holds only the handshake; replay it and inspect the requests sent

	// expectations
	startErr  string // Start fails permanently with this text
	calls     []string
	notCalls  []string
	stop      string
	promptErr string // the prompt fails permanently with this text
	tools     []string
	final     map[string]string
	steps     int
	tokens    Usage
	replies   []string // substring of each permission reply, in order
	errEvents []string // substring of each EventError
	noWrites  bool     // no tool call that writes or executes
}

var (
	claude   = Profile{Agent: "claude-agent-acp"}
	opencode = Profile{Agent: "opencode", Provider: "opencode", Model: "big-pickle"}
	codex    = Profile{Agent: "codex-acp", Model: "gpt-6.1-sol[high]"}
)

var fixtureCases = []fixtureCase{
	{name: "claude deny", file: "claude-agent-acp-deny.jsonl", profile: claude, role: RoleInspector, stop: "end_turn", steps: 2,
		tokens: Usage{InputTokens: 6, OutputTokens: 916, CacheReadTokens: 117304, CacheCreateTokens: 32059}},
	{name: "claude permission reject", file: "claude-agent-acp-permission-reject.jsonl", profile: claude, role: RoleInspector, stop: "end_turn",
		tools: []string{"Write"}, final: map[string]string{"Write": "failed"}, steps: 1,
		tokens:  Usage{InputTokens: 4, OutputTokens: 528, CacheReadTokens: 70218, CacheCreateTokens: 31950},
		replies: []string{`"optionId":"reject"`}, errEvents: []string{"read-only role", "outside workspace"}},
	{name: "claude bypass write", file: "claude-agent-acp-bypass-write.jsonl", profile: claude, role: RoleBuilder, stop: "end_turn",
		tools: []string{"Write"}, final: map[string]string{"Write": "completed"}, steps: 1,
		tokens: Usage{InputTokens: 18, OutputTokens: 335, CacheReadTokens: 38370, CacheCreateTokens: 45121}},

	{name: "opencode hand-written stream", file: "opencode_prompt.jsonl", profile: opencode, role: RoleBuilder, stop: "end_turn",
		tools: []string{"read", "shell"}, final: map[string]string{"read": "completed", "shell": "failed"}, steps: 2,
		tokens: Usage{InputTokens: 100, OutputTokens: 20 + 5, CacheReadTokens: 7}},
	{name: "opencode edit", file: "opencode-edit.jsonl", profile: opencode, role: RoleBuilder, stop: "end_turn",
		tools: []string{"read", "edit"}, final: map[string]string{"read": "completed", "edit": "completed"}, steps: 1,
		tokens: Usage{InputTokens: 4561, OutputTokens: 233 + 136, CacheReadTokens: 12160}},
	{name: "opencode read-only role with write tools denied", file: "opencode-deny.jsonl", profile: opencode, role: RoleInspector, stop: "end_turn",
		noWrites: true, steps: 2, tokens: Usage{InputTokens: 3915, OutputTokens: 150 + 202, CacheReadTokens: 3194}},
	{name: "opencode session/load from a fresh process", file: "opencode-load.jsonl", profile: opencode, role: RoleBuilder, loadID: "ses_e7366b306ffeYzYopQuNV1rPgi",
		stop: "end_turn", steps: 1, tokens: Usage{InputTokens: 122, OutputTokens: 3, CacheReadTokens: 5694},
	},
	{name: "opencode free model limit is permanent", file: "opencode-error-free-limit.jsonl", profile: opencode, role: RoleForeman, policy: ToolPolicy{AllowNone: true},
		promptErr: "Free usage limit reached"},
	{name: "opencode provider 401 is permanent", file: "opencode-error-auth.jsonl", profile: Profile{Agent: "opencode", Provider: "acme", Model: "m1"}, role: RoleBuilder,
		promptErr: "Authentication required"},
	{name: "opencode unknown model is rejected before the prompt", file: "opencode-error-bad-model.jsonl",
		profile: Profile{Agent: "opencode", Provider: "opencode", Model: "does-not-exist"}, role: RoleBuilder, startsAt: "session",
		startErr: "opencode/does-not-exist is not available"},

	{name: "opencode new session selects the free model", file: "opencode-edit.jsonl", profile: opencode, role: RoleBuilder, startsAt: "session",
		calls: []string{`"configId":"model"`, `"value":"opencode/big-pickle"`}, notCalls: []string{`"configId":"mode"`}},
	{name: "codex new session selects mode, model and effort", file: "codex-acp-new.jsonl", profile: codex, role: RoleBuilder, startsAt: "session",
		env: map[string]string{"OPENAI_API_KEY": "dummy"},
		calls: []string{`"methodId":"api-key"`, `"configId":"mode","sessionId":"01a18c9b-5fa0-70c0-b435-e9a8226ec1ed","value":"agent-full-access"`,
			`"configId":"model","sessionId":"01a18c9b-5fa0-70c0-b435-e9a8226ec1ed","value":"gpt-6.1-sol"`, `"configId":"reasoning_effort","sessionId":"01a18c9b-5fa0-70c0-b435-e9a8226ec1ed","value":"high"`}},
	{name: "codex read-only role runs in read-only mode", file: "codex-acp-new.jsonl", profile: Profile{Agent: "codex-acp", Model: "gpt-6-luna"}, role: RoleInspector, startsAt: "session",
		calls:    []string{`"configId":"mode","sessionId":"01a18c9b-5fa0-70c0-b435-e9a8226ec1ed","value":"read-only"`, `"value":"gpt-6-luna"`},
		notCalls: []string{"authenticate", "reasoning_effort"}},
	{name: "codex foreman without tools runs in read-only mode", file: "codex-acp-new.jsonl", profile: codex, role: RoleForeman, policy: ToolPolicy{AllowNone: true}, startsAt: "session",
		calls: []string{`"value":"read-only"`}},
	{name: "codex unknown model is rejected", file: "codex-acp-new.jsonl", profile: Profile{Agent: "codex-acp", Model: "gpt-9"}, role: RoleBuilder, startsAt: "session",
		startErr: "gpt-9 is not available"},
	{name: "codex without credentials fails session/new permanently", file: "codex-acp-error-auth.jsonl", profile: codex, role: RoleBuilder, startsAt: "session",
		startErr: "Authentication required"},
	{name: "codex edit", file: "codex-acp-edit-synthetic.jsonl", profile: codex, role: RoleBuilder, stop: "end_turn",
		tools: []string{"Run `cat main.go`", "Edit files"}, final: map[string]string{"Run `cat main.go`": "completed", "Edit files": "completed"}, steps: 2,
		tokens: Usage{InputTokens: 1200, OutputTokens: 340, CacheReadTokens: 8000}},
	{name: "codex read-only role declines a command and the turn goes on", file: "codex-acp-deny-command-synthetic.jsonl", profile: codex, role: RoleInspector, stop: "end_turn",
		tools: []string{"Run `touch notes.txt`"}, final: map[string]string{"Run `touch notes.txt`": "failed"}, steps: 1,
		tokens: Usage{InputTokens: 900, OutputTokens: 40}, replies: []string{`"optionId":"decline"`}, errEvents: []string{"read-only role"}},
	{name: "codex read-only role cancels a file change", file: "codex-acp-deny-filechange-synthetic.jsonl", profile: codex, role: RoleInspector, stop: "cancelled",
		final: map[string]string{"Edit files": "failed"}, replies: []string{`"optionId":"cancel"`}, errEvents: []string{"read-only role"}},
	{name: "codex usage limit is permanent", file: "codex-acp-error-quota-synthetic.jsonl", profile: codex, role: RoleBuilder, promptErr: "usage limit"},
}

func TestAgentFixtures(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range fixtureCases {
		seen[tc.file] = true
		t.Run(tc.name, func(t *testing.T) {
			if tc.startsAt != "" {
				checkStart(t, tc)
				return
			}
			r := replay(t, tc)
			if tc.promptErr != "" {
				assertPermanent(t, r.err, tc.promptErr)
				return
			}
			if r.err != nil {
				t.Fatalf("prompt: %v", r.err)
			}
			if r.stop != tc.stop || len(r.of(EventStop)) != 1 {
				t.Errorf("stop = %q (%d stop events), want %q", r.stop, len(r.of(EventStop)), tc.stop)
			}
			if got := r.tokens(); !reflect.DeepEqual(got, tc.tokens) {
				t.Errorf("tokens = %+v, want %+v", got, tc.tokens)
			}
			if got := len(r.of(EventStep)); got != tc.steps {
				t.Errorf("steps = %d, want %d: %+v", got, tc.steps, r.of(EventStep))
			}
			for _, e := range r.of(EventStep) {
				if e.Text == "" {
					t.Errorf("empty step: %+v", e)
				}
			}
			if tc.tools != nil && !slices.Equal(r.tools(), tc.tools) {
				t.Errorf("tool calls = %q, want %q", r.tools(), tc.tools)
			}
			for _, e := range r.of(EventToolCall) {
				if e.Tool == "" || e.ToolCallID == "" {
					t.Errorf("tool call without name or id: %+v", e)
				}
			}
			for name, status := range tc.final {
				if got := r.finalStatus()[name]; got != status {
					t.Errorf("final status of %q = %q, want %q (all: %v)", name, got, status, r.finalStatus())
				}
			}
			if tc.noWrites {
				for _, e := range r.of(EventToolCall) {
					if low := strings.ToLower(e.Tool); strings.Contains(low, "write") || strings.Contains(low, "edit") || strings.Contains(low, "shell") || strings.Contains(low, "bash") {
						t.Errorf("read-only role ran %q", e.Tool)
					}
				}
			}
			if len(r.replies) != len(tc.replies) {
				t.Fatalf("permission replies = %q, want %d", r.replies, len(tc.replies))
			}
			for i, want := range tc.replies {
				if !strings.Contains(r.replies[i], want) {
					t.Errorf("reply %d = %s, want %s", i, r.replies[i], want)
				}
			}
			errs := r.of(EventError)
			if len(errs) != len(tc.errEvents) {
				t.Fatalf("error events = %+v, want %q", errs, tc.errEvents)
			}
			for i, want := range tc.errEvents {
				if !strings.Contains(errs[i].Err.Error(), want) {
					t.Errorf("error event %d = %v, want %q", i, errs[i].Err, want)
				}
			}
		})
	}
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") && !seen[e.Name()] {
			t.Errorf("testdata/%s has no case in fixtureCases", e.Name())
		}
	}
}

func spec(tc fixtureCase) SessionSpec {
	return SessionSpec{Role: tc.role, Profile: tc.profile, ToolPolicy: tc.policy, SessionID: tc.loadID}
}

func fixturePath(t *testing.T, file string) string {
	t.Helper()
	abs, err := filepath.Abs("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func fixtureEnv(t *testing.T, tc fixtureCase, extra ...string) []string {
	t.Helper()
	env := append([]string{"ACP_FAKE_FIXTURE=" + fixturePath(t, tc.file)}, extra...)
	if tc.loadID != "" || tc.startsAt != "" {
		env = append(env, "ACP_FAKE_NEWFIXTURE="+fixturePath(t, tc.file))
	}
	for k, v := range tc.env {
		env = append(env, k+"="+v)
	}
	return env
}

func replay(t *testing.T, tc fixtureCase) fixtureRun {
	t.Helper()
	replyLog := filepath.Join(t.TempDir(), "replies")
	sp := spec(tc)
	sp.Profile.Model = "" // model selection is covered by the startsAt cases; replay fakes do not advertise this agent's options
	s, err := startFake(t, "replay", sp, fixtureEnv(t, tc, "ACP_FAKE_REPLYLOG="+replyLog)...)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if tc.loadID != "" && s.ID() != tc.loadID {
		t.Fatalf("session id = %q, want the loaded %q", s.ID(), tc.loadID)
	}
	stop, perr := prompt(t, s)
	b, _ := os.ReadFile(replyLog)
	r := fixtureRun{events: drain(s), stop: stop, err: perr}
	if txt := strings.TrimSpace(string(b)); txt != "" {
		r.replies = strings.Split(txt, "\n")
	}
	return r
}

func checkStart(t *testing.T, tc fixtureCase) {
	t.Helper()
	s, err := startFake(t, "echo", spec(tc), fixtureEnv(t, tc)...)
	if tc.startErr != "" {
		assertPermanent(t, err, tc.startErr)
		return
	}
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_, _ = prompt(t, s)
	log := strings.Join(echoOf(t, drain(s)).Log, "\n")
	for _, want := range tc.calls {
		if !strings.Contains(log, want) {
			t.Errorf("requests lack %s:\n%s", want, log)
		}
	}
	for _, not := range tc.notCalls {
		if strings.Contains(log, not) {
			t.Errorf("requests contain %s:\n%s", not, log)
		}
	}
}

func assertPermanent(t *testing.T, err error, text string) {
	t.Helper()
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.IsTransient || !strings.Contains(err.Error(), text) {
		t.Fatalf("err = %v, want a permanent ProviderError containing %q", err, text)
	}
}

func replayClaude(t *testing.T, fixture string, role Role) fixtureRun {
	t.Helper()
	return replay(t, fixtureCase{file: fixture, profile: claude, role: role})
}
