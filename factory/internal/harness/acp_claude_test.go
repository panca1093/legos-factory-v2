package harness

import (
	"errors"
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
		{"Invalid params: model not found: opencode/nope", "", false},
		{"Internal error: quota", "provider.free-limit", false},
		{"Internal error: x", "usageLimitExceeded", false},
		{"Internal error: x", "sessionBudgetExceeded", false},
		{"Internal error: x", "unauthorized", false},
		{"Internal error: x", "rateLimitExceeded", true},
		{"Internal error: x", "serverOverloaded", true},
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
	if len(e.Claude) != 3 || !strings.HasSuffix(e.Claude[0], "/home/inspector-session") || e.Claude[1] != "1" || e.Claude[2] != "sonnet" {
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
