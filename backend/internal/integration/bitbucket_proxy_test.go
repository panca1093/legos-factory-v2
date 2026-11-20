package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestBitbucketProxySendsBearerAndIdentityAndParsesPlainBody(t *testing.T) {
	var gotAuth, gotEnc string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotEnc = r.Header.Get("Authorization"), r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Encoding", "gzip") // the proxy mislabels plain JSON
		w.Header().Set("X-OAuth-Scopes", "pullrequest, account, pipeline")
		io.WriteString(w, `{"uuid":"{me}","username":"me"}`)
	}))
	defer srv.Close()
	b := NewBitbucketProxy(srv.URL, func(context.Context, string) (string, error) { return "tok1", nil }, nil)
	u, err := b.Me(context.Background())
	if err != nil || u.Login != "me" || u.ID != "{me}" {
		t.Fatalf("%+v %v", u, err)
	}
	if gotAuth != "Bearer tok1" || gotEnc != "identity" {
		t.Fatalf("auth %q encoding %q", gotAuth, gotEnc)
	}
	info, err := b.TokenInfo(context.Background())
	if err != nil || info.CanWrite != model.TriYes {
		t.Fatalf("pullrequest scope must allow reviewing: %+v %v", info, err)
	}
	if b.GitAuth() != (model.GitAuth{}) {
		t.Fatal("the proxy token must never be offered as git credentials")
	}
}

func TestBitbucketProxyRemintsOnceOn401(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"Invalid or expired curl session token"}`)
			return
		}
		io.WriteString(w, `{"uuid":"{me}","username":"me"}`)
	}))
	defer srv.Close()
	var stales []string
	tok := func(_ context.Context, stale string) (string, error) {
		stales = append(stales, stale)
		if stale != "" {
			return "fresh", nil
		}
		return "stale", nil
	}
	if _, err := NewBitbucketProxy(srv.URL, tok, nil).Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || fmt.Sprintf("%q", stales) != `["" "stale"]` {
		t.Fatalf("calls %d stales %q", calls.Load(), stales)
	}

	calls.Store(0)
	always := func(context.Context, string) (string, error) { return "bad", nil }
	_, err := NewBitbucketProxy(srv.URL, always, nil).Me(context.Background())
	var pe *model.ProviderError
	if !errors.As(err, &pe) || pe.Status != 401 || !strings.Contains(pe.Message, "expired curl session") || calls.Load() != 2 {
		t.Fatalf("the second 401 must surface after exactly one retry: %v calls %d", err, calls.Load())
	}
	if strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("token leaked: %v", err)
	}
}

func TestBitbucketProxyFollowsApiHostNextLinksThroughTheProxy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			io.WriteString(w, `{"values":[{"id":2,"title":"b","author":{"uuid":"{z}"}}]}`)
			return
		}
		io.WriteString(w, `{"values":[{"id":1,"title":"a","author":{"uuid":"{z}"}}],"next":"https://api.bitbucket.org/2.0/repositories/ws/repo/pullrequests?state=OPEN&page=2"}`)
	}))
	defer srv.Close()
	got, err := NewBitbucketProxy(srv.URL, func(context.Context, string) (string, error) { return "t", nil }, nil).
		ListPullRequests(context.Background(), "ws/repo", model.PRFilter{})
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestBitbucketProxyNeverSendsTheTokenToAnotherHost(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
		io.WriteString(w, `{"uuid":"{me}"}`)
	}))
	defer other.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer proxy.Close()
	_, err := NewBitbucketProxy(proxy.URL, func(context.Context, string) (string, error) { return "secret", nil }, nil).Me(context.Background())
	if err == nil || leaked.Load() != 0 {
		t.Fatalf("a cross-host redirect must be refused: %v leaked=%d", err, leaked.Load())
	}
}

func TestBitbucketProxyReplaysAPostBodyOnTheRetry(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	tok := func(_ context.Context, stale string) (string, error) {
		if stale != "" {
			return "fresh", nil
		}
		return "old", nil
	}
	b := NewBitbucketProxy(srv.URL, tok, nil)
	if err := b.api.do(context.Background(), http.MethodPost, "/x", map[string]string{"a": "b"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != `{"a":"b"}` || bodies[1] != bodies[0] {
		t.Fatalf("%q", bodies)
	}
}

func TestBitbucketListKeepsStateOpenInsideEveryBBQLQuery(t *testing.T) {
	s := newRev(t)
	var qs []string
	s.on("GET /repositories/ws/repo/pullrequests", func(w http.ResponseWriter, r *http.Request) {
		qs = append(qs, r.URL.Query().Get("q"))
		io.WriteString(w, `{"values":[]}`)
	})
	b := NewBitbucket(s.URL, "u", "p", nil)
	me := model.HostUser{Login: "me", ID: "{me}"}
	if _, err := b.ListPullRequests(ctx, "ws/repo", model.PRFilter{Author: me, Reviewer: me}); err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 || qs[0] != `state="OPEN" AND author.uuid="{me}"` || qs[1] != `state="OPEN" AND reviewers.uuid="{me}"` {
		t.Fatalf("%q", qs)
	}
}
