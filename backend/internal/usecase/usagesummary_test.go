package usecase

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestUserSummary(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	repo := &store.UsageRepo{DB: db}
	ctx := context.Background()
	jkt, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, jkt)

	add := func(id, user, role string, at time.Time, in, out, cost int64) {
		if err := repo.Insert(ctx, model.UsageRecord{ID: id, UserID: user, Role: role, Provider: "p", Model: "m",
			TokensIn: in, TokensOut: out, CostMicros: cost, Source: "measured", CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	add("a", "u1", "builder", now.Add(-time.Hour), 100, 10, 1000)
	add("b", "u1", "builder", now.Add(-2*time.Hour), 200, 20, 2000)
	add("c", "u1", "inspector", now.AddDate(0, 0, -2), 50, 5, 500)
	add("d", "u1", "architect", now.AddDate(0, 0, -20), 7, 7, 70)                      // earlier month, inside 30 days
	add("e", "u1", "builder", time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC), 1, 1, 1) // 03:00 Nov 1 in Jakarta: this month
	add("f", "u1", "builder", now.AddDate(0, 0, -40), 9, 9, 9)                         // outside the window
	add("g", "u2", "builder", now.Add(-time.Hour), 999, 999, 999)                      // another user

	tr := NewUsageTracker(repo)
	s, err := tr.UserSummary(ctx, "u1", 30, now, jkt)
	if err != nil {
		t.Fatal(err)
	}
	if s.Days != 30 || len(s.Activity) != 30 || s.From != "2026-10-08" || s.To != "2026-11-06" {
		t.Fatalf("window: %d %d %s %s", s.Days, len(s.Activity), s.From, s.To)
	}
	count := map[string]int{}
	for _, d := range s.Activity {
		count[d.Date] = d.Count
	}
	if count["2026-11-06"] != 2 || count["2026-11-04"] != 1 || count["2026-10-17"] != 1 || count["2026-11-01"] != 1 || count["2026-11-05"] != 0 {
		t.Fatalf("activity: %v", count)
	}
	if len(s.Roles) != 3 || s.Roles[0].Role != "builder" || s.Roles[0].Runs != 3 || s.Roles[0].CostMicros != 3001 ||
		s.Roles[0].TokensIn != 301 || s.Roles[0].TokensOut != 31 || s.Roles[1].Role != "inspector" || s.Roles[2].Role != "architect" {
		t.Fatalf("roles: %+v", s.Roles)
	}
	m := s.MonthToDate
	if m.Month != "2026-11" || m.Runs != 4 || m.CostMicros != 3501 || m.TokensIn != 351 || m.TokensOut != 36 {
		t.Fatalf("month to date: %+v", m)
	}

	short, err := tr.UserSummary(ctx, "u1", 3, now, jkt)
	if err != nil || len(short.Activity) != 3 || len(short.Roles) != 2 || short.MonthToDate.Runs != 4 {
		t.Fatalf("3 days: %+v %v", short, err)
	}
	if capped, _ := tr.UserSummary(ctx, "u1", 5000, now, jkt); capped.Days != 365 {
		t.Fatalf("days must cap at 365: %d", capped.Days)
	}
	if def, _ := tr.UserSummary(ctx, "u1", 0, now, jkt); def.Days != 30 {
		t.Fatalf("default days: %d", def.Days)
	}
	empty, _ := tr.UserSummary(ctx, "nobody", 7, now, jkt)
	if len(empty.Activity) != 7 || empty.Roles == nil || len(empty.Roles) != 0 {
		t.Fatalf("empty user: %+v", empty)
	}
}
