package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

// fakeServer mimics the verified OpenCode v2 API surface used by the adapter.
func fakeServer(t *testing.T) (*httptest.Server, *opencodeStub) {
	t.Helper()
	st := &opencodeStub{
		sessions: map[string]*stubSession{},
		failPrompt: false,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/model", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"providerID":"p1","modelID":"m1","name":"Model One"},{"providerID":"p1","modelID":"m2","name":"Model Two"}]}`)
	})
	mux.HandleFunc("POST /api/session", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		id := st.createSession()
		fmt.Fprintf(w, `{"data":{"id":%q}}`, id)
	})
	mux.HandleFunc("POST /api/session/{id}/prompt", func(w http.ResponseWriter, r *http.Request) {
		if st.failPrompt {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"_tag":"InvalidRequestError","message":"bad"}`)
			return
		}
		id := r.PathValue("id")
		st.startAssistant(id)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"id":"msg-user"}}`)
	})
	mux.HandleFunc("GET /api/session/{id}/message", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, st.messagesJSON(id))
	})
	mux.HandleFunc("POST /api/session/{id}/interrupt", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		st.interrupt(id)
		w.WriteHeader(200)
		fmt.Fprint(w, `{}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, st
}

type stubSession struct {
	id       string
	running  bool
	finished bool
}

type opencodeStub struct {
	mu           sync.Mutex
	sessions     map[string]*stubSession
	next         int
	failPrompt   bool
}

func (s *opencodeStub) createSession() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := fmt.Sprintf("ses_%d", s.next)
	s.sessions[id] = &stubSession{id: id}
	return id
}

func (s *opencodeStub) startAssistant(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess := s.sessions[id]; sess != nil {
		sess.running = true
		go func() {
			time.Sleep(1200 * time.Millisecond)
			s.mu.Lock()
			sess.running = false
			sess.finished = true
			s.mu.Unlock()
		}()
	}
}

func (s *opencodeStub) interrupt(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess := s.sessions[id]; sess != nil {
		sess.running = false
		sess.finished = true
	}
}

func (s *opencodeStub) messagesJSON(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	if sess == nil {
		return `{"data":[]}`
	}
	user := `{"type":"user","finish":""}`
	if sess.finished {
		return fmt.Sprintf(`{"data":[%s,{"type":"assistant","finish":"stop","content":[{"type":"text","text":"plan JSON here"}],"tokens":{"input":10,"output":20},"cost":0.5}]}`, user)
	}
	if sess.running {
		return fmt.Sprintf(`{"data":[%s,{"type":"assistant","finish":"","content":[]}]}`, user)
	}
	return `{"data":[` + user + `]}`
}

func TestModelDiscovery(t *testing.T) {
	srv, _ := fakeServer(t)
	c := NewOpenCodeClient(srv.URL, "opencode", "pass")
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ProviderID != "p1" || models[1].ModelID != "m2" {
		t.Fatalf("unexpected models: %+v", models)
	}
}

func TestFullSessionLifecycle(t *testing.T) {
	srv, _ := fakeServer(t)
	h := NewOpenCodeHarness(NewOpenCodeClient(srv.URL, "opencode", "pass"))
	ses, err := h.Start(context.Background(), model.AgentRequest{
		Role: model.AgentArchitect, Prompt: "draft", ProviderID: "p1", ModelID: "m2",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.Result(context.Background(), ses.ID())
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "plan JSON here" {
		t.Fatalf("unexpected result text: %q", res.Text)
	}
	if res.TokensIn != 10 || res.TokensOut != 20 {
		t.Fatalf("tokens not parsed: %+v", res)
	}
	if res.CostMicros != 500000 || res.CostSource != "measured" {
		t.Fatalf("cost not parsed: %+v", res)
	}
}

func TestPromptFailureSurfaces(t *testing.T) {
	srv, st := fakeServer(t)
	st.failPrompt = true
	h := NewOpenCodeHarness(NewOpenCodeClient(srv.URL, "opencode", "pass"))
	_, err := h.Start(context.Background(), model.AgentRequest{ProviderID: "p1", ModelID: "m1"})
	if err == nil {
		t.Fatal("expected error when prompt submission fails")
	}
}

func TestCancelInterruptsAndResultsError(t *testing.T) {
	srv, _ := fakeServer(t)
	h := NewOpenCodeHarness(NewOpenCodeClient(srv.URL, "opencode", "pass"))
	ses, err := h.Start(context.Background(), model.AgentRequest{ProviderID: "p1", ModelID: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Cancel(context.Background(), ses.ID()); err != nil {
		t.Fatal(err)
	}
	_, err = h.Result(context.Background(), ses.ID())
	if err == nil || err.Error() != "cancelled" {
		t.Fatalf("expected cancelled error, got %v", err)
	}
	// cancel of unknown session
	if err := h.Cancel(context.Background(), "nope"); err != model.ErrAgentSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestModelsEmptyListOnNoProviders(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/model", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := NewOpenCodeClient(srv.URL, "", "")
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 0 {
		t.Fatalf("expected empty list, got %+v", models)
	}
}

// OpenCode v2 lists messages newest-first; a tool-using reply spans several
// assistant steps and only the final one (finish=stop) carries the answer.
func TestAbsorbMultiStepNewestFirst(t *testing.T) {
	step := func(created int, finish, text string) string {
		content := `[{"type":"tool","name":"read"}]`
		if text != "" {
			content = fmt.Sprintf(`[{"type":"reasoning","text":"thinking"},{"type":"text","text":%q}]`, text)
		}
		return fmt.Sprintf(`{"type":"assistant","time":{"created":%d},"finish":%q,"content":%s,"tokens":{"input":1,"output":2},"cost":0}`, created, finish, content)
	}
	user := `{"type":"user","time":{"created":100}}`
	cases := []struct {
		name     string
		data     string
		wantDone bool
		wantText string
	}{
		{"final answer after tool steps",
			`[{"type":"idle","time":{"created":500}},` + step(400, "stop", `{"decision":"REVISE"}`) + `,` + step(300, "tool-calls", "") + `,` + step(200, "tool-calls", "") + `,` + user + `]`,
			true, `{"decision":"REVISE"}`},
		{"only tool steps so far",
			`[` + step(300, "tool-calls", "") + `,` + step(200, "tool-calls", "") + `,` + user + `]`,
			false, ""},
		{"newest step still streaming",
			`[` + step(400, "", "") + `,` + step(300, "tool-calls", "") + `,` + user + `]`,
			false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var msgs []message
			if err := json.Unmarshal([]byte(tc.data), &msgs); err != nil {
				t.Fatal(err)
			}
			s := &sessionState{id: "ses"}
			if done := s.absorb(msgs); done != tc.wantDone {
				t.Fatalf("done = %v, want %v", done, tc.wantDone)
			}
			if s.result.Text != tc.wantText {
				t.Fatalf("text = %q, want %q", s.result.Text, tc.wantText)
			}
		})
	}
}

func TestAbsorbProviderErrorFailsSession(t *testing.T) {
	user := `{"type":"user","time":{"created":100}}`
	idle := `{"type":"idle","time":{"created":300},"outcome":"failed","error":{"type":"provider.quota","message":"Go usage limit exceeded","status":429,"retryAfterMs":13972000}}`
	cases := []struct {
		name, assistant, want string
		permanent             bool
	}{
		{"quota",
			`{"type":"assistant","time":{"created":200},"finish":"error","error":{"type":"provider.quota","message":"Go usage limit exceeded","status":429},"content":[]}`,
			"opencode provider error (provider.quota, 429): Go usage limit exceeded", true},
		{"invalid request",
			`{"type":"assistant","time":{"created":200},"finish":"error","error":{"type":"provider.invalid-request","message":"Upstream request failed: privacy settings","status":400},"content":[]}`,
			"opencode provider error (provider.invalid-request, 400): Upstream request failed: privacy settings", true},
		{"transient",
			`{"type":"assistant","time":{"created":200},"finish":"error","error":{"type":"provider.unavailable","message":"overloaded","status":503},"content":[]}`,
			"opencode provider error (provider.unavailable, 503): overloaded", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var msgs []message
			if err := json.Unmarshal([]byte(`[`+idle+`,`+tc.assistant+`,`+user+`]`), &msgs); err != nil {
				t.Fatal(err)
			}
			s := &sessionState{id: "ses"}
			if !s.absorb(msgs) {
				t.Fatal("a failed turn must finish the session")
			}
			if s.err == nil || !strings.Contains(s.err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", s.err, tc.want)
			}
			if errors.Is(s.err, model.ErrPermanent) != tc.permanent {
				t.Fatalf("permanent = %v, want %v", !tc.permanent, tc.permanent)
			}
		})
	}
}

func TestAbsorbReportsEachToolStepOnce(t *testing.T) {
	data := `[` +
		`{"id":"m3","type":"assistant","time":{"created":300},"finish":"","content":[]},` +
		`{"id":"m2","type":"assistant","time":{"created":200},"finish":"tool-calls","tokens":{"output":7},"content":[` +
		`{"type":"tool","name":"read","state":{"status":"error","input":{"filePath":"/Users/me/repo/web/app/layout.tsx"}}}]},` +
		`{"id":"m1","type":"assistant","time":{"created":100},"finish":"tool-calls","tokens":{"output":5},"content":[` +
		`{"type":"reasoning","text":"look around"},` +
		`{"type":"tool","name":"grep","state":{"status":"completed","input":{"path":"/Users/me/repo","pattern":"tenant"}}},` +
		`{"type":"tool","name":"bash","state":{"status":"completed","input":{"command":"go test ./...\nexit 0"}}}]},` +
		`{"id":"u1","type":"user","time":{"created":50}}]`
	var msgs []message
	if err := json.Unmarshal([]byte(data), &msgs); err != nil {
		t.Fatal(err)
	}
	var got []model.AgentStep
	s := &sessionState{id: "ses", onStep: func(st model.AgentStep) { got = append(got, st) }}
	s.absorb(msgs)
	s.absorb(msgs)
	want := []model.AgentStep{
		{N: 1, TokensOut: 5, Tools: []model.AgentTool{{Name: "grep", Target: "tenant"}, {Name: "bash", Target: "go test ./..."}}},
		{N: 2, TokensOut: 7, Tools: []model.AgentTool{{Name: "read", Target: "web/app/layout.tsx", Failed: true}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("steps = %+v\nwant   %+v", got, want)
	}
}
