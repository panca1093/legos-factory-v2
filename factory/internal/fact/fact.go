package fact

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const (
	KindWOCreated      = "wo.created"
	KindWORequeued     = "wo.requeued"
	KindWOCancelled    = "wo.cancelled"
	KindWOFailed       = "wo.failed"
	KindWORetried      = "wo.retried"
	KindSandboxStarted = "sandbox.started"
	KindSandboxStopped = "sandbox.stopped"
	// KindSandboxRestarted records an infra failure answered by a new sandbox on the same volume.
	KindSandboxRestarted = "sandbox.restarted"
	KindRunStarted       = "run.started"
	KindRunStep          = "run.step"
	KindRunFinished      = "run.finished"
	KindTaskCommitted    = "task.committed"
	KindTaskFailed       = "task.failed"
	KindVerifyPassed     = "verify.passed"
	KindVerifyFailed     = "verify.failed"
	KindPROpened         = "pr.opened"
	KindPRPushed         = "pr.pushed"
	KindCheckObserved    = "check.observed"
	KindChecksStable     = "checks.stable"
	KindPRMerged         = "pr.merged"
	KindPRClosed         = "pr.closed"
)

type Fact struct {
	ID      int64           `json:"id"`
	Subject string          `json:"subject"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
	At      time.Time       `json:"at"`
}

type WOCreated struct {
	Project   string `json:"project,omitempty"`
	Objective string `json:"objective,omitempty"`
	// LevelCap is the autonomy cap chosen at creation (SPEC §9); 0 makes the plan gate require a human.
	LevelCap int `json:"level_cap"`
	// Line is fixed at creation: true runs the Architect/Foreman line, false the single-builder path; absent in older facts means false.
	Line bool `json:"line,omitempty"`
	// ParentWO and BaseBranch mark a follow-up: its worktree starts from the parent's PR branch instead of the default branch (ADR-F11).
	ParentWO   string `json:"parent_wo,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
}

// Failure classes (ADR-F10).
const (
	ClassProvider    = "provider"
	ClassInfra       = "infra"
	ClassTask        = "task"
	ClassIntegration = "integration"
	ClassVerify      = "verify"
	ClassForeman     = "foreman"
)

type WOFailed struct {
	Class    string `json:"class"`
	Reason   string `json:"reason,omitempty"`
	Message  string `json:"message"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// WORetried is a human un-failing a failed work order; it resumes from the facts already recorded.
type WORetried struct {
	By   string `json:"by"`
	Note string `json:"note,omitempty"`
}

type RunStarted struct {
	Run         string `json:"run"`
	Role        string `json:"role"`
	Task        string `json:"task,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	BillingKind string `json:"billing_kind,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}

type WORequeued struct {
	Reason  string   `json:"reason"`
	Failing []string `json:"failing,omitempty"`
	Details string   `json:"details,omitempty"`
}

type RunStep struct {
	Run     string `json:"run"`
	Step    string `json:"step"`
	Summary string `json:"summary,omitempty"`
}

type RunFinished struct {
	Run        string          `json:"run"`
	StopReason string          `json:"stop_reason"`
	Usage      json.RawMessage `json:"usage,omitempty"`
	CostUSD    float64         `json:"cost_usd,omitempty"`
}

type TaskCommitted struct {
	Task string `json:"task"`
	SHA  string `json:"sha"`
}

type TaskFailed struct {
	Task     string `json:"task"`
	Class    string `json:"class"`
	Reason   string `json:"reason,omitempty"`
	Message  string `json:"message"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type SandboxRestarted struct {
	Workspace string `json:"workspace"`
	Attempt   int    `json:"attempt"`
	Message   string `json:"message"`
}

type VerifyFailed struct {
	Commands []string `json:"commands"`
	Output   string   `json:"output"`
}

type PROpened struct {
	Number  int    `json:"number"`
	URL     string `json:"url"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"head_sha"`
}

type PRPushed struct {
	HeadSHA string `json:"head_sha"`
}

type CheckObserved struct {
	Name    string `json:"name"`
	State   string `json:"state"` // pending | success | failure
	HeadSHA string `json:"head_sha"`
}

// ChecksStable records that the engine judged the observed check set final for HeadSHA; later check changes void it.
type ChecksStable struct {
	HeadSHA string `json:"head_sha"`
}

func Decode[T any](f Fact) (T, error) {
	var v T
	if err := json.Unmarshal(f.Payload, &v); err != nil {
		return v, fmt.Errorf("decode %s fact %d: %w", f.Kind, f.ID, err)
	}
	return v, nil
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS facts (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	subject TEXT NOT NULL,
	kind    TEXT NOT NULL,
	payload TEXT NOT NULL,
	at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS facts_subject ON facts(subject, id);`

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init fact schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// NewFact is a fact to append; AppendBatch writes several atomically.
type NewFact struct {
	Kind    string
	Payload any
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insert(ctx context.Context, x execer, subject, kind string, payload any) (Fact, error) {
	if payload == nil {
		payload = struct{}{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Fact{}, fmt.Errorf("marshal %s payload: %w", kind, err)
	}
	at := time.Now().UTC()
	res, err := x.ExecContext(ctx,
		`INSERT INTO facts(subject, kind, payload, at) VALUES(?,?,?,?)`,
		subject, kind, string(raw), at.UnixNano())
	if err != nil {
		return Fact{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Fact{}, err
	}
	return Fact{ID: id, Subject: subject, Kind: kind, Payload: raw, At: at}, nil
}

func (s *Store) Append(ctx context.Context, subject, kind string, payload any) (Fact, error) {
	return insert(ctx, s.db, subject, kind, payload)
}

// ErrConflict means a fact newer than the caller's view exists for the subject.
var ErrConflict = errors.New("fact conflict: the subject has newer facts")

// AppendBatch writes all facts in one transaction: either every fact is recorded, in order, or none.
// A non-zero afterID is the id of the last fact the caller saw; the batch fails with ErrConflict when a newer one exists.
func (s *Store) AppendBatch(ctx context.Context, subject string, afterID int64, in []NewFact) ([]Fact, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if afterID != 0 {
		var newer int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM facts WHERE subject = ? AND id > ?)`, subject, afterID).Scan(&newer); err != nil {
			return nil, err
		}
		if newer != 0 {
			return nil, ErrConflict
		}
	}
	out := make([]Fact, 0, len(in))
	for _, n := range in {
		f, err := insert(ctx, tx, subject, n.Kind, n.Payload)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) List(ctx context.Context, subject string) ([]Fact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, subject, kind, payload, at FROM facts WHERE subject = ? ORDER BY id`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		var f Fact
		var payload string
		var at int64
		if err := rows.Scan(&f.ID, &f.Subject, &f.Kind, &payload, &at); err != nil {
			return nil, err
		}
		f.Payload = json.RawMessage(payload)
		f.At = time.Unix(0, at).UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) Subjects(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT subject FROM facts GROUP BY subject ORDER BY MIN(id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sub string
		if err := rows.Scan(&sub); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}
