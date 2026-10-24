package scheduler

import (
	"context"
	"database/sql"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// in-memory SQLite gives each pooled connection its own database
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	// minimal jobs table for package tests
	_, err = db.Exec(`CREATE TABLE jobs (
		id TEXT PRIMARY KEY, kind TEXT, wo_id TEXT, task_id TEXT, payload TEXT DEFAULT '{}',
		state TEXT DEFAULT 'PENDING', attempts INTEGER DEFAULT 0, max_attempts INTEGER DEFAULT 3,
		run_after INTEGER DEFAULT 0, claimed_by TEXT DEFAULT '', error TEXT DEFAULT '',
		created_at INTEGER, updated_at INTEGER);
		CREATE TABLE work_orders (id TEXT PRIMARY KEY, paused_at INTEGER)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestConcurrentClaimingNoDoubleExecution(t *testing.T) {
	db := testDB(t)
	log := slog.Default()
	s := New(&store.JobRepo{DB: db}, log, 4)
	s.Register("test", func(ctx context.Context, job model.Job) error {
		time.Sleep(20 * time.Millisecond)
		return nil
	})
	ctx := context.Background()
	var ids []string
	for i := 0; i < 5; i++ {
		id, err := s.Enqueue(ctx, "test", "wo1", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	_ = ids
	s.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		states := map[string]int{}
		rows, err := db.Query(`SELECT state, COUNT(*) FROM jobs GROUP BY state`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var st string
			var n int
			rows.Scan(&st, &n)
			states[st] = n
		}
		rows.Close()
		if states["COMPLETED"] == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("jobs did not complete: %v", states)
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.Stop()
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state = 'COMPLETED'`).Scan(&total); err != nil || total != 5 {
		t.Fatalf("expected 5 completed, got %d err=%v", total, err)
	}
}

func TestWorkerPoolBound(t *testing.T) {
	db := testDB(t)
	log := slog.Default()
	s := New(&store.JobRepo{DB: db}, log, 2)
	var concurrent atomic.Int64
	var peak atomic.Int64
	s.Register("test", func(ctx context.Context, job model.Job) error {
		n := concurrent.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
		concurrent.Add(-1)
		return nil
	})
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		if _, err := s.Enqueue(ctx, "test", "wo1", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	s.Start(ctx)
	deadline := time.Now().Add(6 * time.Second)
	for {
		var done int
		if err := db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state = 'COMPLETED'`).Scan(&done); err != nil {
			t.Fatal(err)
		}
		if done == 6 || time.Now().After(deadline) {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	s.Stop()
	if p := peak.Load(); p > 2 {
		t.Fatalf("worker pool exceeded bound: peak concurrency %d", p)
	}
}

func TestRetryAndPermanentFailure(t *testing.T) {
	db := testDB(t)
	log := slog.Default()
	s := New(&store.JobRepo{DB: db}, log, 2)
	var calls atomic.Int64
	s.Register("flaky", func(ctx context.Context, job model.Job) error {
		if calls.Add(1) < 3 {
			return context.DeadlineExceeded // retried
		}
		return nil
	})
	s.Register("broken", func(ctx context.Context, job model.Job) error {
		return context.Canceled // always fails: job never completes
	})
	ctx := context.Background()
	if _, err := s.Enqueue(ctx, "flaky", "wo1", "", nil); err != nil {
		t.Fatal(err)
	}
	// cap broken job to 1 attempt so it fails permanently instead of looping
	var bid string
	{
		id, err := s.Enqueue(ctx, "broken", "wo1", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		bid = id
		if _, err := db.Exec(`UPDATE jobs SET max_attempts = 1 WHERE id = ?`, bid); err != nil {
			t.Fatal(err)
		}
	}
	s.Start(ctx)
	deadline := time.Now().Add(8 * time.Second)
	for {
		var done, failed int
		db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state='COMPLETED'`).Scan(&done)
		db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state='FAILED'`).Scan(&failed)
		if done >= 1 && failed >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected 1 completed and 1 failed, got done=%d failed=%d", done, failed)
		}
		time.Sleep(25 * time.Millisecond)
	}
	s.Stop()
	if n := calls.Load(); n < 2 {
		t.Fatalf("flaky job should have been retried, calls=%d", n)
	}
	var failedErr string
	if err := db.QueryRow(`SELECT error FROM jobs WHERE state='FAILED'`).Scan(&failedErr); err != nil || failedErr == "" {
		t.Fatalf("failed job should record error: %q err=%v", failedErr, err)
	}
}

func TestOrphanReconcile(t *testing.T) {
	db := testDB(t)
	// simulate jobs claimed by a dead process
	now := time.Now().Unix()
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(`INSERT INTO jobs (id, kind, wo_id, task_id, payload, state, created_at, updated_at)
			VALUES (?,?,?,?,?,'RUNNING',?,?)`, "orphan_"+uuid.NewString()[:8], "test", "wo1", nil, "{}", now, now); err != nil {
			t.Fatal(err)
		}
	}
	var _ atomic.Value
	s := New(&store.JobRepo{DB: db}, slog.Default(), 1)
	s.Start(context.Background())
	defer s.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var failed int
		db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state='FAILED'`).Scan(&failed)
		if failed == 2 || time.Now().After(deadline) {
			if failed != 2 {
				t.Fatalf("orphans not reconciled to FAILED: %d", failed)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var err string
	if err2 := db.QueryRow(`SELECT error FROM jobs LIMIT 1`).Scan(&err); err2 == nil {
		if err == "" {
			t.Fatal("orphan error message should explain reconciliation requirement")
		}
	}
}

func TestPausedWorkOrdersAreNotClaimed(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO work_orders (id, paused_at) VALUES ('paused', 1), ('running', NULL)`); err != nil {
		t.Fatal(err)
	}
	s := New(&store.JobRepo{DB: db}, slog.Default(), 2)
	var ran atomic.Int64
	var unpaused atomic.Bool
	s.Register("test", func(ctx context.Context, job model.Job) error {
		if job.WOID == "paused" && !unpaused.Load() {
			t.Error("a paused work order's job was claimed")
		}
		ran.Add(1)
		return nil
	})
	ctx := context.Background()
	for _, wo := range []string{"paused", "running"} {
		if _, err := s.Enqueue(ctx, "test", wo, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	s.Start(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for ran.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond) // give the poller a chance to wrongly claim
	if ran.Load() != 1 {
		t.Fatalf("only the running order's job may run while the other is paused, ran=%d", ran.Load())
	}
	unpaused.Store(true)
	if _, err := db.Exec(`UPDATE work_orders SET paused_at = NULL WHERE id = 'paused'`); err != nil {
		t.Fatal(err)
	}
	for ran.Load() < 2 && time.Now().Before(deadline.Add(2*time.Second)) {
		time.Sleep(20 * time.Millisecond)
	}
	s.Stop()
	if ran.Load() != 2 {
		t.Fatalf("resumed job should run once unpaused, ran=%d", ran.Load())
	}
}
