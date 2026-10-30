package workflow

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"legos-factory/factory/internal/fact"
)

const (
	ToolApproveSpec       = "approve_spec"
	ToolRejectSpec        = "reject_spec"
	ToolApprovePlan       = "approve_plan"
	ToolRejectPlan        = "reject_plan"
	ToolDispatchBuild     = "dispatch_build"
	ToolRequestInspection = "request_inspection"
	ToolRetryTask         = "retry_task"
	ToolSerialize         = "serialize"
	ToolEscalateToHuman   = "escalate_to_human"
)

type Limits struct {
	MaxBuilders              int
	MaxSpecRounds            int
	MaxPlanRounds            int
	MaxInspectionRounds      int
	MaxRetries               int
	MaxConsecutiveRejections int
	BuilderFallbacks         int // number of fallback builder profiles other_model can move through
}

// MaxTotalInspections bounds a task's inspections across retries, which reset the per-attempt round count.
func (l Limits) MaxTotalInspections() int { return l.MaxInspectionRounds * 2 }

func DefaultLimits() Limits {
	return Limits{MaxBuilders: 3, MaxSpecRounds: 3, MaxPlanRounds: 3, MaxInspectionRounds: 3, MaxRetries: 2, MaxConsecutiveRejections: 3}
}

// Call is one Foreman tool invocation; only the fields of its Tool are used.
type Call struct {
	Tool    string
	Note    string // approve_*
	Reason  string // reject_*, escalate_to_human
	TaskIDs []string
	TaskID  string
	Change  string
	A, B    string
}

func ApproveSpec(note string) Call    { return Call{Tool: ToolApproveSpec, Note: note} }
func RejectSpec(reason string) Call   { return Call{Tool: ToolRejectSpec, Reason: reason} }
func ApprovePlan(note string) Call    { return Call{Tool: ToolApprovePlan, Note: note} }
func RejectPlan(reason string) Call   { return Call{Tool: ToolRejectPlan, Reason: reason} }
func DispatchBuild(ids []string) Call { return Call{Tool: ToolDispatchBuild, TaskIDs: ids} }
func RequestInspection(id string) Call {
	return Call{Tool: ToolRequestInspection, TaskID: id}
}
func RetryTask(id, change string) Call {
	return Call{Tool: ToolRetryTask, TaskID: id, Change: change}
}
func Serialize(a, b string) Call         { return Call{Tool: ToolSerialize, A: a, B: b} }
func EscalateToHuman(reason string) Call { return Call{Tool: ToolEscalateToHuman, Reason: reason} }

