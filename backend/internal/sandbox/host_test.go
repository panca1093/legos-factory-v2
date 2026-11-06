package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

// ghRig stands in for GitHub with a bare repo, and for the user's folder with
// a clone that is behind it and has a different branch checked out.
func ghRig(t *testing.T) (folder, remote string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	mustGit(t, root, "init", "-q", "--bare", remote)
	seed := filepath.Join(root, "seed")
	mustGit(t, root, "clone", "-q", remote, seed)
	mustGit(t, seed, "checkout", "-q", "-b", "main")
	mustGit(t, seed, "commit", "-q", "--allow-empty", "-m", "one")
	mustGit(t, seed, "push", "-q", "origin", "main")
	folder = filepath.Join(root, "folder")
	mustGit(t, root, "clone", "-q", remote, folder)
	mustGit(t, folder, "checkout", "-q", "-b", "wip")
	mustGit(t, seed, "commit", "-q", "--allow-empty", "-m", "two")
	mustGit(t, seed, "push", "-q", "origin", "main")
	return folder, remote
}

func TestPrepareGHModeStartsFromLatestRemoteDefault(t *testing.T) {
	folder, remote := ghRig(t)
	data := t.TempDir()
	auth := &model.GitAuth{RemoteURL: "file://" + remote, DefaultBranch: "main"}
	ws, err := Prepare(context.Background(), data, "wo_1", folder, auth)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, ws.Dir, "log", "-1", "--format=%s"); got != "two" {
		t.Fatalf("workspace starts at %q, want the latest remote commit", got)
	}
	if got := mustGit(t, ws.Dir, "remote", "get-url", "origin"); got != "file://"+remote {
		t.Fatalf("origin = %q, want the remote", got)
	}
	if got := mustGit(t, folder, "rev-parse", "--abbrev-ref", "HEAD"); got != "wip" {
		t.Fatalf("user's folder was modified: HEAD on %q", got)
	}

	mustGit(t, ws.Dir, "commit", "-q", "--allow-empty", "-m", "work")
	base, err := Local{}.PushBranch(context.Background(), ws.Dir, "legos-factory/wo_1", auth)
	if err != nil || base != "main" {
		t.Fatalf("base=%q err=%v", base, err)
	}
	if got := mustGit(t, remote, "log", "-1", "--format=%s", "legos-factory/wo_1"); got != "work" {
		t.Fatalf("remote branch tip = %q", got)
	}
}

func TestPrepareGHModeFallsBackToFolderWhenFetchFails(t *testing.T) {
	folder, _ := ghRig(t)
	auth := &model.GitAuth{RemoteURL: "file://" + filepath.Join(t.TempDir(), "nowhere.git"), DefaultBranch: "main"}
	ws, err := Prepare(context.Background(), t.TempDir(), "wo_2", folder, auth)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, ws.Dir, "log", "-1", "--format=%s"); got != "one" {
		t.Fatalf("fallback should use the folder's copy of main, got %q", got)
	}
}

func TestPrepareGHModeFailsWithoutAnyCopyOfTheDefaultBranch(t *testing.T) {
	folder, remote := ghRig(t)
	data := t.TempDir()
	_, err := Prepare(context.Background(), data, "wo_4", folder, &model.GitAuth{RemoteURL: "file://" + remote, DefaultBranch: "nope"})
	if err == nil {
		t.Fatal("must not silently build on the folder's checked-out branch")
	}
	if _, statErr := os.Stat(filepath.Join(data, "workspaces", "wo_4")); !os.IsNotExist(statErr) {
		t.Fatal("failed preparation must not leave a workspace behind")
	}
}

func TestPushBranchGHModeNeverTargetsTheFolder(t *testing.T) {
	folder, remote := ghRig(t)
	ws, err := Prepare(context.Background(), t.TempDir(), "wo_5", folder, nil) // never retargeted: origin is the folder
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, ws.Dir, "commit", "-q", "--allow-empty", "-m", "work")

	for _, bad := range []*model.GitAuth{
		{RemoteURL: folder, DefaultBranch: "main"},
		{RemoteURL: "file://" + remote},
	} {
		if _, err := (Local{}).PushBranch(context.Background(), ws.Dir, "legos-factory/wo_5", bad); err == nil {
			t.Fatalf("push with %+v must be refused", bad)
		}
	}
	good := &model.GitAuth{RemoteURL: "file://" + remote, DefaultBranch: "main"}
	if base, err := (Local{}).PushBranch(context.Background(), ws.Dir, "legos-factory/wo_5", good); err != nil || base != "main" {
		t.Fatalf("base=%q err=%v", base, err)
	}
	if mustGit(t, remote, "log", "-1", "--format=%s", "legos-factory/wo_5") != "work" {
		t.Fatal("remote did not receive the branch")
	}
	if out := mustGit(t, folder, "branch", "--list", "legos-factory/*"); out != "" {
		t.Fatalf("the user's folder received a branch: %q", out)
	}
}

func TestIsRemoteURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://github.com/a/b.git": true, "ssh://git@github.com/a/b.git": true, "git@github.com:a/b.git": true,
		"file:///tmp/x.git": true, "/Users/me/code": false, "../x": false, "": false, "/a@b:c": false,
	} {
		if isRemoteURL(u) != want {
			t.Errorf("isRemoteURL(%q) = %v, want %v", u, !want, want)
		}
	}
}

func TestPrepareWithoutRemoteURLKeepsLocalOrigin(t *testing.T) {
	folder, _ := ghRig(t)
	ws, err := Prepare(context.Background(), t.TempDir(), "wo_3", folder, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, ws.Dir, "remote", "get-url", "origin"); got != folder {
		t.Fatalf("origin = %q, want the folder", got)
	}
}

func TestHostInitAndInspect(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "fresh")
	h := Host{}
	if err := h.Init(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if !h.IsGitRepo(dir) || h.OriginURL(ctx, dir) != "" || h.DefaultBranch(ctx, dir) == "" {
		t.Fatalf("repo=%v origin=%q branch=%q", h.IsGitRepo(dir), h.OriginURL(ctx, dir), h.DefaultBranch(ctx, dir))
	}
	if err := h.Init(ctx, dir); err == nil {
		t.Fatal("Init over an existing folder must fail")
	}
	mustGit(t, dir, "remote", "add", "origin", "git@github.com:acme/greeter.git")
	if got := h.OriginURL(ctx, dir); got != "git@github.com:acme/greeter.git" {
		t.Fatalf("origin=%q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
}
