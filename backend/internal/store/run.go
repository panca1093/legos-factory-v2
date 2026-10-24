package store

import (
	"context"
	"database/sql"
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
		tokens_in = ?, tokens_out = ?, cost_micros = ?, finished_at = ? WHERE id = ?`,
		res.Text, res.SessionRef, res.TokensIn, res.TokensOut, res.CostMicros, time.Now().Unix(), id)
	return err
}

func (r *RunRepo) Fail(ctx context.Context, id, errMsg string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE runs SET state = 'failed', error = ?, finished_at = ? WHERE id = ?`,
		errMsg, time.Now().Unix(), id)
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

func (r *RunRepo) List(ctx context.Context, woID string) ([]model.Run, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, task_id, kind, state, harness,
		output, error, tokens_in, tokens_out, cost_micros, created_at
		FROM runs WHERE work_order_id = ? ORDER BY created_at`, woID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Run{}
	for rows.Next() {
		run := model.Run{WOID: woID}
		var createdAt int64
		if err := rows.Scan(&run.ID, &run.TaskID, &run.Kind, &run.State, &run.Harness, &run.Output, &run.Error,
			&run.TokensIn, &run.TokensOut, &run.CostMicros, &createdAt); err != nil {
			return nil, err
		}
		run.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, run)
	}
	return out, rows.Err()
}
