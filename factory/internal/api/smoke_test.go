package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/profile"
	"legos-factory/factory/internal/sandbox"
)

type gateHarness struct{ gate chan struct{} }

func (h gateHarness) Start(_ context.Context, s harness.SessionSpec) (harness.Session, error) {
	return &gateSession{gate: h.gate, dir: s.Dir, ch: make(chan harness.Event, 2)}, nil
}

type gateSession struct {
	gate chan struct{}
	dir  string
	ch   chan harness.Event
}

func (s *gateSession) ID() string                   { return "s" }
func (s *gateSession) Events() <-chan harness.Event { return s.ch }
func (s *gateSession) Close() error                 { close(s.ch); return nil }
func (s *gateSession) Prompt(context.Context, string) (string, error) {
	<-s.gate
	s.ch <- harness.Event{Kind: harness.EventUsage, Usage: harness.Usage{InputTokens: 1}}
	return "end_turn", os.WriteFile(filepath.Join(s.dir, "SMOKE.md"), []byte("smoke ok\n"), 0o644)
}

func TestSmokeIsAsyncAndSingleFlight(t *testing.T) {
	c, s := newClient(t)
	gate := make(chan struct{})
	s.Engine.Harness = gateHarness{gate}
	s.Engine.Runtime = sandbox.Local{Root: t.TempDir()}
	cred, err := s.Profiles.CreateCredential(context.Background(), profile.Credential{Name: "c", Kind: "api_key", Provider: "acme", BaseURL: "http://127.0.0.1:1"}, "tok")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Profiles.CreateProfile(context.Background(), profile.Profile{Name: "p", Agent: "claude-agent-acp", CredentialID: cred.ID, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	c.mustCall("POST", "/profiles/"+p.ID+"/smoke", nil, http.StatusAccepted)
	c.mustCall("POST", "/profiles/"+p.ID+"/smoke", nil, http.StatusConflict)
	close(gate)

	deadline := time.Now().Add(60 * time.Second)
	for {
		var got profile.Profile
		if err := json.Unmarshal(c.mustCall("GET", "/profiles/"+p.ID, nil, http.StatusOK), &got); err != nil {
			t.Fatal(err)
		}
		if got.SmokeAt != nil {
			if got.SmokeError != "" || got.VerifiedAt == nil {
				t.Fatalf("profile = %+v", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("smoke never finished")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
