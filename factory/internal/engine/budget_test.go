package engine

import (
	"context"
	"testing"
	"time"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
)

func TestExhaustedTransientFailsOnlyThatTask(t *testing.T) {
	h := newScripted()
	release := make(chan struct{})
	h.build = func(task string, _ int, dir string) error {
		if task == "b" {
			select {
			case <-release:
			case <-time.After(10 * time.Second):
				return context.DeadlineExceeded
			}
			return writeIn("b/f.txt", "b\n")(task, 0, dir)
		}
		return nil
	}
	h.inject = func(role harness.Role, task string, _ int) error {
		if role == harness.RoleBuilder && task == "a" {
			return providerErr(true)
		}
		return nil
	}
	asked := 0
	h.onRole = func(s harness.SessionSpec) {
		if s.Role == harness.RoleForeman {
			if asked++; asked == 4 {
				close(release)
			}
		}
	}
	rt := &crashRuntime{}
	h.say(harness.RoleArchitect, "spec", planJSON(taskA+","+taskB))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["a","b"]}`),
		call("escalate_to_human", `{"reason":"a is down"}`))
	ev := lineEnv(t, h)
	rt.Runtime = ev.e.Runtime
	ev.e.Runtime = rt
	ev.e.RetryBase, ev.e.TransientRetries = time.Millisecond, 1
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatalf("Run = %v, want the failure recorded on the task only", err)
	}
	var tf fact.TaskFailed
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindTaskFailed {
			tf, _ = fact.Decode[fact.TaskFailed](f)
		}
	}
	if tf.Task != "a" || tf.Class != fact.ClassProvider || tf.Reason != "transient_exhausted" {
		t.Errorf("task.failed = %+v", tf)
	}
	if k := ev.kinds(t, ev.e, id); count(k, fact.KindSandboxRestarted) != 0 || count(k, fact.KindWOFailed) != 0 || rt.started() != 1 {
		t.Errorf("kinds = %v, sandboxes = %d", k, rt.started())
	}
}

func TestTransientBudgetSpansRunsAndFailsWorkOrder(t *testing.T) {
	h := newScripted()
	h.inject = func(role harness.Role, _ string, _ int) error {
		if role == harness.RoleArchitect {
			return providerErr(true)
		}
		return nil
	}
	ev := lineEnv(t, h)
	ev.e.RetryBase, ev.e.TransientRetries = time.Millisecond, 2
	id := ev.create(t)
	for i := 0; i < 10 && failedClass(t, ev.e, id) == ""; i++ {
		_ = ev.e.Run(context.Background(), ev.p, id)
	}
	if c := failedClass(t, ev.e, id); c != fact.ClassProvider {
		t.Fatalf("class = %q, want provider after the work-order budget", c)
	}
	if err := ev.e.Retry(context.Background(), id, "again"); err != nil {
		t.Fatal(err)
	}
	if _, retries, _ := ev.e.budgetUsed(context.Background(), id); retries != 0 {
		t.Errorf("retries after wo.retried = %d", retries)
	}
}

func TestInfraRestartBudgetSpansRuns(t *testing.T) {
	h := newScripted()
	rt := &crashRuntime{}
	h.build = func(string, int, string) error { rt.killLatest(); return nil }
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`))
	ev := lineEnv(t, h)
	rt.Runtime = ev.e.Runtime
	ev.e.Runtime = rt
	id := ev.create(t)
	for i := 0; i < maxInfraRestarts; i++ {
		if _, err := ev.e.Facts.Append(context.Background(), id, fact.KindSandboxRestarted, fact.SandboxRestarted{Attempt: i + 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if c := failedClass(t, ev.e, id); c != fact.ClassInfra {
		t.Fatalf("class = %q", c)
	}
	if rt.started() != 1 {
		t.Errorf("sandboxes = %d, want no further restart", rt.started())
	}
}
