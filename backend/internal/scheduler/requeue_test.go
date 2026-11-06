package scheduler

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestErrRequeueRunsTheJobAgainWithoutSpendingAnAttempt(t *testing.T) {
	db := testDB(t)
	s := New(&store.JobRepo{DB: db}, slog.Default(), 1)
	var calls atomic.Int64
	s.Register("ask", func(context.Context, model.Job) error {
		if calls.Add(1) <= 3 { // more than MaxAttempts: a retry would have failed the job
			return model.ErrRequeue
		}
		return nil
	})
	id, err := s.Enqueue(context.Background(), "ask", "wo1", "t1", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Start(context.Background())
	defer s.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var state string
		var attempts int
		db.QueryRow(`SELECT state, attempts FROM jobs WHERE id = ?`, id).Scan(&state, &attempts)
		if state == "COMPLETED" {
			if attempts != 0 || calls.Load() != 4 {
				t.Fatalf("attempts = %d, calls = %d", attempts, calls.Load())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job state %s after %d calls", state, calls.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
