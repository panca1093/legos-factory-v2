package engine

import (
	"context"
	"testing"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/workflow"
)

func TestApproveAfterUnmappedVerifyFailureReRunsVerify(t *testing.T) {
	h := newScripted()
	h.build = func(task string, n int, dir string) error { return writeIn(task+"/f.txt", task+"\n")(task, n, dir) }
	h.say(harness.RoleArchitect, "spec", planJSON(taskA+","+taskBAfterA))
	h.say(harness.RoleForeman, sequentialForeman()...)
	ev := lineEnv(t, h, `if [ -f ../verify-mark ] || [ -f verify-mark ]; then exit 0; fi; touch verify-mark; echo "mystery failure"; exit 1`)
	id := ev.create(t)
	ctx := context.Background()
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusAwaitingHuman {
		t.Fatalf("status = %s", st)
	}
	if p := phase(t, ev, id); p != workflow.PhaseAwaitingHuman {
		t.Fatalf("phase = %s", p)
	}
	if _, err := ev.e.Facts.Append(ctx, id, workflow.KindHumanApproved, workflow.HumanApproved{Gate: workflow.GateEscalation}); err != nil {
		t.Fatal(err)
	}
	if p := phase(t, ev, id); p != workflow.PhaseIntegrating {
		t.Fatalf("phase after approve = %s, want integrating so verify re-runs", p)
	}
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	if k := ev.kinds(t, ev.e, id); count(k, fact.KindVerifyFailed) != 1 || count(k, fact.KindVerifyPassed) != 1 || count(k, workflow.KindEscalated) != 1 {
		t.Errorf("kinds = %v", k)
	}
}
