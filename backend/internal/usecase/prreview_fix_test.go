package usecase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func okPost(g *rvRig, n int) model.PostRequest {
	return model.PostRequest{Confirm: true, Expect: model.PostExpect{Login: "ana", HeadSHA: "h1", Comments: n, Verdict: "comment"}}
}

func TestReviewRunUsesTheReadOnlyPRReviewerRole(t *testing.T) {
	g := newRvRig(t)
	if _, err := g.r.StartRun(context.Background(), g.ana, "p1", 7); err != nil {
		t.Fatal(err)
	}
	g.r.Wait()
	if len(g.ag.roles) == 0 || g.ag.roles[0] != model.AgentPRReviewer {
		t.Fatalf("roles: %v", g.ag.roles)
	}
}

func TestRunsOfOnePRUseTheirOwnCheckout(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	g.r.StartRun(ctx, g.ana, "p1", 7)
	g.r.Wait()
	g.r.StartRun(ctx, g.ana, "p1", 7)
	g.r.Wait()
	if len(g.ws.prepared) != 2 || g.ws.prepared[0] == g.ws.prepared[1] || g.ws.removedRuns[0] != g.ws.prepared[0] || g.ws.removedRuns[1] != g.ws.prepared[1] {
		t.Fatalf("prepared %v removed %v", g.ws.prepared, g.ws.removedRuns)
	}
}

func TestPartialPostWithAnErrorStillRecordsWhatWasPosted(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	seedReview(t, g, "accepted", "accepted", "accepted")
	rv := g.rv["c1"]
	rv.post = model.PostedReview{Comments: map[string]string{"d0": "c-1"}, Failed: map[string]string{"d1": "line gone"}}
	rv.postErr = &model.ProviderError{Provider: "bitbucket", Status: 502, Message: "boom"}
	_, err := g.r.Post(ctx, g.ana, "p1", 7, okPost(g, 3))
	if err == nil {
		t.Fatal("the provider error must still reach the caller")
	}
	d := g.st.drafts
	if d[0].Status != "posted" || d[0].PostedRef != "c-1" {
		t.Fatalf("posted comment lost: %+v", d[0])
	}
	if d[1].Status != "accepted" || d[1].PostError != "line gone" {
		t.Fatalf("failed comment: %+v", d[1])
	}
	if d[2].Status != "accepted" || d[2].PostError == "" {
		t.Fatalf("a comment the provider never confirmed keeps an error: %+v", d[2])
	}
	cur, _ := g.st.Get(ctx, "ana", "github", "github.com", "acme/app", 7)
	if cur.PostedAt == nil || cur.PostedVerdict != "comment" {
		t.Fatalf("the partial post is recorded: %+v", cur)
	}
}

func TestApproveOnlyPostIsRecorded(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	rev := seedReview(t, g)
	g.st.SetDecision(ctx, rev.ID, "", "approve")
	g.rv["c1"].detail.Author = model.HostUser{Login: "zed", ID: "id-zed"}
	g.rv["c1"].post = model.PostedReview{}
	if _, err := g.r.Post(ctx, g.ana, "p1", 7, model.PostRequest{Confirm: true, Expect: model.PostExpect{Login: "ana", HeadSHA: "h1", Verdict: "approve"}}); err != nil {
		t.Fatal(err)
	}
	cur, _ := g.st.Get(ctx, "ana", "github", "github.com", "acme/app", 7)
	if cur.PostedAt == nil || cur.PostedVerdict != "approve" || cur.PostedHeadSHA != "h1" {
		t.Fatalf("%+v", cur)
	}

	g2 := newRvRig(t)
	rev2 := seedReview(t, g2)
	g2.st.SetDecision(ctx, rev2.ID, "", "approve")
	g2.rv["c1"].post = model.PostedReview{VerdictErr: "not allowed"}
	g2.r.Post(ctx, g2.ana, "p1", 7, model.PostRequest{Confirm: true, Expect: model.PostExpect{Login: "ana", HeadSHA: "h1", Verdict: "approve"}})
	if cur, _ := g2.st.Get(ctx, "ana", "github", "github.com", "acme/app", 7); cur.PostedAt != nil {
		t.Fatalf("a refused verdict with nothing else posted is not a posted review: %+v", cur)
	}
}

func TestRetryAfterPartialFailureSendsOnlyTheWaitingDrafts(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	rev := seedReview(t, g, "accepted", "accepted")
	g.st.SetDecision(ctx, rev.ID, "my summary", "approve")
	rv := g.rv["c1"]
	rv.post = model.PostedReview{Ref: "rev-1", Comments: map[string]string{"d0": "c-1"}, Failed: map[string]string{"d1": "nope"}}
	if _, err := g.r.Post(ctx, g.ana, "p1", 7, model.PostRequest{Confirm: true, Expect: model.PostExpect{Login: "ana", HeadSHA: "h1", Comments: 2, Verdict: "approve"}}); err != nil {
		t.Fatal(err)
	}
	if rv.posted[0].Body == "" || rv.posted[0].Verdict != "approve" {
		t.Fatalf("first post carries summary and verdict: %+v", rv.posted[0])
	}
	rv.post = model.PostedReview{Comments: map[string]string{"d1": "c-2"}}
	if _, err := g.r.Post(ctx, g.ana, "p1", 7, model.PostRequest{Confirm: true, Expect: model.PostExpect{Login: "ana", HeadSHA: "h1", Comments: 1, Verdict: "approve"}}); err != nil {
		t.Fatal(err)
	}
	second := rv.posted[1]
	if len(second.Comments) != 1 || second.Comments[0].DraftID != "d1" || second.Body != "" || second.Verdict != "comment" {
		t.Fatalf("retry sends only the failed comment: %+v", second)
	}
	if g.st.drafts[1].Status != "posted" {
		t.Fatalf("%+v", g.st.drafts[1])
	}
	cur, _ := g.st.Get(ctx, "ana", "github", "github.com", "acme/app", 7)
	if cur.PostedVerdict != "approve" || cur.PostedRef != "rev-1" {
		t.Fatalf("the original post record stays: %+v", cur)
	}
}

