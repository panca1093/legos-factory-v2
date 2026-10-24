// Package usage records agent execution cost and enforces local budget
// limits. Where provider pricing is unknown, cost is recorded as an
// estimate with source="estimated" and never presented as billed value.
package usage

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

type Record struct {
	ID         string `json:"id"`
	WOID       string `json:"work_order_id,omitempty"`
	UserID     string `json:"user_id,omitempty"`
	Role       string `json:"role"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	TokensIn   int64  `json:"tokens_in"`
	TokensOut  int64  `json:"tokens_out"`
	CostMicros int64  `json:"cost_micros"`
	Source     string `json:"source"` // measured|estimated|unknown
	CreatedAt  time.Time `json:"created_at"`
}

type Tracker struct {
	DB *sql.DB

	mu           sync.Mutex
	limitMicros  int64
	reserved     map[int64]int64 // reservationID -> reserved micros
	totalRes     int64
}

func NewTracker(db *sql.DB) *Tracker {
	return &Tracker{DB: db, reserved: map[int64]int64{}}
}

func (t *Tracker) Record(ctx context.Context, r Record) error {
	if r.ID == "" {
		r.ID = fmt.Sprintf("use_%s_%d", r.Role, time.Now().UnixNano())
	}
	r.CreatedAt = time.Now()
	_, err := t.DB.ExecContext(ctx, `INSERT INTO usage_records (id, wo_id, user_id, role, provider, model, tokens_in, tokens_out, cost_micros, source, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, nullStr(r.WOID), nullStr(r.UserID), r.Role, r.Provider, r.Model,
		r.TokensIn, r.TokensOut, r.CostMicros, r.Source, r.CreatedAt.Unix())
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Summary aggregates usage. source of truth is the usage_records table.
func (t *Tracker) Summary(ctx context.Context) (map[string]any, error) {
	var totalMicros, totalIn, totalOut int64
	err := t.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_micros),0), COALESCE(SUM(tokens_in),0), COALESCE(SUM(tokens_out),0)
		FROM usage_records`).Scan(&totalMicros, &totalIn, &totalOut)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"cost_micros": totalMicros,
		"tokens_in":   totalIn,
		"tokens_out":  totalOut,
	}, nil
}

// SetBudgetLimit sets the workspace spending cap in cost micros (0 = unset).
func (t *Tracker) SetBudgetLimit(micros int64) {
	t.mu.Lock()
	t.limitMicros = micros
	t.mu.Unlock()
}

// BudgetStatus reports limit, spent (recorded), reserved, and available.
func (t *Tracker) BudgetStatus() (limit, spent, reserved, available int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	spent = t.spentLocked()
	limit = t.limitMicros
	reserved = t.totalRes
	if limit == 0 {
		available = 1 << 62
	} else {
		available = limit - spent - reserved
	}
	return
}

// Reserve tries to reserve estMicros against the remaining budget. The
// reservation is held in memory (single-process MVP); it prevents parallel
// jobs from independently consuming the same remaining budget.
func (t *Tracker) Reserve(estMicros int64) (release func(actual int64), ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	spent := t.spentLocked()
	available := int64(1 << 62)
	if t.limitMicros > 0 {
		available = t.limitMicros - spent - t.totalRes
	}
	if estMicros > available {
		return nil, false
	}
	id := time.Now().UnixNano()
	t.reserved[id] = estMicros
	t.totalRes += estMicros
	return func(actual int64) {
		t.mu.Lock()
		defer t.mu.Unlock()
		if v, ok := t.reserved[id]; ok {
			delete(t.reserved, id)
			t.totalRes -= v
		}
	}, true
}

func (t *Tracker) spentLocked() int64 {
	var spent int64
	_ = t.DB.QueryRow(`SELECT COALESCE(SUM(cost_micros),0) FROM usage_records`).Scan(&spent)
	return spent
}
