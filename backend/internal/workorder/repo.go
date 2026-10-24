package workorder

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("work order not found")

type Repository struct{ DB *sql.DB }

const woCols = `id, project_id, title, objective, acceptance, state, autonomy,
	builder_model, inspector_model, clarif_question, clarif_answer, revision,
	created_by, created_at, updated_at`

func (r *Repository) Create(ctx context.Context, wo *WorkOrder) error {
	if wo.ID == "" {
		wo.ID = "wo_" + uuid.NewString()[:12]
	}
	now := time.Now()
	wo.CreatedAt, wo.UpdatedAt = now, now
	_, err := r.DB.ExecContext(ctx, `INSERT INTO work_orders
		(id, project_id, title, objective, acceptance, state, autonomy, builder_model, inspector_model,
		 clarif_question, clarif_answer, revision, created_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		wo.ID, wo.ProjectID, wo.Title, wo.Objective, wo.Acceptance, string(wo.State), wo.Autonomy,
		wo.BuilderModel, wo.InspectorModel, wo.ClarifQuestion, wo.ClarifAnswer, wo.Revision,
		wo.CreatedBy, now.Unix(), now.Unix())
	return err
}

func (r *Repository) Get(ctx context.Context, id string) (*WorkOrder, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT `+woCols+` FROM work_orders WHERE id = ?`, id)
	return scanWO(row.Scan)
}

func (r *Repository) List(ctx context.Context) ([]WorkOrder, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT `+woCols+` FROM work_orders ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkOrder
	for rows.Next() {
		wo, err := scanWO(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *wo)
	}
	return out, rows.Err()
}

type rowScanner func(dest ...any) error

func scanWO(scan rowScanner) (*WorkOrder, error) {
	var wo WorkOrder
	var state string
	var createdAt, updatedAt int64
	err := scan(&wo.ID, &wo.ProjectID, &wo.Title, &wo.Objective, &wo.Acceptance, &state, &wo.Autonomy,
		&wo.BuilderModel, &wo.InspectorModel, &wo.ClarifQuestion, &wo.ClarifAnswer, &wo.Revision,
		&wo.CreatedBy, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	wo.State = State(state)
	wo.CreatedAt = time.Unix(createdAt, 0)
	wo.UpdatedAt = time.Unix(updatedAt, 0)
	return &wo, nil
}

// UpdateState persists a state change; the conditional WHERE makes the
// validated transition atomic (a concurrent writer cannot double-apply it).
func (r *Repository) UpdateState(ctx context.Context, id string, s State) error {
	res, err := r.DB.ExecContext(ctx, `UPDATE work_orders SET state = ?, updated_at = ?
		WHERE id = ? AND state NOT IN ('READY','CANCELLED','BLOCKED')`,
		string(s), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateFields applies a partial update (only non-empty strings, revision >= 0).
func (r *Repository) UpdateFields(ctx context.Context, id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	set := ""
	args := []any{}
	for k, v := range fields {
		if set != "" {
			set += ", "
		}
		set += fmt.Sprintf("%s = ?", k)
		args = append(args, v)
	}
	set += ", updated_at = ?"
	args = append(args, time.Now().Unix(), id)
	res, err := r.DB.ExecContext(ctx, `UPDATE work_orders SET `+set+` WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- plan tasks ----

func (r *Repository) ReplacePlan(ctx context.Context, woID string, tasks []Task) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM plan_tasks WHERE work_order_id = ?`, woID); err != nil {
		return err
	}
	now := time.Now().Unix()
	for i, t := range tasks {
		if t.ID == "" {
			t.ID = fmt.Sprintf("task_%s_%d", woID, i+1)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO plan_tasks (id, work_order_id, seq, title, objective, role, created_at)
			VALUES (?,?,?,?,?,?,?)`, t.ID, woID, i, t.Title, t.Objective, t.Role, now); err != nil {
			return err
		}
		for _, d := range t.Deps {
			if _, err := tx.ExecContext(ctx, `INSERT INTO task_deps (task_id, depends_on) VALUES (?,?)`, t.ID, d); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *Repository) Plan(ctx context.Context, woID string) ([]Task, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, work_order_id, seq, title, objective, role FROM plan_tasks
		WHERE work_order_id = ? ORDER BY seq`, woID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	byID := map[string]*Task{}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.WOID, &t.Seq, &t.Title, &t.Objective, &t.Role); err != nil {
			return nil, err
		}
		t.Deps = []string{}
		out = append(out, t)
		byID[t.ID] = &out[len(out)-1]
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	depRows, err := r.DB.QueryContext(ctx, `SELECT task_id, depends_on FROM task_deps
		JOIN plan_tasks ON plan_tasks.id = task_deps.task_id WHERE plan_tasks.work_order_id = ?`, woID)
	if err != nil {
		return nil, err
	}
	defer depRows.Close()
	for depRows.Next() {
		var taskID, dep string
		if err := depRows.Scan(&taskID, &dep); err != nil {
			return nil, err
		}
		if t, ok := byID[taskID]; ok {
			t.Deps = append(t.Deps, dep)
		}
	}
	return out, depRows.Err()
}

// ---- events ----

type Event struct {
	ID        int64          `json:"id"`
	WOID      string         `json:"work_order_id"`
	Actor     string         `json:"actor"`
	Kind      string         `json:"kind"`
	Message   string         `json:"message"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func (r *Repository) AppendEvent(ctx context.Context, woID, actor, kind, message string, payload map[string]any) (*Event, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	raw, _ := json.Marshal(payload)
	now := time.Now()
	res, err := r.DB.ExecContext(ctx, `INSERT INTO wof_events (wo_id, actor, kind, message, payload, created_at)
		VALUES (?,?,?,?,?,?)`, woID, actor, kind, message, string(raw), now.Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Event{ID: id, WOID: woID, Actor: actor, Kind: kind, Message: message, Payload: payload, CreatedAt: now}, nil
}

func (r *Repository) Events(ctx context.Context, woID string, afterID int64, limit int) ([]Event, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, wo_id, actor, kind, message, payload, created_at
		FROM wof_events WHERE wo_id = ? AND id > ? ORDER BY id ASC LIMIT ?`, woID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var payload string
		var createdAt int64
		if err := rows.Scan(&e.ID, &e.WOID, &e.Actor, &e.Kind, &e.Message, &payload, &createdAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(payload), &e.Payload)
		e.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}
