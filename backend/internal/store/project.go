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

// SetLanguage records the toolchains detected in the project's repository.
func (r *ProjectRepo) SetLanguage(ctx context.Context, id, language string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE projects SET language = ? WHERE id = ?`, language, id)
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

// DeleteWithOrders removes the project and the given orders in one transaction,
// so a refusal (changed or running order, or an order created meanwhile) removes
// nothing. Members, knowledge and KB docs cascade, chats keep their rows with
// project_id cleared.
func (r *ProjectRepo) DeleteWithOrders(ctx context.Context, id string, orders []model.WorkOrder) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := range orders {
		if err := deleteSnapshot(ctx, tx, &orders[i]); err != nil && !errors.Is(err, model.ErrWorkOrderNotFound) {
			return err
		}
	}
	var left int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_orders WHERE project_id = ?`, id).Scan(&left); err != nil {
		return err
	}
	if left > 0 {
		return fmt.Errorf("%w: project %s gained %d work order(s) meanwhile; try again", model.ErrStateConflict, id, left)
	}
	// the contentless FTS table is not reached by FK cascades
	if _, err := tx.ExecContext(ctx, `DELETE FROM kb_fts WHERE rowid IN (SELECT m.fts_rowid FROM kb_fts_map m
		JOIN kb_docs d ON d.id = m.doc_id WHERE d.project_id = ?)`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrProjectNotFound
	}
	return tx.Commit()
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
