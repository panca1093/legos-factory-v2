package agent

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

// TestLiveOpenCode exercises the real OpenCode server end to end and is
// gated on LF_LIVE_OPENCODE_URL to keep the default `go test ./...` hermetic.
// Run with:
//
//	LF_LIVE_OPENCODE_URL=http://127.0.0.1:14099 \
//	LF_LIVE_OPENCODE_PASSWORD=... go test ./internal/agent/ -run TestLiveOpenCode -v
func TestLiveOpenCode(t *testing.T) {
	url := os.Getenv("LF_LIVE_OPENCODE_URL")
	if url == "" {
		t.Skip("set LF_LIVE_OPENCODE_URL to verify against a real server")
	}
	pass := os.Getenv("LF_LIVE_OPENCODE_PASSWORD")
	c := NewOpenCodeClient(url, "opencode", pass)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	models, err := c.Models(ctx)
	if err != nil {
		t.Fatalf("model discovery: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("no models available on the live server; authenticate a provider first")
	}
	t.Logf("discovered %d models, using %s", len(models), models[0].ProviderID+"/"+models[0].ModelID)

	h := NewOpenCodeHarness(c)
	ses, err := h.Start(ctx, model.AgentRequest{
		Role:       model.AgentBuilder,
		Prompt:     "Reply with exactly the four characters: OKAY",
		ProviderID: models[0].ProviderID,
		ModelID:    models[0].ModelID,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res, err := h.Result(ctx, ses.ID())
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.Text == "" {
		t.Fatal("empty assistant text")
	}
	t.Logf("text=%q tokens_in=%d tokens_out=%d cost_micros=%d cost_source=%s session=%s",
		res.Text, res.TokensIn, res.TokensOut, res.CostMicros, res.CostSource, res.SessionRef)

	// cancellation against the live server
	t0 := time.Now()
	ses2, err := h.Start(ctx, model.AgentRequest{
		Role:       model.AgentBuilder,
		Prompt:     "Write a 300-line poem about bricks before answering anything else.",
		ProviderID: models[0].ProviderID,
		ModelID:    models[0].ModelID,
	})
	if err != nil {
		t.Fatalf("start2: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if err := h.Cancel(ctx, ses2.ID()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	_, err = h.Result(ctx, ses2.ID())
	if err == nil {
		t.Fatal("expected cancelled error after interrupt")
	}
	t.Logf("cancelled after %s: %v", time.Since(t0).Round(time.Millisecond), err)
	fmt.Println("LIVE_OP CODE_VERIFICATION_OK")
}
