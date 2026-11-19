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

const efCols = `id, user_id, path, kind, project_id, wo_id, task_id, trusted, opened_at`

func scanFolder(row rowScanner) (*model.EditorFolder, error) {
	var f model.EditorFolder
	var trusted int
	var opened int64
	if err := row(&f.ID, &f.UserID, &f.Path, &f.Kind, &f.ProjectID, &f.WOID, &f.TaskID, &trusted, &opened); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrEditorFolderNotFound
		}
		return nil, err
	}
	f.Trusted = trusted != 0
	f.OpenedAt = time.Unix(opened, 0)
	return &f, nil
}

// Folder returns the user's own folder; another user's id is not found.
func (r *EditorRepo) Folder(ctx context.Context, userID, id string) (*model.EditorFolder, error) {
	return scanFolder(r.DB.QueryRowContext(ctx, `SELECT `+efCols+` FROM editor_folders WHERE user_id = ? AND id = ?`, userID, id).Scan)
}

// UpsertFolder registers f by (user, path). A reopened folder keeps its id and never loses trust.
func (r *EditorRepo) UpsertFolder(ctx context.Context, f model.EditorFolder) (*model.EditorFolder, error) {
	if f.ID == "" {
		f.ID = "fld_" + uuid.NewString()[:12]
	}
	trusted := 0
	if f.Trusted {
		trusted = 1
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO editor_folders (`+efCols+`) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(user_id, path) DO UPDATE SET kind = excluded.kind, project_id = excluded.project_id,
		 wo_id = excluded.wo_id, task_id = excluded.task_id, opened_at = excluded.opened_at,
		 trusted = MAX(editor_folders.trusted, excluded.trusted)`,
		f.ID, f.UserID, f.Path, f.Kind, f.ProjectID, f.WOID, f.TaskID, trusted, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("upsert editor folder: %w", err)
	}
	return scanFolder(r.DB.QueryRowContext(ctx, `SELECT `+efCols+` FROM editor_folders WHERE user_id = ? AND path = ?`, f.UserID, f.Path).Scan)
}

func (r *EditorRepo) DeleteFolder(ctx context.Context, userID, id string) error {
	_, err := r.DB.ExecContext(ctx, `DELETE FROM editor_folders WHERE user_id = ? AND id = ?`, userID, id)
	return err
}

func (r *EditorRepo) SetTrusted(ctx context.Context, userID, id string, trusted bool) error {
	v := 0
	if trusted {
		v = 1
	}
	res, err := r.DB.ExecContext(ctx, `UPDATE editor_folders SET trusted = ? WHERE user_id = ? AND id = ?`, v, userID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrEditorFolderNotFound
	}
	return nil
}

// ServerPath is the user's confirmed custom binary for a language server, "" when none.
func (r *EditorRepo) ServerPath(ctx context.Context, userID, server string) (string, error) {
	var p string
	err := r.DB.QueryRowContext(ctx, `SELECT path FROM editor_server_paths WHERE user_id = ? AND server = ?`, userID, server).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return p, err
}

func (r *EditorRepo) SetServerPath(ctx context.Context, userID, server, path string) error {
	if path == "" {
		_, err := r.DB.ExecContext(ctx, `DELETE FROM editor_server_paths WHERE user_id = ? AND server = ?`, userID, server)
		return err
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO editor_server_paths (user_id, server, path, confirmed_at) VALUES (?,?,?,?)
		ON CONFLICT(user_id, server) DO UPDATE SET path = excluded.path, confirmed_at = excluded.confirmed_at`,
		userID, server, path, time.Now().Unix())
	return err
}

func (r *EditorRepo) ServerPaths(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT server, path FROM editor_server_paths WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var s, p string
		if err := rows.Scan(&s, &p); err != nil {
			return nil, err
		}
		out[s] = p
	}
	return out, rows.Err()
}
