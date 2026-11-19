package http

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

func TestBusDeliversUserEventsOnlyToThatUser(t *testing.T) {
	b := NewBus()
	ana, unAna := b.SubscribeUser("ana")
	defer unAna()
	bob, unBob := b.SubscribeUser("bob")
	defer unBob()
	global, unGlobal := b.Subscribe("*")
	defer unGlobal()
	wo, unWO := b.Subscribe("wo_1")
	defer unWO()

	b.Publish(model.LiveEvent{UserID: "ana", Kind: "pr_review.run_started"})
	b.Publish(model.LiveEvent{WOID: "wo_1", Kind: "state_changed"})

	select {
	case e := <-ana:
		if e.Kind != "pr_review.run_started" {
			t.Fatalf("%+v", e)
		}
	default:
		t.Fatal("ana must receive her event")
	}
	for name, ch := range map[string]<-chan model.LiveEvent{"bob": bob} {
		select {
		case e := <-ch:
			t.Fatalf("%s got %+v", name, e)
		default:
		}
	}
	if e := <-global; e.Kind != "state_changed" {
		t.Fatalf("the global stream must see only the WO event, got %+v", e)
	}
	if e := <-wo; e.Kind != "state_changed" {
		t.Fatalf("%+v", e)
	}
}

func TestEventsStreamCarriesOnlyTheCallersUserEvents(t *testing.T) {
	srv := bootChat(t)
	srv.Bus = NewBus()
	ts := httptest.NewServer(srv.Echo)
	t.Cleanup(ts.Close) // runs after the stream bodies are closed

	open := func(user string) *bufio.Scanner {
		req, _ := http.NewRequest("GET", ts.URL+"/api/v1/events", nil)
		req.AddCookie(&http.Cookie{Name: "factory_session", Value: "sess-" + user})
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("%v %v", err, res)
		}
		t.Cleanup(func() { res.Body.Close() })
		return bufio.NewScanner(res.Body)
	}
	ana, bob := open("ana"), open("bob")
	time.Sleep(100 * time.Millisecond) // both subscriptions exist before the publish

	srv.Bus.Publish(model.LiveEvent{UserID: "bob", Kind: "cli_login.code", Payload: map[string]any{"login_id": "l1", "code": "X"}})
	srv.Bus.Publish(model.LiveEvent{UserID: "ana", Kind: "pr_review.run_finished", Payload: map[string]any{"number": 7}})

	next := func(sc *bufio.Scanner) map[string]any {
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var m map[string]any
				if err := json.Unmarshal([]byte(data), &m); err != nil {
					t.Fatal(err)
				}
				return m
			}
		}
		t.Fatal("stream ended")
		return nil
	}
	if m := next(ana); m["kind"] != "pr_review.run_finished" || m["payload"].(map[string]any)["number"] != float64(7) {
		t.Fatalf("ana: %v", m)
	}
	if m := next(bob); m["kind"] != "cli_login.code" || m["payload"].(map[string]any)["login_id"] != "l1" {
		t.Fatalf("bob: %v", m)
	}
}

func TestReviewRoutesAuthorizeAndAnswerWithoutAnIdentity(t *testing.T) {
	srv, _, _ := bootDelete(t)
	if code, _ := chatDo(srv, "ana", "GET", "/api/v1/projects/p1/pull-requests", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured: %d", code)
	}
	srv.Reviews = &usecase.Reviews{Projects: srv.Projects, Integrations: &usecase.Integrations{}}

	if code, _ := chatDo(srv, "", "GET", "/api/v1/projects/p1/pull-requests", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _ := chatDo(srv, "bob", "GET", "/api/v1/projects/p1/pull-requests", ""); code != http.StatusNotFound {
		t.Fatalf("non-member list: %d", code)
	}
	if code, _ := chatDo(srv, "bob", "GET", "/api/v1/projects/p1/pull-requests/7/review/drafts", ""); code != http.StatusNotFound {
		t.Fatalf("non-member drafts: %d", code)
	}
	if code, _ := chatDo(srv, "vic", "PATCH", "/api/v1/projects/p1/pull-requests/7/review", `{"verdict":"approve"}`); code != http.StatusForbidden {
		t.Fatalf("viewer must not decide: %d", code)
	}
	if code, _ := chatDo(srv, "vic", "POST", "/api/v1/projects/p1/pull-requests/7/review/post", `{}`); code != http.StatusForbidden {
		t.Fatalf("viewer must not post: %d", code)
	}
	if code, _ := chatDo(srv, "ana", "GET", "/api/v1/projects/p1/pull-requests/x", ""); code != http.StatusBadRequest {
		t.Fatalf("bad number: %d", code)
	}
	if code, _ := chatDo(srv, "ana", "GET", "/api/v1/projects/p1/pull-requests?filter=zzz", ""); code != http.StatusBadRequest {
		t.Fatalf("bad filter: %d", code)
	}

	code, body := chatDo(srv, "ana", "GET", "/api/v1/projects/p1/pull-requests?filter=related", "")
	var res struct {
		Identity any
		Rows     []any
		Error    struct{ Code string }
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &res) != nil || res.Error.Code != "no_identity" || res.Identity != nil || res.Rows == nil {
		t.Fatalf("no identity is an answer, not a failure: %d %s", code, body)
	}
	if code, _ = chatDo(srv, "ana", "GET", "/api/v1/projects/p1/pull-requests/7", ""); code != http.StatusConflict {
		t.Fatalf("detail without identity: %d", code)
	}
	if code, _ = chatDo(srv, "ana", "POST", "/api/v1/review-identities/cli/login", `{"tool":"gh","host":"github.com"}`); code != http.StatusForbidden {
		t.Fatalf("cli login outside local mode: %d", code)
	}
	if code, _ = chatDo(srv, "ana", "GET", "/api/v1/projects/summary", ""); code == http.StatusNotFound {
		t.Fatal("summary route must not be shadowed by /projects/:id")
	}
}
