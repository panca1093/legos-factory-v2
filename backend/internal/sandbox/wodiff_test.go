package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func tg(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func put(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// wsWithBase builds a clone-like workspace: a base commit on main with
// origin/main set, ready for the work order's commit on top.
func wsWithBase(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	tg(t, dir, "init", "-q", "-b", "main")
	for n, c := range files {
		put(t, dir, n, c)
	}
	tg(t, dir, "add", "-A")
	tg(t, dir, "commit", "-q", "-m", "base")
	tg(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	tg(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return dir
}

func commitAll(t *testing.T, dir string) string {
	t.Helper()
	tg(t, dir, "add", "-A")
	tg(t, dir, "commit", "-q", "-m", "wo")
	return tg(t, dir, "rev-parse", "HEAD")
}

func find(t *testing.T, files []fileView, path string) fileView {
	t.Helper()
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no file %q in %+v", path, files)
	return fileView{}
}

type fileView struct {
	Path, OldPath, Status, Patch string
	Add, Del                     int
	Binary, Truncated            bool
}

func diffOf(t *testing.T, dir, sha string) ([]fileView, bool) {
	t.Helper()
	d, err := Local{}.CommitDiff(context.Background(), dir, sha)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Available {
		t.Fatalf("unavailable: %s", d.Reason)
	}
	var out []fileView
	for _, f := range d.Files {
		out = append(out, fileView{f.Path, f.OldPath, f.Status, f.Patch, f.Additions, f.Deletions, f.Binary, f.Truncated})
	}
	return out, d.Truncated
}

func TestCommitDiffStatuses(t *testing.T) {
	dir := wsWithBase(t, map[string]string{
		"keep.txt":          "a\nb\nc\n",
		"gone.txt":          "bye\n",
		"old name.txt":      "one\ntwo\nthree\nfour\nfive\nsix\n",
		"bin.dat":           "\x00\x01\x02",
		"package-lock.json": "{}\n",
	})
	put(t, dir, "keep.txt", "a\nB\nc\n")
	put(t, dir, "new.txt", "hello\n")
	put(t, dir, "bin.dat", "\x00\x01\x02\x03")
	put(t, dir, "package-lock.json", "{\"x\":1}\n")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	tg(t, dir, "mv", "old name.txt", "new name.txt")
	sha := commitAll(t, dir)

	// a later commit must not leak in when the work order pins its own sha
	put(t, dir, "later.txt", "x\n")
	commitAll(t, dir)

	files, trunc := diffOf(t, dir, sha)
	if trunc || len(files) != 6 {
		t.Fatalf("trunc=%v files=%+v", trunc, files)
	}
	if m := find(t, files, "keep.txt"); m.Status != "modified" || m.Add != 1 || m.Del != 1 || !strings.HasPrefix(m.Patch, "@@") || !strings.Contains(m.Patch, "+B") {
		t.Fatalf("modified: %+v", m)
	}
	if a := find(t, files, "new.txt"); a.Status != "added" || a.Add != 1 || !strings.Contains(a.Patch, "+hello") {
		t.Fatalf("added: %+v", a)
	}
	if d := find(t, files, "gone.txt"); d.Status != "deleted" || d.Del != 1 || !strings.Contains(d.Patch, "-bye") {
		t.Fatalf("deleted: %+v", d)
	}
	if r := find(t, files, "new name.txt"); r.Status != "renamed" || r.OldPath != "old name.txt" || r.Add != 0 || r.Del != 0 {
		t.Fatalf("renamed: %+v", r)
	}
	if b := find(t, files, "bin.dat"); !b.Binary || b.Patch != "" || b.Truncated {
		t.Fatalf("binary: %+v", b)
	}
	if l := find(t, files, "package-lock.json"); l.Add != 1 || l.Del != 1 || l.Patch != "" || !l.Truncated {
		t.Fatalf("lockfile: %+v", l)
	}
}

func TestCommitDiffFallsBackToHead(t *testing.T) {
	dir := wsWithBase(t, map[string]string{"a.txt": "a\n"})
	put(t, dir, "b.txt", "b\n")
	commitAll(t, dir)
	files, _ := diffOf(t, dir, "")
	if len(files) != 1 || files[0].Path != "b.txt" {
		t.Fatalf("files=%+v", files)
	}
}

func TestCommitDiffTruncation(t *testing.T) {
	dir := wsWithBase(t, map[string]string{"a.txt": "a\n"})
	line := strings.Repeat("x", 99) + "\n"
	put(t, dir, "big.txt", strings.Repeat(line, 3000)) // ~300 KB > per-file cap
	for i := 0; i < 12; i++ {
		put(t, dir, "m"+string(rune('a'+i))+".txt", strings.Repeat(line, 2000)) // 200 KB each: exceeds 2 MB in total
	}
	sha := commitAll(t, dir)
	files, trunc := diffOf(t, dir, sha)
	big := find(t, files, "big.txt")
	if !big.Truncated || len(big.Patch) > maxFilePatchBytes || !strings.HasSuffix(big.Patch, "\n") || big.Add != 3000 {
		t.Fatalf("per-file cap: truncated=%v len=%d add=%d", big.Truncated, len(big.Patch), big.Add)
	}
	if !trunc {
		t.Fatal("total cap must set the top-level truncated flag")
	}
	total, emptied := 0, 0
	for _, f := range files {
		total += len(f.Patch)
		if f.Patch == "" && f.Truncated {
			emptied++
		}
	}
	if total > maxTotalPatchBytes || emptied == 0 {
		t.Fatalf("total=%d emptied=%d", total, emptied)
	}
}

func TestCommitDiffLiteralPathsSecretsAndFallback(t *testing.T) {
	dir := wsWithBase(t, map[string]string{"app/i/p.tsx": "i\n", "a.txt": "a\n"})
	put(t, dir, "app/[id]/p.tsx", "dyn\n")
	put(t, dir, ":(top)x.txt", "magic\n")
	put(t, dir, ".env.local", "TOKEN=abc\n")
	put(t, dir, "app/i/p.tsx", "i2\n")
	sha := commitAll(t, dir)

	d, err := Local{}.CommitDiff(context.Background(), dir, sha)
	if err != nil || !d.Available || d.HeadFallback {
		t.Fatalf("%+v %v", d, err)
	}
	by := map[string]string{}
	for _, f := range d.Files {
		by[f.Path] = f.Patch
		if f.Path == ".env.local" && (f.Patch != "" || !f.Truncated || f.Additions != 1) {
			t.Fatalf("secret file must be counts only: %+v", f)
		}
	}
	if strings.Contains(by["app/[id]/p.tsx"], "i2") || !strings.Contains(by["app/[id]/p.tsx"], "+dyn") {
		t.Fatalf("[id] must match literally: %q", by["app/[id]/p.tsx"])
	}
	if !strings.Contains(by["app/i/p.tsx"], "+i2") || strings.Contains(by["app/i/p.tsx"], "dyn") {
		t.Fatalf("app/i patch: %q", by["app/i/p.tsx"])
	}
	if !strings.Contains(by[":(top)x.txt"], "+magic") {
		t.Fatalf("magic-looking name: %q", by[":(top)x.txt"])
	}

	d, _ = Local{}.CommitDiff(context.Background(), dir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if !d.Available || !d.HeadFallback {
		t.Fatalf("unknown sha must fall back to HEAD and say so: %+v", d)
	}
}

func TestCommitDiffUnavailable(t *testing.T) {
	ctx := context.Background()
	l := Local{}
	for name, dir := range map[string]string{
		"missing":  filepath.Join(t.TempDir(), "nope"),
		"not repo": t.TempDir(),
	} {
		d, err := l.CommitDiff(ctx, dir, "")
		if err != nil || d.Available || d.Reason == "" || d.Files == nil {
			t.Fatalf("%s: %+v err=%v", name, d, err)
		}
	}
	empty := t.TempDir()
	tg(t, empty, "init", "-q", "-b", "main")
	if d, _ := l.CommitDiff(ctx, empty, ""); d.Available || d.Reason == "" {
		t.Fatalf("no commits: %+v", d)
	}
	noChange := wsWithBase(t, map[string]string{"a.txt": "a\n"})
	if d, _ := l.CommitDiff(ctx, noChange, ""); d.Available || d.Reason == "" {
		t.Fatalf("nothing committed: %+v", d)
	}
}
