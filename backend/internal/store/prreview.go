package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"legos-factory/backend/internal/model"
)

// ErrReviewNotFound wraps sql.ErrNoRows so callers can match either.
var ErrReviewNotFound = fmt.Errorf("review record not found: %w", sql.ErrNoRows)

type ReviewRepo struct{ DB *sql.DB }

const prCols = `id, user_id, project_id, provider, host, repo, number, head_sha, summary, verdict,
	posted_at, posted_head_sha, posted_verdict, posted_ref, posted_as, created_at, updated_at`

func (r *ReviewRepo) Upsert(ctx context.Context, p *model.PRReview) error {
	if p.ID == "" {
		p.ID = "rev_" + uuid.NewString()[:12]
	}
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	if p.Verdict == "" {
		p.Verdict = model.VerdictComment
	}
	var postedAt any
	if p.PostedAt != nil {
		postedAt = p.PostedAt.Unix()
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO pr_reviews
		(id, user_id, project_id, provider, host, repo, number, head_sha, summary, verdict,
		 posted_at, posted_head_sha, posted_verdict, posted_ref, posted_as, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(user_id, provider, host, repo, number) DO UPDATE SET
		 project_id = excluded.project_id, head_sha = excluded.head_sha, summary = excluded.summary,
		 verdict = excluded.verdict, posted_at = excluded.posted_at, posted_head_sha = excluded.posted_head_sha,
		 posted_verdict = excluded.posted_verdict, posted_ref = excluded.posted_ref,
		 posted_as = excluded.posted_as, updated_at = excluded.updated_at`,
		p.ID, p.UserID, p.ProjectID, p.Provider, p.Host, p.Repo, p.Number, p.HeadSHA, p.Summary, p.Verdict,
		postedAt, p.PostedHeadSHA, p.PostedVerdict, p.PostedRef, p.PostedAs, p.CreatedAt.Unix(), now.Unix())
	if err != nil {
		return err
	}
	var created int64
	if err := r.DB.QueryRowContext(ctx, `SELECT id, created_at FROM pr_reviews
		WHERE user_id = ? AND provider = ? AND host = ? AND repo = ? AND number = ?`,
		p.UserID, p.Provider, p.Host, p.Repo, p.Number).Scan(&p.ID, &created); err != nil {
		return err
	}
	p.CreatedAt = time.Unix(created, 0)
	return nil
}

func scanReview(scan func(...any) error) (*model.PRReview, error) {
	var p model.PRReview
	var postedAt sql.NullInt64
	var created, updated int64
	if err := scan(&p.ID, &p.UserID, &p.ProjectID, &p.Provider, &p.Host, &p.Repo, &p.Number, &p.HeadSHA,
		&p.Summary, &p.Verdict, &postedAt, &p.PostedHeadSHA, &p.PostedVerdict, &p.PostedRef, &p.PostedAs,
		&created, &updated); err != nil {
		return nil, err
	}
	if postedAt.Valid {
		t := time.Unix(postedAt.Int64, 0)
		p.PostedAt = &t
	}
	p.CreatedAt, p.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	return &p, nil
}

func (r *ReviewRepo) Get(ctx context.Context, userID, provider, host, repo string, number int) (*model.PRReview, error) {
	p, err := scanReview(r.DB.QueryRowContext(ctx, `SELECT `+prCols+` FROM pr_reviews
		WHERE user_id = ? AND provider = ? AND host = ? AND repo = ? AND number = ?`,
		userID, provider, host, repo, number).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrReviewNotFound
	}
	return p, err
}

func (r *ReviewRepo) ListForUser(ctx context.Context, userID string, keys []model.PRKey) ([]model.PRReview, error) {
	out := []model.PRReview{}
	if len(keys) == 0 {
		return out, nil
	}
	conds := make([]string, len(keys))
	args := []any{userID}
	for i, k := range keys {
		conds[i] = "(provider = ? AND host = ? AND repo = ? AND number = ?)"
		args = append(args, k.Provider, k.Host, k.Repo, k.Number)
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT `+prCols+` FROM pr_reviews WHERE user_id = ? AND (`+
		strings.Join(conds, " OR ")+`) ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanReview(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *ReviewRepo) SetDecision(ctx context.Context, id, summary, verdict string) error {
	return r.exec(ctx, `UPDATE pr_reviews SET summary = ?, verdict = ?, updated_at = ? WHERE id = ?`,
		summary, verdict, time.Now().Unix(), id)
}

func (r *ReviewRepo) MarkPosted(ctx context.Context, id, headSHA, verdict, ref, postedAs string, at time.Time) error {
	return r.exec(ctx, `UPDATE pr_reviews SET posted_at = ?, posted_head_sha = ?, posted_verdict = ?,
		posted_ref = ?, posted_as = ?, updated_at = ? WHERE id = ?`,
		at.Unix(), headSHA, verdict, ref, postedAs, time.Now().Unix(), id)
}

func (r *ReviewRepo) exec(ctx context.Context, q string, args ...any) error {
	res, err := r.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrReviewNotFound
	}
	return nil
}

func (r *ReviewRepo) StartRun(ctx context.Context, run model.PRReviewRun) error {
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	if run.State == "" {
		run.State = model.ReviewRunRunning
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO pr_review_runs
		(id, review_id, head_sha, state, agent, harness, model, summary, error, started_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.ReviewID, run.HeadSHA, run.State, run.Agent, run.Harness, run.Model, run.Summary, run.Error,
		run.StartedAt.Unix())
	return err
}

func (r *ReviewRepo) FinishRun(ctx context.Context, id string, res model.AgentResult, costMicros int64, summary, errMsg string) error {
	state := model.ReviewRunCompleted
	if errMsg != "" {
		state = model.ReviewRunFailed
	}
	return r.exec(ctx, `UPDATE pr_review_runs SET state = ?, summary = ?, error = ?, tokens_in = ?, tokens_out = ?,
		cache_read_tokens = ?, cache_write_tokens = ?, cost_micros = ?, finished_at = ? WHERE id = ?`,
		state, summary, errMsg, res.TokensIn, res.TokensOut, res.CacheRead, res.CacheWrite, costMicros,
		time.Now().Unix(), id)
}

func (r *ReviewRepo) LatestRun(ctx context.Context, reviewID string) (*model.PRReviewRun, error) {
	var run model.PRReviewRun
	var started int64
	var finished sql.NullInt64
	err := r.DB.QueryRowContext(ctx, `SELECT id, review_id, head_sha, state, agent, harness, model, summary, error,
		tokens_in, tokens_out, cache_read_tokens, cache_write_tokens, cost_micros, started_at, finished_at
		FROM pr_review_runs WHERE review_id = ? ORDER BY started_at DESC, rowid DESC LIMIT 1`, reviewID).
		Scan(&run.ID, &run.ReviewID, &run.HeadSHA, &run.State, &run.Agent, &run.Harness, &run.Model, &run.Summary,
			&run.Error, &run.TokensIn, &run.TokensOut, &run.CacheReadTokens, &run.CacheWriteTokens, &run.CostMicros,
			&started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	run.StartedAt = time.Unix(started, 0)
	if finished.Valid {
		t := time.Unix(finished.Int64, 0)
		run.FinishedAt = &t
	}
	return &run, nil
}

func (r *ReviewRepo) FailRunning(ctx context.Context, reason string) (int, error) {
	res, err := r.DB.ExecContext(ctx, `UPDATE pr_review_runs SET state = ?, error = ?, finished_at = ?
		WHERE state = ?`, model.ReviewRunFailed, reason, time.Now().Unix(), model.ReviewRunRunning)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

const dCols = `id, review_id, run_id, path, old_path, side, line, old_line, inline, head_sha, severity, title,
	body, ai_body, status, posted_ref, post_error, created_at, updated_at`

func (r *ReviewRepo) AddDrafts(ctx context.Context, reviewID, runID string, ds []model.PRDraft) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, d := range ds {
		if d.ID == "" {
			d.ID = "drf_" + uuid.NewString()[:12]
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO pr_review_drafts
			(id, review_id, run_id, path, old_path, side, line, old_line, inline, head_sha, severity, title,
			 body, ai_body, status, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			d.ID, reviewID, runID, d.Path, d.OldPath, d.Side, d.Line, d.OldLine, d.Inline, d.HeadSHA, d.Severity,
			d.Title, d.Body, d.Body, model.DraftPending, now, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddUserDraft stores the user's own comment: no run, no AI text, with the status the usecase chose.
func (r *ReviewRepo) AddUserDraft(ctx context.Context, d model.PRDraft) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO pr_review_drafts
		(id, review_id, run_id, path, old_path, side, line, old_line, inline, head_sha, severity, title,
		 body, ai_body, status, created_at, updated_at)
		VALUES (?,?,'',?,?,?,?,?,?,?,?,?,?,'',?,?,?)`,
		d.ID, d.ReviewID, d.Path, d.OldPath, d.Side, d.Line, d.OldLine, d.Inline, d.HeadSHA, d.Severity,
		d.Title, d.Body, d.Status, d.CreatedAt.Unix(), d.UpdatedAt.Unix())
	return err
}

func scanDraft(scan func(...any) error) (*model.PRDraft, error) {
	var d model.PRDraft
	var created, updated int64
	if err := scan(&d.ID, &d.ReviewID, &d.RunID, &d.Path, &d.OldPath, &d.Side, &d.Line, &d.OldLine, &d.Inline,
		&d.HeadSHA, &d.Severity, &d.Title, &d.Body, &d.AIBody, &d.Status, &d.PostedRef, &d.PostError,
		&created, &updated); err != nil {
		return nil, err
	}
	d.CreatedAt, d.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	return &d, nil
}

func (r *ReviewRepo) Drafts(ctx context.Context, reviewID string) ([]model.PRDraft, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT `+dCols+` FROM pr_review_drafts WHERE review_id = ?
		ORDER BY created_at, rowid`, reviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PRDraft{}
	for rows.Next() {
		d, err := scanDraft(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (r *ReviewRepo) UpdateDraft(ctx context.Context, reviewID, id string, status, body *string) (*model.PRDraft, error) {
	if err := r.exec(ctx, `UPDATE pr_review_drafts SET status = COALESCE(?, status), body = COALESCE(?, body),
		updated_at = ? WHERE id = ? AND review_id = ?`, status, body, time.Now().Unix(), id, reviewID); err != nil {
		return nil, err
	}
	return scanDraft(r.DB.QueryRowContext(ctx, `SELECT `+dCols+` FROM pr_review_drafts WHERE id = ?`, id).Scan)
}

func (r *ReviewRepo) SupersedePending(ctx context.Context, reviewID, keepRunID string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE pr_review_drafts SET status = ?, updated_at = ?
		WHERE review_id = ? AND status = ? AND run_id != ?`,
		model.DraftSuperseded, time.Now().Unix(), reviewID, model.DraftPending, keepRunID)
	return err
}

