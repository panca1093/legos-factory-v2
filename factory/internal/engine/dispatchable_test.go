package engine

import (
	"testing"

	"legos-factory/factory/internal/workflow"
)

func TestNeedsDecisionSkipsTasksThatCannotBeDispatched(t *testing.T) {
	task := func(id string, status workflow.TaskStatus, paths ...string) workflow.Task {
		return workflow.Task{PlanTask: workflow.PlanTask{ID: id, Paths: paths}, Status: status}
	}
	lim := workflow.Limits{MaxBuilders: 2}
	cases := map[string]struct {
		tasks []workflow.Task
		want  bool
	}{
		"builder slots full": {[]workflow.Task{task("a", workflow.TaskBuilding, "a/"), task("b", workflow.TaskBuilding, "b/"), task("c", workflow.TaskPending, "c/")}, false},
		"overlaps active":    {[]workflow.Task{task("a", workflow.TaskBuilding, "pkg/"), task("b", workflow.TaskPending, "pkg/x")}, false},
		"dispatchable":       {[]workflow.Task{task("a", workflow.TaskBuilding, "a/"), task("b", workflow.TaskPending, "b/")}, true},
		"failed still asks":  {[]workflow.Task{task("a", workflow.TaskBuilding, "a/"), task("b", workflow.TaskBuilding, "b/"), task("c", workflow.TaskFailed, "c/")}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st := workflow.State{Phase: workflow.PhaseBuilding, Tasks: tc.tasks}
			if got := needsDecision(st, lim); got != tc.want {
				t.Errorf("needsDecision = %v, want %v", got, tc.want)
			}
		})
	}
}
