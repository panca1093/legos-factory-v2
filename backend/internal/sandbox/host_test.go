package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
