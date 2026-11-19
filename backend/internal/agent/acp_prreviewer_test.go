package agent

import (
	"testing"

	"legos-factory/backend/internal/model"
)

func TestACPPRReviewerIsReadOnlyOnEveryAgent(t *testing.T) {
	ws := t.TempDir()
	cases := []struct {
		kind, path, tool, want string
	}{
		{"execute", ws, "", "no"},
		{"execute", ws, "Bash", "no"},
		{"edit", ws + "/a.go", "", "no"},
		{"edit", ws + "/a.go", "Write", "no"},
		{"delete", ws + "/a.go", "", "no"},
		{"read", ws + "/a.go", "Read", "yes"},
		{"search", ws, "Grep", "yes"},
		{"read", "/etc/passwd", "Read", "no"},
		{"other", ws, "mcp__jira__create_issue", "no"},
	}
	for _, agent := range []string{"codex", "claude"} {
		for _, c := range cases {
			var h *ACPHarness
			env := []string{"ACP_FAKE_KIND=" + c.kind, "ACP_FAKE_PATH=" + c.path, "ACP_FAKE_TOOLNAME=" + c.tool}
			if agent == "codex" {
				h = fakeCodex(t, "permission", env...)
			} else {
				h = fakeHarness(t, "permission", env...)
				h.Provider = "claude"
			}
			res, err := runFake(t, h, model.AgentRequest{Role: model.AgentPRReviewer, WorkspaceDir: ws, Prompt: "go", Env: []string{"CODEX_HOME=" + t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			if res.Text != c.want {
				t.Fatalf("%s %s %s: answered %q, want %q", agent, c.kind, c.tool, res.Text, c.want)
			}
		}
	}
}

func TestACPCodexPRReviewerRunsReadOnly(t *testing.T) {
	res, err := runFake(t, fakeCodex(t, "setcalls"), model.AgentRequest{Role: model.AgentPRReviewer, Prompt: "go", Env: []string{"CODEX_HOME=" + t.TempDir()}})
	if err != nil || res.Text != "mode=read-only" {
		t.Fatalf("%q, %v", res.Text, err)
	}
}
