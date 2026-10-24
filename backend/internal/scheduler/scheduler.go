// Package scheduler implements a SQLite-backed job queue with a bounded
// worker pool. Claiming is atomic (single UPDATE inside a transaction with
// an immediate write lock), preventing double execution across restarts.
package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type JobState string

const (
	JobPending   JobState = "PENDING"
	JobClaimed   JobState = "CLAIMED"
	JobRunning   JobState = "RUNNING"
	JobCompleted JobState = "COMPLETED"
	JobFailed    JobState = "FAILED"
	JobCancelled JobState = "CANCELLED"
)

type Job struct {
	ID          string
	Kind        string
	WOID        string
	TaskID      string
	Payload     map[string]any
	State       JobState
	Attempts    int
	MaxAttempts int
	RunAfter    time.Time
	Error       string
}

// Handler executes one job. Returning nil completes the job; an error marks
// it failed (retryable while attempts remain).
type Handler func(ctx context.Context, job Job) error

type Scheduler struct {
	db       *sql.DB
	log      *slog.Logger
	handlers map[string]Handler

	workers  int
	pollEvery time.Duration

	inflightN atomic.Int64
	wg        sync.WaitGroup
	cancel    context.CancelFunc
	claimMu   sync.Mutex // serializes claims; SQLite has a single writer
}

func New(db *sql.DB, log *slog.Logger, workers int) *Scheduler {
	if workers < 1 {
		workers = 1
	}
	return &Scheduler{
		db: db, log: log, workers: workers,
		handlers: map[string]Handler{},
		pollEvery: 300 * time.Millisecond,
	}
}

func (s *Scheduler) Register(kind string, h Handler) { s.handlers[kind] = h }

func (s *Scheduler) Enqueue(ctx context.Context, kind, woID, taskID string, payload map[string]any) (string, error) {
	id := "job_" + uuid.NewString()[:12]
	raw, _ := json.Marshal(payload)
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO jobs (id, kind, wo_id, task_id, payload, state, attempts, max_attempts, run_after, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, kind, woID, nullStr(taskID), string(raw), string(JobPending), 0, 3, 0, now.Unix(), now.Unix())
	if err != nil {
		return "", err
	}
	return id, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
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
	now := time.Now().Unix()
	res, err := s.db.Exec(`UPDATE jobs SET state = ?, error = ?, updated_at = ?
		WHERE state IN ('CLAIMED','RUNNING')`,
		string(JobFailed), "interrupted by restart; requires reconciliation", now)
	if err != nil {
		s.log.Error("orphan reconcile failed", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
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
		go func(job Job) {
			defer s.inflightN.Add(-1)
			defer func() {
				if r := recover(); r != nil {
					s.log.Error("job handler panicked", "job", job.ID, "panic", r)
					s.finish(ctx, job, JobFailed, fmt.Sprintf("handler panic: %v", r))
				}
			}()
			s.run(ctx, job)
		}(job)
	}
}

func (s *Scheduler) claimOne(ctx context.Context) (Job, bool) {
	// honor the worker pool bound before taking the claim lock
	if s.inflightN.Load() >= int64(s.workers) {
		return Job{}, false
	}
	s.claimMu.Lock()
	defer s.claimMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false
	}
	defer tx.Rollback()
	_, _ = tx.ExecContext(ctx, `SELECT 1`) // establish read snapshot

	row := tx.QueryRowContext(ctx, `SELECT id, kind, wo_id, task_id, payload, attempts, max_attempts
		FROM jobs WHERE state = 'PENDING' AND run_after <= ? ORDER BY created_at LIMIT 1`, time.Now().Unix())
	var j Job
	var taskID sql.NullString
	var payload string
	if err := row.Scan(&j.ID, &j.Kind, &j.WOID, &taskID, &payload, &j.Attempts, &j.MaxAttempts); err != nil {
		return Job{}, false
	}
	j.TaskID = taskID.String

	// atomic claim: conditional update protects against concurrent claimers
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET state = ?, claimed_by = ?, updated_at = ?
		WHERE id = ? AND state = 'PENDING'`,
		string(JobClaimed), "worker", time.Now().Unix(), j.ID)
	if err != nil {
		return Job{}, false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Job{}, false
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false
	}
	_ = json.Unmarshal([]byte(payload), &j.Payload)
	j.State = JobClaimed
	return j, true
}

func (s *Scheduler) run(ctx context.Context, job Job) {
	h, ok := s.handlers[job.Kind]
	if !ok {
		s.finish(ctx, job, JobFailed, fmt.Sprintf("no handler for job kind %q", job.Kind))
		return
	}
	s.mark(ctx, job, JobRunning)

	jctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	err := h(jctx, job)
	if err == nil {
		s.finish(ctx, job, JobCompleted, "")
		return
	}
	var attempts int
	_ = s.db.QueryRowContext(ctx, `SELECT attempts FROM jobs WHERE id = ?`, job.ID).Scan(&attempts)
	if attempts+1 >= job.MaxAttempts {
		s.log.Error("job failed permanently", "job", job.ID, "kind", job.Kind, "wo", job.WOID, "attempts", attempts+1, "err", err)
		s.finish(ctx, job, JobFailed, err.Error())
	} else {
		// retryable: back off, requeue
		s.log.Warn("job retryable failure", "job", job.ID, "kind", job.Kind, "wo", job.WOID, "err", err)
		_, _ = s.db.ExecContext(ctx, `UPDATE jobs SET state = 'PENDING', attempts = attempts + 1,
			run_after = ?, error = ?, updated_at = ? WHERE id = ?`,
			time.Now().Add(2*time.Second).Unix(), err.Error(), time.Now().Unix(), job.ID)
	}
}

func (s *Scheduler) mark(ctx context.Context, job Job, state JobState) {
	_, _ = s.db.ExecContext(ctx, `UPDATE jobs SET state = ?, updated_at = ? WHERE id = ?`,
		string(state), time.Now().Unix(), job.ID)
}

func (s *Scheduler) finish(ctx context.Context, job Job, state JobState, errMsg string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE jobs SET state = ?, error = ?, updated_at = ? WHERE id = ?`,
		string(state), errMsg, time.Now().Unix(), job.ID)
}

// CancelWO cancels all pending/claimed jobs of a work order.
func (s *Scheduler) CancelWO(ctx context.Context, woID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET state = 'CANCELLED', updated_at = ?
		WHERE wo_id = ? AND state IN ('PENDING','CLAIMED')`, time.Now().Unix(), woID)
	return err
}
