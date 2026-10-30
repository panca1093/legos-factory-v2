package engine

import (
	"context"
	"testing"
	"time"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/sandbox"
)

type hungSandbox struct{ sandbox.Sandbox }

func (hungSandbox) Exec(ctx context.Context, _ string, _ map[string]string, _ ...string) (string, string, int, error) {
	<-ctx.Done()
	return "", "", -1, ctx.Err()
}

func TestSandboxDownProbeTimesOut(t *testing.T) {
	old := probeTimeout
	probeTimeout = 50 * time.Millisecond
	defer func() { probeTimeout = old }()
	done := make(chan bool, 1)
	go func() { done <- sandboxDown(context.Background(), hungSandbox{}) }()
	select {
	case down := <-done:
		if !down {
			t.Error("a hung sandbox must count as down")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probe hung")
	}
}

func TestProgressIDSkipsRunAndSandboxFacts(t *testing.T) {
	facts := []fact.Fact{
		{ID: 1, Kind: fact.KindWOCreated}, {ID: 2, Kind: fact.KindTaskCommitted},
		{ID: 3, Kind: fact.KindSandboxStarted}, {ID: 4, Kind: fact.KindRunStarted}, {ID: 5, Kind: fact.KindRunStep},
		{ID: 6, Kind: fact.KindRunFinished}, {ID: 7, Kind: fact.KindSandboxStopped},
	}
	if got := progressID(facts); got != 2 {
		t.Errorf("progressID = %d, want 2", got)
	}
	if got := progressID(append(facts, fact.Fact{ID: 8, Kind: fact.KindVerifyPassed})); got != 8 {
		t.Errorf("progressID = %d, want 8", got)
	}
}
