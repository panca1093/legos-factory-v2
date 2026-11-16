package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"legos-factory/backend/internal/model"
)

type MCPRepo struct{ DB *sql.DB }

const mcpCols = `id, name, transport, command, args, url, auth, roles, tools, status, last_error, discovered_at, created_at, updated_at`

func (r *MCPRepo) Create(ctx context.Context, s *model.MCPServerRec, sealed []byte) error {
	if s.ID == "" {
		s.ID = "mcp_" + uuid.NewString()[:12]
	}
	now := time.Now()
	s.CreatedAt, s.UpdatedAt = now, now
	args, roles, tools := mcpJSON(s)
	if sealed == nil {
		sealed = []byte{}
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO mcp_servers
		(id, name, transport, command, args, url, auth, secret_enc, roles, tools, status, last_error, discovered_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Name, s.Transport, s.Command, args, s.URL, s.Auth, sealed, roles, tools, s.Status, s.LastError,
		unixOrNil(s.DiscoveredAt), now.Unix(), now.Unix())
	return err
}

// Update writes every field of s; a nil sealed keeps the stored secret.
func (r *MCPRepo) Update(ctx context.Context, s *model.MCPServerRec, sealed []byte) error {
	s.UpdatedAt = time.Now()
	args, roles, tools := mcpJSON(s)
	var secret any
	if sealed != nil {
		secret = sealed
	}
	res, err := r.DB.ExecContext(ctx, `UPDATE mcp_servers SET name=?, command=?, args=?, url=?, auth=?,
		secret_enc = COALESCE(?, secret_enc), roles=?, tools=?, status=?, last_error=?, discovered_at=?, updated_at=? WHERE id=?`,
		s.Name, s.Command, args, s.URL, s.Auth, secret, roles, tools, s.Status, s.LastError,
		unixOrNil(s.DiscoveredAt), s.UpdatedAt.Unix(), s.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrMCPNotFound
	}
	return nil
}

func (r *MCPRepo) Get(ctx context.Context, id string) (*model.MCPServerRec, error) {
	return scanMCP(r.DB.QueryRowContext(ctx, `SELECT `+mcpCols+` FROM mcp_servers WHERE id = ?`, id).Scan)
}

func (r *MCPRepo) List(ctx context.Context) ([]model.MCPServerRec, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT `+mcpCols+` FROM mcp_servers ORDER BY created_at, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.MCPServerRec{}
	for rows.Next() {
		s, err := scanMCP(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// Secret returns the sealed credentials; callers open them with the Sealer.
func (r *MCPRepo) Secret(ctx context.Context, id string) ([]byte, error) {
	var sealed []byte
	err := r.DB.QueryRowContext(ctx, `SELECT secret_enc FROM mcp_servers WHERE id = ?`, id).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrMCPNotFound
	}
	return sealed, err
}

func (r *MCPRepo) Delete(ctx context.Context, id string) error {
	res, err := r.DB.ExecContext(ctx, `DELETE FROM mcp_servers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrMCPNotFound
	}
	return nil
}

func mcpJSON(s *model.MCPServerRec) (args, roles, tools []byte) {
	if s.Args == nil {
		s.Args = []string{}
	}
	if s.Roles == nil {
		s.Roles = []string{}
	}
	if s.Tools == nil {
		s.Tools = []model.MCPTool{}
	}
	args, _ = json.Marshal(s.Args)
	roles, _ = json.Marshal(s.Roles)
	tools, _ = json.Marshal(s.Tools)
	return
}

func scanMCP(scan rowScanner) (*model.MCPServerRec, error) {
	var s model.MCPServerRec
	var args, roles, tools string
	var disc sql.NullInt64
	var created, updated int64
	err := scan(&s.ID, &s.Name, &s.Transport, &s.Command, &args, &s.URL, &s.Auth, &roles, &tools, &s.Status, &s.LastError, &disc, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrMCPNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := errors.Join(json.Unmarshal([]byte(args), &s.Args), json.Unmarshal([]byte(roles), &s.Roles), json.Unmarshal([]byte(tools), &s.Tools)); err != nil {
		return nil, err
	}
	if disc.Valid {
		t := time.Unix(disc.Int64, 0)
		s.DiscoveredAt = &t
	}
	s.CreatedAt, s.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	return &s, nil
}