type callArgs struct {
	Note    string   `json:"note,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	TaskIDs []string `json:"task_ids,omitempty"`
	TaskID  string   `json:"task_id,omitempty"`
	Change  string   `json:"change,omitempty"`
	TaskA   string   `json:"task_a,omitempty"`
	TaskB   string   `json:"task_b,omitempty"`
}

func ParseCall(tool string, args json.RawMessage) (Call, error) {
	var a callArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return Call{}, fmt.Errorf("%s: invalid arguments: %w", tool, err)
		}
	}
	c := Call{Tool: tool, Note: a.Note, Reason: a.Reason, TaskIDs: a.TaskIDs, TaskID: a.TaskID, Change: a.Change, A: a.TaskA, B: a.TaskB}
	switch tool {
	case ToolApproveSpec, ToolRejectSpec, ToolApprovePlan, ToolRejectPlan, ToolDispatchBuild,
		ToolRequestInspection, ToolRetryTask, ToolSerialize, ToolEscalateToHuman:
		return c, nil
	}
	return Call{}, fmt.Errorf("unknown tool %q", tool)
}

func (c Call) Args() json.RawMessage {
	raw, _ := json.Marshal(callArgs{Note: c.Note, Reason: c.Reason, TaskIDs: c.TaskIDs, TaskID: c.TaskID, Change: c.Change, TaskA: c.A, TaskB: c.B})
	return raw
}

// key is the call's identity for replay: free-text notes and reasons do not count.
func (c Call) key() string {
	ids := slices.Clone(c.TaskIDs)
	slices.Sort(ids)
	return strings.Join(append([]string{c.Tool, c.TaskID, c.Change, c.A, c.B}, ids...), "\x00")
}

type NewFact = fact.NewFact

// Check returns nil when the call is legal in this state; the error text goes back to the Foreman verbatim.
func (s State) Check(call Call, lim Limits) error {
	err := s.check(call, lim)
	if err != nil {
		return fmt.Errorf("%s rejected: %w", call.Tool, err)
	}
	return nil
}

func (s State) check(c Call, lim Limits) error {
	switch c.Tool {
	case ToolApproveSpec:
		return s.needPhase(PhaseSpecReady)
	case ToolRejectSpec:
		if err := s.needPhase(PhaseSpecReady); err != nil {
			return err
		}
		if c.Reason == "" {
			return fmt.Errorf("reason is required")
		}
		if s.SpecRejections >= lim.MaxSpecRounds {
			return fmt.Errorf("spec revision limit reached (%d rounds); approve or escalate_to_human", lim.MaxSpecRounds)
		}
	case ToolApprovePlan:
		if err := s.needPhase(PhasePlanReady); err != nil {
			return err
		}
		return s.checkPlan()
	case ToolRejectPlan:
		if err := s.needPhase(PhasePlanReady); err != nil {
			return err
		}
		if c.Reason == "" {
			return fmt.Errorf("reason is required")
		}
		if s.PlanRejections >= lim.MaxPlanRounds {
			return fmt.Errorf("plan revision limit reached (%d rounds); approve or escalate_to_human", lim.MaxPlanRounds)
		}
	case ToolDispatchBuild:
		return s.checkDispatch(c.TaskIDs, lim)
	case ToolRequestInspection:
		if err := s.needPhase(PhaseBuilding); err != nil {
			return err
		}
		t, ok := s.Task(c.TaskID)
		if !ok {
			return fmt.Errorf("unknown task %q", c.TaskID)
		}
		if t.Status != TaskBuilt {
			return fmt.Errorf("task %s is %s; inspection needs a built task", t.ID, t.Status)
		}
		if t.Commits <= t.InspectedCommits {
			return fmt.Errorf("task %s has no new commits since its last inspection", t.ID)
		}
		if t.TotalInspections >= lim.MaxTotalInspections() {
			return fmt.Errorf("task %s used its lifetime cap of %d inspections; escalate_to_human", t.ID, lim.MaxTotalInspections())
		}
		if t.InspectionRounds >= lim.MaxInspectionRounds {
			return fmt.Errorf("task %s used all %d inspection rounds; retry_task or escalate_to_human", t.ID, lim.MaxInspectionRounds)
		}
	case ToolRetryTask:
		if err := s.needPhase(PhaseBuilding); err != nil {
			return err
		}
		t, ok := s.Task(c.TaskID)
		if !ok {
			return fmt.Errorf("unknown task %q", c.TaskID)
		}
		if t.Status != TaskFailed {
			return fmt.Errorf("task %s is %s; only failed tasks can be retried", t.ID, t.Status)
		}
		if t.Retries >= lim.MaxRetries {
			return fmt.Errorf("task %s used all %d retries; escalate_to_human", t.ID, lim.MaxRetries)
		}
		if t.TotalInspections >= lim.MaxTotalInspections() {
			return fmt.Errorf("task %s was rejected by the Inspector in its lifetime cap of %d inspections; a retry cannot reset that, escalate_to_human", t.ID, lim.MaxTotalInspections())
		}
		if !slices.Contains([]string{ChangeSame, ChangeOtherModel, ChangeSplit}, c.Change) {
			return fmt.Errorf("change %q invalid; use same, other_model or split", c.Change)
		}
		if c.Change == ChangeSplit && t.SplitFrom != "" {
			return fmt.Errorf("task %s already came from splitting %s; use same or other_model, or escalate_to_human", t.ID, t.SplitFrom)
		}
		if c.Change == ChangeOtherModel && t.ModelChanges >= lim.BuilderFallbacks {
			return fmt.Errorf("no fallback model left for task %s (%d configured, %d used); use same, split or escalate_to_human", t.ID, lim.BuilderFallbacks, t.ModelChanges)
		}
	case ToolSerialize:
		if c.A == c.B {
			return fmt.Errorf("cannot serialize task %q with itself", c.A)
		}
		for _, cf := range s.Conflicts {
			if cf.A == c.A && cf.B == c.B && !cf.Serialized {
				if err := s.needPhase(PhaseIntegrating); err != nil {
					return err
				}
				if s.ancestors(c.A)[c.B] {
					return fmt.Errorf("task %s depends on %s; serializing %s onto %s would create a cycle", c.A, c.B, c.B, c.A)
				}
				return nil
			}
		}
		for _, cf := range s.Conflicts {
			if cf.A == c.B && cf.B == c.A && !cf.Serialized {
				return fmt.Errorf("the recorded order is task_a=%s (already integrated), task_b=%s (conflicting); task_b is the one re-run on top of task_a", cf.A, cf.B)
			}
		}
		return fmt.Errorf("no unresolved integration conflict recorded between %s and %s", c.A, c.B)
	case ToolEscalateToHuman:
	default:
		return fmt.Errorf("unknown tool")
	}
	return nil
}

func (s State) needPhase(want Phase) error {
	if s.Phase != want {
		return fmt.Errorf("work order is %s; this call needs %s", s.Phase, want)
	}
	return nil
}

func (s State) checkDispatch(ids []string, lim Limits) error {
	if err := s.needPhase(PhaseBuilding); err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("task_ids is empty")
	}
	active := s.ActiveBuilders()
	if len(active)+len(ids) > lim.MaxBuilders {
		return fmt.Errorf("%d builders active, dispatching %d exceeds the limit of %d", len(active), len(ids), lim.MaxBuilders)
	}
	seen := map[string]bool{}
	var batch []Task
	for _, id := range ids {
		t, ok := s.Task(id)
		switch {
		case !ok:
			return fmt.Errorf("unknown task %q", id)
		case seen[id]:
			return fmt.Errorf("task %s listed twice", id)
		case t.Status != TaskPending:
			return fmt.Errorf("task %s is %s; only pending tasks can be dispatched", id, t.Status)
		}
		seen[id] = true
		for _, dep := range t.DependsOn {
			if d, ok := s.Task(dep); !ok || d.Status != TaskDone {
				return fmt.Errorf("task %s depends on %s which is not done", id, dep)
			}
		}
		batch = append(batch, t)
	}
	var running []Task
	for _, id := range active {
		t, _ := s.Task(id)
		running = append(running, t)
	}
	for i, t := range batch {
		for _, o := range append(slices.Clone(running), batch[:i]...) {
			if p, q, bad := overlap(t.Paths, o.Paths); bad {
				return fmt.Errorf("task %s owns %q which overlaps %q owned by %s", t.ID, p, q, o.ID)
			}
		}
	}
	return nil
}

// CanDispatch reports whether dispatching the task alone would be accepted now.
func (s State) CanDispatch(id string, lim Limits) bool {
	return s.checkDispatch([]string{id}, lim) == nil
}

func (s State) checkPlan() error {
	if len(s.Tasks) == 0 {
		return fmt.Errorf("plan has no tasks")
	}
	ids := map[string]bool{}
	for _, t := range s.Tasks {
		if t.ID == "" || ids[t.ID] {
			return fmt.Errorf("plan has an empty or duplicate task id %q", t.ID)
		}
		ids[t.ID] = true
	}
	for _, t := range s.Tasks {
		if len(t.Paths) == 0 {
			return fmt.Errorf("task %s has no owned_paths; every task must own at least one path", t.ID)
		}
		for _, p := range t.Paths {
			if _, err := CleanPath(p); err != nil {
				return fmt.Errorf("task %s: %w", t.ID, err)
			}
		}
		for _, d := range t.DependsOn {
			if !ids[d] {
				return fmt.Errorf("task %s depends on unknown task %s", t.ID, d)
			}
		}
	}
	reach := map[string]map[string]bool{}
	for _, t := range s.Tasks {
		reach[t.ID] = s.ancestors(t.ID)
		if reach[t.ID][t.ID] {
			return fmt.Errorf("task %s depends on itself through a cycle", t.ID)
		}
	}
	for i, a := range s.Tasks {
		for _, b := range s.Tasks[i+1:] {
			if reach[a.ID][b.ID] || reach[b.ID][a.ID] {
				continue
			}
			if p, q, bad := overlap(a.Paths, b.Paths); bad {
				return fmt.Errorf("tasks %s and %s can run in parallel but own overlapping paths %q and %q; add depends_on or make the paths disjoint", a.ID, b.ID, p, q)
			}
		}
	}
	return nil
}

func (s State) ancestors(id string) map[string]bool {
	out := map[string]bool{}
	var walk func(string)
	walk = func(cur string) {
		t, _ := s.Task(cur)
		for _, d := range t.DependsOn {
			if !out[d] {
				out[d] = true
				walk(d)
			}
		}
	}
	walk(id)
	return out
}

// CleanPath normalizes an owned path and rejects empty, absolute and repo-escaping ones.
func CleanPath(p string) (string, error) {
	c := path.Clean(strings.TrimSpace(p))
	switch {
	case strings.TrimSpace(p) == "":
		return "", fmt.Errorf("empty owned path")
	case path.IsAbs(c):
		return "", fmt.Errorf("owned path %q is absolute; use a path relative to the repository root", p)
	case c == ".." || strings.HasPrefix(c, "../"):
		return "", fmt.Errorf("owned path %q escapes the repository", p)
	}
	return c, nil
}

// Within reports whether every file under sub is also under one of the parent paths (strict containment, unlike the overlap test).
func Within(sub string, parents []string) bool {
	x := prefix(sub)
	for _, p := range parents {
		y := prefix(p)
		if y == "" || x == y || strings.HasPrefix(x, y+"/") {
			return true
		}
	}
	return false
}

// ValidateSplit checks sub-tasks against the plan as it would stand after replacing task id: ids are fresh, owned paths
// stay within the parent's, and the resulting plan is valid.
func (s State) ValidateSplit(id string, subs []PlanTask) error {
	parent, ok := s.Task(id)
	if !ok {
		return fmt.Errorf("unknown task %q", id)
	}
	if len(subs) < 2 {
		return fmt.Errorf("a split needs at least 2 tasks, got %d", len(subs))
	}
	for _, t := range subs {
		if slices.ContainsFunc(s.Tasks, func(o Task) bool { return o.ID == t.ID }) {
			return fmt.Errorf("task id %q already exists in the plan", t.ID)
		}
		for _, p := range t.Paths {
			if !Within(p, parent.Paths) {
				return fmt.Errorf("task %s owns %q which is not within the parent's paths %v", t.ID, p, parent.Paths)
			}
		}
	}
	next := s
	next.Tasks = slices.Clone(s.Tasks)
	next.applySplit(PlanSplit{Task: id, Tasks: subs})
	return next.checkPlan()
}

// Owned reports whether file falls under one of the owned paths, with the same prefix semantics as the parallel-overlap guard.
func Owned(file string, owned []string) bool {
	for _, o := range owned {
		if pathsOverlap(file, o) {
			return true
		}
	}
	return false
}

// prefix reduces a simple glob to its literal directory prefix on segment boundaries; "" matches everything.
func prefix(p string) string {
	var keep []string
	for _, seg := range strings.Split(strings.Trim(strings.TrimPrefix(p, "./"), "/"), "/") {
		if seg == "" || seg == "." || strings.ContainsAny(seg, "*?[") {
			break
		}
		keep = append(keep, seg)
	}
	return strings.Join(keep, "/")
}

func pathsOverlap(a, b string) bool {
	x, y := prefix(a), prefix(b)
	return x == "" || y == "" || x == y || strings.HasPrefix(x, y+"/") || strings.HasPrefix(y, x+"/")
}

func overlap(as, bs []string) (string, string, bool) {
	for _, a := range as {
		for _, b := range bs {
			if pathsOverlap(a, b) {
				return a, b, true
			}
		}
	}
	return "", "", false
}

// Rejection records a refused Foreman reply (an illegal call, or one that did not parse) and escalates at the M-th in a row.
func (s State) Rejection(tool string, args json.RawMessage, err error, lim Limits) []NewFact {
	out := []NewFact{{Kind: KindForemanDecision, Payload: ForemanDecision{Tool: tool, Args: args, Reason: err.Error()}}}
	if s.ConsecutiveRejections+1 >= lim.MaxConsecutiveRejections {
		out = append(out, NewFact{Kind: KindEscalated, Payload: Escalated{Reason: fmt.Sprintf("%d consecutive rejected Foreman calls; last: %s", s.ConsecutiveRejections+1, err), Class: fact.ClassForeman}})
	}
	return out
}

// Apply returns the facts to append for call, in order. A rejected call still yields a rejection decision (plus escalated at the M-th in a row) and the Check error.
func (s State) Apply(call Call, lim Limits) ([]NewFact, error) {
	err := s.Check(call, lim)
	if _, dup := s.Decided(call); err == nil && dup {
		err = fmt.Errorf("%s rejected: already decided: %s", call.Tool, call.Tool)
	}
	if err != nil {
		return s.Rejection(call.Tool, call.Args(), err, lim), err
	}
	reason := call.Note
	if reason == "" {
		reason = call.Reason
	}
	out := []NewFact{{Kind: KindForemanDecision, Payload: ForemanDecision{Tool: call.Tool, Args: call.Args(), Accepted: true, Reason: reason}}}
	switch call.Tool {
	case ToolDispatchBuild:
		for _, id := range call.TaskIDs {
			out = append(out, NewFact{Kind: KindTaskDispatched, Payload: TaskRef{Task: id}})
		}
	case ToolRequestInspection:
		out = append(out, NewFact{Kind: KindInspectionRequested, Payload: TaskRef{Task: call.TaskID}})
	case ToolRetryTask:
		out = append(out, NewFact{Kind: KindTaskRetry, Payload: TaskRetry{Task: call.TaskID, Change: call.Change}})
	case ToolSerialize:
		out = append(out, NewFact{Kind: KindTasksSerialized, Payload: TasksSerialized{A: call.A, B: call.B}})
	case ToolEscalateToHuman:
		out = append(out, NewFact{Kind: KindEscalated, Payload: Escalated{Reason: call.Reason, Class: fact.ClassForeman}})
	}
	return out, nil
}

type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func schema(props string, required ...string) json.RawMessage {
	req, _ := json.Marshal(required)
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{%s},"required":%s,"additionalProperties":false}`, props, req))
}

