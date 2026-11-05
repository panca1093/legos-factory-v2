package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func TestPullRequestStatus(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 6, 3, 4, 5, 0, time.UTC)
	ts := at.Format(time.RFC3339)

	type provider interface {
		PullRequestStatus(context.Context, string) (model.PRStatus, error)
	}
	tests := []struct {
		name   string
		route  string
		bodies []map[string]any // open, merged, closed
		build  func(url string) provider
		ref    string
	}{
		{"github", "GET /repos/acme/greeter/pulls/12",
			[]map[string]any{
				{"state": "open"},
				{"state": "closed", "merged_at": ts, "closed_at": ts},
				{"state": "closed", "merged_at": nil, "closed_at": ts},
			},
			func(u string) provider { return NewGitHub(u, "t", nil) }, "github:acme/greeter#12"},
		{"gitlab", "GET /projects/acme%2Fgreeter/merge_requests/7",
			[]map[string]any{
				{"state": "opened"},
				{"state": "merged", "merged_at": ts},
				{"state": "closed", "closed_at": ts},
			},
			func(u string) provider { return NewGitLab(u, "t", nil) }, "gitlab:acme/greeter!7"},
		{"bitbucket", "GET /repositories/acme/greeter/pullrequests/3",
			[]map[string]any{
				{"state": "OPEN"},
				{"state": "MERGED", "updated_on": ts},
				{"state": "DECLINED", "updated_on": ts},
			},
			func(u string) provider { return NewBitbucket(u, "u", "t", nil) }, "bitbucket:acme/greeter#3"},
	}
	want := []string{model.PRStateOpen, model.PRStateMerged, model.PRStateClosed}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFake(t, func(*http.Request) bool { return true })
			i := 0
			f.routes[tc.route] = func(*http.Request) (int, any) { return 200, tc.bodies[i] }
			p := tc.build(srv.URL)
			for i = range tc.bodies {
				st, err := p.PullRequestStatus(ctx, tc.ref)
				if err != nil || st.State != want[i] {
					t.Fatalf("case %d: %+v %v, want %s", i, st, err, want[i])
				}
				if st.State != model.PRStateOpen && !st.At.Equal(at) {
					t.Fatalf("case %d: time %v, want %v", i, st.At, at)
				}
			}
			if _, err := p.PullRequestStatus(ctx, "local:wo_1@abc"); err == nil {
				t.Fatal("a foreign ref must be rejected")
			}
		})
	}
}
