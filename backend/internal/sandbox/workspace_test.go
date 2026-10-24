package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

func TestGitCommitRecordsWorkspaceChange(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
	run("init", "-q")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "base")
	base, err := Local{}.Commit(context.Background(), dir, "noop")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte("package f\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := Local{}.Commit(context.Background(), dir, "wo_1 rev 0")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(sha) || sha == base {
		t.Fatalf("expected a new 40-hex commit, base=%s got=%s", base, sha)
	}
	if again, _ := (Local{}).Commit(context.Background(), dir, "clean"); again != sha {
		t.Fatalf("clean tree should return HEAD %s, got %s", sha, again)
	}
}

func TestGitCommitNonRepoIsUnversioned(t *testing.T) {
	sha, err := Local{}.Commit(context.Background(), t.TempDir(), "x")
	if err != nil || sha != "" {
		t.Fatalf("non-git workspace: sha=%q err=%v", sha, err)
	}
}
