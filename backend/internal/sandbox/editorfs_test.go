package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestEditorConfinementTable(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeFile(t, outside, "secret", "s")
	writeFile(t, root, "ok.txt", "ok")
	writeFile(t, root, ".git/config", "[core]")
	writeFile(t, root, "d/in.txt", "in")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "abs-link")))
	must(os.Symlink("../"+filepath.Base(outside)+"/secret", filepath.Join(root, "rel-link")))
	must(os.Symlink(outside, filepath.Join(root, "dir-link")))
	must(os.Symlink("ok.txt", filepath.Join(root, "inside-link")))
	must(os.Symlink("inside-link", filepath.Join(root, "chain-link")))

	e := EditorFS{}
	for _, tc := range []struct {
		name, rel string
		want      error
	}{
		{"dotdot", "../x", model.ErrOutsideFolder},
		{"absolute", "/etc/passwd", model.ErrOutsideFolder},
		{"dotdot mid", "a/../../x", model.ErrOutsideFolder},
		{"nul", "a\x00b", model.ErrBadPath},
		{"backslash", `a\b`, model.ErrBadPath},
		{"empty", "", model.ErrBadPath},
		{"dot", ".", model.ErrBadPath},
		{"too long", strings.Repeat("a", 4097), model.ErrBadPath},
		{"git config", ".git/config", model.ErrOutsideFolder},
		{"git upper", ".GIT/config", model.ErrOutsideFolder},
		{"nested git", "d/.git/config", model.ErrOutsideFolder},
		{"abs symlink", "abs-link", model.ErrOutsideFolder},
		{"rel symlink", "rel-link", model.ErrOutsideFolder},
		{"dir symlink", "dir-link/secret", model.ErrOutsideFolder},
		{"temp name", ".x.legos-tmp-1", model.ErrBadPath},
	} {
		t.Run("read "+tc.name, func(t *testing.T) {
			if _, err := e.Read(root, tc.rel, 1<<20); !errors.Is(err, tc.want) {
				t.Fatalf("Read(%q) = %v, want %v", tc.rel, err, tc.want)
			}
		})
		t.Run("write "+tc.name, func(t *testing.T) {
			if _, err := e.Write(root, tc.rel, []byte("x"), "", true); !errors.Is(err, tc.want) {
				t.Fatalf("Write(%q) = %v, want %v", tc.rel, err, tc.want)
			}
		})
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret")); string(b) != "s" {
		t.Fatalf("outside file was touched: %q", b)
	}
	if _, err := e.Read(root, "chain-link", 1<<20); err != nil {
		t.Fatalf("symlink chain inside the folder must read: %v", err)
	}
	if _, _, err := e.List(root, "dir-link"); !errors.Is(err, model.ErrOutsideFolder) {
		t.Fatalf("list through an outside dir link: %v", err)
	}
	if err := e.Mkdir(root, "dir-link/new"); !errors.Is(err, model.ErrOutsideFolder) {
		t.Fatalf("mkdir through an outside dir link: %v", err)
	}
	if err := e.Rename(root, "ok.txt", "dir-link/stolen"); !errors.Is(err, model.ErrOutsideFolder) {
		t.Fatalf("rename out of the folder: %v", err)
	}
	if err := e.Rename(root, ".git/config", "cfg"); !errors.Is(err, model.ErrOutsideFolder) {
		t.Fatalf("rename from .git: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "stolen")); err == nil {
		t.Fatal("rename escaped the root")
	}
}

func TestEditorListHidesGitAndMarksSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeFile(t, root, ".git/HEAD", "ref")
	writeFile(t, root, "b.txt", "b")
	writeFile(t, root, "A.txt", "a")
	writeFile(t, root, "sub/x", "x")
	writeFile(t, root, ".hidden.legos-tmp-1", "t")
	_ = os.Symlink("b.txt", filepath.Join(root, "in"))
	_ = os.Symlink(outside, filepath.Join(root, "out"))
	_ = os.Symlink("nope", filepath.Join(root, "broken"))

	got, trunc, err := EditorFS{}.List(root, "")
	if err != nil || trunc != 0 {
		t.Fatalf("%v %d", err, trunc)
	}
	var names []string
	for _, en := range got {
		names = append(names, en.Name)
	}
	if want := []string{"sub", "A.txt", "b.txt", "broken", "in", "out"}; !slices.Equal(names, want) {
		t.Fatalf("order/hidden: %v want %v", names, want)
	}
	inside := map[string]bool{}
	for _, en := range got {
		if en.Kind == "symlink" {
			if en.TargetInside == nil {
				t.Fatalf("%s lacks target_inside", en.Name)
			}
			inside[en.Name] = *en.TargetInside
		} else if en.TargetInside != nil {
			t.Fatalf("%s must not carry target_inside", en.Name)
		}
	}
	if !inside["in"] || inside["out"] || inside["broken"] {
		t.Fatalf("target_inside: %v", inside)
	}
	if _, _, err := (EditorFS{}).List(root, "b.txt"); !errors.Is(err, model.ErrBadPath) {
		t.Fatalf("listing a file: %v", err)
	}
	if _, _, err := (EditorFS{}).List(root, ".git"); !errors.Is(err, model.ErrOutsideFolder) {
		t.Fatalf("listing .git: %v", err)
	}
}

