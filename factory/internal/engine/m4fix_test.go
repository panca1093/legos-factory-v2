package engine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/sandbox"
)

type restartFailsRuntime struct{ *crashRuntime }

func (r restartFailsRuntime) Start(ctx context.Context, ws sandbox.Workspace, env map[string]string) (sandbox.Sandbox, error) {
	if r.started() >= 1 {
		return nil, errors.New("docker unavailable")
	}
	return r.crashRuntime.Start(ctx, ws, env)
}

func TestFailedSandboxRestartReturnsErrorWithoutPanic(t *testing.T) {
	h := newScripted()
	rt := &crashRuntime{}
	h.build = func(task string, n int, dir string) error { rt.killLatest(); return nil }
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`))
	ev := lineEnv(t, h)
	rt.Runtime = ev.e.Runtime
	ev.e.Runtime = restartFailsRuntime{rt}
	id := ev.create(t)
	err := ev.e.Run(context.Background(), ev.p, id)
	if err == nil || err.Error() != "docker unavailable" {
		t.Fatalf("err = %v", err)
	}
}

func TestRunnerRecoversFromPanicAndBacksOff(t *testing.T) {
	ev := newEnv(t)
	ev.create(t)
	var calls atomic.Int32
	r := &Runner{Engine: ev.e, Slots: 1, Poll: 5 * time.Millisecond, Backoff: time.Hour,
		Projects: func(string) (Project, error) { calls.Add(1); panic("boom") }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Start(ctx); close(done) }()
	waitFor(t, "first attempt", func() bool { return calls.Load() == 1 })
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("attempts = %d, want the panicked work order backed off", calls.Load())
	}
	cancel()
	<-done
}
