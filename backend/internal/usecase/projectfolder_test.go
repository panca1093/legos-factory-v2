package usecase

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectFolderIsTheFolderAtItsHead(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "one")
	if err := os.WriteFile(filepath.Join(dir, "dirty.go"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, sha, err := ProjectFolder(context.Background(), dir)
	if err != nil || got != dir || sha != run("rev-parse", "HEAD") {
		t.Fatalf("got %q %q %v", got, sha, err)
	}
}

func TestProjectFolderRejectsUnusableFolders(t *testing.T) {
	plain := t.TempDir()
	empty := t.TempDir()
	if out, err := exec.Command("git", "-C", empty, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	file := filepath.Join(plain, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for url, want := range map[string]string{
		"":                           "not an absolute path",
		"relative/dir":               "not an absolute path",
		"https://github.com/a/b.git": "not an absolute path",
		filepath.Join(plain, "gone"): "does not exist",
		file:                         "does not exist or is not a directory",
		plain:                        "not a git repository",
		empty:                        "no commits",
	} {
		if _, _, err := ProjectFolder(context.Background(), url); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: err %v, want %q", url, err, want)
		}
	}
}

func TestChatRepoPreambleSaysChangesNeedApproval(t *testing.T) {
	if !strings.Contains(chatRepoPreamble, "waits for the user's approval") {
		t.Fatal(chatRepoPreamble)
	}
}
