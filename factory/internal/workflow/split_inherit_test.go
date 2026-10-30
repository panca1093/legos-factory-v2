package workflow

import (
	"strings"
	"testing"

	"legos-factory/factory/internal/fact"
)

func TestSplitSubTasksInheritRetriesAndCannotSplitAgain(t *testing.T) {
	s := newSim(t)
	s.building(pt("a", nil, "a/"), pt("b", nil, "b/"))
	s.must(DispatchBuild([]string{"a", "b"}))
	s.emit(KindIntegrationConflict, IntegrationConflict{A: "a", B: "b", Files: []string{"x"}})
	s.emit(fact.KindTaskFailed, fact.TaskFailed{Task: "a", Class: "task"})
	s.must(RetryTask("a", ChangeSplit))
	s.emit(KindPlanSplit, PlanSplit{Task: "a", Tasks: []PlanTask{pt("a1", nil, "a/x/"), pt("a2", nil, "a/y/")}})
	st := s.state()
	a1, _ := st.Task("a1")
	if a1.Retries != 1 || a1.SplitFrom != "a" {
		t.Errorf("a1 = %+v, want inherited retries and SplitFrom", a1)
	}
	if len(st.Conflicts) != 0 {
		t.Errorf("conflicts = %+v, want the replaced task's dropped", st.Conflicts)
	}
	s.must(DispatchBuild([]string{"a1"}))
	s.emit(fact.KindTaskFailed, fact.TaskFailed{Task: "a1", Class: "task"})
	err := s.call(RetryTask("a1", ChangeSplit))
	if err == nil || !strings.Contains(err.Error(), "came from splitting") {
		t.Fatalf("err = %v", err)
	}
	s.must(RetryTask("a1", ChangeSame))
}
