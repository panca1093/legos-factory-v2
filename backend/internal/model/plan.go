package model

import "fmt"

type Task struct {
	ID        string   `json:"id"`
	WOID      string   `json:"work_order_id"`
	Seq       int      `json:"seq"`
	Title     string   `json:"title"`
	Objective string   `json:"objective"`
	Role      string   `json:"role"`
	Deps      []string `json:"deps"`
}

type Plan struct {
	WorkOrderID string `json:"work_order_id"`
	Tasks       []Task `json:"tasks"`
	Summary     string `json:"summary"`
	Approved    bool   `json:"approved"`
}

// ValidateDAG checks that tasks form a dependency graph without cycles,
// reference only existing tasks, and have a computable execution order.
func ValidateDAG(tasks []Task) error {
	ids := map[string]bool{}
	for _, t := range tasks {
		if t.ID == "" {
			return fmt.Errorf("DAG_INVALID: task has empty id")
		}
		if ids[t.ID] {
			return fmt.Errorf("DAG_INVALID: duplicate task id %q", t.ID)
		}
		ids[t.ID] = true
	}
	indeg := map[string]int{}
	adj := map[string][]string{}
	for _, t := range tasks {
		if _, ok := indeg[t.ID]; !ok {
			indeg[t.ID] = 0
		}
		for _, d := range t.Deps {
			if !ids[d] {
				return fmt.Errorf("DAG_INVALID: task %q depends on unknown task %q", t.ID, d)
			}
			if d == t.ID {
				return fmt.Errorf("DAG_INVALID: task %q depends on itself", t.ID)
			}
			adj[d] = append(adj[d], t.ID)
			indeg[t.ID]++
		}
	}
	queue := []string{}
	for id, n := range indeg {
		if n == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		visited++
		for _, m := range adj[n] {
			indeg[m]--
			if indeg[m] == 0 {
				queue = append(queue, m)
			}
		}
	}
	if visited != len(tasks) {
		return fmt.Errorf("DAG_CYCLE: dependency graph contains a cycle")
	}
	return nil
}

// ReadyTasks returns tasks whose dependencies are all satisfied by done.
func ReadyTasks(tasks []Task, done map[string]bool) []Task {
	var out []Task
	for _, t := range tasks {
		if done[t.ID] {
			continue
		}
		ok := true
		for _, d := range t.Deps {
			if !done[d] {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, t)
		}
	}
	return out
}