func TestEditorListTruncates(t *testing.T) {
	root := t.TempDir()
	for i := range editorListMax + 3 {
		writeFile(t, root, "f"+strconv.Itoa(i), "")
	}
	got, trunc, err := EditorFS{}.List(root, ".")
	if err != nil || len(got) != editorListMax || trunc != 3 {
		t.Fatalf("len=%d trunc=%d err=%v", len(got), trunc, err)
	}
}

func TestEditorReadLimits(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "t.txt", "hello\r\nworld")
	writeFile(t, root, "bin", "ab\x00cd")
	writeFile(t, root, "late-nul", strings.Repeat("a", editorSniffBytes)+"\x00")
	writeFile(t, root, "exact", strings.Repeat("a", 100))
	writeFile(t, root, "over", strings.Repeat("a", 101))
	e := EditorFS{}

	fc, err := e.Read(root, "t.txt", 100)
	if err != nil || fc.Content != "hello\r\nworld" || fc.Size != 12 || fc.Hash != hashHex([]byte("hello\r\nworld")) {
		t.Fatalf("%+v %v", fc, err)
	}
	if _, err := e.Read(root, "bin", 100); !errors.Is(err, model.ErrBinary) {
		t.Fatalf("binary: %v", err)
	}
	if _, err := e.Read(root, "late-nul", 1<<20); err != nil {
		t.Fatalf("a NUL after the first 8 KB is not binary: %v", err)
	}
	if _, err := e.Read(root, "exact", 100); err != nil {
		t.Fatalf("exactly max must read: %v", err)
	}
	if _, err := e.Read(root, "over", 100); !errors.Is(err, model.ErrTooLarge) {
		t.Fatalf("over max: %v", err)
	}
	if _, err := e.Read(root, "missing", 100); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := e.Read(root, "t.txt", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Read(filepath.Join(root, "gone"), "t.txt", 100); !errors.Is(err, model.ErrFolderGone) {
		t.Fatalf("missing root: %v", err)
	}
	writeFile(t, root, "d/x", "x")
	if _, err := e.Read(root, "d", 100); !errors.Is(err, model.ErrBadPath) {
		t.Fatalf("directory: %v", err)
	}
}

func TestEditorWritePreconditionAndCreate(t *testing.T) {
	root := t.TempDir()
	e := EditorFS{}
	st, err := e.Write(root, "new.txt", []byte("one"), "", true)
	if err != nil || st.Hash != hashHex([]byte("one")) || st.Size != 3 {
		t.Fatalf("create: %+v %v", st, err)
	}
	if fi, _ := os.Stat(filepath.Join(root, "new.txt")); fi.Mode().Perm() != 0o644 {
		t.Fatalf("new file mode %v", fi.Mode())
	}

	var c *model.EditorConflict
	if _, err := e.Write(root, "new.txt", []byte("x"), "", true); !errors.As(err, &c) || c.Current.Hash != st.Hash {
		t.Fatalf("create over existing must conflict with the current stat: %v", err)
	}
	if _, err := e.Write(root, "new.txt", []byte("two"), "stale", false); !errors.As(err, &c) || c.Current.Hash != st.Hash {
		t.Fatalf("stale hash: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "new.txt")); string(b) != "one" {
		t.Fatalf("a conflict must not touch the file: %q", b)
	}
	st2, err := e.Write(root, "new.txt", []byte("two"), st.Hash, false)
	if err != nil || st2.Hash != hashHex([]byte("two")) {
		t.Fatalf("%+v %v", st2, err)
	}
	if _, err := e.Write(root, "absent.txt", []byte("x"), "h", false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("update of a missing file: %v", err)
	}
	if _, err := e.Write(root, "no/dir/f.txt", []byte("x"), "", true); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("create in a missing dir: %v", err)
	}
	writeFile(t, root, "d/x", "x")
	if _, err := e.Write(root, "d", []byte("x"), "", false); !errors.Is(err, model.ErrBadPath) {
		t.Fatalf("write over a directory: %v", err)
	}
	assertNoTemp(t, root)
}

