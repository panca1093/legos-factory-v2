package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"legos-factory/backend/internal/model"
)

type RunRepo struct{ DB *sql.DB }

func (r *RunRepo) Start(ctx context.Context, id, woID, taskID, kind, harness string) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO runs (id, work_order_id, task_id, kind, state, harness, created_at)
		VALUES (?,?,?,?,?,?,?)`, id, woID, nullStr(taskID), kind, "running", harness, time.Now().Unix())
	return err
}

func (r *RunRepo) Complete(ctx context.Context, id string, res model.AgentResult) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE runs SET state = 'completed', output = ?, session_ref = ?,
		tokens_in = ?, tokens_out = ?, cache_read_tokens = ?, cache_write_tokens = ?, cost_micros = ?, finished_at = ? WHERE id = ?`,
		res.Text, res.SessionRef, res.TokensIn, res.TokensOut, res.CacheRead, res.CacheWrite, res.CostMicros, time.Now().Unix(), id)
	return err
}

func (r *RunRepo) Fail(ctx context.Context, id, errMsg string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE runs SET state = 'failed', error = ?, finished_at = ? WHERE id = ?`,
		errMsg, time.Now().Unix(), id)
	return err
}

// Wait closes a builder run that ended on a question; the task is not done and shows as waiting.
func (r *RunRepo) Wait(ctx context.Context, id, question string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE runs SET state = 'waiting', error = ?, finished_at = ? WHERE id = ?`,
		question, time.Now().Unix(), id)
	return err
}

// CompletedBuilderTasks returns the plan task ids that have a completed builder run.
func (r *RunRepo) CompletedBuilderTasks(ctx context.Context, woID string) (map[string]bool, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT DISTINCT task_id FROM runs
		WHERE work_order_id = ? AND kind = 'builder' AND state = 'completed' AND task_id IS NOT NULL`, woID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	done := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		done[id] = true
	}
	return done, rows.Err()
}

// LatestRemediationState is the state of the newest builder run without a plan task, or "" when there is none.
func (r *RunRepo) LatestRemediationState(ctx context.Context, woID string) (string, error) {
	var state string
	err := r.DB.QueryRowContext(ctx, `SELECT state FROM runs
		WHERE work_order_id = ? AND kind = 'builder' AND task_id IS NULL
		ORDER BY created_at DESC, id DESC LIMIT 1`, woID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return state, err
}

func (r *RunRepo) List(ctx context.Context, woID string) ([]model.Run, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, task_id, kind, state, harness,
		output, error, tokens_in, tokens_out, cache_read_tokens, cache_write_tokens, cost_micros, created_at, finished_at
		FROM runs WHERE work_order_id = ? ORDER BY created_at`, woID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Run{}
	for rows.Next() {
		run := model.Run{WOID: woID}
		var createdAt int64
		var finishedAt sql.NullInt64
		if err := rows.Scan(&run.ID, &run.TaskID, &run.Kind, &run.State, &run.Harness, &run.Output, &run.Error,
			&run.TokensIn, &run.TokensOut, &run.CacheRead, &run.CacheWrite, &run.CostMicros, &createdAt, &finishedAt); err != nil {
			return nil, err
		}
		run.CreatedAt = time.Unix(createdAt, 0)
		if finishedAt.Valid {
			t := time.Unix(finishedAt.Int64, 0)
			run.FinishedAt = &t
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
