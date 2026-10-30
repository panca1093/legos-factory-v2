package workflow

import "testing"

func TestInspectionBlockingDecidesAndLogicChangedAggregates(t *testing.T) {
	s := newSim(t)
	s.building(pt("a", nil, "a/"), pt("b", nil, "b/"))
	s.must(DispatchBuild([]string{"a", "b"}))
	s.built("a")
	s.built("b")
	s.must(RequestInspection("a"))
	s.must(RequestInspection("b"))
	s.emit(KindInspectionDone, InspectionDone{Task: "a", Findings: 1, SeverityMax: "minor"})
	s.emit(KindInspectionDone, InspectionDone{Task: "b", Findings: 1, Blocking: 1, SeverityMax: "major", LogicChanged: true})
	if a, _ := s.state().Task("a"); a.Status != TaskDone {
		t.Errorf("minor-only task = %s, want done", a.Status)
	}
	if b, _ := s.state().Task("b"); b.Status != TaskPending {
		t.Errorf("blocking task = %s, want pending", b.Status)
	}
	if !s.state().LogicChanged() {
		t.Error("any task with logic_changed must set it")
	}
	s.must(DispatchBuild([]string{"b"}))
	s.built("b")
	s.must(RequestInspection("b"))
	s.emit(KindInspectionDone, InspectionDone{Task: "b"})
	if s.state().LogicChanged() {
		t.Error("the latest inspection of each task decides logic_changed")
	}
}