func assertNoTemp(t *testing.T, root string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.Contains(d.Name(), editorTmpMarker) {
			t.Errorf("leftover temp file %s", p)
		}
		return nil
	})
}

func TestEditorWriteIsAtomicAndKeepsMode(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "run.sh", "old")
	if err := os.Chmod(filepath.Join(root, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := EditorFS{}
	cur, _ := e.Read(root, "run.sh", 100)

	var sawOld, sawTemp bool
	editorBeforeRename = func() {
		b, _ := os.ReadFile(filepath.Join(root, "run.sh"))
		sawOld = string(b) == "old"
		ents, _ := os.ReadDir(root)
		for _, en := range ents {
			sawTemp = sawTemp || strings.Contains(en.Name(), editorTmpMarker)
		}
	}
	t.Cleanup(func() { editorBeforeRename = nil })
	if _, err := e.Write(root, "run.sh", []byte("new content"), cur.Hash, false); err != nil {
		t.Fatal(err)
	}
	if !sawOld || !sawTemp {
		t.Fatalf("before the rename the old bytes must be intact (old=%v) and the temp present (temp=%v)", sawOld, sawTemp)
	}
	fi, _ := os.Stat(filepath.Join(root, "run.sh"))
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode lost: %v", fi.Mode())
	}
	if b, _ := os.ReadFile(filepath.Join(root, "run.sh")); string(b) != "new content" {
		t.Fatalf("%q", b)
	}
	assertNoTemp(t, root)
}

func TestEditorWriteThroughSymlinkWritesTheTarget(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeFile(t, root, "real.txt", "old")
	writeFile(t, outside, "secret", "s")
	_ = os.Symlink("real.txt", filepath.Join(root, "link"))
	_ = os.Symlink("link", filepath.Join(root, "link2"))
	_ = os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "bad"))
	e := EditorFS{}
	cur, _ := e.Read(root, "link2", 100)
	if _, err := e.Write(root, "link2", []byte("via link"), cur.Hash, false); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(filepath.Join(root, "link2")); fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatal("the link was replaced by a regular file")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "real.txt")); string(b) != "via link" {
		t.Fatalf("target: %q", b)
	}
	if _, err := e.Write(root, "bad", []byte("x"), "", true); !errors.Is(err, model.ErrOutsideFolder) {
		t.Fatalf("outside link: %v", err)
	}
}

func TestEditorMkdirAndRename(t *testing.T) {
	root := t.TempDir()
	e := EditorFS{}
	if err := e.Mkdir(root, "a/b"); err != nil {
		t.Fatal(err)
	}
	if err := e.Mkdir(root, "a/b"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("mkdir existing: %v", err)
	}
	writeFile(t, root, "a/f.txt", "f")
	writeFile(t, root, "g.txt", "g")
	if err := e.Rename(root, "a/f.txt", "g.txt"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("rename onto existing: %v", err)
	}
	if err := e.Rename(root, "a/f.txt", "a/b/h.txt"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a/b/h.txt")); string(b) != "f" {
		t.Fatal("rename lost the content")
	}
	if err := e.Rename(root, "nope", "x"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rename missing: %v", err)
	}
	if err := e.Rename(root, "a", "A2"); err != nil {
		t.Fatalf("directory rename: %v", err)
	}
}

func TestParseStatusPorcelainV2(t *testing.T) {
	in := strings.Join([]string{
		"# branch.oid abcdef0123456789", "# branch.head main", "# branch.upstream origin/main", "# branch.ab +2 -1",
		"1 .M N... 100644 100644 100644 aaaa bbbb dir/mod file.go",
		"1 A. N... 000000 100644 100644 0000 bbbb added.go",
		"2 R. N... 100644 100644 100644 aaaa bbbb R100 new name.go", "old name.go",
		"u UU N... 100644 100644 100644 100644 a b c conflict.go",
		"? untracked/", "? loose.txt", "",
	}, "\x00")
	gs := parseStatus([]byte(in), "")
	if !gs.Repo || gs.Branch != "main" || gs.Ahead != 2 || gs.Behind != 1 {
		t.Fatalf("%+v", gs)
	}
	want := []model.GitStatusEntry{
		{Path: "dir/mod file.go", X: ".", Y: "M"}, {Path: "added.go", X: "A", Y: "."},
		{Path: "new name.go", X: "R", Y: "."}, {Path: "conflict.go", X: "U", Y: "U"},
		{Path: "untracked/", X: "?", Y: "?"}, {Path: "loose.txt", X: "?", Y: "?"},
	}
	if !slices.Equal(gs.Entries, want) {
		t.Fatalf("entries:\n got %+v\nwant %+v", gs.Entries, want)
	}

	det := parseStatus([]byte("# branch.oid 1234567890abcdef\x00# branch.head (detached)\x00"), "")
	if det.Branch != "1234567" || len(det.Entries) != 0 {
		t.Fatalf("detached: %+v", det)
	}

	sub := parseStatus([]byte("1 .M N... 1 1 1 a b app/x.go\x001 .M N... 1 1 1 a b other/y.go\x00? app/z\x00"), "app/")
	if !slices.Equal(sub.Entries, []model.GitStatusEntry{{Path: "x.go", X: ".", Y: "M"}, {Path: "z", X: "?", Y: "?"}}) {
		t.Fatalf("subfolder prefix: %+v", sub.Entries)
	}

	var big bytes.Buffer
	for i := 0; i <= editorStatusMax; i++ {
		big.WriteString("? f\x00")
	}
	if tm := parseStatus(big.Bytes(), ""); !tm.TooMany || tm.Entries != nil {
		t.Fatalf("too many: %v %d", tm.TooMany, len(tm.Entries))
	}
}

func TestEditorGitIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	e := EditorFS{}
	ctx := context.Background()

	if gs, err := e.GitStatus(ctx, root); err != nil || gs.Repo {
		t.Fatalf("non-repo: %+v %v", gs, err)
	}
	if ig, err := e.Ignored(ctx, root, []string{"a"}); err != nil || len(ig) != 0 {
		t.Fatalf("non-repo ignored: %v %v", ig, err)
	}
	writeFile(t, root, "plain/a.txt", "a")
	writeFile(t, root, "plain/node_modules/dep/i.js", "")
	files, err := e.Files(ctx, root, 100)
	if err != nil || !slices.Equal(files, []string{"plain/a.txt"}) {
		t.Fatalf("walk fallback: %v %v", files, err)
	}

	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	writeFile(t, repo, ".gitignore", "build/\n*.log\n")
	writeFile(t, repo, "tracked.go", "package x")
	writeFile(t, repo, "gone.go", "x")
	writeFile(t, repo, "new/keep.txt", "k")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "init")
	writeFile(t, repo, "tracked.go", "package y")
	writeFile(t, repo, "new/dir/u.txt", "u")
	writeFile(t, repo, "build/out", "o")
	writeFile(t, repo, "x.log", "l")
	if err := os.Remove(filepath.Join(repo, "gone.go")); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "mv", "tracked.go", "moved.go")

	gs, err := e.GitStatus(ctx, repo)
	if err != nil || !gs.Repo || gs.Branch != "main" {
		t.Fatalf("%+v %v", gs, err)
	}
	got := map[string]string{}
	for _, en := range gs.Entries {
		got[en.Path] = en.X + en.Y
	}
	if got["moved.go"] != "RM" || got["new/dir/"] != "??" || got["gone.go"] != ".D" || len(got) != 3 {
		t.Fatalf("status entries: %v", got)
	}

	ig, err := e.Ignored(ctx, repo, []string{"build", "x.log", "moved.go", ".gitignore"})
	if err != nil || !ig["build"] || !ig["x.log"] || ig["moved.go"] || ig[".gitignore"] {
		t.Fatalf("ignored: %v %v", ig, err)
	}

	files, err = e.Files(ctx, repo, 100)
	if err != nil || !slices.Equal(files, []string{".gitignore", "moved.go", "new/dir/u.txt", "new/keep.txt"}) {
		t.Fatalf("files (untracked listed, ignored and deleted dropped): %v %v", files, err)
	}
	if capped, _ := e.Files(ctx, repo, 2); len(capped) != 2 {
		t.Fatalf("cap: %v", capped)
	}

	sub, err := e.GitStatus(ctx, filepath.Join(repo, "new"))
	if err != nil || len(sub.Entries) != 1 || sub.Entries[0].Path != "dir/" {
		t.Fatalf("subfolder status paths must be relative to the folder: %+v %v", sub, err)
	}

	runGit(t, repo, "-c", "advice.detachedHead=false", "checkout", "-q", "--detach")
	if d, _ := e.GitStatus(ctx, repo); len(d.Branch) != 7 {
		t.Fatalf("detached head shows the short sha, got %q", d.Branch)
	}
}

func TestEditorGitNeverTakesLocksOrRunsFsmonitor(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo, marker := t.TempDir(), filepath.Join(t.TempDir(), "ran")
	runGit(t, repo, "init", "-q")
	writeFile(t, repo, "f", "f")
	script := filepath.Join(t.TempDir(), "mon.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "config", "core.fsmonitor", script)
	if _, err := (EditorFS{}).GitStatus(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repository-configured fsmonitor command was executed")
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "index.lock")); err == nil {
		t.Fatal("index.lock left behind")
	}
}
