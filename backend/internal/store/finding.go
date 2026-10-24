package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"legos-factory/backend/internal/model"
)

type FindingRepo struct{ DB *sql.DB }

func (r *FindingRepo) Create(ctx context.Context, f model.Finding) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO findings (id, run_id, wo_id, severity, title, detail, location, status, specialty, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, f.ID, f.RunID, f.WOID, f.Severity, f.Title, f.Detail, f.Location, f.Status, f.Specialty, time.Now().Unix())
	return err
}

func (r *FindingRepo) Get(ctx context.Context, woID, id string) (*model.Finding, error) {
	f := model.Finding{WOID: woID}
	var createdAt int64
	err := r.DB.QueryRowContext(ctx, `SELECT id, run_id, severity, title, detail, location, status, specialty, note, decided_by, created_at
		FROM findings WHERE id = ? AND wo_id = ?`, id, woID).Scan(&f.ID, &f.RunID, &f.Severity, &f.Title, &f.Detail, &f.Location,
		&f.Status, &f.Specialty, &f.Note, &f.DecidedBy, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrFindingNotFound
	}
	if err != nil {
		return nil, err
	}
	f.CreatedAt = time.Unix(createdAt, 0)
	return &f, nil
}

func (r *FindingRepo) SetDisposition(ctx context.Context, id, status, note, by string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE findings SET status = ?, note = ?, decided_by = ?, updated_at = ? WHERE id = ?`,
		status, note, by, time.Now().Unix(), id)
	return err
}

func (r *FindingRepo) ResolveOpen(ctx context.Context, woID string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE findings SET status = 'resolved', updated_at = ?
		WHERE wo_id = ? AND status IN ('open','confirmed','escalated')`, time.Now().Unix(), woID)
	return err
}

func (r *FindingRepo) CountOpenBlockers(ctx context.Context, woID string) (int, error) {
	var n int
	err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM findings WHERE wo_id = ? AND severity = 'blocker'
		AND status IN ('open','confirmed','escalated')`, woID).Scan(&n)
	return n, err
}

func (r *FindingRepo) List(ctx context.Context, woID string) ([]model.Finding, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, run_id, severity, title, detail, location, status, specialty, note, decided_by, created_at
		FROM findings WHERE wo_id = ? ORDER BY created_at`, woID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Finding{}
	for rows.Next() {
		f := model.Finding{WOID: woID}
		var createdAt int64
		if err := rows.Scan(&f.ID, &f.RunID, &f.Severity, &f.Title, &f.Detail, &f.Location, &f.Status, &f.Specialty, &f.Note, &f.DecidedBy, &createdAt); err != nil {
			return nil, err
		}
		f.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, f)
	}
	return out, rows.Err()
}
