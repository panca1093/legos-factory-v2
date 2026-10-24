// Package usage records agent execution cost and enforces local budget
// limits. Where provider pricing is unknown, cost is recorded as an
// estimate with source="estimated" and never presented as billed value.
package usage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type Tracker struct {
	Repo *store.UsageRepo

	mu           sync.Mutex
	limitMicros  int64
	reserved     map[int64]int64 // reservationID -> reserved micros
	totalRes     int64
}

func NewTracker(repo *store.UsageRepo) *Tracker {
	return &Tracker{Repo: repo, reserved: map[int64]int64{}}
}

func (t *Tracker) Record(ctx context.Context, r model.UsageRecord) error {
	if r.ID == "" {
		r.ID = fmt.Sprintf("use_%s_%d", r.Role, time.Now().UnixNano())
	}
	r.CreatedAt = time.Now()
	return t.Repo.Insert(ctx, r)
}

// Summary aggregates usage. source of truth is the usage_records table.
func (t *Tracker) Summary(ctx context.Context) (model.UsageTotals, error) {
	return t.Repo.Totals(ctx)
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
	return t.Repo.SpentMicros(context.Background())
}
