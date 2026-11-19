package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func fakeCodex(t *testing.T, scenario string, extraEnv ...string) *ACPHarness {
	t.Helper()
	h := fakeHarness(t, scenario, append([]string{"ACP_FAKE_CODEX=1"}, extraEnv...)...)
	h.Provider = "codex"
	return h
}

func TestACPCodexRefusesEveryRequestOfReadOnlyRoles(t *testing.T) {
	ws := t.TempDir()
	cases := []struct {
		role       model.AgentRole
		kind, path string
		want       string
	}{
		{model.AgentBuilder, "execute", ws, "yes"},
		{model.AgentBuilder, "edit", ws + "/a.go", "yes"},
		{model.AgentInspector, "execute", ws, "yes"},
		{model.AgentInspector, "edit", "/etc/x", "no"},
		{model.AgentBuilder, "edit", "/etc/x", "no"},
		{model.AgentCataloger, "read", ws + "/a.go", "no"},
		{model.AgentForeman, "read", ws + "/a.go", "no"},
		{model.AgentArchitect, "execute", ws, "no"},
		{model.AgentChat, "read", ws + "/a.go", "no"},
		{model.AgentChat, "execute", ws, "no"},
	}
	for _, c := range cases {
		res, err := runFake(t, fakeCodex(t, "permission", "ACP_FAKE_KIND="+c.kind, "ACP_FAKE_PATH="+c.path),
			model.AgentRequest{Role: c.role, WorkspaceDir: ws, Prompt: "go", Env: []string{"CODEX_HOME=" + t.TempDir()}})
		if err != nil {
			t.Fatal(err)
		}
		if res.Text != c.want {
			t.Fatalf("%s %s: answered %q, want %q", c.role, c.kind, res.Text, c.want)
		}
	}
}

func TestACPCodexRepoChatWritesWaitForApproval(t *testing.T) {
	ws := t.TempDir()
	for _, kind := range []string{"edit", "delete", "move", "execute"} {
		for _, approve := range []bool{false, true} {
			var asked int
			res, err := runFake(t, fakeCodex(t, "permission", "ACP_FAKE_KIND="+kind, "ACP_FAKE_PATH="+ws+"/a.go"),
				model.AgentRequest{Role: model.AgentChat, WorkspaceDir: ws, RepoAccess: true, Prompt: "go", Env: []string{"CODEX_HOME=" + t.TempDir()},
					OnPermission: func(context.Context, model.PermissionRequest) bool { asked++; return approve }})
			if err != nil {
				t.Fatal(err)
			}
			want := map[bool]string{true: "yes", false: "no"}[approve]
			if res.Text != want || asked != 1 {
				t.Fatalf("codex repo chat %s approve=%v: answered %q after %d asks", kind, approve, res.Text, asked)
			}
		}
	}
	res, err := runFake(t, fakeCodex(t, "permission", "ACP_FAKE_KIND=edit", "ACP_FAKE_PATH="+ws+"/a.go"),
		model.AgentRequest{Role: model.AgentChat, WorkspaceDir: ws, RepoAccess: true, Prompt: "go", Env: []string{"CODEX_HOME=" + t.TempDir()}})
	if err != nil || res.Text != "no" {
		t.Fatalf("with no one to ask a write is denied: %q %v", res.Text, err)
	}
}

func TestACPCodexRefusedTurnNamesWhy(t *testing.T) {
	ws := t.TempDir()
	for role, want := range map[model.AgentRole]string{
		model.AgentInspector: "fell outside the workspace",
		model.AgentChat:      "read-only on codex",
	} {
		_, err := runFake(t, fakeCodex(t, "permission-cancel", "ACP_FAKE_KIND=edit", "ACP_FAKE_PATH=/etc/x"),
			model.AgentRequest{Role: role, WorkspaceDir: ws, Prompt: "go", Env: []string{"CODEX_HOME=" + t.TempDir()}})
		if !errors.Is(err, model.ErrPermanent) || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v, want permanent error containing %q", role, err, want)
		}
	}
}

func TestACPCodexModeFollowsRole(t *testing.T) {
	for role, want := range map[model.AgentRole]string{
		model.AgentBuilder: "mode=workspace-write", model.AgentInspector: "mode=workspace-write", model.AgentForeman: "mode=read-only", model.AgentArchitect: "mode=read-only", model.AgentChat: "mode=read-only", model.AgentCataloger: "mode=read-only",
	} {
		res, err := runFake(t, fakeCodex(t, "setcalls"), model.AgentRequest{Role: role, Prompt: "go", Env: []string{"CODEX_HOME=" + t.TempDir()}})
		if err != nil || res.Text != want {
			t.Fatalf("%s: %q, %v; want %q", role, res.Text, err, want)
		}
	}
}

func TestACPAuthenticationRequiredIsPermanentAndNamesTheAgent(t *testing.T) {
	for scenario, h := range map[string]*ACPHarness{"auth": fakeHarness(t, "auth"), "auth-new": fakeCodex(t, "auth-new")} {
		h.Provider = map[string]string{"auth": "claude", "auth-new": "codex"}[scenario]
		_, err := runFake(t, h, model.AgentRequest{Role: model.AgentBuilder, Prompt: "go", Env: []string{"CODEX_HOME=" + t.TempDir()}})
		name := map[string]string{"auth": "Claude", "auth-new": "Codex"}[scenario]
		if err == nil || !errors.Is(err, model.ErrPermanent) || !strings.Contains(err.Error(), name+" account is not signed in") {
			t.Fatalf("%s: %v", scenario, err)
		}
	}
}

func TestACPCodexRunGetsPrivateHomeWithoutMCPServers(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "auth.json"), []byte(`{"token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "config.toml"), []byte("[mcp_servers.leak]\ncommand=\"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := runFake(t, fakeCodex(t, "codexhome"), model.AgentRequest{Role: model.AgentBuilder, Prompt: "go", Env: []string{"CODEX_HOME=" + src}})
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Home, Config, Auth string }
	if err := json.Unmarshal([]byte(res.Text), &got); err != nil {
		t.Fatal(err)
	}
	if got.Home == src || got.Auth != `{"token":"t"}` || strings.Contains(got.Config, "mcp_servers") && !strings.HasPrefix(got.Config, "#") {
		t.Fatalf("private home: %+v", got)
	}
	if strings.Contains(strings.ReplaceAll(got.Config, "no MCP servers", ""), "[mcp_servers") {
		t.Fatalf("config leaks the user's MCP servers: %q", got.Config)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(got.Home); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("private home was not removed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestACPCodexRotatedLoginIsSavedBack(t *testing.T) {
	src := t.TempDir()
	_ = os.WriteFile(filepath.Join(src, "auth.json"), []byte(`old`), 0o600)
	if _, err := runFake(t, fakeCodex(t, "codexhome", "ACP_FAKE_ROTATE=1"), model.AgentRequest{Role: model.AgentBuilder, Prompt: "go", Env: []string{"CODEX_HOME=" + src}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(src, "auth.json"))
		if string(b) == "rotated" {
			if fi, _ := os.Stat(filepath.Join(src, "auth.json")); fi.Mode().Perm() != 0o600 {
				t.Fatalf("mode %v", fi.Mode())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("auth.json = %q", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestACPHarnessNames(t *testing.T) {
	if got := NewACPHarness([]string{"codex-acp"}, t.TempDir()); got.Provider != "codex" || got.Name() != "codex-acp" {
		t.Fatalf("%s %s", got.Provider, got.Name())
	}
}
