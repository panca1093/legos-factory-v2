package app

import (
	"context"
	"database/sql"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/sandbox"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

type editorBackend interface {
	Folder(ctx context.Context, userID, id string) (*model.EditorFolder, error)
	ServerPaths(ctx context.Context, userID string) (map[string]string, error)
	SetServerPath(ctx context.Context, userID, server, path string) error
}

// editorPorts returns the folder store and filesystem adapter of the files builder (ed/files). Until that branch
// merges both are nil and the live editor stays off (its routes answer 404 NOT_AVAILABLE).
// Merge: return &store.EditorRepo{DB: db}, &sandbox.EditorFS{}.
func editorPorts(db *sql.DB) (editorBackend, usecase.EditorFS) { return nil, nil }

func newEditorLive(db *sql.DB, runs *store.RunRepo) *usecase.EditorLive {
	repo, fs := editorPorts(db)
	if repo == nil || fs == nil {
		return nil
	}
	host := &sandbox.LSPHost{}
	return &usecase.EditorLive{
		Folders: repo, Store: repo, FS: fs, Runs: runs, Host: host, Servers: sandbox.LSPAllowlist,
		Reg: &usecase.LSPRegistry{Host: host},
	}
}
