package sandbox

import (
	"context"
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

func TestGitAuthRedaction(t *testing.T) {
	a := &GitAuth{Username: "u", Token: "s3cr3t/tok"}
	if got := a.apply("https://example.com/r.git"); !strings.Contains(got, "u:s3cr3t%2Ftok@example.com") {
		t.Fatalf("apply = %s", got)
	}
	if got := a.apply("/local/path"); got != "/local/path" {
		t.Fatalf("local url changed: %s", got)
	}
	if got := a.redact("fatal: https://u:s3cr3t%2Ftok@example.com s3cr3t/tok"); strings.Contains(got, "s3cr3t") {
		t.Fatalf("not redacted: %s", got)
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
