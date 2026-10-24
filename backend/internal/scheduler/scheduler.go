// Package scheduler implements a SQLite-backed job queue with a bounded
// worker pool. Claiming is atomic (single UPDATE inside a transaction with
// an immediate write lock), preventing double execution across restarts.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

// Handler executes one job. Returning nil completes the job; an error marks
// it failed (retryable while attempts remain).
type Handler func(ctx context.Context, job model.Job) error

type Scheduler struct {
	jobs     *store.JobRepo
	log      *slog.Logger
	handlers map[string]Handler

	workers  int
	pollEvery time.Duration

	inflightN atomic.Int64
	wg        sync.WaitGroup
	cancel    context.CancelFunc
	claimMu   sync.Mutex // serializes claims; SQLite has a single writer
}

func New(jobs *store.JobRepo, log *slog.Logger, workers int) *Scheduler {
	if workers < 1 {
		workers = 1
	}
	return &Scheduler{
		jobs: jobs, log: log, workers: workers,
		handlers: map[string]Handler{},
		pollEvery: 300 * time.Millisecond,
	}
}

func (s *Scheduler) Register(kind string, h Handler) { s.handlers[kind] = h }

func (s *Scheduler) Enqueue(ctx context.Context, kind, woID, taskID string, payload map[string]any) (string, error) {
	id := "job_" + uuid.NewString()[:12]
	err := s.jobs.Insert(ctx, model.Job{ID: id, Kind: kind, WOID: woID, TaskID: taskID, Payload: payload, MaxAttempts: 3})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Start launches the poller and workers. Reconciles orphaned CLAIMED/RUNNING
// jobs left behind by a previous process (their handlers died mid-run; they
// are re-queued only for pure jobs — everything else goes to FAILED for
// explicit human reconciliation).
func (s *Scheduler) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel

	s.reconcileOrphans()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.pollEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.claimAndDispatch(ctx)
			}
		}
	}()
}

// Stop drains: no new claims, wait for in-flight jobs.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	s.inflightN.Store(0) // workers have drained
}

func (s *Scheduler) reconcileOrphans() {
	n, err := s.jobs.FailOrphans(context.Background(), "interrupted by restart; requires reconciliation")
	if err != nil {
		s.log.Error("orphan reconcile failed", "err", err)
		return
	}
	if n > 0 {
		s.log.Warn("reconciled orphaned jobs", "count", n)
	}
}

func (s *Scheduler) claimAndDispatch(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		job, ok := s.claimOne(ctx)
		if !ok {
			return
		}
		s.inflightN.Add(1)
		go func(job model.Job) {
			defer s.inflightN.Add(-1)
			defer func() {
				if r := recover(); r != nil {
					s.log.Error("job handler panicked", "job", job.ID, "panic", r)
					s.finish(ctx, job, model.JobFailed, fmt.Sprintf("handler panic: %v", r))
				}
			}()
			s.run(ctx, job)
		}(job)
	}
}

func (s *Scheduler) claimOne(ctx context.Context) (model.Job, bool) {
	// honor the worker pool bound before taking the claim lock
	if s.inflightN.Load() >= int64(s.workers) {
		return model.Job{}, false
	}
	s.claimMu.Lock()
	defer s.claimMu.Unlock()
	return s.jobs.ClaimNext(ctx, "worker")
}

func (s *Scheduler) run(ctx context.Context, job model.Job) {
	h, ok := s.handlers[job.Kind]
	if !ok {
		s.finish(ctx, job, model.JobFailed, fmt.Sprintf("no handler for job kind %q", job.Kind))
		return
	}
	s.mark(ctx, job, model.JobRunning)

	jctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	err := h(jctx, job)
	if err == nil {
		s.finish(ctx, job, model.JobCompleted, "")
		return
	}
	attempts := s.jobs.Attempts(ctx, job.ID)
	if attempts+1 >= job.MaxAttempts || errors.Is(err, model.ErrPermanent) {
		s.log.Error("job failed permanently", "job", job.ID, "kind", job.Kind, "wo", job.WOID, "attempts", attempts+1, "err", err)
		s.finish(ctx, job, model.JobFailed, err.Error())
	} else {
		// retryable: back off, requeue
		s.log.Warn("job retryable failure", "job", job.ID, "kind", job.Kind, "wo", job.WOID, "err", err)
		s.jobs.Requeue(ctx, job.ID, time.Now().Add(2*time.Second), err.Error())
	}
}

func (s *Scheduler) mark(ctx context.Context, job model.Job, state model.JobState) {
	s.jobs.SetState(ctx, job.ID, state)
}

func (s *Scheduler) finish(ctx context.Context, job model.Job, state model.JobState, errMsg string) {
	s.jobs.Finish(ctx, job.ID, state, errMsg)
}

// CancelWO cancels all pending/claimed jobs of a work order.
func (s *Scheduler) CancelWO(ctx context.Context, woID string) error {
	return s.jobs.CancelWO(ctx, woID)
}

// HasActive reports whether a PENDING/CLAIMED/RUNNING job exists for the key.
func (s *Scheduler) HasActive(ctx context.Context, woID, taskID, kind string) bool {
	return s.jobs.HasActive(ctx, woID, taskID, kind)
}
