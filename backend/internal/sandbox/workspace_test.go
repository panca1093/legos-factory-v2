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

func TestRemoteWithoutAuthIsRejected(t *testing.T) {
	data := t.TempDir()
	_, err := Local{DataDir: data}.Prepare(context.Background(), "wo_2", "https://example.invalid/acme/greeter.git", nil)
	if err == nil || !strings.Contains(err.Error(), "needs a git connection") || !strings.Contains(err.Error(), "https://example.invalid/acme/greeter.git") {
		t.Fatalf("expected an actionable git-connection error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(data, "workspaces", "wo_2")); statErr == nil {
		t.Fatal("no empty workspace must be left behind")
	}
}

func TestEmptyRepoURLGivesEmptyWorkspace(t *testing.T) {
	dir, err := Local{DataDir: t.TempDir()}.Prepare(context.Background(), "wo_3", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() || isLocalGitRepo(dir) {
		t.Fatalf("expected an empty non-git directory, got %v %v", info, err)
	}
}

func TestChangedFilesSinceClone(t *testing.T) {
	origin := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = origin
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s", out)
		}
	}
	l := Local{DataDir: t.TempDir()}
	dir, err := l.Prepare(context.Background(), "wo_c", origin, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "api"), 0o755)
	os.WriteFile(filepath.Join(dir, "api", "auth.go"), []byte("package api\n"), 0o644)
	if _, err := l.Commit(context.Background(), dir, "c1"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "view.vue"), []byte("<template/>\n"), 0o644)
	files, err := l.ChangedFiles(context.Background(), dir)
	if err != nil || strings.Join(files, ",") != "api/auth.go,view.vue" {
		t.Fatalf("changed files: %v %v", files, err)
	}
}
