package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
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

func TestPushBranchPublishesHeadAndReportsBase(t *testing.T) {
	origin := t.TempDir()
	run := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", args, out)
		}
		return string(out)
	}
	run(origin, "init", "-q", "-b", "main")
	run(origin, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "base")

	l := Local{DataDir: t.TempDir()}
	dir, err := l.Prepare(context.Background(), "wo_1", origin, &model.GitAuth{Username: "u", Password: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte("package f\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := l.Commit(context.Background(), dir, "wo_1 rev 0")
	if err != nil {
		t.Fatal(err)
	}
	base, err := l.PushBranch(context.Background(), dir, "legos-factory/wo_1", &model.GitAuth{Username: "u", Password: "s3cret"})
	if err != nil || base != "main" {
		t.Fatalf("push: base=%q err=%v", base, err)
	}
	if got := strings.TrimSpace(run(origin, "rev-parse", "refs/heads/legos-factory/wo_1")); got != sha {
		t.Fatalf("origin branch %s != workspace commit %s", got, sha)
	}
	if strings.TrimSpace(run(origin, "rev-parse", "main")) == sha {
		t.Fatal("push must not touch the default branch")
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if strings.Contains(string(cfg), "s3cret") || strings.Contains(string(cfg), "extraHeader") {
		t.Fatal("credentials must not be persisted in the workspace git config")
	}
	if _, err := l.PushBranch(context.Background(), dir, "legos-factory/wo_1", nil); err != nil {
		t.Fatalf("re-push of the same commit must be a no-op: %v", err)
	}
}

func TestRemoteWithoutAuthIsNotCloned(t *testing.T) {
	dir, err := Local{DataDir: t.TempDir()}.Prepare(context.Background(), "wo_2", "https://example.invalid/acme/greeter.git", nil)
	if err != nil {
		t.Fatal(err)
	}
	if isLocalGitRepo(dir) {
		t.Fatal("remote repos must only be cloned with a bound connection")
	}
}
