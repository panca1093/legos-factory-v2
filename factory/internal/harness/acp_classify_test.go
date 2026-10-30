package harness

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name      string
		msg       string
		tags      errorTags
		transient bool
	}{
		{"codex usage limit", "Internal error", errorTags{Codex: "usageLimitExceeded"}, false},
		{"codex overloaded", "Internal error", errorTags{Codex: "serverOverloaded"}, true},
		{"codex connection failed", "Internal error: bad request body echoed", errorTags{Codex: "httpConnectionFailed"}, true},
		{"codex context window", "Internal error", errorTags{Codex: "contextWindowExceeded"}, false},
		{"opencode free limit", "Free usage limit reached", errorTags{Opencode: "provider.free-limit"}, true},
		{"claude usage limit with 429 text", "429 too many requests", errorTags{Claude: "usage-limit"}, false},
		{"claude budget", "spent", errorTags{Claude: "budget"}, false},
		{"claude rate limit", "slow down", errorTags{Claude: "rate-limit"}, true},
		{"structured kind beats permanent text", "Authentication required", errorTags{Codex: "serverOverloaded"}, true},
		{"unknown kind falls back to text", "usage limit hit", errorTags{Claude: "weird"}, false},
		{"text 429 is transient", "429 Too Many Requests", errorTags{}, true},
		{"text overloaded is transient", "503 Overloaded: quota of capacity", errorTags{}, true},
		{"429 beats usage limit text", "429 usage limit window", errorTags{}, true},
		{"text model not found", "Invalid params: model not found: opencode/nope", errorTags{}, false},
		{"text auth", "Authentication required", errorTags{}, false},
		{"text funds", "Insufficient account funds", errorTags{}, false},
		{"bare context word is transient", "failed to read context from stream", errorTags{}, true},
		{"context window text", "exceeds the context window", errorTags{}, false},
		{"context length text", "context length exceeded", errorTags{}, false},
		{"maximum context text", "maximum context reached", errorTags{}, false},
		{"plain network error", "connection reset", errorTags{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyError(c.msg, c.tags); got.IsTransient != c.transient {
				t.Errorf("transient = %v, want %v", got.IsTransient, c.transient)
			}
		})
	}
}

func TestCodexInfoName(t *testing.T) {
	for in, want := range map[string]string{`"usageLimitExceeded"`: "usageLimitExceeded", `{"httpConnectionFailed":{"httpStatusCode":502}}`: "httpConnectionFailed", ``: "", `null`: ""} {
		if got := codexInfoName(json.RawMessage(in)); got != want {
			t.Errorf("codexInfoName(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestReadOnlyOpencodeDeniesFixtureTools(t *testing.T) {
	cfg := opencodeConfig(RoleInspector, ToolPolicy{})
	for _, tool := range []string{"task", "subagent", "webfetch", "websearch", "bash", "edit", "write"} {
		if !strings.Contains(cfg, `"`+tool+`":"deny"`) {
			t.Errorf("read-only config lacks deny for %s: %s", tool, cfg)
		}
	}
	// the capture shows a read-only agent reaching for the subagent tool; every non-read tool it announced must be denied
	b, err := os.ReadFile("testdata/opencode-deny.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var announced []string
	for _, line := range strings.Split(string(b), "\n") {
		var m struct {
			Msg struct {
				Params struct {
					Update struct {
						SessionUpdate string `json:"sessionUpdate"`
						Name          string `json:"name"`
						Kind          string `json:"kind"`
					} `json:"update"`
				} `json:"params"`
			} `json:"msg"`
		}
		if json.Unmarshal([]byte(line), &m) == nil {
			if u := m.Msg.Params.Update; u.SessionUpdate == "tool_call" && u.Kind != "read" && u.Name != "" {
				announced = append(announced, u.Name)
			}
		}
	}
	if len(announced) == 0 {
		t.Fatal("no non-read tool_call found in opencode-deny.jsonl")
	}
	for _, name := range announced {
		if !slices.Contains(readOnlyDeny, name) {
			t.Errorf("fixture tool %q is not in readOnlyDeny", name)
		}
	}
}
