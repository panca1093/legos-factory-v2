package opencode

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/agents"
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
	c := NewClient(srv.URL, "opencode", "pass")
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
	h := NewHarness(NewClient(srv.URL, "opencode", "pass"))
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
	h := NewHarness(NewClient(srv.URL, "opencode", "pass"))
	_, err := h.Start(context.Background(), model.AgentRequest{ProviderID: "p1", ModelID: "m1"})
	if err == nil {
		t.Fatal("expected error when prompt submission fails")
	}
}

func TestCancelInterruptsAndResultsError(t *testing.T) {
	srv, _ := fakeServer(t)
	h := NewHarness(NewClient(srv.URL, "opencode", "pass"))
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
	if err := h.Cancel(context.Background(), "nope"); err != agents.ErrSessionNotFound {
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
	c := NewClient(srv.URL, "", "")
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 0 {
		t.Fatalf("expected empty list, got %+v", models)
	}
}
