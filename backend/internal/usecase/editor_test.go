package usecase

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/sandbox"
	"legos-factory/backend/internal/store"
)

type editorRig struct {
	ed        *Editor
	ana, bob  *model.User
	project   string
	data, hom string
	members   *store.MemberRepo
}

func newEditorRig(t *testing.T) *editorRig {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	users := &store.UserRepo{DB: db}
	ana := &model.User{ID: "ana", Email: "ana@t", Name: "ana", Role: model.RoleEngineer}
	bob := &model.User{ID: "bob", Email: "bob@t", Name: "bob", Role: model.RoleEngineer}
	for _, u := range []*model.User{ana, bob} {
		if err := users.Create(ctx, *u); err != nil {
			t.Fatal(err)
		}
	}
	r := &editorRig{ana: ana, bob: bob, project: t.TempDir(), data: t.TempDir(), hom: t.TempDir()}
	projRepo, members, woRepo := &store.ProjectRepo{DB: db}, &store.MemberRepo{DB: db}, &store.WorkOrderRepo{DB: db}
	r.members = members
	if err := projRepo.Create(ctx, &model.Project{ID: "p1", Name: "p1", RepoURL: r.project}); err != nil {
		t.Fatal(err)
	}
	if err := members.Add(ctx, "p1", "ana"); err != nil {
		t.Fatal(err)
	}
	if err := woRepo.Create(ctx, &model.WorkOrder{ID: "wo_1", ProjectID: "p1", Title: "t", Objective: "o", State: model.StateCreated, CreatedBy: "ana"}); err != nil {
		t.Fatal(err)
	}
	r.ed = &Editor{
		Store: &store.EditorRepo{DB: db}, FS: sandbox.EditorFS{},
		Projects: &Projects{Store: projRepo, Members: members, Users: users},
		Proj:     projRepo, WO: woRepo, DataDir: r.data, Home: r.hom,
	}
	return r
}