func TestSecondPostWhileOneIsInFlightIsRefused(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	seedReview(t, g, "accepted")
	rv := g.rv["c1"]
	rv.postGate, rv.entered = make(chan struct{}), make(chan struct{}, 1)
	rv.post = model.PostedReview{Ref: "r", Comments: map[string]string{"d0": "c"}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		g.r.Post(ctx, g.ana, "p1", 7, okPost(g, 1))
	}()
	<-rv.entered
	if _, err := g.r.Post(ctx, g.ana, "p1", 7, okPost(g, 1)); !errors.Is(err, ErrPostInFlight) {
		t.Fatalf("second post: %v", err)
	}
	close(rv.postGate)
	wg.Wait()
	if len(rv.posted) != 1 {
		t.Fatalf("exactly one provider call, got %d", len(rv.posted))
	}
}

func TestNewRunOutdatesAcceptedDraftsFromAnOlderHeadAndPostNamesBlockers(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	seedReview(t, g, "accepted", "accepted")
	g.st.drafts[0].HeadSHA = "old"
	rv := g.rv["c1"]

	var pc *PostConflict
	_, err := g.r.Post(ctx, g.ana, "p1", 7, okPost(g, 2))
	if !errors.As(err, &pc) || !strings.Contains(pc.Reason, "a.go") || !strings.Contains(pc.Reason, "1 accepted") {
		t.Fatalf("the 409 names what blocks: %v", err)
	}

	g.ag.replies = []string{`{"summary":"s","findings":[]}`}
	if _, err := g.r.StartRun(ctx, g.ana, "p1", 7); err != nil {
		t.Fatal(err)
	}
	g.r.Wait()
	if g.st.drafts[0].Status != "outdated" || g.st.drafts[1].Status != "accepted" {
		t.Fatalf("only the old-head draft is outdated: %+v", g.st.drafts)
	}
	rv.post = model.PostedReview{Ref: "r", Comments: map[string]string{"d1": "c"}}
	if _, err := g.r.Post(ctx, g.ana, "p1", 7, okPost(g, 1)); err != nil {
		t.Fatalf("posting is no longer blocked: %v", err)
	}
}

func TestSameHeadComparesShortAgainstFullHashes(t *testing.T) {
	full := "0123456789abcdef0123456789abcdef01234567"
	for _, c := range []struct {
		a, b string
		want bool
	}{{full, full[:12], true}, {full[:12], full, true}, {full, full, true}, {full, "999999999999", false}, {full, "", false}, {"", "", false}, {full, full[:3], false}} {
		if got := sameHead(c.a, c.b); got != c.want {
			t.Errorf("sameHead(%q, %q) = %v", c.a, c.b, got)
		}
	}
	at := time.Now()
	if got := rowLabel(model.RoleReviewer, &model.PRReview{PostedAt: &at, PostedHeadSHA: full}, 0, false, full[:12]); got != model.LabelNone {
		t.Fatalf("a post at the full head matches the list's short head: %s", got)
	}
}

func TestMissingMatchesOnlyTheNotFoundSentinels(t *testing.T) {
	if !missing(fmt.Errorf("get: %w", sql.ErrNoRows)) || !missing(ErrReviewNotFound) {
		t.Fatal("sentinels")
	}
	if missing(errors.New("connection not found in the pool")) || missing(nil) {
		t.Fatal("message text must not count as a missing row")
	}
}

func TestRecoverCleansLeftoverReviewCheckouts(t *testing.T) {
	g := newRvRig(t)
	if err := g.r.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.ws.cleaned != 1 {
		t.Fatalf("cleaned %d projects", g.ws.cleaned)
	}
}

func TestColdRailReportsStaleAndFillsInTheBackground(t *testing.T) {
	old := railResolve
	railResolve = 20 * time.Millisecond
	defer func() { railResolve = old }()
	ctx := context.Background()
	g := newRvRig(t)
	rv := g.rv["c1"]
	rv.reviewing = []model.PRSummary{rvSummary(3, "zed", 30)}
	rv.meGate = make(chan struct{})

	out := g.r.RailPulls(ctx, g.ana, g.project())
	if !out.Stale || out.Count != 0 || out.Rows == nil || out.Error != nil {
		t.Fatalf("cold and still resolving: %+v", out)
	}
	close(rv.meGate)
	g.r.Wait()
	out = g.r.RailPulls(ctx, g.ana, g.project())
	if out.Stale || out.Count != 1 {
		t.Fatalf("the background fill shows up: %+v", out)
	}

	g = newRvRig(t)
	g.rv["c1"].reviewing = []model.PRSummary{rvSummary(3, "zed", 30)}
	out = g.r.RailPulls(ctx, g.ana, g.project())
	if !out.Stale || out.Count != 0 {
		t.Fatalf("cold list with a known identity is stale, not empty-and-final: %+v", out)
	}
	g.r.Wait()
	if out = g.r.RailPulls(ctx, g.ana, g.project()); out.Stale || out.Count != 1 {
		t.Fatalf("%+v", out)
	}
}
