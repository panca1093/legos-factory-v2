package scm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fake struct {
	mu    sync.Mutex
	calls []string
}

func (f *fake) serve(t *testing.T, authOK func(*http.Request) bool, routes map[string]func(*http.Request) (int, any)) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		f.mu.Lock()
		f.calls = append(f.calls, key)
		f.mu.Unlock()
		if !authOK(r) {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		h, ok := routes[key]
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
	return srv
}

func (f *fake) count(key string) int {
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

func ghAuth(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer ghp_secret" }

var req = PRRequest{Repo: "acme/greeter", Head: "factory/wo_1", Base: "main", Title: "T", Body: "B"}

func ghPullJSON(state string, merged bool) map[string]any {
	return map[string]any{"number": 7, "html_url": "https://gh/pr/7", "state": state, "merged": merged,
		"head": map[string]any{"ref": "factory/wo_1", "sha": "abc"}}
}

func TestGitHubOpenPRCreates(t *testing.T) {
	f := &fake{}
	srv := f.serve(t, ghAuth, map[string]func(*http.Request) (int, any){
		"POST /repos/acme/greeter/pulls": func(r *http.Request) (int, any) {
			if r.Header.Get("X-GitHub-Api-Version") == "" {
				t.Error("missing api version header")
			}
			return 201, ghPullJSON("open", false)
		},
	})
	p, _ := New("github", srv.URL, "", "ghp_secret", nil)
	pr, err := p.OpenPR(context.Background(), req)
	if err != nil || pr.Number != 7 || pr.URL != "https://gh/pr/7" || pr.HeadSHA != "abc" || pr.State != "open" {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
	if a := p.GitAuth(); a.Username != "x-access-token" || a.Token != "ghp_secret" {
		t.Errorf("git auth = %+v", a)
	}
}

func TestGitHubOpenPRIdempotent422(t *testing.T) {
	f := &fake{}
	srv := f.serve(t, ghAuth, map[string]func(*http.Request) (int, any){
		"POST /repos/acme/greeter/pulls": func(*http.Request) (int, any) {
			return 422, map[string]any{"message": "A pull request already exists"}
		},
		"GET /repos/acme/greeter/pulls": func(r *http.Request) (int, any) {
			if h := r.URL.Query().Get("head"); h != "acme:factory/wo_1" {
				t.Errorf("head query = %q", h)
			}
			return 200, []any{ghPullJSON("open", false)}
		},
	})
	p, _ := New("github", srv.URL, "", "ghp_secret", nil)
	pr, err := p.OpenPR(context.Background(), req)
	if err != nil || pr.Number != 7 {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
}

func TestGitHubOpenPR422WithoutExistingPRFails(t *testing.T) {
	f := &fake{}
	srv := f.serve(t, ghAuth, map[string]func(*http.Request) (int, any){
		"POST /repos/acme/greeter/pulls": func(*http.Request) (int, any) { return 422, map[string]any{"message": "No commits between"} },
		"GET /repos/acme/greeter/pulls":  func(*http.Request) (int, any) { return 200, []any{} },
	})
	p, _ := New("github", srv.URL, "", "ghp_secret", nil)
	_, err := p.OpenPR(context.Background(), req)
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Status != 422 || IsTransient(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestGitHubFindPRNone(t *testing.T) {
	f := &fake{}
	srv := f.serve(t, ghAuth, map[string]func(*http.Request) (int, any){
		"GET /repos/acme/greeter/pulls": func(*http.Request) (int, any) { return 200, []any{} },
	})
	p, _ := New("github", srv.URL, "", "ghp_secret", nil)
	if _, ok, err := p.FindPR(context.Background(), "acme/greeter", "x"); ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestGitHubPRState(t *testing.T) {
	cases := []struct {
		state  string
		merged bool
		want   string
	}{{"open", false, "open"}, {"closed", true, "merged"}, {"closed", false, "closed"}}
	for _, c := range cases {
		f := &fake{}
		srv := f.serve(t, ghAuth, map[string]func(*http.Request) (int, any){
			"GET /repos/acme/greeter/pulls/7": func(*http.Request) (int, any) { return 200, ghPullJSON(c.state, c.merged) },
		})
		p, _ := New("github", srv.URL, "", "ghp_secret", nil)
		got, err := p.PRState(context.Background(), "acme/greeter", 7)
		if err != nil || got != c.want {
			t.Errorf("%+v: got %q err=%v", c, got, err)
		}
	}
}

func TestGitHubChecksNormalization(t *testing.T) {
	runs := []map[string]any{
		{"name": "queued", "status": "queued"},
		{"name": "running", "status": "in_progress"},
		{"name": "ok", "status": "completed", "conclusion": "success", "html_url": "https://gh/run/1"},
		{"name": "neutral", "status": "completed", "conclusion": "neutral"},
		{"name": "skipped", "status": "completed", "conclusion": "skipped"},
		{"name": "failure", "status": "completed", "conclusion": "failure"},
		{"name": "cancelled", "status": "completed", "conclusion": "cancelled"},
		{"name": "timed_out", "status": "completed", "conclusion": "timed_out"},
		{"name": "action_required", "status": "completed", "conclusion": "action_required"},
	}
	statuses := []map[string]any{
		{"context": "ci/a", "state": "success", "target_url": "https://ci/a"},
		{"context": "ci/b", "state": "pending"},
		{"context": "ci/c", "state": "failure"},
		{"context": "ci/d", "state": "error"},
	}
	f := &fake{}
	srv := f.serve(t, ghAuth, map[string]func(*http.Request) (int, any){
		"GET /repos/acme/greeter/commits/abc/check-runs": func(*http.Request) (int, any) { return 200, map[string]any{"check_runs": runs} },
		"GET /repos/acme/greeter/commits/abc/status":     func(*http.Request) (int, any) { return 200, map[string]any{"statuses": statuses} },
	})
	p, _ := New("github", srv.URL, "", "ghp_secret", nil)
	got, err := p.Checks(context.Background(), "acme/greeter", "abc")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"queued": "pending", "running": "pending", "ok": "success", "neutral": "success", "skipped": "success",
		"failure": "failure", "cancelled": "failure", "timed_out": "failure", "action_required": "failure",
		"ci/a": "success", "ci/b": "pending", "ci/c": "failure", "ci/d": "failure",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d checks, want %d", len(got), len(want))
	}
	for _, c := range got {
		if want[c.Name] != c.State {
			t.Errorf("%s: got %s want %s", c.Name, c.State, want[c.Name])
		}
	}
	if got[2].URL != "https://gh/run/1" || got[9].URL != "https://ci/a" {
		t.Errorf("urls not carried: %+v %+v", got[2], got[9])
	}
}

func bbAuth(r *http.Request) bool {
	return r.Header.Get("Authorization") == "Basic "+base64.StdEncoding.EncodeToString([]byte("bob:app-secret"))
}

func bbPullJSON(state string) map[string]any {
	return map[string]any{"id": 12, "state": state,
		"links":  map[string]any{"html": map[string]any{"href": "https://bb/pr/12"}},
		"source": map[string]any{"branch": map[string]any{"name": "factory/wo_1"}, "commit": map[string]any{"hash": "def"}}}
}

func TestBitbucketOpenPRFindsExisting(t *testing.T) {
	f := &fake{}
	srv := f.serve(t, bbAuth, map[string]func(*http.Request) (int, any){
		"GET /repositories/acme/greeter/pullrequests": func(r *http.Request) (int, any) {
			if q := r.URL.Query().Get("q"); !strings.Contains(q, `source.branch.name="factory/wo_1"`) {
				t.Errorf("q = %q", q)
			}
			return 200, map[string]any{"values": []any{bbPullJSON("OPEN")}}
		},
	})
	p, _ := New("bitbucket", srv.URL, "bob", "app-secret", nil)
	pr, err := p.OpenPR(context.Background(), req)
	if err != nil || pr.Number != 12 || pr.HeadSHA != "def" || pr.State != "open" {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
	if f.count("POST /repositories/acme/greeter/pullrequests") != 0 {
		t.Error("created a duplicate PR")
	}
	if a := p.GitAuth(); a.Username != "bob" || a.Token != "app-secret" {
		t.Errorf("git auth = %+v", a)
	}
}

func TestBitbucketOpenPRCreates(t *testing.T) {
	f := &fake{}
	srv := f.serve(t, bbAuth, map[string]func(*http.Request) (int, any){
		"GET /repositories/acme/greeter/pullrequests": func(*http.Request) (int, any) {
			return 200, map[string]any{"values": []any{}}
		},
		"POST /repositories/acme/greeter/pullrequests": func(*http.Request) (int, any) { return 201, bbPullJSON("OPEN") },
	})
	p, _ := New("bitbucket", srv.URL, "bob", "app-secret", nil)
	pr, err := p.OpenPR(context.Background(), req)
	if err != nil || pr.Number != 12 {
		t.Fatalf("pr=%+v err=%v", pr, err)
	}
}

func TestBitbucketPRState(t *testing.T) {
	for in, want := range map[string]string{"OPEN": "open", "MERGED": "merged", "DECLINED": "closed", "SUPERSEDED": "closed"} {
		f := &fake{}
		srv := f.serve(t, bbAuth, map[string]func(*http.Request) (int, any){
			"GET /repositories/acme/greeter/pullrequests/12": func(*http.Request) (int, any) { return 200, bbPullJSON(in) },
		})
		p, _ := New("bitbucket", srv.URL, "bob", "app-secret", nil)
		got, err := p.PRState(context.Background(), "acme/greeter", 12)
		if err != nil || got != want {
			t.Errorf("%s: got %q err=%v", in, got, err)
		}
	}
}

func TestBitbucketChecksNormalization(t *testing.T) {
	f := &fake{}
	srv := f.serve(t, bbAuth, map[string]func(*http.Request) (int, any){
		"GET /repositories/acme/greeter/commit/def/statuses": func(*http.Request) (int, any) {
			return 200, map[string]any{"values": []any{
				map[string]any{"name": "build", "state": "INPROGRESS"},
				map[string]any{"name": "test", "state": "SUCCESSFUL", "url": "https://bb/t"},
				map[string]any{"key": "lint", "state": "FAILED"},
				map[string]any{"name": "stop", "state": "STOPPED"},
			}}
		},
	})
	p, _ := New("bitbucket", srv.URL, "bob", "app-secret", nil)
	got, err := p.Checks(context.Background(), "acme/greeter", "def")
	if err != nil {
		t.Fatal(err)
	}
	want := []Check{{"build", "pending", ""}, {"test", "success", "https://bb/t"}, {"lint", "failure", ""}, {"stop", "failure", ""}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestErrorClassificationAndNoTokenLeak(t *testing.T) {
	for _, c := range []struct {
		status    int
		transient bool
	}{{400, false}, {401, false}, {403, false}, {404, false}, {422, false}, {429, true}, {500, true}, {502, true}, {503, true}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			w.Write([]byte(`{"message":"boom"}`))
		}))
		p, _ := New("github", srv.URL, "", "ghp_secret", nil)
		_, err := p.PRState(context.Background(), "acme/greeter", 1)
		var pe *ProviderError
		if !errors.As(err, &pe) || pe.Status != c.status || pe.Message != "boom" || IsTransient(err) != c.transient {
			t.Errorf("status %d: err=%v transient=%v", c.status, err, IsTransient(err))
		}
		if strings.Contains(err.Error(), "ghp_secret") {
			t.Error("token leaked in error")
		}
		srv.Close()
	}
	if IsTransient(errors.New("plain")) {
		t.Error("plain error should be permanent")
	}
}

func TestUnreachableIsTransientAndTokenFree(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	p, _ := New("bitbucket", addr, "bob", "app-secret", nil)
	_, err := p.PRState(context.Background(), "acme/greeter", 1)
	if err == nil || !IsTransient(err) || strings.Contains(err.Error(), "app-secret") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewUnknownKind(t *testing.T) {
	if _, err := New("gitlab", "", "", "", nil); err == nil {
		t.Error("want error")
	}
}
