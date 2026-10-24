// Package opencode implements the AgentHarness interface against the OpenCode
// v2 HTTP server (`opencode serve`). Endpoints verified against v2.1.3:
//
//	GET  /api/model                          model/provider discovery
//	POST /api/session                        create session
//	POST /api/session/{id}/prompt            {"text", "model":{providerID, modelID}}
//	GET  /api/session/{id}/message           message list (assistant finish/tokens)
//	POST /api/session/{id}/interrupt         cancel execution
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

type OpenCodeClient struct {
	baseURL string
	user    string
	pass    string
	http    *http.Client
}

func NewOpenCodeClient(baseURL, user, pass string) *OpenCodeClient {
	return &OpenCodeClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		user:    user,
		pass:    pass,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Model is a discovered OpenCode model entry.
type Model struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
	Name       string `json:"name"`
}

// Models lists models available on the configured server. Returns an empty
// slice (not an error) when the server has no authenticated providers yet.
func (c *OpenCodeClient) Models(ctx context.Context) ([]Model, error) {
	var env struct {
		Data []Model `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/model", nil, &env); err != nil {
		return nil, err
	}
	if env.Data == nil {
		return []Model{}, nil
	}
	return env.Data, nil
}

func (c *OpenCodeClient) do(ctx context.Context, method, path string, body []byte, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.pass != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("opencode %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("opencode %s: status %d: %s", path, resp.StatusCode, b)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *OpenCodeClient) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *OpenCodeClient) post(ctx context.Context, path string, body []byte, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

// ---- Harness implementation ----

type OpenCodeHarness struct {
	client *OpenCodeClient
	mu     sync.Mutex
	ses    map[string]*sessionState
}

type sessionState struct {
	client   *OpenCodeClient
	id       string
	done     chan struct{}
	result   model.AgentResult
	err      error
	finished bool
	mu       sync.Mutex
}

type sessionHandle struct{ id string }

func (s *sessionHandle) ID() string { return s.id }

func NewOpenCodeHarness(client *OpenCodeClient) *OpenCodeHarness {
	return &OpenCodeHarness{client: client, ses: map[string]*sessionState{}}
}

func (h *OpenCodeHarness) Name() string { return "opencode" }

func (h *OpenCodeHarness) Start(ctx context.Context, req model.AgentRequest) (model.AgentSession, error) {
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := h.client.post(ctx, "/api/session", []byte("{}"), &created); err != nil {
		return nil, fmt.Errorf("create opencode session: %w", err)
	}
	if created.Data.ID == "" {
		return nil, fmt.Errorf("opencode returned an empty session id")
	}
	st := &sessionState{client: h.client, id: created.Data.ID, done: make(chan struct{})}
	h.mu.Lock()
	h.ses[st.id] = st
	h.mu.Unlock()

	payload, err := json.Marshal(map[string]any{
		"text":  req.Prompt,
		"model": map[string]string{"providerID": req.ProviderID, "modelID": req.ModelID},
	})
	if err != nil {
		return nil, err
	}
	if err := h.client.post(ctx, "/api/session/"+st.id+"/prompt", payload, &json.RawMessage{}); err != nil {
		return nil, fmt.Errorf("submit prompt: %w", err)
	}
	go st.await()
	return &sessionHandle{id: st.id}, nil
}

// await polls the message list until the assistant turn finishes. The v2 API
// executes prompts asynchronously; polling session messages is a robust
// completion signal that survives SSE reconnects.
func (s *sessionState) await() {
	defer close(s.done)
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		s.mu.Lock()
		if s.finished {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		msgs, err := s.fetchMessages()
		if err != nil {
			continue // transient polling errors are retried until the deadline
		}
		if done := s.absorb(msgs); done {
			return
		}
	}
	s.mu.Lock()
	if !s.finished {
		s.err = fmt.Errorf("opencode execution timed out")
	}
	s.mu.Unlock()
}

// absorb updates state from the latest message list and reports completion.
func (s *sessionState) absorb(msgs []message) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Type != "assistant" {
			continue
		}
		if m.Finish == "" {
			return false // assistant turn still running
		}
		var text strings.Builder
		for _, part := range m.Content {
			if part.Type == "text" {
				text.WriteString(part.Text)
			}
		}
		s.mu.Lock()
		if !s.finished {
			s.finished = true
			s.result = model.AgentResult{
				Text:       text.String(),
				SessionRef: s.id,
				TokensIn:   int64(m.Tokens.Input),
				TokensOut:  int64(m.Tokens.Output),
				CostMicros: int64(m.Cost * 1_000_000),
				CostSource: "measured",
			}
		}
		s.mu.Unlock()
		return true
	}
	return false
}

type message struct {
	Type    string `json:"type"`
	Finish  string `json:"finish"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Tokens struct {
		Input  int `json:"input"`
		Output int `json:"output"`
	} `json:"tokens"`
	Cost float64 `json:"cost"`
}

func (s *sessionState) fetchMessages() ([]message, error) {
	var env struct {
		Data []message `json:"data"`
	}
	err := s.client.get(context.Background(), "/api/session/"+s.id+"/message", &env)
	if err != nil {
		return nil, err
	}
	return env.Data, nil
}

func (h *OpenCodeHarness) Cancel(ctx context.Context, sessionID string) error {
	h.mu.Lock()
	st, ok := h.ses[sessionID]
	h.mu.Unlock()
	if !ok {
		return model.ErrAgentSessionNotFound
	}
	st.mu.Lock()
	if st.finished {
		st.mu.Unlock()
		return nil
	}
	st.finished = true
	st.err = fmt.Errorf("cancelled")
	st.mu.Unlock()
	return h.client.post(ctx, "/api/session/"+sessionID+"/interrupt", []byte("{}"), nil)
}

func (h *OpenCodeHarness) Result(_ context.Context, sessionID string) (model.AgentResult, error) {
	h.mu.Lock()
	st, ok := h.ses[sessionID]
	h.mu.Unlock()
	if !ok {
		return model.AgentResult{}, model.ErrAgentSessionNotFound
	}
	<-st.done
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.err != nil {
		return model.AgentResult{}, st.err
	}
	return st.result, nil
}
