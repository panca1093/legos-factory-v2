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

func TestDiffReportsCountsAndHunksSinceClone(t *testing.T) {
	origin := t.TempDir()
	os.WriteFile(filepath.Join(origin, "keep.txt"), []byte("one\ntwo\nthree\n"), 0o644)
	os.WriteFile(filepath.Join(origin, "my file.txt"), []byte("a\n"), 0o644)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = origin
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s", out)
		}
	}
	l := Local{DataDir: t.TempDir()}
	dir, err := l.Prepare(context.Background(), "wo_d", origin, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("one\nTWO\nthree\nfour\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "my file.txt"), []byte("b\n"), 0o644)
	if _, err := l.Commit(context.Background(), dir, "c1"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "new.go"), []byte("package n\n"), 0o644)
	diffs, err := l.Diff(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]model.FileDiff{}
	for _, d := range diffs {
		byPath[d.Path] = d
	}
	if k := byPath["keep.txt"]; k.Added != 2 || k.Removed != 1 || !strings.HasPrefix(k.Hunks, "@@") || !strings.Contains(k.Hunks, "+TWO") || strings.Contains(k.Hunks, "diff --git") {
		t.Fatalf("keep.txt: %+v", k)
	}
	if m := byPath["my file.txt"]; m.Added != 1 || m.Removed != 1 || !strings.Contains(m.Hunks, "+b") {
		t.Fatalf("a path with a space: %+v", m)
	}
	if n := byPath["new.go"]; n.Added != 1 || !strings.Contains(n.Hunks, "+package n") {
		t.Fatalf("untracked file: %+v", n)
	}
	if none, err := (Local{}).Diff(context.Background(), t.TempDir()); err != nil || none != nil {
		t.Fatalf("non-git workspace: %v %v", none, err)
	}
}

