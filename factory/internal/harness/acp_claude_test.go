package harness

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderErrorExposesTransient(t *testing.T) {
	var err error = &ProviderError{IsTransient: true}
	var tr interface{ Transient() bool }
	if !errors.As(err, &tr) || !tr.Transient() {
		t.Fatal("ProviderError must satisfy Transient() bool")
	}
}

func TestClassifyClaudeErrors(t *testing.T) {
	cases := []struct {
		msg, kind string
		transient bool
	}{
		{"Authentication required", "", false},
		{"Request failed", "usage-limit", false},
		{"Request failed", "budget_exceeded", false},
		{"Request failed", "context", false},
		{"API Error: 529 overloaded", "", true},
	}
	for _, c := range cases {
		if got := classifyError(c.msg, c.kind); got.IsTransient != c.transient {
			t.Errorf("%q/%q transient = %v, want %v", c.msg, c.kind, got.IsTransient, c.transient)
		}
	}
}

func TestClaudeSessionSetup(t *testing.T) {
	wantMeta := `"_meta":{"claudeCode":{"options":{"model":"sonnet","settings":{"permissions":{"deny":["Edit","Write","MultiEdit","NotebookEdit","Bash","WebFetch"]}}}}}`
	s, err := startFake(t, "echo", SessionSpec{
		Role: RoleInspector, ToolPolicy: ToolPolicy{Deny: []string{"WebFetch"}},
		Profile: Profile{Agent: "claude-agent-acp", Model: "sonnet"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = prompt(t, s)
	e := echoOf(t, drain(s))
	log := strings.Join(e.Log, "\n")
	if !strings.Contains(log, wantMeta) || strings.Contains(log, "set_config_option") {
		t.Errorf("log = %s", log)
	}
	if len(e.Claude) != 3 || !strings.HasSuffix(e.Claude[0], "/home/.claude-factory") || e.Claude[1] != "1" || e.Claude[2] != "sonnet" {
		t.Errorf("env = %v", e.Claude)
	}
	if e.Env != "" {
		t.Errorf("opencode config leaked into claude env: %s", e.Env)
	}
}

func TestClaudeBuilderBypassAndEnvOverride(t *testing.T) {
	s, err := startFake(t, "echo", SessionSpec{Role: RoleBuilder, Profile: Profile{Agent: "claude-agent-acp", Model: "sonnet"}},
		"CLAUDE_CONFIG_DIR=/custom", "ANTHROPIC_DEFAULT_HAIKU_MODEL=haiku")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = prompt(t, s)
	e := echoOf(t, drain(s))
	log := strings.Join(e.Log, "\n")
	if !strings.Contains(log, `"configId":"mode"`) || strings.Contains(log, "settings") {
		t.Errorf("log = %s", log)
	}
	if e.Claude[0] != "/custom" || e.Claude[2] != "haiku" {
		t.Errorf("env = %v", e.Claude)
	}
}

func TestClaudeLoadSetsModelOption(t *testing.T) {
	s, err := startFake(t, "echo", SessionSpec{Role: RoleInspector, SessionID: "ses_old", Profile: Profile{Agent: "claude-agent-acp", Model: "opencode-go/kimi"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = prompt(t, s)
	if log := strings.Join(echoOf(t, drain(s)).Log, "\n"); !strings.Contains(log, `session/load`) || !strings.Contains(log, `"configId":"model"`) {
		t.Errorf("log = %s", log)
	}
}

type fixtureRun struct {
	events  []Event
	stop    string
	replies []string
}

func replayClaude(t *testing.T, fixture string, role Role) fixtureRun {
	t.Helper()
	abs, err := filepath.Abs("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	replyLog := filepath.Join(t.TempDir(), "replies")
	s, err := startFake(t, "replay", SessionSpec{Role: role, Profile: Profile{Agent: "claude-agent-acp"}}, "ACP_FAKE_FIXTURE="+abs, "ACP_FAKE_REPLYLOG="+replyLog)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := prompt(t, s)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(replyLog)
	return fixtureRun{events: drain(s), stop: stop, replies: strings.Split(strings.TrimSpace(string(b)), "\n")}
}

func (r fixtureRun) of(kind EventKind) (out []Event) {
	for _, e := range r.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func (r fixtureRun) replyCount() int {
	if len(r.replies) == 1 && r.replies[0] == "" {
		return 0
	}
	return len(r.replies)
}

func TestClaudeFixtureDeny(t *testing.T) {
	r := replayClaude(t, "claude-agent-acp-deny.jsonl", RoleInspector)
	if r.stop != "end_turn" || len(r.of(EventStop)) != 1 || len(r.of(EventError)) != 0 || r.replyCount() != 0 {
		t.Fatalf("stop=%q events=%+v replies=%v", r.stop, r.events, r.replies)
	}
	for _, e := range r.of(EventToolCall) {
		if e.Tool == "" || e.ToolCallID == "" {
			t.Errorf("tool call without name: %+v", e)
		}
	}
	if len(r.of(EventUsage)) == 0 {
		t.Error("no usage event")
	}
}

func TestClaudeFixturePermissionReject(t *testing.T) {
	r := replayClaude(t, "claude-agent-acp-permission-reject.jsonl", RoleInspector)
	if r.replyCount() != 1 || !strings.Contains(r.replies[0], `"optionId":"reject"`) {
		t.Fatalf("replies = %v", r.replies)
	}
	if calls := r.of(EventToolCall); r.stop != "end_turn" || len(calls) != 1 || calls[0].Tool != "Write" {
		t.Fatalf("stop=%q calls=%+v", r.stop, calls)
	}
	ups := r.of(EventToolUpdate)
	if last := ups[len(ups)-1]; last.Status != "failed" || last.Tool != "Write" {
		t.Errorf("last update = %+v", last)
	}
	if errs := r.of(EventError); len(errs) == 0 || !strings.Contains(errs[0].Err.Error(), "read-only role") {
		t.Errorf("errors = %+v", errs)
	}
	if steps := r.of(EventStep); len(steps) != 1 || steps[0].Text == "" {
		t.Errorf("message chunks should coalesce into one step: %+v", steps)
	}
}

func TestClaudeFixtureBypassWrite(t *testing.T) {
	r := replayClaude(t, "claude-agent-acp-bypass-write.jsonl", RoleBuilder)
	if r.stop != "end_turn" || r.replyCount() != 0 || len(r.of(EventError)) != 0 {
		t.Fatalf("stop=%q replies=%v errors=%+v", r.stop, r.replies, r.of(EventError))
	}
	ups := r.of(EventToolUpdate)
	if len(ups) == 0 || ups[len(ups)-1].Status != "completed" || ups[len(ups)-1].Tool != "Write" {
		t.Errorf("updates = %+v", ups)
	}
}
