package sandbox

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newOrigin(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	bare, seed := filepath.Join(tmp, "origin.git"), filepath.Join(tmp, "seed")
	sh(t, tmp, "git", "init", "--bare", "-b", "main", bare)
	sh(t, tmp, "git", "init", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, seed, "git", "add", "-A")
	sh(t, seed, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")
	sh(t, seed, "git", "push", bare, "main")
	return bare
}

func TestLocalGitFlow(t *testing.T) {
	ctx := context.Background()
	origin := newOrigin(t)
	rt := Local{Root: t.TempDir()}

	ws, err := rt.CreateWorkspace(ctx, "wo1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.CreateWorkspace(ctx, "wo1"); err != nil {
		t.Fatalf("not idempotent: %v", err)
	}
	sb, err := rt.Start(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := Clone(ctx, sb, origin, nil, "main"); err != nil {
			t.Fatalf("clone #%d: %v", i, err)
		}
	}
	wt, err := AddWorktree(ctx, sb, "t1", "factory/t1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if wt != ws.WorktreeDir("t1") {
		t.Fatalf("worktree dir = %s", wt)
	}
	if wt2, err := AddWorktree(ctx, sb, "t1", "factory/t1", "main"); err != nil || wt2 != wt {
		t.Fatalf("reuse: %s %v", wt2, err)
	}

	if sha, changed, err := CommitAll(ctx, sb, wt, "noop"); err != nil || changed || sha == "" {
		t.Fatalf("clean tree: sha=%q changed=%v err=%v", sha, changed, err)
	}

	if err := os.WriteFile(filepath.Join(wt, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("a2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if files, err := ChangedFiles(ctx, sb, wt, "main"); err != nil || !reflect.DeepEqual(files, []string{"a.txt", "b.txt"}) {
		t.Fatalf("uncommitted changed files = %v, %v", files, err)
	}
	sha, changed, err := CommitAll(ctx, sb, wt, "feat: b")
	if err != nil || !changed || sha == "" {
		t.Fatalf("commit: sha=%q changed=%v err=%v", sha, changed, err)
	}
	if files, err := ChangedFiles(ctx, sb, wt, "main"); err != nil || !reflect.DeepEqual(files, []string{"a.txt", "b.txt"}) {
		t.Fatalf("committed changed files = %v, %v", files, err)
	}

	if err := Push(ctx, sb, wt, "factory/t1", nil); err != nil {
		t.Fatal(err)
	}
	if got := sh(t, origin, "git", "rev-parse", "refs/heads/factory/t1"); got != sha {
		t.Fatalf("origin branch = %s want %s", got, sha)
	}

	// Resume after worktree loss: branch already exists.
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if _, err := AddWorktree(ctx, sb, "t1", "factory/t1", "main"); err != nil {
		t.Fatalf("re-add: %v", err)
	}

	if err := rt.DeleteWorkspace(ctx, "wo1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws.Root()); !os.IsNotExist(err) {
		t.Fatalf("workspace not removed: %v", err)
	}
}

func TestLocalCommandEnvAndExec(t *testing.T) {
	ctx := context.Background()
	rt := Local{Root: t.TempDir()}
	ws, _ := rt.CreateWorkspace(ctx, "wo2")
	sb, _ := rt.Start(ctx, ws, map[string]string{"A": "1"})

	out, _, code, err := sb.Exec(ctx, ws.Root(), map[string]string{"B": "2"}, "sh", "-c", "echo $A$B $HOME")
	if err != nil || code != 0 || strings.TrimSpace(out) != "12 "+ws.HomeDir() {
		t.Fatalf("out=%q code=%d err=%v", out, code, err)
	}
	if _, _, code, _ := sb.Exec(ctx, ws.Root(), nil, "sh", "-c", "exit 3"); code != 3 {
		t.Fatalf("exit code = %d", code)
	}
	if c := sb.Command(ws.Root(), nil, "x", "y"); c.Dir != ws.Root() || !reflect.DeepEqual(c.Argv, []string{"x", "y"}) {
		t.Fatalf("cmd = %+v", c)
	}
}

func TestLocalRelativeRootResolvesAbsolute(t *testing.T) {
	ctx := context.Background()
	t.Chdir(t.TempDir())
	rt := Local{Root: "work"}
	ws, err := rt.CreateWorkspace(ctx, "wo3")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(ws.Root()) || !filepath.IsAbs(ws.RepoDir()) {
		t.Fatalf("workspace paths not absolute: %s", ws.Root())
	}
	sb, err := rt.Start(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, _, code, err := sb.Exec(ctx, ws.HomeDir(), nil, "pwd")
	if err != nil || code != 0 {
		t.Fatalf("exec: %v %d", err, code)
	}
	want, _ := filepath.EvalSymlinks(ws.HomeDir())
	if got, _ := filepath.EvalSymlinks(strings.TrimSpace(out)); got != want {
		t.Fatalf("pwd = %s want %s", got, want)
	}
}

func TestLocalHidesHostEnv(t *testing.T) {
	ctx := context.Background()
	t.Setenv("FACTORY_TEST_SECRET", "hunter2")
	rt := Local{Root: t.TempDir()}
	ws, _ := rt.CreateWorkspace(ctx, "wo4")
	sb, _ := rt.Start(ctx, ws, map[string]string{"EXPLICIT": "yes"})
	out, _, code, err := sb.Exec(ctx, ws.Root(), nil, "sh", "-c", `echo "[$FACTORY_TEST_SECRET][$EXPLICIT]"; command -v git`)
	if err != nil || code != 0 {
		t.Fatalf("exec: %v %d %q", err, code, out)
	}
	if !strings.HasPrefix(out, "[][yes]\n") {
		t.Fatalf("env = %q", out)
	}
}

func TestGitAuthEnvAndRedaction(t *testing.T) {
	a := &GitAuth{Username: "u", Token: "s3cr3t/tok"}
	enc := base64.StdEncoding.EncodeToString([]byte("u:s3cr3t/tok"))
	env := a.env()
	if env["GIT_CONFIG_COUNT"] != "1" || env["GIT_CONFIG_KEY_0"] != "http.extraHeader" ||
		env["GIT_CONFIG_VALUE_0"] != "Authorization: Basic "+enc || env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("env = %v", env)
	}
	if _, ok := (*GitAuth)(nil).env()["GIT_CONFIG_COUNT"]; ok {
		t.Fatal("nil auth must not set config env")
	}
	if got := a.redact("fatal s3cr3t/tok s3cr3t%2Ftok " + enc); strings.Contains(got, "s3cr3t") || strings.Contains(got, enc) {
		t.Fatalf("not redacted: %s", got)
	}
}

func TestCloneAuthNeverOnDisk(t *testing.T) {
	ctx := context.Background()
	origin := newOrigin(t)
	rt := Local{Root: t.TempDir()}
	ws, _ := rt.CreateWorkspace(ctx, "wo5")
	sb, _ := rt.Start(ctx, ws, nil)

	if err := os.MkdirAll(filepath.Join(ws.RepoDir(), "junk"), 0o755); err != nil { // interrupted earlier clone
		t.Fatal(err)
	}
	auth := &GitAuth{Username: "u", Token: "tok-123"}
	if err := Clone(ctx, sb, origin, auth, "main"); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(ws.RepoDir(), ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "tok-123") || !strings.Contains(string(cfg), origin) {
		t.Fatalf("git config = %s", cfg)
	}
	if _, err := os.Stat(filepath.Join(ws.RepoDir(), "junk")); !os.IsNotExist(err) {
		t.Fatalf("partial dir not removed: %v", err)
	}
	if err := Push(ctx, sb, ws.RepoDir(), "feature", auth); err != nil {
		t.Fatal(err)
	}
	if sh(t, origin, "git", "rev-parse", "refs/heads/feature") == "" {
		t.Fatal("not pushed")
	}
}

func TestPorts(t *testing.T) {
	p := NewPorts(4000, 4001)
	a, _ := p.Acquire("a")
	if a2, _ := p.Acquire("a"); a2 != a {
		t.Fatal("not idempotent")
	}
	b, _ := p.Acquire("b")
	if a == b {
		t.Fatal("duplicate port")
	}
	if _, err := p.Acquire("c"); err == nil {
		t.Fatal("expected exhaustion")
	}
	p.Release("a")
	if c, err := p.Acquire("c"); err != nil || c != a {
		t.Fatalf("reuse after release: %d %v", c, err)
	}
}

func TestLocalWriteFile(t *testing.T) {
	ctx := context.Background()
	rt := Local{Root: t.TempDir()}
	ws, _ := rt.CreateWorkspace(ctx, "wo3")
	sb, _ := rt.Start(ctx, ws, nil)
	p := filepath.Join(ws.HomeDir(), "a", "b", "auth.json")
	if err := sb.WriteFile(ctx, p, []byte(`{"k":"$(x)"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	fi, _ := os.Stat(p)
	if string(b) != `{"k":"$(x)"}` || fi.Mode().Perm() != 0o600 {
		t.Fatalf("got %q %v", b, fi.Mode())
	}
}
