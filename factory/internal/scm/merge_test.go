package scm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestMergeRefusalsAndSuccess(t *testing.T) {
	const ghPath, bbPath = "PUT /repos/acme/greeter/pulls/7/merge", "POST /repositories/acme/greeter/pullrequests/7/merge"
	cases := []struct {
		kind, token, path, method string
		status                    int
		refused                   bool
	}{
		{"github", "ghp_secret", ghPath, "", 200, false},
		{"github", "ghp_secret", ghPath, "rebase", 200, false},
		{"github", "ghp_secret", ghPath, "", 405, true},
		{"github", "ghp_secret", ghPath, "", 403, true},
		{"github", "ghp_secret", ghPath, "", 409, true},
		{"github", "ghp_secret", ghPath, "", 500, false},
		{"bitbucket", "app-secret", bbPath, "", 200, false},
		{"bitbucket", "app-secret", bbPath, "merge", 200, false},
		{"bitbucket", "app-secret", bbPath, "", 403, true},
		{"bitbucket", "app-secret", bbPath, "", 409, true},
	}
	for _, c := range cases {
		f := &fake{}
		auth := ghAuth
		if c.kind == "bitbucket" {
			auth = bbAuth
		}
		srv := f.serve(t, auth, map[string]func(*http.Request) (int, any){
			c.path: func(*http.Request) (int, any) {
				return c.status, map[string]string{"message": "Base branch policy prohibits the merge"}
			},
		})
		p, _ := New(c.kind, srv.URL, "bob", c.token, nil)
		err := p.Merge(context.Background(), "acme/greeter", 7, c.method)
		if errors.Is(err, ErrMergeRefused) != c.refused || (c.status == 200) != (err == nil) {
			t.Errorf("%s %d %q: err=%v", c.kind, c.status, c.method, err)
		}
		if c.status == 500 && !IsTransient(err) {
			t.Errorf("500 must stay a transient provider error: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), c.token) {
			t.Errorf("token leaked: %v", err)
		}
	}
}

func TestMergeSendsMethod(t *testing.T) {
	for _, c := range []struct {
		kind, method, path, field, want string
	}{
		{"github", "", "PUT /repos/acme/greeter/pulls/7/merge", "merge_method", "squash"},
		{"github", "merge", "PUT /repos/acme/greeter/pulls/7/merge", "merge_method", "merge"},
		{"bitbucket", "", "POST /repositories/acme/greeter/pullrequests/7/merge", "merge_strategy", "squash"},
		{"bitbucket", "rebase", "POST /repositories/acme/greeter/pullrequests/7/merge", "merge_strategy", "fast_forward"},
	} {
		var got map[string]string
		f := &fake{}
		auth, token := ghAuth, "ghp_secret"
		if c.kind == "bitbucket" {
			auth, token = bbAuth, "app-secret"
		}
		srv := f.serve(t, auth, map[string]func(*http.Request) (int, any){
			c.path: func(r *http.Request) (int, any) {
				json.NewDecoder(r.Body).Decode(&got)
				return 200, map[string]bool{"merged": true}
			},
		})
		p, _ := New(c.kind, srv.URL, "bob", token, nil)
		if err := p.Merge(context.Background(), "acme/greeter", 7, c.method); err != nil || got[c.field] != c.want {
			t.Errorf("%s %q: body=%v err=%v", c.kind, c.method, got, err)
		}
	}
}

func TestMergeable(t *testing.T) {
	for _, c := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"mergeable": true, "mergeable_state": "clean"}, "clean"},
		{map[string]any{"mergeable": false, "mergeable_state": "dirty"}, "conflict"},
		{map[string]any{"mergeable": nil, "mergeable_state": "unknown"}, "unknown"},
		{map[string]any{"mergeable": true, "mergeable_state": "blocked"}, "clean"},
	} {
		f := &fake{}
		srv := f.serve(t, ghAuth, map[string]func(*http.Request) (int, any){
			"GET /repos/acme/greeter/pulls/7": func(*http.Request) (int, any) { return 200, c.body },
		})
		p, _ := New("github", srv.URL, "", "ghp_secret", nil)
		if got, err := p.Mergeable(context.Background(), "acme/greeter", 7); err != nil || got != c.want {
			t.Errorf("github %v: got %q err=%v", c.body, got, err)
		}
	}
	for _, c := range []struct {
		statuses []string
		want     string
	}{{[]string{"modified", "added"}, "clean"}, {[]string{"modified", "merge conflict"}, "conflict"}, {nil, "unknown"}} {
		f := &fake{}
		var vals []map[string]string
		for _, s := range c.statuses {
			vals = append(vals, map[string]string{"status": s})
		}
		srv := f.serve(t, bbAuth, map[string]func(*http.Request) (int, any){
			"GET /repositories/acme/greeter/pullrequests/7/diffstat": func(*http.Request) (int, any) { return 200, map[string]any{"values": vals} },
		})
		p, _ := New("bitbucket", srv.URL, "bob", "app-secret", nil)
		if got, err := p.Mergeable(context.Background(), "acme/greeter", 7); err != nil || got != c.want {
			t.Errorf("bitbucket %v: got %q err=%v", c.statuses, got, err)
		}
	}
}
