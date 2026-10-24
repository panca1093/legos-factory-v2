package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	"legos-factory/backend/internal/model"
)

type ProjectRepo struct{ DB *sql.DB }

const pCols = `id, workspace_id, name, description, repo_url, language, quality_profile, git_connection_id, remote_repo, created_at`

func (r *ProjectRepo) Create(ctx context.Context, p *model.Project) error {
	if p.ID == "" {
		p.ID = "prj_" + uuid.NewString()[:12]
	}
	if p.WorkspaceID == "" {
		p.WorkspaceID = model.DefaultWorkspaceID
	}
	p.CreatedAt = time.Now()
	_, err := r.DB.ExecContext(ctx, `INSERT INTO projects (id, workspace_id, name, description, repo_url, language, quality_profile, git_connection_id, remote_repo, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, p.ID, p.WorkspaceID, p.Name, p.Description, p.RepoURL, p.Language, p.QualityProfile,
		nullStr(p.GitConnection), p.RemoteRepo, p.CreatedAt.Unix())
	return err
}

// SetBinding points the project at a git connection and provider repo slug;
// an empty connection id unbinds it.
func (r *ProjectRepo) SetBinding(ctx context.Context, id, connectionID, remoteRepo string) error {
	res, err := r.DB.ExecContext(ctx, `UPDATE projects SET git_connection_id = ?, remote_repo = ? WHERE id = ?`,
		nullStr(connectionID), remoteRepo, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrProjectNotFound
	}
	return nil
}

func (r *ProjectRepo) Get(ctx context.Context, id string) (*model.Project, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT `+pCols+` FROM projects WHERE id = ?`, id)
	return scanP(row.Scan)
}

func (r *ProjectRepo) List(ctx context.Context) ([]model.Project, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT `+pCols+` FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Project
	for rows.Next() {
		p, err := scanP(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func scanP(scan rowScanner) (*model.Project, error) {
	var p model.Project
	var conn sql.NullString
	var createdAt int64
	err := scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Description, &p.RepoURL, &p.Language, &p.QualityProfile, &conn, &p.RemoteRepo, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrProjectNotFound
	}
	if err != nil {
		return nil, err
	}
	p.GitConnection = conn.String
	p.CreatedAt = time.Unix(createdAt, 0)
	return &p, nil
}
