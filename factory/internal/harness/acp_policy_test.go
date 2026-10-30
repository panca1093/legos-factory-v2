package harness

import (
	"strings"
	"testing"
)

func TestAllowNonePassesEmptyToolListAndDeniesAll(t *testing.T) {
	s, err := startFake(t, "echo", SessionSpec{
		Role: RoleForeman, ToolPolicy: ToolPolicy{Deny: []string{"Read"}, AllowNone: true},
		Profile: Profile{Agent: "claude-agent-acp", Model: "sonnet"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = prompt(t, s)
	log := strings.Join(echoOf(t, drain(s)).Log, "\n")
	if !strings.Contains(log, `"tools":[]`) || !strings.Contains(log, `"model":"sonnet"`) {
		t.Errorf("claude session/new lacks an explicit empty tool list: %s", log)
	}

	o, err := startFake(t, "echo", SessionSpec{
		Role: RoleForeman, ToolPolicy: ToolPolicy{AllowNone: true},
		Profile: Profile{Agent: "opencode", Provider: "opencode-go", Model: "kimi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = prompt(t, o)
	if env := echoOf(t, drain(o)).Env; !strings.Contains(env, `"*":"deny"`) {
		t.Errorf("opencode config = %s", env)
	}
}

func TestToolListAbsentByDefault(t *testing.T) {
	s, err := startFake(t, "echo", SessionSpec{Role: RoleInspector, Profile: Profile{Agent: "claude-agent-acp", Model: "sonnet"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = prompt(t, s)
	if log := strings.Join(echoOf(t, drain(s)).Log, "\n"); strings.Contains(log, `"tools"`) {
		t.Errorf("tools restricted without AllowNone: %s", log)
	}
}
