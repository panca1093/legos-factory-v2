package engine

import (
	"context"
	"testing"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/workflow"
)

func TestDecideIgnoresBuilderRunFactsLandingMeanwhile(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	id := ev.create(t)
	for _, f := range []struct {
		kind string
		v    any
	}{
		{fact.KindRunStarted, fact.RunStarted{Run: "r1", Role: workflow.RoleArchitect}},
		{workflow.KindSpecReady, workflow.SpecReady{Spec: "s"}},
	} {
		if _, err := ev.e.Facts.Append(ctx, id, f.kind, f.v); err != nil {
			t.Fatal(err)
		}
	}
	o := &orch{e: ev.e, woID: id, lim: workflow.DefaultLimits()}
	calls := 0
	o.beforeAppend = func() {
		calls++
		for _, k := range []string{fact.KindRunStarted, fact.KindRunStep, fact.KindRunFinished} {
			if _, err := ev.e.Facts.Append(ctx, id, k, map[string]string{"run": "builder-run"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	cerr, err := o.decide(ctx, call("approve_spec", `{}`))
	if err != nil || cerr != nil {
		t.Fatalf("decide = %v, %v", cerr, err)
	}
	if calls != 1 {
		t.Errorf("decision re-folded %d times because of run facts", calls)
	}
	if acc, _ := decisions(t, ev, id); len(acc) != 1 {
		t.Errorf("accepted = %v", acc)
	}
}
