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

	a, err := r.UpsertFolder(ctx, model.EditorFolder{UserID: "ana", Path: "/work/p", Kind: model.FolderProject, ProjectID: "p1"})
	if err != nil || a.ID == "" || a.Kind != model.FolderProject || a.ProjectID != "p1" {
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
	if err != nil || b.ID == a.ID {
		t.Fatalf("same path for a second user is its own row: %+v %v", b, err)
	}
	if err := r.DeleteFolder(ctx, "bob", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Folder(ctx, "ana", a.ID); err != nil {
		t.Fatalf("another user's delete must not remove it: %v", err)
	}
}

func TestEditorUpsertKeepsID(t *testing.T) {
	r := &EditorRepo{DB: openTestDB(t)}
	ctx := context.Background()
	first, err := r.UpsertFolder(ctx, model.EditorFolder{UserID: "ana", Path: "/x", Kind: model.FolderPlain})
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.UpsertFolder(ctx, model.EditorFolder{UserID: "ana", Path: "/x", Kind: model.FolderPlain})
	if err != nil || again.ID != first.ID {
		t.Fatalf("reopen must keep id: %+v %v", again, err)
	}
}
