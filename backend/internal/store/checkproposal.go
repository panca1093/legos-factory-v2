package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"legos-factory/backend/internal/model"
)

// CheckProposalRepo keeps one pending check-profile suggestion per project.
type CheckProposalRepo struct{ DB *sql.DB }

// Get returns nil when the project has no suggestion row.
func (r *CheckProposalRepo) Get(ctx context.Context, projectID string) (*model.CheckProposalRow, error) {
	var row model.CheckProposalRow
	var at int64
	err := r.DB.QueryRowContext(ctx, `SELECT project_id, status, proposal, error, updated_at FROM check_proposals WHERE project_id = ?`, projectID).
		Scan(&row.ProjectID, &row.Status, &row.Proposal, &row.Error, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.UpdatedAt = time.Unix(at, 0)
	return &row, nil
}

// Begin marks a run started and reports false when one is already running; an earlier proposal stays visible meanwhile.
func (r *CheckProposalRepo) Begin(ctx context.Context, projectID string) (bool, error) {
	res, err := r.DB.ExecContext(ctx, `INSERT INTO check_proposals (project_id, status, updated_at) VALUES (?, 'running', ?)
		ON CONFLICT(project_id) DO UPDATE SET status = 'running', error = '', updated_at = excluded.updated_at WHERE status != 'running'`,
		projectID, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Finish stores a run's outcome: the proposal when errMsg is empty, otherwise the failure with the earlier proposal kept.
func (r *CheckProposalRepo) Finish(ctx context.Context, projectID, proposal, errMsg string) error {
	if errMsg != "" {
		_, err := r.DB.ExecContext(ctx, `UPDATE check_proposals SET status = 'failed', error = ?, updated_at = ? WHERE project_id = ?`,
			errMsg, time.Now().Unix(), projectID)
		return err
	}
	_, err := r.DB.ExecContext(ctx, `UPDATE check_proposals SET status = 'ready', proposal = ?, error = '', updated_at = ? WHERE project_id = ?`,
		proposal, time.Now().Unix(), projectID)
	return err
}

// Delete drops a finished suggestion; it reports false while a run is in flight.
func (r *CheckProposalRepo) Delete(ctx context.Context, projectID string) (bool, error) {
	if _, err := r.DB.ExecContext(ctx, `DELETE FROM check_proposals WHERE project_id = ? AND status != 'running'`, projectID); err != nil {
		return false, err
	}
	row, err := r.Get(ctx, projectID)
	return row == nil, err
}

// FailRunning ends runs a dead process left running.
func (r *CheckProposalRepo) FailRunning(ctx context.Context, reason string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE check_proposals SET status = 'failed', error = ?, updated_at = ? WHERE status = 'running'`, reason, time.Now().Unix())
	return err
}