func TestDiffNeverReadsSecretsSymlinksOrHugeFiles(t *testing.T) {
	origin := t.TempDir()
	os.WriteFile(filepath.Join(origin, "a.txt"), []byte("a\n"), 0o644)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = origin
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s", out)
		}
	}
	l := Local{DataDir: t.TempDir()}
	dir, err := l.Prepare(context.Background(), "wo_s", origin, nil)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "id_rsa")
	os.WriteFile(outside, []byte("TOP-SECRET-KEY\n"), 0o600)
	if err := os.Symlink(outside, filepath.Join(dir, "leak")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, ".env.local"), []byte("TOKEN=hunter2\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "größe.txt"), []byte("ok\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("line\n", maxUntrackedDiffBytes/5+10)), 0o644)
	diffs, err := l.Diff(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]model.FileDiff{}
	for _, d := range diffs {
		by[d.Path] = d
		if strings.Contains(d.Hunks, "TOP-SECRET") || strings.Contains(d.Hunks, "hunter2") {
			t.Fatalf("secret leaked through %s", d.Path)
		}
	}
	if _, ok := by["leak"]; !ok || by["leak"].Hunks != "" {
		t.Fatalf("symlink: %+v", by["leak"])
	}
	if e, ok := by[".env.local"]; !ok || e.Hunks != "" {
		t.Fatalf(".env: %+v", e)
	}
	if g := by["größe.txt"]; g.Added != 1 || !strings.Contains(g.Hunks, "+ok") {
		t.Fatalf("non-ASCII path must be reported unquoted: %+v", diffs)
	}
	if b := by["big.txt"]; b.Hunks != "" || b.Added != maxUntrackedDiffBytes/5+10 {
		t.Fatalf("a large file keeps its line count without content: added=%d hunks=%d", b.Added, len(b.Hunks))
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCheckoutClonesTrackedFilesAndRefreshesInPlace(t *testing.T) {
	src := t.TempDir()
	commit := func(file string) string {
		if err := os.WriteFile(filepath.Join(src, file), []byte("package a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, src, "add", file)
		gitIn(t, src, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", file)
		return gitIn(t, src, "rev-parse", "HEAD")
	}
	gitIn(t, src, "init", "-q", "-b", "main")
	first := commit("a.go")
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := Local{DataDir: t.TempDir()}
	ctx := context.Background()
	dir, sha, err := l.Checkout(ctx, "chat-p1", src, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(l.DataDir, "workspaces", "chat-p1") || sha != first {
		t.Fatalf("checkout = %s @ %s; want the clone at %s", dir, sha, first)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("an untracked file of the project folder must not be in the checkout: %v", err)
	}

	second := commit("b.go")
	if _, sha, err = l.Checkout(ctx, "chat-p1", src, nil, false); err != nil || sha != first {
		t.Fatalf("without refresh the checkout stays put: %s, %v", sha, err)
	}
	if _, sha, err = l.Checkout(ctx, "chat-p1", src, nil, true); err != nil || sha != second {
		t.Fatalf("refresh = %s, %v; want %s", sha, err, second)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.go")); err != nil {
		t.Fatalf("the refreshed checkout misses the new file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatalf("a refresh must not bring untracked files: %v", err)
	}
}

func TestCheckoutRefreshFollowsTheConnectedRemote(t *testing.T) {
	remote, folder := t.TempDir(), t.TempDir()
	gitIn(t, remote, "init", "-q", "-b", "main")
	gitIn(t, remote, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, folder, "clone", "-q", remote, ".")
	auth := &model.GitAuth{RemoteURL: remote, DefaultBranch: "main"}
	l := Local{DataDir: t.TempDir()}
	if _, _, err := l.Checkout(context.Background(), "chat-p2", folder, auth, true); err != nil {
		t.Fatal(err)
	}
	gitIn(t, remote, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "next")
	want := gitIn(t, remote, "rev-parse", "HEAD")
	if _, sha, err := l.Checkout(context.Background(), "chat-p2", folder, auth, true); err != nil || sha != want {
		t.Fatalf("refresh = %s, %v; want the remote's %s", sha, err, want)
	}
}

func TestCheckoutOfAFolderWithoutGitFails(t *testing.T) {
	l := Local{DataDir: t.TempDir()}
	if _, _, err := l.Checkout(context.Background(), "chat-p3", t.TempDir(), nil, true); err == nil {
		t.Fatal("a folder that is not a git repository has nothing to read")
	}
	if _, err := os.Stat(filepath.Join(l.DataDir, "workspaces", "chat-p3")); !os.IsNotExist(err) {
		t.Fatalf("no empty checkout may be left behind: %v", err)
	}
}

func symlinksIn(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if err == nil && d.Type()&os.ModeSymlink != 0 {
			found = append(found, p)
		}
		return nil
	})
	return found
}

func TestCheckoutHoldsNoSymlinks(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	gitIn(t, src, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, src, "add", ".")
	gitIn(t, src, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "one")
	l := Local{DataDir: t.TempDir()}
	dir, _, err := l.Checkout(context.Background(), "chat-p4", src, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := symlinksIn(t, dir); len(got) != 0 {
		t.Fatalf("symlinks after clone: %v", got)
	}
	if err := os.Symlink(outside, filepath.Join(src, "link2")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, src, "add", ".")
	gitIn(t, src, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "two")
	if _, _, err := l.Checkout(context.Background(), "chat-p4", src, nil, true); err != nil {
		t.Fatal(err)
	}
	if got := symlinksIn(t, dir); len(got) != 0 {
		t.Fatalf("symlinks after refresh: %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.go")); err != nil {
		t.Fatalf("regular files must stay: %v", err)
	}
}

func TestCheckoutWithBrokenGitReclonesAndLeavesTheEnclosingRepoAlone(t *testing.T) {
	src := t.TempDir()
	gitIn(t, src, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, src, "add", ".")
	gitIn(t, src, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "one")

	outer := t.TempDir()
	gitIn(t, outer, "init", "-q", "-b", "main")
	gitIn(t, outer, "remote", "add", "origin", src)
	mine := filepath.Join(outer, "mine.txt")
	if err := os.WriteFile(mine, []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, outer, "add", ".")
	gitIn(t, outer, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "outer")
	if err := os.WriteFile(mine, []byte("uncommitted work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := Local{DataDir: filepath.Join(outer, "data")}
	dir, _, err := l.Checkout(context.Background(), "chat-p5", src, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, sha, err := l.Checkout(context.Background(), "chat-p5", src, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := gitIn(t, src, "rev-parse", "HEAD"); sha != want {
		t.Fatalf("sha = %s, want the source's %s", sha, want)
	}
	if top := gitIn(t, dir, "rev-parse", "--show-toplevel"); !sameDir(top, dir) {
		t.Fatalf("the checkout must be its own repository, got toplevel %s", top)
	}
	if b, _ := os.ReadFile(mine); string(b) != "uncommitted work\n" {
		t.Fatalf("the enclosing repository was touched: %q", b)
	}
}

func sameDir(a, b string) bool {
	ra, _ := filepath.EvalSymlinks(a)
	rb, _ := filepath.EvalSymlinks(b)
	return ra == rb
}

func TestCheckoutThatIsASymlinkNeverTouchesItsTarget(t *testing.T) {
	src := t.TempDir()
	gitIn(t, src, "init", "-q", "-b", "main")
	mine := filepath.Join(src, "a.go")
	if err := os.WriteFile(mine, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, src, "add", ".")
	gitIn(t, src, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "one")
	if err := os.WriteFile(mine, []byte("uncommitted work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := Local{DataDir: t.TempDir()}
	link := filepath.Join(l.DataDir, "workspaces", "chat-p6")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, link); err != nil {
		t.Fatal(err)
	}
	dir, _, err := l.Checkout(context.Background(), "chat-p6", src, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(mine); string(b) != "uncommitted work\n" {
		t.Fatalf("the symlink's target was reset: %q", b)
	}
	if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("the checkout must be a real directory again: %v %v", fi, err)
	}
}
