package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"legos-factory/backend/internal/model"
)

// fakeAPI records every request and answers from a route table keyed by
// "METHOD path" (path includes the escaped query).
type fakeAPI struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []string
	bodies map[string]map[string]any
	routes map[string]func(r *http.Request) (int, any)
	auth   func(r *http.Request) bool
}

func newFake(t *testing.T, auth func(*http.Request) bool) (*fakeAPI, *httptest.Server) {
	f := &fakeAPI{t: t, bodies: map[string]map[string]any{}, routes: map[string]func(*http.Request) (int, any){}, auth: auth}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.EscapedPath()
		f.mu.Lock()
		f.calls = append(f.calls, key)
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			var b map[string]any
			if json.Unmarshal(raw, &b) == nil {
				f.bodies[key] = b
			}
		}
		f.mu.Unlock()
		if !f.auth(r) {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		h, ok := f.routes[key]
		if !ok {
			t.Errorf("unexpected request %s?%s", key, r.URL.RawQuery)
			w.WriteHeader(404)
			return
		}
		status, body := h(r)
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeAPI) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == key {
			n++
		}
	}
	return n
}

func basicIs(r *http.Request, user, pass string) bool {
	return r.Header.Get("Authorization") == "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

var prReq = model.PRRequest{Repo: "acme/greeter", Head: "legos-factory/wo_1", Base: "main", Title: "Greeter", Body: "evidence"}

func TestGitHubContract(t *testing.T) {
	f, srv := newFake(t, func(r *http.Request) bool {
		return r.Header.Get("Authorization") == "Bearer ghp_x" && r.Header.Get("X-GitHub-Api-Version") == "2022-11-28"
	})
	created := false
	f.routes["GET /user"] = func(*http.Request) (int, any) { return 200, map[string]string{"login": "octocat"} }
	f.routes["GET /repos/acme/greeter/pulls"] = func(r *http.Request) (int, any) {
		q := r.URL.Query()
		if q.Get("head") != "acme:legos-factory/wo_1" || q.Get("base") != "main" || q.Get("state") != "open" {
			t.Errorf("bad PR lookup query: %s", r.URL.RawQuery)
		}
		if created {
			return 200, []map[string]any{{"number": 12, "html_url": "https://github.com/acme/greeter/pull/12"}}
		}
		return 200, []any{}
	}
	f.routes["POST /repos/acme/greeter/pulls"] = func(*http.Request) (int, any) {
		created = true
		return 201, map[string]any{"number": 12, "html_url": "https://github.com/acme/greeter/pull/12"}
	}
	gh := NewGitHub(srv.URL, "ghp_x", nil)
	if acct, err := gh.CheckAuth(context.Background()); err != nil || acct != "octocat" {
		t.Fatalf("check auth: %q %v", acct, err)
	}
	pr, err := gh.OpenPullRequest(context.Background(), prReq)
	if err != nil || pr.Number != 12 || pr.Ref != "github:acme/greeter#12" || !strings.HasSuffix(pr.URL, "/pull/12") {
		t.Fatalf("open PR: %+v %v", pr, err)
	}
	body := f.bodies["POST /repos/acme/greeter/pulls"]
	if body["head"] != "legos-factory/wo_1" || body["base"] != "main" || body["title"] != "Greeter" {
		t.Fatalf("bad create body: %v", body)
	}
	if again, err := gh.OpenPullRequest(context.Background(), prReq); err != nil || again.Number != 12 || f.count("POST /repos/acme/greeter/pulls") != 1 {
		t.Fatalf("retry must reuse the open PR: %+v %v posts=%d", again, err, f.count("POST /repos/acme/greeter/pulls"))
	}
	if gh.GitAuth() != (model.GitAuth{Username: "x-access-token", Password: "ghp_x"}) {
		t.Fatalf("git auth: %+v", gh.GitAuth())
	}
}

func TestGitLabContract(t *testing.T) {
	f, srv := newFake(t, func(r *http.Request) bool { return r.Header.Get("PRIVATE-TOKEN") == "glpat" })
	f.routes["GET /user"] = func(*http.Request) (int, any) { return 200, map[string]string{"username": "tanuki"} }
	f.routes["GET /projects/acme%2Fgreeter/merge_requests"] = func(r *http.Request) (int, any) {
		if r.URL.Query().Get("source_branch") != "legos-factory/wo_1" || r.URL.Query().Get("state") != "opened" {
			t.Errorf("bad MR lookup: %s", r.URL.RawQuery)
		}
		return 200, []any{}
	}
	f.routes["POST /projects/acme%2Fgreeter/merge_requests"] = func(*http.Request) (int, any) {
		return 201, map[string]any{"iid": 7, "web_url": "https://gitlab.com/acme/greeter/-/merge_requests/7"}
	}
	gl := NewGitLab(srv.URL, "glpat", nil)
	if acct, err := gl.CheckAuth(context.Background()); err != nil || acct != "tanuki" {
		t.Fatalf("check auth: %q %v", acct, err)
	}
	pr, err := gl.OpenPullRequest(context.Background(), prReq)
	if err != nil || pr.Ref != "gitlab:acme/greeter!7" {
		t.Fatalf("open MR: %+v %v", pr, err)
	}
	if b := f.bodies["POST /projects/acme%2Fgreeter/merge_requests"]; b["source_branch"] != "legos-factory/wo_1" || b["target_branch"] != "main" {
		t.Fatalf("bad MR body: %v", b)
	}
}

func TestBitbucketContract(t *testing.T) {
	f, srv := newFake(t, func(r *http.Request) bool { return basicIs(r, "dev@acme.io", "bbtok") })
	f.routes["GET /user"] = func(*http.Request) (int, any) { return 200, map[string]string{"username": "dev"} }
	f.routes["GET /repositories/acme/greeter/pullrequests"] = func(r *http.Request) (int, any) {
		if q := r.URL.Query().Get("q"); q != `source.branch.name="legos-factory/wo_1" AND destination.branch.name="main"` {
			t.Errorf("bad PR query: %s", q)
		}
		return 200, map[string]any{"values": []any{}}
	}
	f.routes["POST /repositories/acme/greeter/pullrequests"] = func(*http.Request) (int, any) {
		return 201, map[string]any{"id": 3, "links": map[string]any{"html": map[string]string{"href": "https://bitbucket.org/acme/greeter/pull-requests/3"}}}
	}
	bb := NewBitbucket(srv.URL, "dev@acme.io", "bbtok", nil)
	if acct, err := bb.CheckAuth(context.Background()); err != nil || acct != "dev" {
		t.Fatalf("check auth: %q %v", acct, err)
	}
	pr, err := bb.OpenPullRequest(context.Background(), prReq)
	if err != nil || pr.Ref != "bitbucket:acme/greeter#3" || pr.URL == "" {
		t.Fatalf("open PR: %+v %v", pr, err)
	}
	b := f.bodies["POST /repositories/acme/greeter/pullrequests"]
	src, _ := b["source"].(map[string]any)["branch"].(map[string]any)
	if src["name"] != "legos-factory/wo_1" {
		t.Fatalf("bad PR body: %v", b)
	}
}

func TestJiraContract(t *testing.T) {
	f, srv := newFake(t, func(r *http.Request) bool { return basicIs(r, "pm@acme.io", "jtok") })
	f.routes["GET /rest/api/3/myself"] = func(*http.Request) (int, any) {
		return 200, map[string]string{"emailAddress": "pm@acme.io"}
	}
	f.routes["GET /rest/api/3/issue/PAY-42"] = func(r *http.Request) (int, any) {
		return 200, map[string]any{"key": "PAY-42", "fields": map[string]any{
			"summary": "Idempotent payment retries",
			"description": map[string]any{"type": "doc", "content": []any{
				map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "Retries must not double charge."}}},
				map[string]any{"type": "bulletList", "content": []any{
					map[string]any{"type": "listItem", "content": []any{map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "24h window"}}}}},
				}},
			}},
		}}
	}
	j := NewJira(srv.URL, "pm@acme.io", "jtok", nil)
	if acct, err := j.CheckAuth(context.Background()); err != nil || acct != "pm@acme.io" {
		t.Fatalf("check auth: %q %v", acct, err)
	}
	is, err := j.GetIssue(context.Background(), " pay-42 ")
	if err != nil || is.Title != "Idempotent payment retries" || is.URL != srv.URL+"/browse/PAY-42" {
		t.Fatalf("issue: %+v %v", is, err)
	}
	if is.Description != "Retries must not double charge.\n- 24h window" {
		t.Fatalf("ADF flattening: %q", is.Description)
	}
	before := len(f.calls)
	if _, err := j.GetIssue(context.Background(), "../../admin"); err == nil || len(f.calls) != before {
		t.Fatal("malformed keys must be rejected before any request")
	}
}

func TestProviderErrorsArePermanentOnAuthFailure(t *testing.T) {
	_, srv := newFake(t, func(*http.Request) bool { return false })
	_, err := NewGitHub(srv.URL, "wrong", nil).CheckAuth(context.Background())
	var pe *model.ProviderError
	if !errors.As(err, &pe) || pe.Status != 401 || !pe.Permanent() || pe.Message != "Bad credentials" {
		t.Fatalf("expected permanent 401 with provider message, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("errors must never echo the token")
	}
	if (&model.ProviderError{Status: 502}).Permanent() {
		t.Fatal("5xx must stay retryable")
	}
}
