package store

import (
	"context"
	"database/sql"
	"time"

	"legos-factory/backend/internal/model"
)

type MemberRepo struct{ DB *sql.DB }

func (r *MemberRepo) Add(ctx context.Context, projectID, userID string) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO project_members (project_id, user_id, created_at) VALUES (?,?,?)
		ON CONFLICT(project_id, user_id) DO NOTHING`, projectID, userID, time.Now().Unix())
	return err
}

func (r *MemberRepo) Remove(ctx context.Context, projectID, userID string) error {
	_, err := r.DB.ExecContext(ctx, `DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, userID)
	return err
}

func (r *MemberRepo) IsMember(ctx context.Context, projectID, userID string) (bool, error) {
	var n int
	err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_members WHERE project_id = ? AND user_id = ?`,
		projectID, userID).Scan(&n)
	return n > 0, err
}

func (r *MemberRepo) ProjectIDsFor(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT project_id FROM project_members WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *MemberRepo) List(ctx context.Context, projectID string) ([]model.ProjectMember, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT m.project_id, u.id, u.email, u.name, u.role, m.created_at
		FROM project_members m JOIN users u ON u.id = m.user_id WHERE m.project_id = ? ORDER BY m.created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ProjectMember{}
	for rows.Next() {
		var m model.ProjectMember
		var role string
		var createdAt int64
		if err := rows.Scan(&m.ProjectID, &m.UserID, &m.Email, &m.Name, &role, &createdAt); err != nil {
			return nil, err
		}
		m.Role = model.Role(role)
		m.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}
