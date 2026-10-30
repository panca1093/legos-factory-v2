package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
)

func TestWorkOrderFailureCancelsSiblingBuilders(t *testing.T) {
	h := newScripted()
	var ev *env
	var id string
	h.inject = func(role harness.Role, task string, _ int) error {
		if role == harness.RoleBuilder && task == "a" {
			return providerErr(false)
		}
		return nil
	}
	h.build = func(task string, _ int, dir string) error {
		if task != "b" {
			return nil
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) { // b outlives a's failure, as a slow sibling would
			facts, _ := ev.e.Facts.List(context.Background(), id)
			for _, f := range facts {
				if f.Kind == fact.KindWOFailed {
					return writeIn("b/f.txt", "b\n")(task, 0, dir)
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		return errors.New("a never failed the work order")
	}
	h.say(harness.RoleArchitect, "spec", planJSON(taskA+","+taskB))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["a","b"]}`))
	ev = lineEnv(t, h)
	id = ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	failedAt := int64(0)
	for _, f := range mustList(t, ev, id) {
		switch f.Kind {
		case fact.KindWOFailed:
			failedAt = f.ID
		case "task.built":
			if failedAt != 0 {
				t.Errorf("task.built recorded after wo.failed: %s", f.Payload)
			}
		}
	}
	if failedAt == 0 {
		t.Fatal("no wo.failed")
	}
}
