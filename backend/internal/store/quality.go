package store

import (
	"context"
	"database/sql"
	"time"

	"legos-factory/backend/internal/model"
)

type QualityRepo struct{ DB *sql.DB }

func (r *QualityRepo) Save(ctx context.Context, c model.QualityCheck) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO quality_checks (id, wo_id, name, command, outcome, exit_code, output, revision, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		c.ID, c.WOID, c.Name, c.Command, c.Outcome, c.ExitCode, c.Output, c.Revision, time.Now().Unix())
	return err
}

func (r *QualityRepo) Count(ctx context.Context, woID string) (int, error) {
	var n int
	err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM quality_checks WHERE wo_id = ?`, woID).Scan(&n)
	return n, err
}

func (r *QualityRepo) CountForRevision(ctx context.Context, woID string, revision int) (int, error) {
	var n int
	err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM quality_checks WHERE wo_id = ? AND revision = ?`,
		woID, revision).Scan(&n)
	return n, err
}

func (r *QualityRepo) List(ctx context.Context, woID string) ([]model.QualityCheck, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, name, command, outcome, exit_code, output, revision, created_at
		FROM quality_checks WHERE wo_id = ? ORDER BY created_at`, woID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.QualityCheck{}
	for rows.Next() {
		c := model.QualityCheck{WOID: woID}
		var createdAt int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Command, &c.Outcome, &c.ExitCode, &c.Output, &c.Revision, &createdAt); err != nil {
			return nil, err
		}
		c.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}
