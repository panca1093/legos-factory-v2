package usecase

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

// UsageTracker records agent execution cost. Where provider pricing is unknown, cost is recorded as an
// estimate with source="estimated" and never presented as billed value.
type UsageTracker struct {
	Repo UsageStore

	mu           sync.Mutex
	reserved     map[int64]int64 // reservationID -> reserved micros
	totalRes     int64
}

func NewUsageTracker(repo UsageStore) *UsageTracker {
	return &UsageTracker{Repo: repo, reserved: map[int64]int64{}}
}

func (t *UsageTracker) Record(ctx context.Context, r model.UsageRecord) error {
	if r.ID == "" {
		r.ID = fmt.Sprintf("use_%s_%d", r.Role, time.Now().UnixNano())
	}
	r.CreatedAt = time.Now()
	return t.Repo.Insert(ctx, r)
}

// Summary aggregates usage. source of truth is the usage_records table.
func (t *UsageTracker) Summary(ctx context.Context) (model.UsageTotals, error) {
	return t.Repo.Totals(ctx)
}

// BudgetStatus reports what is recorded as spent and what running jobs have reserved. There is no spending cap.
func (t *UsageTracker) BudgetStatus() (spent, reserved int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.spentLocked(), t.totalRes
}

// Reserve holds estMicros for a running job; with no cap it always succeeds.
func (t *UsageTracker) Reserve(estMicros int64) (release func(actual int64), ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
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

func (t *UsageTracker) spentLocked() int64 {
	return t.Repo.SpentMicros(context.Background())
}

const (
	defaultSummaryDays = 30
	maxSummaryDays     = 365
)

// UserSummary counts one record per agent run on work orders the user created, bucketed by day in loc.
func (t *UsageTracker) UserSummary(ctx context.Context, userID string, days int, now time.Time, loc *time.Location) (*model.UsageSummary, error) {
	if days <= 0 {
		days = defaultSummaryDays
	}
	days = min(days, maxSummaryDays)
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	start := today.AddDate(0, 0, -(days - 1))
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	since := start
	if monthStart.Before(since) {
		since = monthStart
	}
	recs, err := t.Repo.Since(ctx, userID, since)
	if err != nil {
		return nil, err
	}

	s := &model.UsageSummary{Days: days, From: start.Format(time.DateOnly), To: today.Format(time.DateOnly),
		Activity: make([]model.UsageDay, days), Roles: []model.UsageRole{}}
	s.MonthToDate.Month = monthStart.Format("2006-01")
	idx := make(map[string]int, days)
	for i := range s.Activity {
		d := start.AddDate(0, 0, i).Format(time.DateOnly)
		s.Activity[i].Date = d
		idx[d] = i
	}
	roles := map[string]*model.UsageRole{}
	for _, r := range recs {
		at := r.CreatedAt.In(loc)
		if !at.Before(monthStart) {
			m := &s.MonthToDate
			m.TokensIn, m.TokensOut, m.CostMicros, m.Runs = m.TokensIn+r.TokensIn, m.TokensOut+r.TokensOut, m.CostMicros+r.CostMicros, m.Runs+1
		}
		i, ok := idx[at.Format(time.DateOnly)]
		if !ok {
			continue
		}
		s.Activity[i].Count++
		ro := roles[r.Role]
		if ro == nil {
			ro = &model.UsageRole{Role: r.Role}
			roles[r.Role] = ro
		}
		ro.TokensIn, ro.TokensOut, ro.CostMicros, ro.Runs = ro.TokensIn+r.TokensIn, ro.TokensOut+r.TokensOut, ro.CostMicros+r.CostMicros, ro.Runs+1
	}
	for _, ro := range roles {
		s.Roles = append(s.Roles, *ro)
	}
	sort.Slice(s.Roles, func(a, b int) bool {
		if s.Roles[a].CostMicros != s.Roles[b].CostMicros {
			return s.Roles[a].CostMicros > s.Roles[b].CostMicros
		}
		return s.Roles[a].Role < s.Roles[b].Role
	})
	return s, nil
}
