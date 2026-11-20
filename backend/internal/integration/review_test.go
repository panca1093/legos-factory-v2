package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

type seen struct {
	method, path, query string
	header              http.Header
	body                map[string]any
}

// revSrv answers "METHOD /escaped/path" routes and records every request.
type revSrv struct {
	t      *testing.T
	mu     sync.Mutex
	reqs   []seen
	routes map[string]http.HandlerFunc
	URL    string
}

func newRev(t *testing.T) *revSrv {
	s := &revSrv{t: t, routes: map[string]http.HandlerFunc{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		json.Unmarshal(raw, &b)
		key := r.Method + " " + r.URL.EscapedPath()
		s.mu.Lock()
		s.reqs = append(s.reqs, seen{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Clone(), b})
		h := s.routes[key]
		s.mu.Unlock()
		if h == nil {
			t.Errorf("unexpected request %s?%s", key, r.URL.RawQuery)
			w.WriteHeader(404)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

func (s *revSrv) on(key string, h http.HandlerFunc) { s.routes[key] = h }

func (s *revSrv) json(key string, status int, body string, hdr ...string) {
	s.on(key, func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(hdr); i += 2 {
			w.Header().Set(hdr[i], hdr[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
}

func (s *revSrv) find(method, path string) []seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []seen
	for _, r := range s.reqs {
		if r.method == method && r.path == path {
			out = append(out, r)
		}
	}
	return out
}

func (s *revSrv) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func asProviderErr(t *testing.T, err error) *model.ProviderError {
	t.Helper()
	var pe *model.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("want *ProviderError, got %v", err)
	}
	return pe
}

var ctx = context.Background()

// ---- client.go ----

func TestRateLimitClassification(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	h := http.Header{"Retry-After": {"30"}}
	if d := retryAfter(h, now); d != 30*time.Second {
		t.Fatalf("retry-after seconds: %v", d)
	}
	h = http.Header{"Retry-After": {now.Add(45 * time.Second).UTC().Format(http.TimeFormat)}}
	if d := retryAfter(h, now); d != 45*time.Second {
		t.Fatalf("retry-after date: %v", d)
	}
	h = http.Header{"X-Ratelimit-Reset": {fmt.Sprint(now.Add(2 * time.Minute).Unix())}}
	if d := retryAfter(h, now); d != 2*time.Minute {
		t.Fatalf("github reset: %v", d)
	}
	h = http.Header{"Ratelimit-Reset": {fmt.Sprint(now.Add(time.Minute).Unix())}}
	if d := retryAfter(h, now); d != time.Minute {
		t.Fatalf("gitlab reset: %v", d)
	}
	pe := providerError("gitlab", 429, http.Header{}, []byte(`{"message":"Retry later"}`))
	if !pe.RateLimited || pe.Permanent() {
		t.Fatalf("429 must be a retryable rate limit: %+v", pe)
	}
	pe = providerError("github", 403, http.Header{}, []byte(`{"message":"Resource not accessible by personal access token"}`))
	if pe.RateLimited || !pe.Permanent() {
		t.Fatalf("plain 403 stays permanent: %+v", pe)
	}
	pe = providerError("github", 403, http.Header{}, []byte(`{"message":"You have exceeded a secondary rate limit."}`))
	if !pe.RateLimited || pe.Permanent() {
		t.Fatalf("secondary limit message: %+v", pe)
	}
	pe = providerError("gitlab", 403, http.Header{"X-Ratelimit-Remaining": {"0"}}, []byte(`{"message":"forbidden"}`))
	if pe.RateLimited {
		t.Fatalf("only GitHub 403 is classified by headers")
	}
}

func TestPaginationLinkToOtherHostRejected(t *testing.T) {
	s := newRev(t)
	s.json("GET /repos/o/r/pulls", 200, `[]`, "Link", `<https://evil.example/steal>; rel="next"`)
	_, err := NewGitHub(s.URL, "tok", nil).ListPullRequests(ctx, "o/r", model.PRFilter{})
	if err == nil || !strings.Contains(err.Error(), "another host") {
		t.Fatalf("want another-host refusal, got %v", err)
	}
}

func TestCheckRepoRejectsTraversal(t *testing.T) {
	for _, repo := range []string{"../etc", "a/..", "a", "a/b/c", "a b/c", ""} {
		if checkRepo("github", repo) == nil {
			t.Errorf("%q accepted", repo)
		}
	}
	if checkRepo("github", "acme/greeter.go") != nil {
		t.Error("valid slug rejected")
	}
}

func TestPatchHelpers(t *testing.T) {
	p := withHeader("@@ -1,2 +1,2 @@\n a\n-b\n+c", "x.go", "x.go", "modified")
	if !strings.HasPrefix(p, "--- a/x.go\n+++ b/x.go\n@@") || !strings.HasSuffix(p, "+c\n") {
		t.Fatalf("header: %q", p)
	}
	if got := withHeader("@@ -0,0 +1 @@\n+x", "n", "n", "added"); !strings.HasPrefix(got, "--- /dev/null\n+++ b/n\n") {
		t.Fatalf("added: %q", got)
	}
	if got := withHeader("@@ -1 +0,0 @@\n-x", "n", "n", "deleted"); !strings.HasPrefix(got, "--- a/n\n+++ /dev/null\n") {
		t.Fatalf("deleted: %q", got)
	}
	if withHeader("", "a", "a", "modified") != "" {
		t.Fatal("empty hunks must stay empty")
	}
	if add, del := countPatch(p); add != 1 || del != 1 {
		t.Fatalf("count %d/%d", add, del)
	}
}

func TestCapFiles(t *testing.T) {
	files := make([]model.PRFile, maxReviewFiles+2)
	for i := range files {
		files[i] = model.PRFile{Path: fmt.Sprint(i), Patch: "--- a\n+++ b\n@@\n+x\n", Additions: 1}
	}
	files = append(files, model.PRFile{Path: "big", Additions: 5})
	out := capFiles(files)
	if out[0].Truncated || out[maxReviewFiles].Patch != "" || !out[maxReviewFiles].Truncated {
		t.Fatal("file budget not enforced")
	}
	if !out[len(out)-1].Truncated {
		t.Fatal("withheld patch with changed lines must be Truncated")
	}
	bin := capFiles([]model.PRFile{{Path: "img.png"}})
	if bin[0].Truncated {
		t.Fatal("binary file with no changed lines is not truncated")
	}
}

// ---- GitHub ----

const ghPRJSON = `{"number":7,"title":"Add thing","html_url":"https://github.com/o/r/pull/7","body":"why","draft":false,
"updated_at":"2026-11-01T10:00:00Z","user":{"login":"alice","id":11},
"head":{"ref":"feat","sha":"headsha","repo":{"full_name":"fork/r"}},"base":{"ref":"main","sha":"basesha"},
"requested_reviewers":[{"login":"me","id":42}],"additions":10,"deletions":2,"changed_files":3}`

func ghPRList(extra ...string) string {
	return "[" + ghPRJSON + `,{"number":8,"title":"Other","html_url":"u8","user":{"login":"bob","id":12},
"head":{"ref":"x","sha":"s8"},"base":{"ref":"main","sha":"b"},"requested_reviewers":[],"updated_at":"2026-11-02T10:00:00Z"}` + strings.Join(extra, "") + "]"
}

func TestGitHubListPaginatesFiltersAndUsesETag(t *testing.T) {
	s := newRev(t)
	s.on("GET /repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			io.WriteString(w, `[{"number":9,"title":"p2","user":{"login":"me","id":42},"head":{"ref":"h"},"base":{"ref":"main"},"updated_at":"2026-11-03T10:00:00Z"}]`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/pulls?state=open&page=2>; rel="next", <%s/x>; rel="last"`, s.URL, s.URL))
		w.Header().Set("ETag", `"v1"`)
		io.WriteString(w, ghPRList())
	})
	g := NewGitHub(s.URL, "secret-token", nil)
	got, err := g.ListPullRequests(ctx, "o/r", model.PRFilter{Reviewer: model.HostUser{Login: "me", ID: "42"}, Author: model.HostUser{Login: "me", ID: "42"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Number != 7 || got[1].Number != 9 {
		t.Fatalf("related filter = reviewer or author, across pages: %+v", got)
	}
	if got[0].Author.Login != "alice" || got[0].Author.ID != "11" || got[0].RequestedReviewers[0].ID != "42" || got[0].HeadSHA != "headsha" || got[0].Head != "feat" || got[0].Base != "main" {
		t.Fatalf("mapping: %+v", got[0])
	}
	first := s.find("GET", "/repos/o/r/pulls")[0]
	if first.query != "state=open&sort=updated&direction=desc&per_page=100" || first.header.Get("Authorization") != "Bearer secret-token" || first.header.Get("X-GitHub-Api-Version") == "" {
		t.Fatalf("request: %+v", first)
	}

	s.on("GET /repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			io.WriteString(w, `[]`)
			return
		}
		if r.Header.Get("If-None-Match") != `"v1"` {
			t.Errorf("conditional header missing")
		}
		w.WriteHeader(304)
	})
	all, err := g.ListPullRequests(ctx, "o/r", model.PRFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("304 must serve the cached page: %v %v", all, err)
	}
}

func TestGitHubDetail(t *testing.T) {
	s := newRev(t)
	s.json("GET /repos/o/r/pulls/7", 200, ghPRJSON)
	s.json("GET /repos/o/r/commits/headsha/check-runs", 200, `{"check_runs":[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"failure"},{"status":"in_progress"}]}`)
	s.json("GET /repos/o/r/commits/headsha/status", 200, `{"state":"pending","total_count":0}`)
	s.json("GET /user", 200, `{"login":"me","id":42}`)
	s.json("GET /repos/o/r/pulls/7/reviews", 200, `[{"user":{"login":"me","id":42},"state":"COMMENTED","commit_id":"old"},{"user":{"login":"zed","id":1},"state":"APPROVED","commit_id":"x"},{"user":{"login":"me","id":42},"state":"APPROVED","commit_id":"headsha"},{"user":{"login":"me","id":42},"state":"PENDING","commit_id":"y"}]`)
	d, err := NewGitHub(s.URL, "t", nil).GetPullRequest(ctx, "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if d.CI != "failing" || d.SourceRepo != "fork/r" || d.BaseSHA != "basesha" || d.Additions != 10 || d.ChangedFiles != 3 || d.Body != "why" {
		t.Fatalf("detail: %+v", d)
	}
	if d.MyLatestReview == nil || d.MyLatestReview.Verdict != model.VerdictApprove || d.MyLatestReview.CommitID != "headsha" {
		t.Fatalf("latest review: %+v", d.MyLatestReview)
	}
}

func TestGitHubCIDegradesToNone(t *testing.T) {
	s := newRev(t)
	s.json("GET /repos/o/r/pulls/7", 200, ghPRJSON)
	s.json("GET /repos/o/r/commits/headsha/check-runs", 403, `{"message":"Resource not accessible by personal access token"}`)
	s.json("GET /repos/o/r/commits/headsha/status", 200, `{"state":"success","total_count":2}`)
	s.json("GET /user", 200, `{"login":"me","id":42}`)
	s.json("GET /repos/o/r/pulls/7/reviews", 200, `[]`)
	d, err := NewGitHub(s.URL, "t", nil).GetPullRequest(ctx, "o/r", 7)
	if err != nil || d.CI != "passing" || d.MyLatestReview != nil {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestGitHubFiles(t *testing.T) {
	s := newRev(t)
	s.json("GET /repos/o/r/pulls/7/files", 200, `[
{"filename":"a.go","status":"modified","additions":1,"deletions":1,"patch":"@@ -1 +1 @@\n-a\n+b"},
{"filename":"new.go","status":"added","additions":1,"deletions":0,"patch":"@@ -0,0 +1 @@\n+x"},
{"filename":"gone.go","previous_filename":"gone.go","status":"removed","additions":0,"deletions":1,"patch":"@@ -1 +0,0 @@\n-x"},
{"filename":"b.go","previous_filename":"old.go","status":"renamed","additions":0,"deletions":0},
{"filename":"logo.png","status":"added","additions":0,"deletions":0},
{"filename":"huge.sql","status":"modified","additions":9000,"deletions":0}]`)
	fs, err := NewGitHub(s.URL, "t", nil).PullRequestFiles(ctx, "o/r", 7)
	if err != nil || len(fs) != 6 {
		t.Fatalf("%v %v", fs, err)
	}
	if fs[0].Patch != "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-a\n+b\n" || fs[0].Truncated {
		t.Fatalf("modified: %+v", fs[0])
	}
	if !strings.HasPrefix(fs[1].Patch, "--- /dev/null\n+++ b/new.go") || fs[1].Status != "added" {
		t.Fatalf("added: %+v", fs[1])
	}
	if fs[2].Status != "deleted" || !strings.Contains(fs[2].Patch, "+++ /dev/null") {
		t.Fatalf("deleted: %+v", fs[2])
	}
	if fs[3].Status != "renamed" || fs[3].OldPath != "old.go" || fs[3].Patch != "" || fs[3].Truncated {
		t.Fatalf("rename-only: %+v", fs[3])
	}
	if fs[4].Patch != "" || fs[4].Truncated {
		t.Fatalf("binary: %+v", fs[4])
	}
	if !fs[5].Truncated || fs[5].Patch != "" {
		t.Fatalf("withheld patch: %+v", fs[5])
	}
}

func TestGitHubPostReview(t *testing.T) {
	s := newRev(t)
	s.json("POST /repos/o/r/pulls/7/reviews", 200, `{"id":555}`)
	got, err := NewGitHub(s.URL, "t", nil).PostReview(ctx, "o/r", 7, model.ReviewSubmission{
		HeadSHA: "headsha", Body: "summary", Verdict: model.VerdictRequestChanges,
		Comments: []model.InlineComment{
			{DraftID: "d1", Path: "a.go", Side: "new", Line: 12, Body: "new side"},
			{DraftID: "d2", Path: "a.go", Side: "old", Line: 3, Body: "old side"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref != "555" || got.Comments["d1"] != "555" || got.Comments["d2"] != "555" || len(got.Failed) != 0 {
		t.Fatalf("%+v", got)
	}
	b := s.find("POST", "/repos/o/r/pulls/7/reviews")[0].body
	if b["commit_id"] != "headsha" || b["event"] != "REQUEST_CHANGES" || b["body"] != "summary" {
		t.Fatalf("body: %v", b)
	}
	cs := b["comments"].([]any)
	c1, c2 := cs[0].(map[string]any), cs[1].(map[string]any)
	if c1["side"] != "RIGHT" || c1["line"] != float64(12) || c1["path"] != "a.go" || c2["side"] != "LEFT" || c2["line"] != float64(3) {
		t.Fatalf("comments: %v", cs)
	}
}

func TestGitHubPostReviewVerdictsAndFailure(t *testing.T) {
	s := newRev(t)
	s.json("POST /repos/o/r/pulls/7/reviews", 422, `{"message":"Unprocessable Entity","errors":["Line could not be resolved"]}`)
	g := NewGitHub(s.URL, "t", nil)
	_, err := g.PostReview(ctx, "o/r", 7, model.ReviewSubmission{HeadSHA: "h", Verdict: model.VerdictApprove, Comments: []model.InlineComment{{DraftID: "d", Path: "a", Side: "new", Line: 1}}})
	pe := asProviderErr(t, err)
	if pe.Status != 422 || pe.RateLimited || !pe.Permanent() {
		t.Fatalf("%+v", pe)
	}
	if ev := s.find("POST", "/repos/o/r/pulls/7/reviews")[0].body["event"]; ev != "APPROVE" {
		t.Fatalf("event %v", ev)
	}
	_, _ = g.PostReview(ctx, "o/r", 7, model.ReviewSubmission{HeadSHA: "h", Verdict: model.VerdictComment})
	if ev := s.find("POST", "/repos/o/r/pulls/7/reviews")[1].body["event"]; ev != "COMMENT" {
		t.Fatalf("event %v", ev)
	}
}

func TestGitHubRateLimitedResponses(t *testing.T) {
	reset := time.Now().Add(90 * time.Second).Unix()
	s := newRev(t)
	s.json("GET /user", 403, `{"message":"API rate limit exceeded for user ID 1."}`,
		"X-RateLimit-Remaining", "0", "X-RateLimit-Reset", fmt.Sprint(reset))
	_, err := NewGitHub(s.URL, "t", nil).Me(ctx)
	pe := asProviderErr(t, err)
	if !pe.RateLimited || pe.Permanent() || pe.RetryAfter < 60*time.Second || pe.RetryAfter > 91*time.Second {
		t.Fatalf("%+v", pe)
	}
	s2 := newRev(t)
	s2.json("GET /repos/o/r/pulls", 429, `{"message":"slow down"}`, "Retry-After", "7")
	_, err = NewGitHub(s2.URL, "t", nil).ListPullRequests(ctx, "o/r", model.PRFilter{})
	pe = asProviderErr(t, err)
	if !pe.RateLimited || pe.RetryAfter != 7*time.Second {
		t.Fatalf("%+v", pe)
	}
}

func TestGitHubTokenInfo(t *testing.T) {
	s := newRev(t)
	s.json("GET /user", 200, `{"login":"me","id":1}`,
		"X-OAuth-Scopes", "repo, read:org", "github-authentication-token-expiration", "2026-12-01 10:30:00 UTC")
	info, err := NewGitHub(s.URL, "t", nil).TokenInfo(ctx)
	if err != nil || !info.CanRead || info.CanWrite != model.TriYes || len(info.Scopes) != 2 || info.ExpiresAt == nil || info.ExpiresAt.Format(time.RFC3339) != "2026-12-01T10:30:00Z" {
		t.Fatalf("%+v %v", info, err)
	}
	s = newRev(t)
	s.json("GET /user", 200, `{"login":"me","id":1}`)
	info, _ = NewGitHub(s.URL, "t", nil).TokenInfo(ctx)
	if info.CanWrite != model.TriUnknown || info.ExpiresAt != nil {
		t.Fatalf("fine-grained token: %+v", info)
	}
	s = newRev(t)
	s.json("GET /user", 200, `{}`, "X-OAuth-Scopes", "read:org")
	info, _ = NewGitHub(s.URL, "t", nil).TokenInfo(ctx)
	if info.CanWrite != model.TriNo {
		t.Fatalf("classic without repo: %+v", info)
	}
	s = newRev(t)
	s.json("GET /user", 200, `{}`, "X-OAuth-Scopes", "public_repo")
	if info, _ = NewGitHub(s.URL, "t", nil).TokenInfo(ctx); info.CanWrite != model.TriUnknown {
		t.Fatalf("public_repo only: %+v", info)
	}
	s = newRev(t)
	s.json("GET /user", 401, `{"message":"Bad credentials"}`)
	if _, err = NewGitHub(s.URL, "t", nil).TokenInfo(ctx); asProviderErr(t, err).Status != 401 {
		t.Fatal("401 must surface")
	}
}

func TestGitHubMeCachedPerClient(t *testing.T) {
	s := newRev(t)
	s.json("GET /user", 200, `{"login":"me","id":42}`)
	g := NewGitHub(s.URL, "t", nil)
	for range 3 {
		if u, err := g.Me(ctx); err != nil || u.Login != "me" || u.ID != "42" {
			t.Fatalf("%+v %v", u, err)
		}
	}
	if n := len(s.find("GET", "/user")); n != 1 {
		t.Fatalf("Me hit the API %d times", n)
	}
}

func TestGitHubHonoursContext(t *testing.T) {
	s := newRev(t)
	c, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := NewGitHub(s.URL, "t", nil).ListPullRequests(c, "o/r", model.PRFilter{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
	if s.total() != 0 {
		t.Fatal("request sent on a cancelled context")
	}
}

// ---- GitLab ----

const glMRJSON = `{"iid":3,"title":"Fix","web_url":"https://gl/g/p/-/merge_requests/3","description":"desc","author":{"id":5,"username":"alice"},
"source_branch":"feat","target_branch":"main","sha":"headsha","draft":true,"reviewers":[{"id":9,"username":"me"}],"updated_at":"2026-11-01T10:00:00Z",
"changes_count":"1000+","source_project_id":77,"target_project_id":10,
"diff_refs":{"base_sha":"b","start_sha":"s","head_sha":"headsha"},"head_pipeline":{"status":"running"}}`

func TestGitLabListMergesQueriesAndPaginates(t *testing.T) {
	s := newRev(t)
	s.on("GET /projects/g%2Fsub%2Fp/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("author_username") != "":
			if q.Get("page") == "2" {
				io.WriteString(w, `[{"iid":4,"title":"older","author":{"id":9,"username":"me"},"updated_at":"2026-10-01T00:00:00Z"}]`)
				return
			}
			w.Header().Set("X-Next-Page", "2")
			io.WriteString(w, `[`+glMRJSON+`]`)
		case q.Get("reviewer_username") != "":
			io.WriteString(w, `[`+glMRJSON+`]`)
		default:
			t.Errorf("unfiltered query: %s", r.URL.RawQuery)
		}
	})
	got, err := NewGitLab(s.URL, "gl-token", nil).ListPullRequests(ctx, "g/sub/p", model.PRFilter{Author: model.HostUser{Login: "me", ID: "9"}, Reviewer: model.HostUser{Login: "me", ID: "9"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Number != 3 || got[1].Number != 4 || !got[0].Draft || got[0].RequestedReviewers[0].Login != "me" || got[0].HeadSHA != "headsha" {
		t.Fatalf("%+v", got)
	}
	reqs := s.find("GET", "/projects/g%2Fsub%2Fp/merge_requests")
	if len(reqs) != 3 || reqs[0].header.Get("PRIVATE-TOKEN") != "gl-token" || !strings.Contains(reqs[0].query, "state=opened") {
		t.Fatalf("%+v", reqs)
	}
}

func TestGitLabDetail(t *testing.T) {
	s := newRev(t)
	s.json("GET /projects/g%2Fp/merge_requests/3", 200, glMRJSON)
	s.json("GET /projects/77", 200, `{"path_with_namespace":"fork/p"}`)
	s.json("GET /user", 200, `{"id":9,"username":"me"}`)
	s.json("GET /projects/g%2Fp/merge_requests/3/approvals", 200, `{"approved_by":[{"user":{"id":9,"username":"me"}}]}`)
	d, err := NewGitLab(s.URL, "t", nil).GetPullRequest(ctx, "g/p", 3)
	if err != nil {
		t.Fatal(err)
	}
	if d.BaseSHA != "b" || d.StartSHA != "s" || d.HeadSHA != "headsha" || d.CI != "running" || d.SourceRepo != "fork/p" || d.ChangedFiles != 1000 || d.Body != "desc" {
		t.Fatalf("%+v", d)
	}
	if d.MyLatestReview == nil || d.MyLatestReview.Verdict != model.VerdictApprove {
		t.Fatalf("approval: %+v", d.MyLatestReview)
	}
}

func TestGitLabFilesDiffsAndChangesFallback(t *testing.T) {
	diffs := `[
{"old_path":"a.go","new_path":"a.go","diff":"@@ -1 +1 @@\n-a\n+b\n"},
{"old_path":"n.go","new_path":"n.go","new_file":true,"diff":"@@ -0,0 +1 @@\n+x\n"},
{"old_path":"o.go","new_path":"p.go","renamed_file":true,"diff":""},
{"old_path":"big.go","new_path":"big.go","too_large":true,"diff":""}]`
	s := newRev(t)
	s.on("GET /projects/g%2Fp/merge_requests/3/diffs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			io.WriteString(w, `[{"old_path":"d.go","new_path":"d.go","deleted_file":true,"diff":"@@ -1 +0,0 @@\n-x\n"}]`)
			return
		}
		w.Header().Set("X-Next-Page", "2")
		io.WriteString(w, diffs)
	})
	fs, err := NewGitLab(s.URL, "t", nil).PullRequestFiles(ctx, "g/p", 3)
	if err != nil || len(fs) != 5 {
		t.Fatalf("%v %v", fs, err)
	}
	if fs[0].Patch != "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-a\n+b\n" || fs[0].Additions != 1 || fs[0].Deletions != 1 {
		t.Fatalf("%+v", fs[0])
	}
	if fs[1].Status != "added" || !strings.HasPrefix(fs[1].Patch, "--- /dev/null") || fs[2].Status != "renamed" || fs[2].OldPath != "o.go" {
		t.Fatalf("%+v %+v", fs[1], fs[2])
	}
	if !fs[3].Truncated || fs[4].Status != "deleted" {
		t.Fatalf("%+v %+v", fs[3], fs[4])
	}

	old := newRev(t)
	old.json("GET /projects/g%2Fp/merge_requests/3/diffs", 404, `{"message":"404 Not Found"}`)
	old.json("GET /projects/g%2Fp/merge_requests/3/changes", 200, `{"changes":[{"old_path":"a","new_path":"a","diff":"@@ -1 +1 @@\n-a\n+b\n"}]}`)
	fs, err = NewGitLab(old.URL, "t", nil).PullRequestFiles(ctx, "g/p", 3)
	if err != nil || len(fs) != 1 || fs[0].Path != "a" {
		t.Fatalf("fallback: %v %v", fs, err)
	}
}

func glReviewServer(t *testing.T) *revSrv {
	s := newRev(t)
	s.json("GET /projects/g%2Fp/merge_requests/3", 200, glMRJSON)
	return s
}

func TestGitLabPostReviewPartialFailureAndApprove(t *testing.T) {
	s := glReviewServer(t)
	n := 0
	s.on("POST /projects/g%2Fp/merge_requests/3/draft_notes", func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 2 {
			w.WriteHeader(400)
			io.WriteString(w, `{"message":"Position is invalid"}`)
			return
		}
		fmt.Fprintf(w, `{"id":%d}`, 100+n)
	})
	s.json("POST /projects/g%2Fp/merge_requests/3/draft_notes/bulk_publish", 204, ``)
	s.json("POST /projects/g%2Fp/merge_requests/3/approve", 201, `{}`)
	got, err := NewGitLab(s.URL, "t", nil).PostReview(ctx, "g/p", 3, model.ReviewSubmission{
		HeadSHA: "headsha", Body: "summary", Verdict: model.VerdictApprove,
		Comments: []model.InlineComment{
			{DraftID: "ctx", Path: "a.go", OldPath: "a.go", Side: "new", Line: 10, OldLine: 8, Body: "context line"},
			{DraftID: "bad", Path: "a.go", Side: "new", Line: 99, Body: "bad"},
			{DraftID: "old", Path: "a.go", OldPath: "a.go", Side: "old", Line: 4, Body: "removed"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Comments["ctx"] != "101" || got.Comments["old"] != "103" || !strings.Contains(got.Failed["bad"], "Position is invalid") || got.VerdictErr != "" {
		t.Fatalf("%+v", got)
	}
	notes := s.find("POST", "/projects/g%2Fp/merge_requests/3/draft_notes")
	if len(notes) != 4 {
		t.Fatalf("3 comments + summary expected, got %d", len(notes))
	}
	pos := notes[0].body["position"].(map[string]any)
	if pos["base_sha"] != "b" || pos["start_sha"] != "s" || pos["head_sha"] != "headsha" || pos["new_line"] != float64(10) || pos["old_line"] != float64(8) || pos["position_type"] != "text" {
		t.Fatalf("context anchor needs both lines: %v", pos)
	}
	pos = notes[2].body["position"].(map[string]any)
	if pos["old_line"] != float64(4) || pos["new_line"] != nil {
		t.Fatalf("old anchor: %v", pos)
	}
	if notes[3].body["position"] != nil || notes[3].body["note"] != "summary" {
		t.Fatalf("summary draft: %v", notes[3].body)
	}
	if len(s.find("POST", "/projects/g%2Fp/merge_requests/3/draft_notes/bulk_publish")) != 1 {
		t.Fatal("drafts must be published once")
	}
	if ap := s.find("POST", "/projects/g%2Fp/merge_requests/3/approve"); len(ap) != 1 || ap[0].body["sha"] != "headsha" {
		t.Fatalf("approve: %+v", ap)
	}
}

func TestGitLabPostReviewGuards(t *testing.T) {
	s := glReviewServer(t)
	g := NewGitLab(s.URL, "t", nil)
	_, err := g.PostReview(ctx, "g/p", 3, model.ReviewSubmission{HeadSHA: "headsha", Verdict: model.VerdictRequestChanges})
	if pe := asProviderErr(t, err); pe.Status != 422 {
		t.Fatalf("%+v", pe)
	}
	if s.total() != 0 {
		t.Fatal("request-changes must fail before any call")
	}
	_, err = g.PostReview(ctx, "g/p", 3, model.ReviewSubmission{HeadSHA: "stale", Body: "x"})
	if pe := asProviderErr(t, err); pe.Status != 409 {
		t.Fatalf("head moved must be 409: %+v", pe)
	}
	if len(s.find("POST", "/projects/g%2Fp/merge_requests/3/draft_notes")) != 0 {
		t.Fatal("nothing may be drafted on a moved head")
	}
}

func TestGitLabPostReviewPublishFailureRemovesDrafts(t *testing.T) {
	s := glReviewServer(t)
	s.json("POST /projects/g%2Fp/merge_requests/3/draft_notes", 201, `{"id":55}`)
	s.json("POST /projects/g%2Fp/merge_requests/3/draft_notes/bulk_publish", 500, `{"message":"boom"}`)
	s.json("DELETE /projects/g%2Fp/merge_requests/3/draft_notes/55", 204, ``)
	_, err := NewGitLab(s.URL, "t", nil).PostReview(ctx, "g/p", 3, model.ReviewSubmission{HeadSHA: "headsha", Body: "x"})
	if asProviderErr(t, err).Status != 500 || len(s.find("DELETE", "/projects/g%2Fp/merge_requests/3/draft_notes/55")) != 1 {
		t.Fatalf("drafts must be cleaned up: %v", err)
	}
}

func TestGitLabApproveFailureIsVerdictErr(t *testing.T) {
	s := glReviewServer(t)
	s.json("POST /projects/g%2Fp/merge_requests/3/draft_notes", 201, `{"id":1}`)
	s.json("POST /projects/g%2Fp/merge_requests/3/draft_notes/bulk_publish", 204, ``)
	s.json("POST /projects/g%2Fp/merge_requests/3/approve", 401, `{"message":"401 Unauthorized"}`)
	got, err := NewGitLab(s.URL, "t", nil).PostReview(ctx, "g/p", 3, model.ReviewSubmission{HeadSHA: "headsha", Body: "x", Verdict: model.VerdictApprove})
	if err != nil || got.VerdictErr == "" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestGitLabTokenInfo(t *testing.T) {
	s := newRev(t)
	s.json("GET /personal_access_tokens/self", 200, `{"scopes":["api"],"expires_at":"2026-12-31"}`)
	info, err := NewGitLab(s.URL, "t", nil).TokenInfo(ctx)
	if err != nil || !info.CanRead || info.CanWrite != model.TriYes || info.ExpiresAt == nil || info.ExpiresAt.Format("2006-01-02") != "2026-12-31" {
		t.Fatalf("%+v %v", info, err)
	}
	s = newRev(t)
	s.json("GET /personal_access_tokens/self", 200, `{"scopes":["read_api"],"expires_at":null}`)
	if info, _ = NewGitLab(s.URL, "t", nil).TokenInfo(ctx); !info.CanRead || info.CanWrite != model.TriNo || info.ExpiresAt != nil {
		t.Fatalf("read_api: %+v", info)
	}
	s = newRev(t)
	s.json("GET /personal_access_tokens/self", 404, `{"message":"404 Not Found"}`)
	s.json("GET /user", 200, `{"id":1,"username":"me"}`)
	if info, err = NewGitLab(s.URL, "t", nil).TokenInfo(ctx); err != nil || !info.CanRead || info.CanWrite != model.TriUnknown {
		t.Fatalf("oauth fallback: %+v %v", info, err)
	}
	s = newRev(t)
	s.json("GET /personal_access_tokens/self", 401, `{"message":"401 Unauthorized"}`)
	if _, err = NewGitLab(s.URL, "t", nil).TokenInfo(ctx); asProviderErr(t, err).Status != 401 {
		t.Fatal("401 must surface")
	}
}

func TestGitLabRateLimit(t *testing.T) {
	reset := time.Now().Add(30 * time.Second).Unix()
	s := newRev(t)
	s.json("GET /user", 429, `{"message":"Retry later"}`, "RateLimit-Reset", fmt.Sprint(reset))
	_, err := NewGitLab(s.URL, "t", nil).Me(ctx)
	pe := asProviderErr(t, err)
	if !pe.RateLimited || pe.Permanent() || pe.RetryAfter <= 0 || pe.RetryAfter > 31*time.Second {
		t.Fatalf("%+v", pe)
	}
}

// ---- Bitbucket ----

const bbPRJSON = `{"id":5,"title":"BB","description":"d","draft":false,"updated_on":"2026-11-01T10:00:00Z",
"author":{"uuid":"{a}","nickname":"alice"},"links":{"html":{"href":"https://bb/pr/5"}},
"source":{"branch":{"name":"feat"},"commit":{"hash":"abcdef123456"},"repository":{"full_name":"ws/repo"}},
"destination":{"branch":{"name":"main"},"commit":{"hash":"fedcba654321"}},
"participants":[{"user":{"uuid":"{me}","nickname":"me"},"role":"REVIEWER","approved":false,"state":"changes_requested"}]}`

func TestBitbucketListQueriesAndDedup(t *testing.T) {
	s := newRev(t)
	s.on("GET /repositories/ws/repo/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q != `state="OPEN" AND author.uuid="{me}"` && q != `state="OPEN" AND reviewers.uuid="{me}"` {
			t.Errorf("unexpected q %q", q)
		}
		io.WriteString(w, `{"values":[`+bbPRJSON+`]}`)
	})
	b := NewBitbucket(s.URL, "me@x.io", "app-pass", nil)
	got, err := b.ListPullRequests(ctx, "ws/repo", model.PRFilter{Author: model.HostUser{Login: "me", ID: "{me}"}, Reviewer: model.HostUser{Login: "me", ID: "{me}"}})
	if err != nil || len(got) != 1 || got[0].Number != 5 || got[0].Author.ID != "{a}" || got[0].Head != "feat" || got[0].RequestedReviewers[0].ID != "{me}" {
		t.Fatalf("%+v %v", got, err)
	}
	reqs := s.find("GET", "/repositories/ws/repo/pullrequests")
	if len(reqs) != 2 || !strings.HasPrefix(reqs[0].header.Get("Authorization"), "Basic ") || !strings.Contains(reqs[0].query, "state=OPEN") {
		t.Fatalf("%+v", reqs)
	}
}

func TestBitbucketListPaginatesAndFallsBackOnBadQuery(t *testing.T) {
	s := newRev(t)
	s.on("GET /repositories/ws/repo/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("q") != "":
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Invalid BBQL"}}`)
		case q.Get("page") == "2":
			io.WriteString(w, `{"values":[{"id":6,"title":"other","author":{"uuid":"{z}"},"participants":[{"user":{"uuid":"{me}"},"role":"REVIEWER"}]}]}`)
		default:
			fmt.Fprintf(w, `{"values":[{"id":9,"title":"nope","author":{"uuid":"{z}"}}],"next":"%s/repositories/ws/repo/pullrequests?state=OPEN&page=2"}`, s.URL)
		}
	})
	got, err := NewBitbucket(s.URL, "u", "p", nil).ListPullRequests(ctx, "ws/repo", model.PRFilter{Reviewer: model.HostUser{ID: "{me}"}})
	if err != nil || len(got) != 1 || got[0].Number != 6 {
		t.Fatalf("local filter over participants: %+v %v", got, err)
	}
}

func TestBitbucketDetail(t *testing.T) {
	s := newRev(t)
	s.json("GET /repositories/ws/repo/pullrequests/5", 200, bbPRJSON)
	s.json("GET /repositories/ws/repo/commit/abcdef123456", 200, `{"hash":"abcdef1234567890full"}`)
	s.json("GET /repositories/ws/repo/commit/fedcba654321", 200, `{"hash":"fedcba654321base"}`)
	s.json("GET /repositories/ws/repo/pullrequests/5/diffstat", 200, `{"values":[{"status":"modified","lines_added":3,"lines_removed":1},{"status":"added","lines_added":5,"lines_removed":0}]}`)
	s.json("GET /repositories/ws/repo/pullrequests/5/statuses", 200, `{"values":[{"state":"SUCCESSFUL"},{"state":"INPROGRESS"}]}`)
	s.json("GET /user", 200, `{"uuid":"{me}","nickname":"me"}`)
	d, err := NewBitbucket(s.URL, "u", "p", nil).GetPullRequest(ctx, "ws/repo", 5)
	if err != nil {
		t.Fatal(err)
	}
	if d.HeadSHA != "abcdef1234567890full" || d.BaseSHA != "fedcba654321base" || d.Additions != 8 || d.Deletions != 1 || d.ChangedFiles != 2 || d.CI != "running" || d.SourceRepo != "ws/repo" || d.Body != "d" {
		t.Fatalf("%+v", d)
	}
	if d.MyLatestReview == nil || d.MyLatestReview.Verdict != model.VerdictRequestChanges {
		t.Fatalf("%+v", d.MyLatestReview)
	}
}

const bbDiff = `diff --git a/a.go b/a.go
index 111..222 100644
--- a/a.go
+++ b/a.go
@@ -1 +1 @@
-a
+b
diff --git a/n.go b/n.go
new file mode 100644
--- /dev/null
+++ b/n.go
@@ -0,0 +1 @@
+x
diff --git a/old.go b/new.go
similarity index 100%
rename from old.go
rename to new.go
diff --git a/img.png b/img.png
Binary files a/img.png and b/img.png differ
`

func TestBitbucketFilesSplitsRawDiff(t *testing.T) {
	s := newRev(t)
	s.json("GET /repositories/ws/repo/pullrequests/5/diffstat", 200, `{"values":[
{"status":"modified","lines_added":1,"lines_removed":1,"old":{"path":"a.go"},"new":{"path":"a.go"}},
{"status":"added","lines_added":1,"lines_removed":0,"new":{"path":"n.go"}},
{"status":"renamed","lines_added":0,"lines_removed":0,"old":{"path":"old.go"},"new":{"path":"new.go"}},
{"status":"modified","lines_added":0,"lines_removed":0,"old":{"path":"img.png"},"new":{"path":"img.png"}},
{"status":"removed","lines_added":0,"lines_removed":40,"old":{"path":"gone.txt"}}]}`)
	s.on("GET /repositories/ws/repo/pullrequests/5/diff", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/plain" {
			t.Errorf("diff accept: %q", r.Header.Get("Accept"))
		}
		io.WriteString(w, bbDiff)
	})
	fs, err := NewBitbucket(s.URL, "u", "p", nil).PullRequestFiles(ctx, "ws/repo", 5)
	if err != nil || len(fs) != 5 {
		t.Fatalf("%v %v", fs, err)
	}
	if fs[0].Patch != "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-a\n+b\n" {
		t.Fatalf("%q", fs[0].Patch)
	}
	if fs[1].Status != "added" || fs[1].OldPath != "n.go" || !strings.HasPrefix(fs[1].Patch, "--- /dev/null\n+++ b/n.go\n") {
		t.Fatalf("%+v", fs[1])
	}
	if fs[2].Status != "renamed" || fs[2].OldPath != "old.go" || fs[2].Patch != "" || fs[2].Truncated {
		t.Fatalf("%+v", fs[2])
	}
	if fs[3].Patch != "" || fs[3].Truncated {
		t.Fatalf("binary: %+v", fs[3])
	}
	if fs[4].Status != "deleted" || fs[4].Path != "gone.txt" || !fs[4].Truncated {
		t.Fatalf("missing text for changed lines is Truncated: %+v", fs[4])
	}
}

func TestBitbucketPostReviewSkipsPostedAndReportsPartialFailure(t *testing.T) {
	s := newRev(t)
	n := 0
	s.on("POST /repositories/ws/repo/pullrequests/5/comments", func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 2 {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"line out of range"}}`)
			return
		}
		fmt.Fprintf(w, `{"id":%d}`, 900+n)
	})
	s.json("POST /repositories/ws/repo/pullrequests/5/request-changes", 200, `{}`)
	got, err := NewBitbucket(s.URL, "u", "p", nil).PostReview(ctx, "ws/repo", 5, model.ReviewSubmission{
		HeadSHA: "h", Body: "overall", Verdict: model.VerdictRequestChanges,
		Comments: []model.InlineComment{
			{DraftID: "done", Path: "a.go", Side: "new", Line: 1, Body: "x", PostedRef: "800"},
			{DraftID: "new", Path: "a.go", Side: "new", Line: 7, Body: "added line"},
			{DraftID: "bad", Path: "a.go", Side: "new", Line: 700, Body: "bad"},
			{DraftID: "old", Path: "a.go", OldPath: "a.go", Side: "old", Line: 3, Body: "removed line"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Comments["done"] != "800" || got.Comments["new"] != "901" || got.Comments["old"] != "903" || !strings.Contains(got.Failed["bad"], "line out of range") || got.VerdictErr != "" || len(got.Failed) != 1 {
		t.Fatalf("%+v", got)
	}
	cs := s.find("POST", "/repositories/ws/repo/pullrequests/5/comments")
	if len(cs) != 4 {
		t.Fatalf("posted-ref comment must be skipped; summary posted: %d calls", len(cs))
	}
	in := cs[0].body["inline"].(map[string]any)
	if in["to"] != float64(7) || in["path"] != "a.go" || in["from"] != nil || cs[0].body["content"].(map[string]any)["raw"] != "added line" {
		t.Fatalf("new-side anchor: %v", cs[0].body)
	}
	in = cs[2].body["inline"].(map[string]any)
	if in["from"] != float64(3) || in["to"] != nil {
		t.Fatalf("old-side anchor: %v", cs[2].body)
	}
	if cs[3].body["inline"] != nil || cs[3].body["content"].(map[string]any)["raw"] != "overall" {
		t.Fatalf("summary: %v", cs[3].body)
	}
	if got.Ref != "904" || len(s.find("POST", "/repositories/ws/repo/pullrequests/5/request-changes")) != 1 {
		t.Fatalf("ref %q", got.Ref)
	}
}

func TestBitbucketPostReviewVerdictFailureAndRateLimit(t *testing.T) {
	s := newRev(t)
	s.json("POST /repositories/ws/repo/pullrequests/5/approve", 409, `{"error":{"message":"You can't approve your own pull request"}}`)
	got, err := NewBitbucket(s.URL, "u", "p", nil).PostReview(ctx, "ws/repo", 5, model.ReviewSubmission{Verdict: model.VerdictApprove})
	if err != nil || !strings.Contains(got.VerdictErr, "own pull request") {
		t.Fatalf("%+v %v", got, err)
	}

	s = newRev(t)
	s.json("POST /repositories/ws/repo/pullrequests/5/comments", 429, `{}`, "Retry-After", "20")
	got, err = NewBitbucket(s.URL, "u", "p", nil).PostReview(ctx, "ws/repo", 5, model.ReviewSubmission{
		Comments: []model.InlineComment{{DraftID: "a", Path: "x", Side: "new", Line: 1}, {DraftID: "b", Path: "x", Side: "new", Line: 2}},
	})
	if pe := asProviderErr(t, err); !pe.RateLimited || pe.RetryAfter != 20*time.Second {
		t.Fatalf("%+v", pe)
	}
	if n := len(s.find("POST", "/repositories/ws/repo/pullrequests/5/comments")); n != 1 || len(got.Failed) != 0 {
		t.Fatalf("posting must stop at a rate limit: %d calls, failed %v", n, got.Failed)
	}
}

func TestBitbucketTokenInfoAndMe(t *testing.T) {
	s := newRev(t)
	s.json("GET /user", 200, `{"uuid":"{me}","nickname":"me","display_name":"Me M"}`)
	b := NewBitbucket(s.URL, "u", "p", nil)
	info, err := b.TokenInfo(ctx)
	if err != nil || !info.CanRead || info.CanWrite != model.TriUnknown || info.ExpiresAt != nil {
		t.Fatalf("%+v %v", info, err)
	}
	if me, _ := b.Me(ctx); me.ID != "{me}" || me.Login != "me" {
		t.Fatalf("%+v", me)
	}
	s = newRev(t)
	s.json("GET /user", 401, `{"error":{"message":"Invalid credentials"}}`)
	if _, err = NewBitbucket(s.URL, "u", "p", nil).TokenInfo(ctx); asProviderErr(t, err).Status != 401 {
		t.Fatal("401 must surface")
	}
}

var (
	_ usecase.PRReviewer = (*GitHub)(nil)
	_ usecase.PRReviewer = (*GitLab)(nil)
	_ usecase.PRReviewer = (*Bitbucket)(nil)
	_ usecase.CLIAuth    = (*GHAuth)(nil)
)
