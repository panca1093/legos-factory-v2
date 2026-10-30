package engine

import (
	"context"
	"testing"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/workflow"
)

func TestLoadProjectMaxLevelAndProtectedPatterns(t *testing.T) {
	p, err := loadProjectJSON(t, `"max_level":2,"protected_paths":["**/billing/**","*.sql"]`)
	if err != nil || p.MaxLevel == nil || *p.MaxLevel != 2 {
		t.Fatalf("max_level: %v %v", p.MaxLevel, err)
	}
	if p, err = loadProjectJSON(t, `"protected_paths":[]`); err != nil || p.MaxLevel != nil {
		t.Errorf("default max_level must be unset (3): %v %v", p.MaxLevel, err)
	}
	for _, body := range []string{`"max_level":4`, `"max_level":-1`, `"protected_paths":["a/["]`, `"protected_paths":["a/**b"]`, `"protected_paths":[""]`} {
		if _, err := loadProjectJSON(t, body); err == nil {
			t.Errorf("%s accepted", body)
		}
	}
}

func TestProjectMaxLevelCapsEffective(t *testing.T) {
	h := newScripted()
	h.build = writeIn("docs/guide.md", "built\n")
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["docs/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), dispatch(), inspectCall())
	h.say(harness.RoleInspector, cleanInspection)
	ev := lineEnv(t, h)
	two := 2
	ev.p.MaxLevel = &two
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if l := levelFact(t, ev, id); l.DiffLevel != 3 || l.Cap != 2 || l.Effective != 2 {
		t.Fatalf("level = %+v", l)
	}
}

func TestUninspectedCommitsAfterLastInspection(t *testing.T) {
	f := func(kind string) fact.Fact { return fact.Fact{Kind: kind} }
	cases := []struct {
		name  string
		facts []fact.Fact
		want  bool
	}{
		{"requeue before inspection", []fact.Fact{f(fact.KindWORequeued), f(workflow.KindInspectionDone)}, false},
		{"ci fix after inspection", []fact.Fact{f(workflow.KindInspectionDone), f(fact.KindWORequeued)}, true},
		{"rebase after inspection", []fact.Fact{f(workflow.KindInspectionDone), f(fact.KindPROpened), f(fact.KindWORequeued)}, true},
		{"requeue and no inspection", []fact.Fact{f(fact.KindWORequeued)}, true},
		{"clean", []fact.Fact{f(workflow.KindInspectionDone), f(fact.KindPROpened)}, false},
	}
	for _, c := range cases {
		if got := uninspectedCommits(c.facts); got != c.want {
			t.Errorf("%s = %v", c.name, got)
		}
	}
}

func TestInspectorMissingLogicChangedCountsAsChanged(t *testing.T) {
	_, ev, id := greenLine(t, "docs/guide.md", "docs/", `{"findings":[],"summary":"ok"}`)
	if l := levelFact(t, ev, id); l.InspectorLevel != 2 || l.Effective != 2 {
		t.Fatalf("level = %+v", l)
	}
}
