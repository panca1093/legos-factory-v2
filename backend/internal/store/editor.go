package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"legos-factory/backend/internal/model"
)

type EditorRepo struct{ DB *sql.DB }

const efCols = `id, user_id, path, kind, project_id, wo_id, task_id, opened_at`

func scanFolder(row rowScanner) (*model.EditorFolder, error) {
	var f model.EditorFolder
	var opened int64
	if err := row(&f.ID, &f.UserID, &f.Path, &f.Kind, &f.ProjectID, &f.WOID, &f.TaskID, &opened); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrEditorFolderNotFound
		}
		return nil, err
	}
	f.OpenedAt = time.Unix(opened, 0)
	return &f, nil
}

// Folder returns the user's own folder; another user's id is not found.
func (r *EditorRepo) Folder(ctx context.Context, userID, id string) (*model.EditorFolder, error) {
	return scanFolder(r.DB.QueryRowContext(ctx, `SELECT `+efCols+` FROM editor_folders WHERE user_id = ? AND id = ?`, userID, id).Scan)
}

// UpsertFolder registers f by (user, path). A reopened folder keeps its id.
func (r *EditorRepo) UpsertFolder(ctx context.Context, f model.EditorFolder) (*model.EditorFolder, error) {
	if f.ID == "" {
		f.ID = "fld_" + uuid.NewString()[:12]
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO editor_folders (`+efCols+`) VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(user_id, path) DO UPDATE SET kind = excluded.kind, project_id = excluded.project_id,
		 wo_id = excluded.wo_id, task_id = excluded.task_id, opened_at = excluded.opened_at`,
		f.ID, f.UserID, f.Path, f.Kind, f.ProjectID, f.WOID, f.TaskID, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("upsert editor folder: %w", err)
	}
	return scanFolder(r.DB.QueryRowContext(ctx, `SELECT `+efCols+` FROM editor_folders WHERE user_id = ? AND path = ?`, f.UserID, f.Path).Scan)
}

func (r *EditorRepo) DeleteFolder(ctx context.Context, userID, id string) error {
	_, err := r.DB.ExecContext(ctx, `DELETE FROM editor_folders WHERE user_id = ? AND id = ?`, userID, id)
	return err
}