func (r *ReviewRepo) MarkOutdated(ctx context.Context, reviewID string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := []any{model.DraftOutdated, time.Now().Unix(), reviewID, model.DraftPending, model.DraftAccepted, model.DraftDiscarded}
	marks := make([]string, len(paths))
	for i, p := range paths {
		marks[i] = "?"
		args = append(args, p)
	}
	_, err := r.DB.ExecContext(ctx, `UPDATE pr_review_drafts SET status = ?, updated_at = ?
		WHERE review_id = ? AND status IN (?,?,?) AND path IN (`+strings.Join(marks, ",")+`)`, args...)
	return err
}

func (r *ReviewRepo) SetPosted(ctx context.Context, draftID, ref, errMsg string) error {
	if errMsg != "" {
		return r.exec(ctx, `UPDATE pr_review_drafts SET post_error = ?, updated_at = ? WHERE id = ?`,
			errMsg, time.Now().Unix(), draftID)
	}
	return r.exec(ctx, `UPDATE pr_review_drafts SET status = ?, posted_ref = ?, post_error = '', updated_at = ?
		WHERE id = ?`, model.DraftPosted, ref, time.Now().Unix(), draftID)
}

func (r *ReviewRepo) SetViewed(ctx context.Context, reviewID, path, patchHash string, viewed bool) error {
	if !viewed {
		_, err := r.DB.ExecContext(ctx, `DELETE FROM pr_review_viewed WHERE review_id = ? AND path = ?`, reviewID, path)
		return err
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO pr_review_viewed (review_id, path, patch_hash, viewed_at)
		VALUES (?,?,?,?) ON CONFLICT(review_id, path) DO UPDATE SET
		patch_hash = excluded.patch_hash, viewed_at = excluded.viewed_at`,
		reviewID, path, patchHash, time.Now().Unix())
	return err
}

func (r *ReviewRepo) Viewed(ctx context.Context, reviewID string) (map[string]string, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT path, patch_hash FROM pr_review_viewed WHERE review_id = ?`, reviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, h string
		if err := rows.Scan(&p, &h); err != nil {
			return nil, err
		}
		out[p] = h
	}
	return out, rows.Err()
}

func (r *ReviewRepo) Identity(ctx context.Context, userID, provider, host string) (*model.ReviewIdentity, error) {
	var id model.ReviewIdentity
	var pinned int64
	err := r.DB.QueryRowContext(ctx, `SELECT user_id, provider, host, source, connection_id, login, host_user_id, pinned_at
		FROM review_identities WHERE user_id = ? AND provider = ? AND host = ?`, userID, provider, host).
		Scan(&id.UserID, &id.Provider, &id.Host, &id.Source, &id.ConnectionID, &id.Login, &id.HostUserID, &pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id.PinnedAt = time.Unix(pinned, 0)
	return &id, nil
}

func (r *ReviewRepo) PinIdentity(ctx context.Context, id model.ReviewIdentity) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO review_identities
		(user_id, provider, host, source, connection_id, login, host_user_id, pinned_at) VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(user_id, provider, host) DO UPDATE SET source = excluded.source,
		connection_id = excluded.connection_id, login = excluded.login, host_user_id = excluded.host_user_id,
		pinned_at = excluded.pinned_at`,
		id.UserID, id.Provider, id.Host, id.Source, id.ConnectionID, id.Login, id.HostUserID, time.Now().Unix())
	return err
}
