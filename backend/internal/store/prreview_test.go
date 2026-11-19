package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func newReview(t *testing.T, r *ReviewRepo, user string, number int) *model.PRReview {
	t.Helper()
	p := &model.PRReview{UserID: user, ProjectID: "p1", Provider: "github", Host: "github.com", Repo: "o/r", Number: number, HeadSHA: "h1"}
	if err := r.Upsert(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReviewUpsertIsUniquePerKeyAndGetNotFound(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	r := &ReviewRepo{DB: db}
	a := newReview(t, r, "u1", 7)
	if a.ID == "" || a.CreatedAt.IsZero() {
		t.Fatalf("id and created_at must be set: %+v", a)
	}
	b := &model.PRReview{UserID: "u1", ProjectID: "p1", Provider: "github", Host: "github.com", Repo: "o/r", Number: 7, HeadSHA: "h2", Summary: "s"}
	if err := r.Upsert(ctx, b); err != nil {
		t.Fatal(err)
	}
	if b.ID != a.ID {
		t.Fatalf("second upsert created a new row: %s vs %s", b.ID, a.ID)
	}
	got, err := r.Get(ctx, "u1", "github", "github.com", "o/r", 7)
	if err != nil || got.HeadSHA != "h2" || got.Summary != "s" || got.Verdict != model.VerdictComment {
		t.Fatalf("got %+v err %v", got, err)
	}
	newReview(t, r, "u2", 7)
	var n int
	db.QueryRow(`SELECT count(*) FROM pr_reviews`).Scan(&n)
	if n != 2 {
		t.Fatalf("rows = %d, want 2 (one per user)", n)
	}
	if _, err := r.Get(ctx, "u1", "github", "github.com", "o/r", 99); !errors.Is(err, ErrReviewNotFound) || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("err = %v", err)
	}
}

func TestReviewListForUserBatchesKeysAndScopesUser(t *testing.T) {
	r := &ReviewRepo{DB: openTestDB(t)}
	ctx := context.Background()
	for _, n := range []int{1, 2, 3} {
		newReview(t, r, "u1", n)
	}
	newReview(t, r, "u2", 1)
	keys := []model.PRKey{{Provider: "github", Host: "github.com", Repo: "o/r", Number: 1}, {Provider: "github", Host: "github.com", Repo: "o/r", Number: 3}, {Provider: "gitlab", Host: "gitlab.com", Repo: "o/r", Number: 2}}
	got, err := r.ListForUser(ctx, "u1", keys)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d err %v", len(got), err)
	}
	if got, _ := r.ListForUser(ctx, "u1", nil); len(got) != 0 {
		t.Fatal("no keys must return nothing")
	}
}

