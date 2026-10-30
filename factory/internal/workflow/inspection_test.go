package workflow

import (
	"strings"
	"testing"

	"legos-factory/factory/internal/fact"
)

func TestTotalInspectionsSurviveRetryAndCapBlocksRetry(t *testing.T) {
	s := newSim(t)
	s.building(pt("a", nil, "a/"))
	round := func() {
		s.must(DispatchBuild([]string{"a"}))
		s.built("a")
		s.must(RequestInspection("a"))
		s.inspected("a", 1)
	}
	for range 3 {
		round()
	}
	s.emit(fact.KindTaskFailed, fact.TaskFailed{Task: "a", Class: fact.ClassTask, Reason: "inspector_rejected"})
	s.must(RetryTask("a", ChangeSame))
	if a, _ := s.state().Task("a"); a.InspectionRounds != 0 || a.TotalInspections != 3 {
		t.Fatalf("after retry rounds=%d total=%d", a.InspectionRounds, a.TotalInspections)
	}
	for range 3 {
		round()
	}
	s.emit(fact.KindTaskFailed, fact.TaskFailed{Task: "a", Class: fact.ClassTask, Reason: "inspector_rejected"})
	if err := s.call(RetryTask("a", ChangeSame)); err == nil || !strings.Contains(err.Error(), "lifetime cap") {
		t.Fatalf("retry past the lifetime cap: %v", err)
	}
}

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
