package harness

import (
	"strings"
	"testing"
)

func TestPermissionReplyByAgentAndRole(t *testing.T) {
	cases := []struct {
		name    string
		profile Profile
		role    Role
		policy  ToolPolicy
		kind    string
		opts    string
		want    string // option id chosen; "" means the prompt was cancelled
	}{
		{"codex inspector rejects execute", Profile{Agent: "codex-acp"}, RoleInspector, ToolPolicy{}, "execute", "", "no"},
		{"codex inspector rejects even a read", Profile{Agent: "codex-acp"}, RoleInspector, ToolPolicy{}, "read", "", "no"},
		{"codex inspector rejects an unlabelled request", Profile{Agent: "codex-acp"}, RoleArchitect, ToolPolicy{}, "", "", "no"},
		{"codex inspector cancels when only allow is offered", Profile{Agent: "codex-acp"}, RoleInspector, ToolPolicy{}, "read", "allow_only", ""},
		{"codex builder is allowed", Profile{Agent: "codex-acp"}, RoleBuilder, ToolPolicy{}, "edit", "", "yes"},
		{"opencode foreman rejects a read", Profile{Agent: "opencode"}, RoleForeman, ToolPolicy{AllowNone: true}, "read", "", "no"},
		{"opencode foreman rejects an edit", Profile{Agent: "opencode"}, RoleForeman, ToolPolicy{AllowNone: true}, "edit", "", "no"},
		{"opencode foreman cancels when only allow is offered", Profile{Agent: "opencode"}, RoleForeman, ToolPolicy{AllowNone: true}, "read", "allow_only", ""},
		{"opencode inspector is still allowed so the turn survives", Profile{Agent: "opencode"}, RoleInspector, ToolPolicy{}, "read", "", "yes"},
		{"opencode builder is allowed", Profile{Agent: "opencode"}, RoleBuilder, ToolPolicy{}, "edit", "", "yes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ws := t.TempDir()
			s, err := startFake(t, "permission", SessionSpec{Role: c.role, Profile: c.profile, ToolPolicy: c.policy, Dir: ws},
				"ACP_FAKE_KIND="+c.kind, "ACP_FAKE_PATH="+ws+"/a.go", "ACP_FAKE_OPTS="+c.opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := prompt(t, s); err != nil {
				t.Fatal(err)
			}
			var got string
			for _, e := range drain(s) {
				if e.Kind == EventStep {
					got = e.Text
				}
			}
			if got != c.want {
				t.Errorf("answered %q, want %q", got, c.want)
			}
		})
	}
}

func TestExitErrRedactsEnvAndConfigKeys(t *testing.T) {
	cfg := `{"provider":{"acme":{"options":{"apiKey":"k-123","baseURL":"https://x"}}}}`
	for name, leak := range map[string]string{"long env value": "sk-longsecretvalue", "apiKey inside the config": "k-123"} {
		t.Run(name, func(t *testing.T) {
			s, err := startFake(t, "crash", SessionSpec{Role: RoleBuilder, Profile: Profile{Agent: "opencode"}},
				"OPENCODE_CONFIG_CONTENT="+cfg, "ACP_FAKE_LEAK="+leak)
			if err != nil {
				t.Fatal(err)
			}
			_, err = prompt(t, s)
			if err == nil || !strings.Contains(err.Error(), "provider exploded [redacted]") || strings.Contains(err.Error(), leak) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestExitErrKeepsShortEnvValues(t *testing.T) {
	s, err := startFake(t, "crash", SessionSpec{Role: RoleBuilder}, "ACP_FAKE_LEAK=abc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = prompt(t, s); err == nil || !strings.Contains(err.Error(), "abc") {
		t.Errorf("short value was redacted: %v", err)
	}
}

func TestCodexAuthenticateWithEitherAPIKey(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		sent bool
	}{
		{map[string]string{"OPENAI_API_KEY": "dummy"}, true},
		{map[string]string{"CODEX_API_KEY": "dummy"}, true},
		{nil, false},
	} {
		c := fixtureCase{file: "codex-acp-new.jsonl", profile: codex, role: RoleBuilder, startsAt: "session", env: tc.env}
		s, err := startFake(t, "echo", spec(c), fixtureEnv(t, c)...)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = prompt(t, s)
		log := strings.Join(echoOf(t, drain(s)).Log, "\n")
		if got := strings.Contains(log, `"methodId":"api-key"`); got != tc.sent {
			t.Errorf("env %v: authenticate sent = %v, want %v", tc.env, got, tc.sent)
		}
	}
}

func TestToolKindCarriedOnCallAndUpdate(t *testing.T) {
	s, err := startFake(t, "toolkind", SessionSpec{Role: RoleBuilder})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prompt(t, s); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range drain(s) {
		if e.Kind == EventToolCall || e.Kind == EventToolUpdate {
			kinds = append(kinds, string(e.Kind)+":"+e.ToolKind)
		}
	}
	if strings.Join(kinds, ",") != "tool_call:read,tool_update:read" {
		t.Errorf("kinds = %v", kinds)
	}
}
