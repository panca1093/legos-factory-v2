package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	"legos-factory/backend/internal/model"
)

type ConnectionRepo struct{ DB *sql.DB }

const cCols = `id, workspace_id, provider, name, scope, owner_id, base_url, username, status,
	account, verified_at, last_error, length(secret_enc) > 0, created_at, updated_at`

// Create stores c with its sealed credential.
func (r *ConnectionRepo) Create(ctx context.Context, c *model.Connection, sealed []byte) error {
	if c.ID == "" {
		c.ID = "conn_" + uuid.NewString()[:12]
	}
	if c.WorkspaceID == "" {
		c.WorkspaceID = model.DefaultWorkspaceID
	}
	if c.Status == "" {
		c.Status = model.ConnectionActive
	}
	now := time.Now()
	c.CreatedAt, c.UpdatedAt, c.HasCredential = now, now, len(sealed) > 0
	_, err := r.DB.ExecContext(ctx, `INSERT INTO connections
		(id, workspace_id, provider, name, scope, owner_id, base_url, username, secret_enc, status, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.WorkspaceID, c.Provider, c.Name, c.Scope, c.OwnerID, c.BaseURL, c.Username, sealed, c.Status,
		now.Unix(), now.Unix())
	return err
}

func (r *ConnectionRepo) Get(ctx context.Context, id string) (*model.Connection, error) {
	return scanC(r.DB.QueryRowContext(ctx, `SELECT `+cCols+` FROM connections WHERE id = ?`, id).Scan)
}

// Secret returns the sealed credential; callers open it with the SecretBox.
func (r *ConnectionRepo) Secret(ctx context.Context, id string) ([]byte, error) {
	var sealed []byte
	err := r.DB.QueryRowContext(ctx, `SELECT secret_enc FROM connections WHERE id = ?`, id).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrConnectionNotFound
	}
	return sealed, err
}

// Visible lists shared connections plus userID's personal ones.
func (r *ConnectionRepo) Visible(ctx context.Context, userID string) ([]model.Connection, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT `+cCols+` FROM connections
		WHERE scope = 'shared' OR owner_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Connection{}
	for rows.Next() {
		c, err := scanC(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *ConnectionRepo) Rename(ctx context.Context, id, name string) error {
	return r.exec(ctx, `UPDATE connections SET name = ?, updated_at = ? WHERE id = ?`, name, time.Now().Unix(), id)
}

func (r *ConnectionRepo) SetStatus(ctx context.Context, id, status string) error {
	return r.exec(ctx, `UPDATE connections SET status = ?, updated_at = ? WHERE id = ?`, status, time.Now().Unix(), id)
}

// RotateSecret replaces the credential and clears verification, since the
// new token has not been checked yet.
func (r *ConnectionRepo) RotateSecret(ctx context.Context, id string, sealed []byte) error {
	return r.exec(ctx, `UPDATE connections SET secret_enc = ?, verified_at = NULL, account = '', last_error = '', updated_at = ?
		WHERE id = ?`, sealed, time.Now().Unix(), id)
}

func (r *ConnectionRepo) RecordCheck(ctx context.Context, id, account, checkErr string) error {
	now := time.Now().Unix()
	if checkErr != "" {
		return r.exec(ctx, `UPDATE connections SET verified_at = NULL, last_error = ?, updated_at = ? WHERE id = ?`, checkErr, now, id)
	}
	return r.exec(ctx, `UPDATE connections SET verified_at = ?, account = ?, last_error = '', updated_at = ? WHERE id = ?`,
		now, account, now, id)
}

func (r *ConnectionRepo) Delete(ctx context.Context, id string) error {
	return r.exec(ctx, `DELETE FROM connections WHERE id = ?`, id)
}

func (r *ConnectionRepo) exec(ctx context.Context, q string, args ...any) error {
	res, err := r.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrConnectionNotFound
	}
	return nil
}

func scanC(scan rowScanner) (*model.Connection, error) {
	var c model.Connection
	var verifiedAt sql.NullInt64
	var createdAt, updatedAt int64
	err := scan(&c.ID, &c.WorkspaceID, &c.Provider, &c.Name, &c.Scope, &c.OwnerID, &c.BaseURL, &c.Username, &c.Status,
		&c.Account, &verifiedAt, &c.LastError, &c.HasCredential, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrConnectionNotFound
	}
	if err != nil {
		return nil, err
	}
	if verifiedAt.Valid {
		t := time.Unix(verifiedAt.Int64, 0)
		c.VerifiedAt = &t
	}
	c.CreatedAt, c.UpdatedAt = time.Unix(createdAt, 0), time.Unix(updatedAt, 0)
	return &c, nil
}