const (
	strProp = `{"type":"string"}`
	idsProp = `{"type":"array","items":{"type":"string"},"minItems":1}`
)

func Tools() []ToolDef {
	return []ToolDef{
		{ToolApproveSpec, "Approve the Architect's spec and move to planning. Only valid when the spec is ready.", schema(`"note":`+strProp, "note")},
		{ToolRejectSpec, "Send the spec back to the Architect with a reason. Only valid when the spec is ready and revision rounds remain.", schema(`"reason":`+strProp, "reason")},
		{ToolApprovePlan, "Approve the plan and start building. Rejected if tasks that can run in parallel own overlapping paths.", schema(`"note":`+strProp, "note")},
		{ToolRejectPlan, "Send the plan back to the Architect with a reason. Only valid when the plan is ready and revision rounds remain.", schema(`"reason":`+strProp, "reason")},
		{ToolDispatchBuild, "Start Builders on pending tasks whose dependencies are done. At most 3 builders run at once and their owned paths must not overlap.", schema(`"task_ids":`+idsProp, "task_ids")},
		{ToolRequestInspection, "Start an Inspector on a built task that has new commits since its last inspection.", schema(`"task_id":`+strProp, "task_id")},
		{ToolRetryTask, "Re-run a failed task. same: the same builder tries again; other_model: switch to the next fallback model (rejected when none is left); split: the Architect replaces the task with smaller tasks within its owned paths. Each retry uses the task's retry budget.", schema(`"task_id":`+strProp+`,"change":{"type":"string","enum":["same","other_model","split"]}`, "task_id", "change")},
		{ToolSerialize, "Resolve a recorded integration conflict by re-running task_b on top of task_a.", schema(`"task_a":`+strProp+`,"task_b":`+strProp, "task_a", "task_b")},
		{ToolEscalateToHuman, "Hand the work order to a human. Always allowed.", schema(`"reason":`+strProp, "reason")},
	}
}