func TestReviewDecisionAndMarkPosted(t *testing.T) {
	r := &ReviewRepo{DB: openTestDB(t)}
	ctx := context.Background()
	p := newReview(t, r, "u1", 1)
	if err := r.SetDecision(ctx, p.ID, "sum", model.VerdictApprove); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1700000000, 0)
	if err := r.MarkPosted(ctx, p.ID, "h1", model.VerdictApprove, "ref1", "alice", at); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get(ctx, "u1", "github", "github.com", "o/r", 1)
	if got.Summary != "sum" || got.Verdict != model.VerdictApprove || got.PostedAt == nil || !got.PostedAt.Equal(at) ||
		got.PostedHeadSHA != "h1" || got.PostedRef != "ref1" || got.PostedAs != "alice" || got.PostedVerdict != model.VerdictApprove {
		t.Fatalf("got %+v", got)
	}
	if err := r.SetDecision(ctx, "missing", "", ""); !errors.Is(err, ErrReviewNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestReviewRunsFinishLatestAndFailRunning(t *testing.T) {
	r := &ReviewRepo{DB: openTestDB(t)}
	ctx := context.Background()
	a, b := newReview(t, r, "u1", 1), newReview(t, r, "u1", 2)
	if run, err := r.LatestRun(ctx, a.ID); run != nil || err != nil {
		t.Fatalf("empty LatestRun = %v, %v", run, err)
	}
	t0 := time.Now().Add(-time.Hour)
	for _, run := range []model.PRReviewRun{
		{ID: "r1", ReviewID: a.ID, HeadSHA: "h", Agent: "claude", Harness: "acp", Model: "m", StartedAt: t0},
		{ID: "r2", ReviewID: a.ID, HeadSHA: "h", Agent: "claude", Harness: "acp", Model: "m", StartedAt: t0.Add(time.Minute)},
		{ID: "r3", ReviewID: b.ID, HeadSHA: "h", Agent: "claude", Harness: "acp", Model: "m", StartedAt: t0},
	} {
		if err := r.StartRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if latest, _ := r.LatestRun(ctx, a.ID); latest.ID != "r2" || latest.State != model.ReviewRunRunning || latest.FinishedAt != nil {
		t.Fatalf("latest = %+v", latest)
	}
	if err := r.FinishRun(ctx, "r1", model.AgentResult{TokensIn: 10, TokensOut: 5, CacheRead: 3, CacheWrite: 2}, 42, "done", ""); err != nil {
		t.Fatal(err)
	}
	if err := r.FinishRun(ctx, "nope", model.AgentResult{}, 0, "", ""); !errors.Is(err, ErrReviewNotFound) {
		t.Fatalf("err = %v", err)
	}
	n, err := r.FailRunning(ctx, "interrupted by restart")
	if err != nil || n != 2 {
		t.Fatalf("FailRunning = %d, %v; want 2", n, err)
	}
	if n, _ := r.FailRunning(ctx, "again"); n != 0 {
		t.Fatalf("second FailRunning = %d", n)
	}
	var state, errMsg string
	r.DB.QueryRow(`SELECT state, error FROM pr_review_runs WHERE id = 'r2'`).Scan(&state, &errMsg)
	if state != model.ReviewRunFailed || errMsg != "interrupted by restart" {
		t.Fatalf("r2 = %s %q", state, errMsg)
	}
	var tin, tout, cr, cw, cost int64
	var sum string
	var fin sql.NullInt64
	r.DB.QueryRow(`SELECT state, summary, tokens_in, tokens_out, cache_read_tokens, cache_write_tokens, cost_micros, finished_at FROM pr_review_runs WHERE id = 'r1'`).
		Scan(&state, &sum, &tin, &tout, &cr, &cw, &cost, &fin)
	if state != model.ReviewRunCompleted || sum != "done" || tin != 10 || tout != 5 || cr != 3 || cw != 2 || cost != 42 || !fin.Valid {
		t.Fatalf("r1 = %s %s %d %d %d %d %d %v", state, sum, tin, tout, cr, cw, cost, fin)
	}
	if err := r.FinishRun(ctx, "r3", model.AgentResult{}, 0, "", "boom"); err != nil {
		t.Fatal(err)
	}
	r.DB.QueryRow(`SELECT state FROM pr_review_runs WHERE id = 'r3'`).Scan(&state)
	if state != model.ReviewRunFailed {
		t.Fatalf("r3 state = %s", state)
	}
}

func draftsByPath(t *testing.T, r *ReviewRepo, reviewID string) map[string]model.PRDraft {
	t.Helper()
	ds, err := r.Drafts(context.Background(), reviewID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]model.PRDraft{}
	for _, d := range ds {
		out[d.Path+"#"+d.Title] = d
	}
	return out
}

func TestReviewDraftsLifecycle(t *testing.T) {
	r := &ReviewRepo{DB: openTestDB(t)}
	ctx := context.Background()
	rev, other := newReview(t, r, "u1", 1), newReview(t, r, "u1", 2)
	mk := func(path, title string) model.PRDraft {
		return model.PRDraft{Path: path, Side: "new", Line: 3, Inline: true, HeadSHA: "h1", Severity: "major", Title: title, Body: "body " + title}
	}
	if err := r.AddDrafts(ctx, rev.ID, "run1", []model.PRDraft{mk("a.go", "x"), mk("b.go", "y")}); err != nil {
		t.Fatal(err)
	}
	got := draftsByPath(t, r, rev.ID)
	d := got["a.go#x"]
	if d.ID == "" || d.Status != model.DraftPending || d.AIBody != "body x" || d.RunID != "run1" || !d.Inline {
		t.Fatalf("draft = %+v", d)
	}

	acc, edited := model.DraftAccepted, "edited"
	u, err := r.UpdateDraft(ctx, rev.ID, d.ID, &acc, &edited)
	if err != nil || u.Status != model.DraftAccepted || u.Body != "edited" || u.AIBody != "body x" {
		t.Fatalf("update = %+v, %v", u, err)
	}
	u, _ = r.UpdateDraft(ctx, rev.ID, d.ID, nil, &edited)
	if u.Status != model.DraftAccepted {
		t.Fatalf("nil status must keep status, got %s", u.Status)
	}
	if _, err := r.UpdateDraft(ctx, other.ID, d.ID, &acc, nil); !errors.Is(err, ErrReviewNotFound) {
		t.Fatalf("draft of another review: err = %v", err)
	}

	if err := r.AddDrafts(ctx, rev.ID, "run2", []model.PRDraft{mk("c.go", "z")}); err != nil {
		t.Fatal(err)
	}
	if err := r.SupersedePending(ctx, rev.ID, "run2"); err != nil {
		t.Fatal(err)
	}
	got = draftsByPath(t, r, rev.ID)
	if got["b.go#y"].Status != model.DraftSuperseded || got["a.go#x"].Status != model.DraftAccepted || got["c.go#z"].Status != model.DraftPending {
		t.Fatalf("after supersede: %v %v %v", got["b.go#y"].Status, got["a.go#x"].Status, got["c.go#z"].Status)
	}
}

func TestReviewMarkOutdatedSkipsPostedAndOtherPaths(t *testing.T) {
	r := &ReviewRepo{DB: openTestDB(t)}
	ctx := context.Background()
	rev := newReview(t, r, "u1", 1)
	mk := func(path, title string) model.PRDraft {
		return model.PRDraft{Path: path, Side: "new", HeadSHA: "h1", Severity: "info", Title: title, Body: "b"}
	}
	if err := r.AddDrafts(ctx, rev.ID, "run1", []model.PRDraft{mk("a.go", "pending"), mk("a.go", "accepted"), mk("a.go", "discarded"), mk("a.go", "posted"), mk("a.go", "superseded"), mk("b.go", "other")}); err != nil {
		t.Fatal(err)
	}
	for title, st := range map[string]string{"accepted": model.DraftAccepted, "discarded": model.DraftDiscarded, "posted": model.DraftPosted, "superseded": model.DraftSuperseded} {
		id := draftsByPath(t, r, rev.ID)["a.go#"+title].ID
		if _, err := r.UpdateDraft(ctx, rev.ID, id, &st, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.MarkOutdated(ctx, rev.ID, []string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkOutdated(ctx, rev.ID, nil); err != nil {
		t.Fatal(err)
	}
	got := draftsByPath(t, r, rev.ID)
	want := map[string]string{"a.go#pending": model.DraftOutdated, "a.go#accepted": model.DraftOutdated, "a.go#discarded": model.DraftOutdated,
		"a.go#posted": model.DraftPosted, "a.go#superseded": model.DraftSuperseded, "b.go#other": model.DraftPending}
	for k, st := range want {
		if got[k].Status != st {
			t.Errorf("%s = %s, want %s", k, got[k].Status, st)
		}
	}
}

func TestReviewSetPostedKeepsStatusOnError(t *testing.T) {
	r := &ReviewRepo{DB: openTestDB(t)}
	ctx := context.Background()
	rev := newReview(t, r, "u1", 1)
	if err := r.AddDrafts(ctx, rev.ID, "run1", []model.PRDraft{{Path: "a.go", Side: "new", HeadSHA: "h", Severity: "info", Title: "t", Body: "b"}}); err != nil {
		t.Fatal(err)
	}
	id := draftsByPath(t, r, rev.ID)["a.go#t"].ID
	acc := model.DraftAccepted
	r.UpdateDraft(ctx, rev.ID, id, &acc, nil)
	if err := r.SetPosted(ctx, id, "", "line not in diff"); err != nil {
		t.Fatal(err)
	}
	d := draftsByPath(t, r, rev.ID)["a.go#t"]
	if d.Status != model.DraftAccepted || d.PostError != "line not in diff" || d.PostedRef != "" {
		t.Fatalf("after failure: %+v", d)
	}
	if err := r.SetPosted(ctx, id, "c123", ""); err != nil {
		t.Fatal(err)
	}
	d = draftsByPath(t, r, rev.ID)["a.go#t"]
	if d.Status != model.DraftPosted || d.PostedRef != "c123" || d.PostError != "" {
		t.Fatalf("after success: %+v", d)
	}
	if err := r.SetPosted(ctx, "missing", "x", ""); !errors.Is(err, ErrReviewNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestReviewViewedUpsertAndDelete(t *testing.T) {
	r := &ReviewRepo{DB: openTestDB(t)}
	ctx := context.Background()
	rev := newReview(t, r, "u1", 1)
	r.SetViewed(ctx, rev.ID, "a.go", "h1", true)
	r.SetViewed(ctx, rev.ID, "b.go", "h2", true)
	r.SetViewed(ctx, rev.ID, "a.go", "h3", true)
	got, err := r.Viewed(ctx, rev.ID)
	if err != nil || len(got) != 2 || got["a.go"] != "h3" || got["b.go"] != "h2" {
		t.Fatalf("viewed = %v, %v", got, err)
	}
	if err := r.SetViewed(ctx, rev.ID, "a.go", "", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Viewed(ctx, rev.ID); len(got) != 1 || got["b.go"] != "h2" {
		t.Fatalf("after delete = %v", got)
	}
}

func TestReviewIdentityPinIsUpsert(t *testing.T) {
	r := &ReviewRepo{DB: openTestDB(t)}
	ctx := context.Background()
	if id, err := r.Identity(ctx, "u1", "github", "github.com"); id != nil || err != nil {
		t.Fatalf("missing = %v, %v", id, err)
	}
	pin := model.ReviewIdentity{UserID: "u1", Provider: "github", Host: "github.com", Source: "connection", ConnectionID: "conn_1", Login: "alice", HostUserID: "42"}
	if err := r.PinIdentity(ctx, pin); err != nil {
		t.Fatal(err)
	}
	pin.Source, pin.ConnectionID, pin.Login = "gh", "", "alice2"
	if err := r.PinIdentity(ctx, pin); err != nil {
		t.Fatal(err)
	}
	got, err := r.Identity(ctx, "u1", "github", "github.com")
	if err != nil || got.Source != "gh" || got.Login != "alice2" || got.ConnectionID != "" || got.HostUserID != "42" || got.PinnedAt.IsZero() {
		t.Fatalf("got %+v, %v", got, err)
	}
	if id, _ := r.Identity(ctx, "u2", "github", "github.com"); id != nil {
		t.Fatal("pin must be per user")
	}
}

func TestReviewDeleteCascadesToRunsDraftsViewed(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	r := &ReviewRepo{DB: db}
	rev := newReview(t, r, "u1", 1)
	r.StartRun(ctx, model.PRReviewRun{ID: "r1", ReviewID: rev.ID, HeadSHA: "h"})
	r.AddDrafts(ctx, rev.ID, "r1", []model.PRDraft{{Path: "a.go", Side: "new", HeadSHA: "h", Severity: "info", Title: "t", Body: "b"}})
	r.SetViewed(ctx, rev.ID, "a.go", "h", true)
	if _, err := db.Exec(`DELETE FROM pr_reviews WHERE id = ?`, rev.ID); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"pr_review_runs", "pr_review_drafts", "pr_review_viewed"} {
		var n int
		db.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n)
		if n != 0 {
			t.Errorf("%s has %d rows after cascade", tbl, n)
		}
	}
}
