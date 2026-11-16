package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestSetWorkersResizesWithoutKillingRunningJobs(t *testing.T) {
	db := testDB(t)
	s := New(&store.JobRepo{DB: db}, slog.Default(), 1)
	var running, peak, done atomic.Int64
	release := make(chan struct{})
	s.Register("slow", func(ctx context.Context, job model.Job) error {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		running.Add(-1)
		done.Add(1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 3; i++ {
		if _, err := s.Enqueue(ctx, "slow", "wo1", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	s.Start(ctx)
	wait := func(cond func() bool, what string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
		}
	}
	wait(func() bool { return running.Load() == 1 }, "first job never started")
	time.Sleep(700 * time.Millisecond)
	if peak.Load() != 1 {
		t.Fatalf("pool of 1 ran %d at once", peak.Load())
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ { // racing resizes
		wg.Add(1)
		go func() { defer wg.Done(); s.SetWorkers(3) }()
	}
	wg.Wait()
	wait(func() bool { return running.Load() == 3 }, "growing the pool did not start the waiting jobs")
	s.SetWorkers(1)
	time.Sleep(400 * time.Millisecond)
	if running.Load() != 3 || done.Load() != 0 {
		t.Fatalf("shrinking killed running jobs: running %d done %d", running.Load(), done.Load())
	}
	close(release)
	wait(func() bool { return done.Load() == 3 }, "running jobs did not finish after a resize")
	s.SetWorkers(0)
	if s.workers.Load() != 1 {
		t.Fatal("the pool never drops below one worker")
	}
}
