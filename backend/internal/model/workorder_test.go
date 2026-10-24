package model

import "testing"

func TestCanTransitionHappyPath(t *testing.T) {
	path := []State{
		StateCreated, StateRequirement, StateDecomposition, StatePlanGeneration,
		StateArchitectReview, StateQueued, StateImplementing, StateVerifying,
		StateSpecialistReview, StatePRPreparation, StateMergeReadiness, StateNeedsHumanReview,
		StateImplementing, // revision loop
		StateVerifying, StateSpecialistReview, StatePRPreparation, StateMergeReadiness, StateReady,
	}
	for i := 0; i < len(path)-1; i++ {
		if err := MustTransition(path[i], path[i+1]); err != nil {
			t.Fatalf("transition %s→%s rejected: %v", path[i], path[i+1], err)
		}
	}
}

func TestInvalidTransitions(t *testing.T) {
	cases := []struct{ from, to State }{
		{StateCreated, StateImplementing},
		{StateCreated, StateArchitectReview},
		{StateQueued, StateVerifying},
		{StateVerifying, StateQueued},
		{StateImplementing, StateReady},
		{StateMergeReadiness, StateImplementing},
		{StateReady, StateImplementing},
		{StateWaitingForHuman, StateImplementing},
	}
	for _, c := range cases {
		if CanTransition(c.from, c.to) {
			t.Errorf("expected %s→%s to be rejected", c.from, c.to)
		}
		if err := MustTransition(c.from, c.to); err == nil {
			t.Errorf("MustTransition(%s,%s) returned nil", c.from, c.to)
		}
	}
}

func TestCancellationFromActiveStates(t *testing.T) {
	for _, s := range []State{StateCreated, StateArchitectReview, StateQueued, StateImplementing, StateVerifying, StateSpecialistReview} {
		if !CanTransition(s, StateCancelled) {
			t.Errorf("cancellation from %s should be allowed", s)
		}
	}
	if CanTransition(StateReady, StateCancelled) {
		t.Error("cancellation from terminal READY must be rejected")
	}
}

func TestValidateDAG(t *testing.T) {
	ok := []Task{
		{ID: "a"}, {ID: "b", Deps: []string{"a"}}, {ID: "c", Deps: []string{"b", "a"}},
	}
	if err := ValidateDAG(ok); err != nil {
		t.Fatalf("valid dag rejected: %v", err)
	}
	if err := ValidateDAG([]Task{{ID: "a", Deps: []string{"b"}}, {ID: "b", Deps: []string{"a"}}}); err == nil {
		t.Fatal("cycle accepted")
	}
	if err := ValidateDAG([]Task{{ID: "a", Deps: []string{"ghost"}}}); err == nil {
		t.Fatal("unknown dependency accepted")
	}
	if err := ValidateDAG([]Task{{ID: "a", Deps: []string{"a"}}}); err == nil {
		t.Fatal("self dependency accepted")
	}
	if err := ValidateDAG([]Task{{ID: ""}}); err == nil {
		t.Fatal("empty id accepted")
	}
}

func TestReadyTasks(t *testing.T) {
	tasks := []Task{
		{ID: "a"}, {ID: "b", Deps: []string{"a"}}, {ID: "c"},
	}
	ready := ReadyTasks(tasks, map[string]bool{})
	if len(ready) != 2 || (ready[0].ID != "a" && ready[1].ID != "a") {
		t.Fatalf("expected roots a and c, got %+v", ready)
	}
	ready = ReadyTasks(tasks, map[string]bool{"a": true})
	if len(ready) != 2 || (ready[0].ID != "b" && ready[1].ID != "b") {
		t.Fatalf("expected b and c ready, got %+v", ready)
	}
	ready = ReadyTasks(tasks, map[string]bool{"a": true, "b": true, "c": true})
	if len(ready) != 0 {
		t.Fatalf("expected none ready, got %+v", ready)
	}
}