func (r *editorRig) open(t *testing.T) *model.EditorFolder {
	t.Helper()
	f, err := r.ed.ProjectFolder(context.Background(), r.ana, "p1")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func put(t *testing.T, dir, rel string, b []byte) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEditorProjectFolderIsTrustedAndPerUser(t *testing.T) {
	r := newEditorRig(t)
	ctx := context.Background()
	f := r.open(t)
	real, _ := filepath.EvalSymlinks(r.project)
	if f.Kind != model.FolderProject || !f.Trusted || f.Path != real || f.ProjectID != "p1" {
		t.Fatalf("%+v", f)
	}
	again := r.open(t)
	if again.ID != f.ID {
		t.Fatal("reopening must return the same folder id")
	}
	if _, err := r.ed.ProjectFolder(ctx, r.bob, "p1"); !errors.Is(err, model.ErrProjectNotFound) {
		t.Fatalf("non-member: %v", err)
	}
	if _, err := r.ed.Folder(ctx, r.bob, f.ID); !errors.Is(err, model.ErrEditorFolderNotFound) {
		t.Fatalf("another user's folder: %v", err)
	}
	if err := r.members.Remove(ctx, "p1", "ana"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ed.Folder(ctx, r.ana, f.ID); !errors.Is(err, model.ErrEditorFolderNotFound) {
		t.Fatalf("access revoked must hide the folder: %v", err)
	}
}

func TestEditorProjectFolderRefusesBadRepoURLs(t *testing.T) {
	r := newEditorRig(t)
	ctx := context.Background()
	projs := r.ed.Proj.(*store.ProjectRepo)
	for _, tc := range []struct {
		name, url string
		want      error
	}{
		{"filesystem root", "/", ErrInvalid},
		{"home", r.hom, ErrInvalid},
		{"parent of home", filepath.Dir(r.hom), ErrInvalid},
		{"data dir", r.data, ErrInvalid},
		{"under data dir", filepath.Join(r.data, "workspaces", "x"), model.ErrFolderGone},
		{"remote", "https://github.com/a/b.git", ErrInvalid},
		{"missing", filepath.Join(t.TempDir(), "nope"), model.ErrFolderGone},
		{"system", "/usr", ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "px_" + strings.ReplaceAll(tc.name, " ", "_")
			if err := projs.Create(ctx, &model.Project{ID: id, Name: id, RepoURL: tc.url}); err != nil {
				t.Fatal(err)
			}
			_ = r.members.Add(ctx, id, "ana")
			if _, err := r.ed.ProjectFolder(ctx, r.ana, id); !errors.Is(err, tc.want) {
				t.Fatalf("%s: %v want %v", tc.url, err, tc.want)
			}
		})
	}
	ws := filepath.Join(r.data, "workspaces", "x")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = projs.Create(ctx, &model.Project{ID: "pw", Name: "pw", RepoURL: ws})
	_ = r.members.Add(ctx, "pw", "ana")
	if _, err := r.ed.ProjectFolder(ctx, r.ana, "pw"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a folder under the data dir: %v", err)
	}
}

func TestEditorCheckRootTable(t *testing.T) {
	r := newEditorRig(t)
	real := func(p string) string {
		p, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	ok, hom, data := real(t.TempDir()), real(r.hom), real(r.data)
	trees := filepath.Join(data, "worktrees", "p1", "wo_1")
	for _, tc := range []struct {
		name     string
		dir      string
		worktree bool
		refused  bool
	}{
		{"ordinary folder", ok, false, false},
		{"root", "/", false, true},
		{"home", hom, false, true},
		{"home child", filepath.Join(hom, "code"), false, false},
		{"home parent", filepath.Dir(hom), false, true},
		{"data dir", data, false, true},
		{"data parent", filepath.Dir(data), false, true},
		{"data child", filepath.Join(data, "accounts"), false, true},
		{"worktree via worktree path", trees, true, false},
		{"worktree path as plain folder", trees, false, true},
		{"/etc", "/etc", false, true},
		{"/usr/local", "/usr/local", false, true},
		{"/System", "/System/Library", false, true},
		{"/private/etc", "/private/etc", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := r.ed.checkRoot(tc.dir, tc.worktree)
			if (err != nil) != tc.refused || (err != nil && !errors.Is(err, ErrInvalid)) {
				t.Fatalf("checkRoot(%s, %v) = %v, refused want %v", tc.dir, tc.worktree, err, tc.refused)
			}
		})
	}
}

func TestEditorWorktreeFolders(t *testing.T) {
	r := newEditorRig(t)
	ctx := context.Background()
	wt := model.WorktreeDir(r.data, "p1", "wo_1")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wt+".tasks/t1", 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := r.ed.WorktreeFolder(ctx, r.ana, "wo_1", "")
	if err != nil || f.Kind != model.FolderWorktree || !f.Trusted || f.WOID != "wo_1" || f.TaskID != "" || f.ProjectID != "p1" {
		t.Fatalf("%+v %v", f, err)
	}
	tf, err := r.ed.WorktreeFolder(ctx, r.ana, "wo_1", "t1")
	if err != nil || tf.TaskID != "t1" || tf.ID == f.ID || !strings.HasSuffix(tf.Path, "wo_1.tasks/t1") {
		t.Fatalf("%+v %v", tf, err)
	}
	if _, err := r.ed.WorktreeFolder(ctx, r.ana, "wo_1", "t9"); !errors.Is(err, model.ErrFolderGone) {
		t.Fatalf("missing task worktree: %v", err)
	}
	if _, err := r.ed.WorktreeFolder(ctx, r.bob, "wo_1", ""); !errors.Is(err, model.ErrWorkOrderNotFound) {
		t.Fatalf("non-member: %v", err)
	}
	if _, err := r.ed.WorktreeFolder(ctx, r.ana, "wo_missing", ""); !errors.Is(err, model.ErrWorkOrderNotFound) {
		t.Fatalf("unknown work order: %v", err)
	}
	for _, task := range []string{"..", "../x", "a/b", "."} {
		if _, err := r.ed.WorktreeFolder(ctx, r.ana, "wo_1", task); !errors.Is(err, ErrInvalid) {
			t.Fatalf("task id %q: %v", task, err)
		}
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ed.Folder(ctx, r.ana, f.ID); !errors.Is(err, model.ErrFolderGone) {
		t.Fatalf("cleaned-up worktree: %v", err)
	}
	if _, err := r.ed.WorktreeFolder(ctx, r.ana, "wo_1", ""); !errors.Is(err, model.ErrFolderGone) {
		t.Fatalf("reopen cleaned-up worktree: %v", err)
	}
}

func TestEditorReadRules(t *testing.T) {
	r := newEditorRig(t)
	ctx := context.Background()
	f := r.open(t)
	put(t, r.project, "plain.txt", []byte("a\nb\n"))
	put(t, r.project, "crlf.txt", []byte("a\r\nb\r\nc"))
	put(t, r.project, "bom.txt", []byte("\xEF\xBB\xBFhello"))
	put(t, r.project, "latin1.txt", []byte("caf\xe9"))
	put(t, r.project, "bin.dat", []byte("x\x00y"))
	put(t, r.project, "edit-max.txt", []byte(strings.Repeat("a", EditorEditableMax)))
	put(t, r.project, "ro.txt", []byte(strings.Repeat("a", EditorEditableMax+1)))
	put(t, r.project, "read-max.txt", []byte(strings.Repeat("a", EditorReadMax)))
	put(t, r.project, "huge.txt", []byte(strings.Repeat("a", EditorReadMax+1)))

	read := func(rel string) model.FileContent {
		t.Helper()
		fc, err := r.ed.Read(ctx, r.ana, f.ID, rel)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		return fc
	}
	if fc := read("plain.txt"); fc.Content != "a\nb\n" || fc.EOL != "LF" || fc.BOM || fc.ReadOnly || fc.Size != 4 || fc.Hash == "" {
		t.Fatalf("%+v", fc)
	}
	if fc := read("crlf.txt"); fc.Content != "a\nb\nc" || fc.EOL != "CRLF" {
		t.Fatalf("crlf must be reported and normalized: %+v", fc)
	}
	if fc := read("bom.txt"); fc.Content != "hello" || !fc.BOM {
		t.Fatalf("bom: %+v", fc)
	}
	if fc := read("latin1.txt"); !fc.ReadOnly || !strings.HasPrefix(fc.Content, "caf") {
		t.Fatalf("non-UTF-8 is read-only: %+v", fc)
	}
	if fc := read("edit-max.txt"); fc.ReadOnly {
		t.Fatal("exactly 2 MB is editable")
	}
	if fc := read("ro.txt"); !fc.ReadOnly {
		t.Fatal("over 2 MB is read-only")
	}
	if fc := read("read-max.txt"); !fc.ReadOnly || fc.Size != EditorReadMax {
		t.Fatalf("exactly 10 MB opens read-only: %v", fc.Size)
	}
	if _, err := r.ed.Read(ctx, r.ana, f.ID, "huge.txt"); !errors.Is(err, model.ErrTooLarge) {
		t.Fatalf("over 10 MB: %v", err)
	}
	if _, err := r.ed.Read(ctx, r.ana, f.ID, "bin.dat"); !errors.Is(err, model.ErrBinary) {
		t.Fatalf("binary: %v", err)
	}
	if _, err := r.ed.Read(ctx, r.ana, f.ID, "../outside"); !errors.Is(err, model.ErrOutsideFolder) {
		t.Fatalf("outside: %v", err)
	}
	if _, err := r.ed.Read(ctx, r.bob, f.ID, "plain.txt"); !errors.Is(err, model.ErrEditorFolderNotFound) {
		t.Fatalf("another user: %v", err)
	}
}

func TestEditorWriteRoundTripKeepsBOMAndCRLF(t *testing.T) {
	r := newEditorRig(t)
	ctx := context.Background()
	f := r.open(t)
	put(t, r.project, "w.txt", []byte("\xEF\xBB\xBFone\r\ntwo\r\n"))

	fc, err := r.ed.Read(ctx, r.ana, f.ID, "w.txt")
	if err != nil || fc.EOL != "CRLF" || !fc.BOM {
		t.Fatalf("%+v %v", fc, err)
	}
	st, err := r.ed.Write(ctx, r.ana, f.ID, "w.txt", "one\nTWO\nthree\n", fc.Hash, false)
	if err != nil {
		t.Fatal(err)
	}
	disk, _ := os.ReadFile(filepath.Join(r.project, "w.txt"))
	if string(disk) != "\xEF\xBB\xBFone\r\nTWO\r\nthree\r\n" {
		t.Fatalf("disk bytes: %q", disk)
	}
	again, _ := r.ed.Read(ctx, r.ana, f.ID, "w.txt")
	if again.Hash != st.Hash || again.Content != "one\nTWO\nthree\n" || !again.BOM || again.EOL != "CRLF" {
		t.Fatalf("round trip: %+v want hash %s", again, st.Hash)
	}

	put(t, r.project, "lf.txt", []byte("x\n"))
	lf, _ := r.ed.Read(ctx, r.ana, f.ID, "lf.txt")
	if _, err := r.ed.Write(ctx, r.ana, f.ID, "lf.txt", "y\r\nz\n", lf.Hash, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.project, "lf.txt")); string(b) != "y\nz\n" {
		t.Fatalf("an LF file stays LF: %q", b)
	}
}

func TestEditorWritePrecondition(t *testing.T) {
	r := newEditorRig(t)
	ctx := context.Background()
	f := r.open(t)
	put(t, r.project, "a.txt", []byte("v1"))
	r.ed.Actor = func(_ context.Context, ff *model.EditorFolder, rel string, _ time.Time) *model.EditorActor {
		if ff.ID != f.ID || rel == "" {
			t.Errorf("actor args: %s %s", ff.ID, rel)
		}
		return &model.EditorActor{Role: "builder", Label: "Builder"}
	}
	fc, _ := r.ed.Read(ctx, r.ana, f.ID, "a.txt")
	put(t, r.project, "a.txt", []byte("v2 from an agent"))

	_, err := r.ed.Write(ctx, r.ana, f.ID, "a.txt", "mine", fc.Hash, false)
	var wc *WriteConflict
	var c *model.EditorConflict
	if !errors.As(err, &wc) || !errors.As(err, &c) {
		t.Fatalf("want a conflict, got %v", err)
	}
	if wc.Current.Hash == fc.Hash || wc.Actor == nil || wc.Actor.Role != "builder" || c.Current.Size != int64(len("v2 from an agent")) {
		t.Fatalf("%+v", wc)
	}
	if b, _ := os.ReadFile(filepath.Join(r.project, "a.txt")); string(b) != "v2 from an agent" {
		t.Fatal("the agent's bytes must survive a conflict")
	}

	if _, err := r.ed.Write(ctx, r.ana, f.ID, "new.txt", "n", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ed.Write(ctx, r.ana, f.ID, "new.txt", "n2", "", true); !errors.As(err, &wc) {
		t.Fatalf("create over an existing file: %v", err)
	}
	if _, err := r.ed.Write(ctx, r.ana, f.ID, "ghost.txt", "n", "h", false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("update of a missing file: %v", err)
	}
	if _, err := r.ed.Write(ctx, r.ana, f.ID, ".git/config", "x", "", true); !errors.Is(err, model.ErrOutsideFolder) {
		t.Fatalf(".git write: %v", err)
	}
	if _, err := r.ed.Write(ctx, r.ana, f.ID, "big.txt", strings.Repeat("a", EditorEditableMax+1), "", true); !errors.Is(err, model.ErrTooLarge) {
		t.Fatalf("over 2 MB: %v", err)
	}
	if _, err := r.ed.Write(ctx, r.ana, f.ID, "big.txt", strings.Repeat("a", EditorEditableMax), "", true); err != nil {
		t.Fatalf("exactly 2 MB: %v", err)
	}

	put(t, r.project, "ro.txt", []byte(strings.Repeat("a", EditorEditableMax+1)))
	ro, _ := r.ed.Read(ctx, r.ana, f.ID, "ro.txt")
	if _, err := r.ed.Write(ctx, r.ana, f.ID, "ro.txt", "x", ro.Hash, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("read-only file: %v", err)
	}
	put(t, r.project, "bin.dat", []byte("\x00\x01"))
	if _, err := r.ed.Write(ctx, r.ana, f.ID, "bin.dat", "x", "h", false); !errors.Is(err, model.ErrBinary) {
		t.Fatalf("binary: %v", err)
	}
	if _, err := r.ed.Write(ctx, r.bob, f.ID, "a.txt", "x", fc.Hash, false); !errors.Is(err, model.ErrEditorFolderNotFound) {
		t.Fatalf("another user's folder: %v", err)
	}
}

func TestEditorTreeMkdirRenameFilesAndTrust(t *testing.T) {
	r := newEditorRig(t)
	ctx := context.Background()
	f := r.open(t)
	put(t, r.project, ".gitignore", []byte("build/\n"))
	put(t, r.project, "src/main.go", []byte("package main"))
	put(t, r.project, "build/out", []byte("o"))
	put(t, r.project, "README.md", []byte("r"))
	if out, err := exec.Command("git", "-C", r.project, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}

	ents, trunc, err := r.ed.Tree(ctx, r.ana, f.ID, "")
	if err != nil || trunc != 0 {
		t.Fatal(err)
	}
	flags := map[string]bool{}
	var order []string
	for _, e := range ents {
		flags[e.Name] = e.Ignored
		order = append(order, e.Name)
	}
	if !flags["build"] || flags["src"] || flags["README.md"] || strings.Join(order, ",") != "build,src,.gitignore,README.md" {
		t.Fatalf("ignored flags / order: %v %v", flags, order)
	}
	sub, _, err := r.ed.Tree(ctx, r.ana, f.ID, "src")
	if err != nil || len(sub) != 1 || sub[0].Name != "main.go" || sub[0].Ignored {
		t.Fatalf("%+v %v", sub, err)
	}
	if _, _, err := r.ed.Tree(ctx, r.ana, f.ID, ".git"); !errors.Is(err, model.ErrOutsideFolder) {
		t.Fatalf(".git listing: %v", err)
	}

	if err := r.ed.Mkdir(ctx, r.ana, f.ID, "pkg/util"); err != nil {
		t.Fatal(err)
	}
	if err := r.ed.Mkdir(ctx, r.ana, f.ID, "pkg/util"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("mkdir existing: %v", err)
	}
	if err := r.ed.Rename(ctx, r.ana, f.ID, "src/main.go", "pkg/util/main.go"); err != nil {
		t.Fatal(err)
	}
	if err := r.ed.Rename(ctx, r.ana, f.ID, "README.md", ".gitignore"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("rename onto existing: %v", err)
	}
	files, err := r.ed.Files(ctx, r.ana, f.ID)
	if err != nil || strings.Join(files, ",") != ".gitignore,README.md,pkg/util/main.go" {
		t.Fatalf("quick open: %v %v", files, err)
	}

	tf, err := r.ed.SetTrusted(ctx, r.ana, f.ID, false)
	if err != nil || tf.Trusted {
		t.Fatalf("%+v %v", tf, err)
	}
	if got, _ := r.ed.Folder(ctx, r.ana, f.ID); got.Trusted {
		t.Fatal("trust revocation not stored")
	}
	if _, err := r.ed.SetTrusted(ctx, r.bob, f.ID, true); !errors.Is(err, model.ErrEditorFolderNotFound) {
		t.Fatalf("another user's trust: %v", err)
	}
}
