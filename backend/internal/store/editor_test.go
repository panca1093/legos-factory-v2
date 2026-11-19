package store

import (
	"context"
	"errors"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestEditorFoldersAreScopedToTheirUser(t *testing.T) {
	r := &EditorRepo{DB: openTestDB(t)}
	ctx := context.Background()

	a, err := r.UpsertFolder(ctx, model.EditorFolder{UserID: "ana", Path: "/work/p", Kind: model.FolderProject, ProjectID: "p1", Trusted: true})
	if err != nil || a.ID == "" || !a.Trusted || a.Kind != model.FolderProject || a.ProjectID != "p1" {
		t.Fatalf("%+v %v", a, err)
	}
	got, err := r.Folder(ctx, "ana", a.ID)
	if err != nil || got.Path != "/work/p" || got.UserID != "ana" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := r.Folder(ctx, "bob", a.ID); !errors.Is(err, model.ErrEditorFolderNotFound) {
		t.Fatalf("another user's folder must be not found: %v", err)
	}
	if _, err := r.Folder(ctx, "ana", "fld_missing"); !errors.Is(err, model.ErrEditorFolderNotFound) {
		t.Fatal(err)
	}

	b, err := r.UpsertFolder(ctx, model.EditorFolder{UserID: "bob", Path: "/work/p", Kind: model.FolderPlain})
	if err != nil || b.ID == a.ID || b.Trusted {
		t.Fatalf("same path for a second user is its own row: %+v %v", b, err)
	}
	if err := r.DeleteFolder(ctx, "bob", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Folder(ctx, "ana", a.ID); err != nil {
		t.Fatalf("another user's delete must not remove it: %v", err)
	}
}

func TestEditorUpsertKeepsIDAndTrust(t *testing.T) {
	r := &EditorRepo{DB: openTestDB(t)}
	ctx := context.Background()
	first, err := r.UpsertFolder(ctx, model.EditorFolder{UserID: "ana", Path: "/x", Kind: model.FolderPlain})
	if err != nil || first.Trusted {
		t.Fatalf("%+v %v", first, err)
	}
	if err := r.SetTrusted(ctx, "ana", first.ID, true); err != nil {
		t.Fatal(err)
	}
	again, err := r.UpsertFolder(ctx, model.EditorFolder{UserID: "ana", Path: "/x", Kind: model.FolderPlain})
	if err != nil || again.ID != first.ID || !again.Trusted {
		t.Fatalf("reopen must keep id and trust: %+v %v", again, err)
	}
	if err := r.SetTrusted(ctx, "ana", first.ID, false); err != nil {
		t.Fatal(err)
	}
	if f, _ := r.Folder(ctx, "ana", first.ID); f.Trusted {
		t.Fatal("trust not revoked")
	}
	if err := r.SetTrusted(ctx, "bob", first.ID, true); !errors.Is(err, model.ErrEditorFolderNotFound) {
		t.Fatalf("trusting another user's folder: %v", err)
	}
}

func TestEditorServerPaths(t *testing.T) {
	r := &EditorRepo{DB: openTestDB(t)}
	ctx := context.Background()
	if p, err := r.ServerPath(ctx, "ana", "gopls"); err != nil || p != "" {
		t.Fatalf("%q %v", p, err)
	}
	for _, p := range []string{"/a/gopls", "/b/gopls"} {
		if err := r.SetServerPath(ctx, "ana", "gopls", p); err != nil {
			t.Fatal(err)
		}
	}
	_ = r.SetServerPath(ctx, "ana", "tsls", "/t")
	_ = r.SetServerPath(ctx, "bob", "gopls", "/bob")
	m, err := r.ServerPaths(ctx, "ana")
	if err != nil || len(m) != 2 || m["gopls"] != "/b/gopls" || m["tsls"] != "/t" {
		t.Fatalf("%v %v", m, err)
	}
	if err := r.SetServerPath(ctx, "ana", "gopls", ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := r.ServerPath(ctx, "ana", "gopls"); p != "" {
		t.Fatalf("empty path clears the entry, got %q", p)
	}
}
