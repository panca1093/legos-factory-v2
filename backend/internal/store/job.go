package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"legos-factory/backend/internal/model"
)

type JobRepo struct{ DB *sql.DB }

func (r *JobRepo) Insert(ctx context.Context, j model.Job) error {
	raw, _ := json.Marshal(j.Payload)
	now := time.Now()
	_, err := r.DB.ExecContext(ctx, `INSERT INTO jobs (id, kind, wo_id, task_id, payload, state, attempts, max_attempts, run_after, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.Kind, j.WOID, nullStr(j.TaskID), string(raw), string(model.JobPending), 0, j.MaxAttempts, 0, now.Unix(), now.Unix())
	return err
}

// FailOrphans marks CLAIMED/RUNNING jobs left by a dead process as FAILED.
func (r *JobRepo) FailOrphans(ctx context.Context, reason string) (int64, error) {
	res, err := r.DB.ExecContext(ctx, `UPDATE jobs SET state = ?, error = ?, updated_at = ?
		WHERE state IN ('CLAIMED','RUNNING')`,
		string(model.JobFailed), reason, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClaimNext atomically moves the oldest eligible PENDING job to CLAIMED.
// ok is false when nothing could be claimed.
func (r *JobRepo) ClaimNext(ctx context.Context, claimedBy string) (job model.Job, ok bool) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, false
	}
	defer tx.Rollback()
	_, _ = tx.ExecContext(ctx, `SELECT 1`) // establish read snapshot

	row := tx.QueryRowContext(ctx, `SELECT id, kind, wo_id, task_id, payload, attempts, max_attempts
		FROM jobs WHERE state = 'PENDING' AND run_after <= ? ORDER BY created_at LIMIT 1`, time.Now().Unix())
	var j model.Job
	var taskID sql.NullString
	var payload string
	if err := row.Scan(&j.ID, &j.Kind, &j.WOID, &taskID, &payload, &j.Attempts, &j.MaxAttempts); err != nil {
		return model.Job{}, false
	}
	j.TaskID = taskID.String

	// atomic claim: conditional update protects against concurrent claimers
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET state = ?, claimed_by = ?, updated_at = ?
		WHERE id = ? AND state = 'PENDING'`,
		string(model.JobClaimed), claimedBy, time.Now().Unix(), j.ID)
	if err != nil {
		return model.Job{}, false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.Job{}, false
	}
	if err := tx.Commit(); err != nil {
		return model.Job{}, false
	}
	_ = json.Unmarshal([]byte(payload), &j.Payload)
	j.State = model.JobClaimed
	return j, true
}

func (r *JobRepo) Attempts(ctx context.Context, id string) int {
	var attempts int
	_ = r.DB.QueryRowContext(ctx, `SELECT attempts FROM jobs WHERE id = ?`, id).Scan(&attempts)
	return attempts
}

func (r *JobRepo) Requeue(ctx context.Context, id string, runAfter time.Time, errMsg string) {
	_, _ = r.DB.ExecContext(ctx, `UPDATE jobs SET state = 'PENDING', attempts = attempts + 1,
		run_after = ?, error = ?, updated_at = ? WHERE id = ?`,
		runAfter.Unix(), errMsg, time.Now().Unix(), id)
}

func (r *JobRepo) SetState(ctx context.Context, id string, state model.JobState) {
	_, _ = r.DB.ExecContext(ctx, `UPDATE jobs SET state = ?, updated_at = ? WHERE id = ?`,
		string(state), time.Now().Unix(), id)
}

func (r *JobRepo) Finish(ctx context.Context, id string, state model.JobState, errMsg string) {
	_, _ = r.DB.ExecContext(ctx, `UPDATE jobs SET state = ?, error = ?, updated_at = ? WHERE id = ?`,
		string(state), errMsg, time.Now().Unix(), id)
}

// CancelWO cancels all pending/claimed jobs of a work order.
func (r *JobRepo) CancelWO(ctx context.Context, woID string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE jobs SET state = 'CANCELLED', updated_at = ?
		WHERE wo_id = ? AND state IN ('PENDING','CLAIMED')`, time.Now().Unix(), woID)
	return err
}

func (r *JobRepo) HasActive(ctx context.Context, woID, taskID, kind string) bool {
	var n int
	err := r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE wo_id = ? AND kind = ?
		AND state IN ('PENDING','CLAIMED','RUNNING') AND (task_id IS ? OR (? = '' AND task_id IS NULL))`,
		woID, kind, nullStr(taskID), taskID).Scan(&n)
	return err == nil && n > 0
}

// CountPendingOrRunning and CountFailed feed the overview metrics.
func (r *JobRepo) CountPendingOrRunning(ctx context.Context) int {
	var n int
	_ = r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE state IN ('PENDING','RUNNING')`).Scan(&n)
	return n
}

func (r *JobRepo) CountFailed(ctx context.Context) int {
	var n int
	_ = r.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE state = 'FAILED'`).Scan(&n)
	return n
}
