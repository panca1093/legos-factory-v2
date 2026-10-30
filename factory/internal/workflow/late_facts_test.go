package workflow

import (
	"testing"

	"legos-factory/factory/internal/fact"
)

func TestTaskFactsAfterWOFailedStillApply(t *testing.T) {
	s := newSim(t)
	s.building(pt("a", nil, "a/"), pt("b", nil, "b/"))
	s.must(DispatchBuild([]string{"a", "b"}))
	s.emit(fact.KindWOFailed, fact.WOFailed{Class: fact.ClassProvider, Message: "no funds"})
	s.emit(KindTaskBuilt, TaskBuiltPayload{Task: "b", SHA: "abc"})
	s.emit(fact.KindTaskFailed, fact.TaskFailed{Task: "a", Class: fact.ClassTask})
	st := s.state()
	if st.Phase != PhaseFailed {
		t.Fatalf("phase = %s", st.Phase)
	}
	if b, _ := st.Task("b"); b.Status != TaskBuilt || b.Commits != 1 {
		t.Errorf("b = %+v, want built", b)
	}
	s.emit(fact.KindWORetried, fact.WORetried{By: "human"})
	st = s.state()
	if b, _ := st.Task("b"); b.Status != TaskBuilt {
		t.Errorf("b after retry = %+v, want it kept built, not rebuilt", b)
	}
	if a, _ := st.Task("a"); a.Status != TaskPending {
		t.Errorf("a after retry = %+v", a)
	}
}
