package workflow

import (
	"encoding/json"
	"slices"

	"legos-factory/factory/internal/fact"
)

const (
	KindSpecReady           = "spec.ready"
	KindPlanReady           = "plan.ready"
	KindForemanDecision     = "foreman.decision"
	KindTaskDispatched      = "task.dispatched"
	KindTaskBuilt           = "task.built"
	KindInspectionRequested = "inspection.requested"
	KindInspectionDone      = "inspection.done"
	KindTaskRetry           = "task.retry"
	KindTasksSerialized     = "tasks.serialized"
	KindIntegrationConflict = "integration.conflict"
	KindEscalated           = "escalated"
)

const (
	RoleCataloger = "cataloger"
	RoleArchitect = "architect"
)

// ReasonNeedsRebase is the wo.requeued reason that moves a work order to needs_rebase instead of building.
const ReasonNeedsRebase = "needs_rebase"

type Phase string

const (
	PhaseQueued        Phase = "queued"
	PhaseCataloging    Phase = "cataloging"
	PhaseSpecifying    Phase = "specifying"
	PhaseSpecReady     Phase = "spec_ready"
	PhasePlanning      Phase = "planning"
	PhasePlanReady     Phase = "plan_ready"
	PhaseBuilding      Phase = "building"
	PhaseIntegrating   Phase = "integrating"
	PhaseVerifying     Phase = "verifying"
	PhasePROpen        Phase = "pr_open"
	PhaseCIFailed      Phase = "ci_failed"
	PhaseCIGreen       Phase = "ci_green"
	PhaseAwaitingHuman Phase = "awaiting_human"
	PhaseNeedsRebase   Phase = "needs_rebase"
	PhaseMerged        Phase = "merged"
	PhaseClosed        Phase = "closed"
	PhaseFailed        Phase = "failed"
	PhaseCancelled     Phase = "cancelled"
)

func (p Phase) Terminal() bool {
	switch p {
	case PhaseMerged, PhaseClosed, PhaseFailed, PhaseCancelled:
		return true
	}
	return false
}

type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskBuilding   TaskStatus = "building"
	TaskBuilt      TaskStatus = "built"
	TaskInspecting TaskStatus = "inspecting"
	TaskDone       TaskStatus = "done"
	TaskFailed     TaskStatus = "failed"
)

const (
	ChangeSame       = "same"
	ChangeOtherModel = "other_model"
	ChangeSplit      = "split"
)

type PlanTask struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Paths       []string `json:"paths"`
	DependsOn   []string `json:"depends_on,omitempty"`
}

type PlanReady struct {
	Tasks []PlanTask `json:"tasks"`
}

type ForemanDecision struct {
	Tool     string          `json:"tool"`
	Args     json.RawMessage `json:"args"`
	Accepted bool            `json:"accepted"`
	Reason   string          `json:"reason,omitempty"`
}

type TaskRef struct {
	Task string `json:"task"`
}

type TaskBuiltPayload struct {
	Task string `json:"task"`
	SHA  string `json:"sha,omitempty"`
}

type InspectionDone struct {
	Task         string `json:"task"`
	Findings     int    `json:"findings"`
	SeverityMax  string `json:"severity_max,omitempty"`
	LogicChanged bool   `json:"logic_changed"`
}

type TaskRetry struct {
	Task   string `json:"task"`
	Change string `json:"change"`
}

type TasksSerialized struct {
	A string `json:"task_a"`
	B string `json:"task_b"`
}

type IntegrationConflict struct {
	A     string   `json:"task_a"`
	B     string   `json:"task_b"`
	Files []string `json:"files,omitempty"`
}

type Escalated struct {
	Reason string `json:"reason"`
}

type Task struct {
	PlanTask
	Status           TaskStatus
	Commits          int
	InspectedCommits int
	InspectionRounds int
	Retries          int
	LastFailureClass string
	LastChange       string
}

type Conflict struct {
	IntegrationConflict
	Serialized bool
}

type Decision struct {
	Fact  fact.Fact
	Call  Call
	Epoch int
}

type State struct {
	Phase                 Phase
	SpecRounds            int
	SpecRejections        int
	PlanRounds            int
	PlanRejections        int
	Tasks                 []Task
	Conflicts             []Conflict
	ConsecutiveRejections int
	Decisions             []Decision // accepted only
	LevelCap              int
	FailureClass          string
	EscalatedFrom         Phase

	// Epoch counts the facts the Foreman reacts to; a decision is replayable only within its epoch.
	Epoch   int
	prFacts []fact.Fact
}

func (s State) Task(id string) (Task, bool) {
	for _, t := range s.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

func (s State) ActiveBuilders() []string {
	var ids []string
	for _, t := range s.Tasks {
		if t.Status == TaskBuilding {
			ids = append(ids, t.ID)
		}
	}
	return ids
}

func (s *State) task(id string) *Task {
	for i := range s.Tasks {
		if s.Tasks[i].ID == id {
			return &s.Tasks[i]
		}
	}
	return nil
}

// setPhase is a no-op once the order is terminal or parked with a human; terminal facts and escalation assign Phase directly.
func (s *State) setPhase(p Phase) {
	if s.Phase.Terminal() || s.Phase == PhaseAwaitingHuman {
		return
	}
	s.Phase = p
}

func (s *State) in(ps ...Phase) bool { return slices.Contains(ps, s.Phase) }

func (s *State) allDone() bool {
	if len(s.Tasks) == 0 {
		return false
	}
	for _, t := range s.Tasks {
		if t.Status != TaskDone {
			return false
		}
	}
	return true
}

// Fold is pure: it derives the workflow state from a work order's facts in append order.
func Fold(facts []fact.Fact) State {
	s := State{Phase: PhaseQueued}
	for _, f := range facts {
		s.apply(f)
	}
	return s
}

func decode[T any](f fact.Fact) (T, bool) {
	v, err := fact.Decode[T](f)
	return v, err == nil
}

func (s *State) apply(f fact.Fact) {
	if s.Phase.Terminal() {
		return
	}
	switch f.Kind {
	case fact.KindWOCreated:
		s.Epoch++
		if v, ok := decode[struct {
			LevelCap int `json:"level_cap"`
		}](f); ok {
			s.LevelCap = v.LevelCap
		}
	case fact.KindRunStarted:
		r, ok := decode[fact.RunStarted](f)
		if !ok {
			return
		}
		switch r.Role {
		case RoleCataloger:
			if s.in(PhaseQueued) {
				s.setPhase(PhaseCataloging)
			}
		case RoleArchitect:
			if s.in(PhaseQueued, PhaseCataloging) {
				s.setPhase(PhaseSpecifying)
			}
		}
	case KindSpecReady:
		if s.in(PhaseQueued, PhaseCataloging, PhaseSpecifying) {
			s.SpecRounds++
			s.Epoch++
			s.setPhase(PhaseSpecReady)
		}
	case KindPlanReady:
		p, ok := decode[PlanReady](f)
		if ok && s.in(PhasePlanning) {
			s.PlanRounds++
			s.Epoch++
			s.Tasks = nil
			for _, pt := range p.Tasks {
				s.Tasks = append(s.Tasks, Task{PlanTask: pt, Status: TaskPending})
			}
			s.setPhase(PhasePlanReady)
		}
	case KindForemanDecision:
		s.applyDecision(f)
	case KindTaskDispatched:
		if v, ok := decode[TaskRef](f); ok {
			if t := s.task(v.Task); t != nil {
				t.Status = TaskBuilding
			}
		}
	case KindTaskBuilt:
		if v, ok := decode[TaskBuiltPayload](f); ok {
			if t := s.task(v.Task); t != nil {
				t.Status = TaskBuilt
				t.Commits++
				s.Epoch++
			}
		}
	case fact.KindTaskFailed:
		if v, ok := decode[fact.TaskFailed](f); ok {
			if t := s.task(v.Task); t != nil {
				t.Status = TaskFailed
				t.LastFailureClass = v.Class
				s.Epoch++
			}
		}
	case KindInspectionRequested:
		if v, ok := decode[TaskRef](f); ok {
			if t := s.task(v.Task); t != nil {
				t.Status = TaskInspecting
				t.InspectionRounds++
				t.InspectedCommits = t.Commits
			}
		}
	case KindInspectionDone:
		if v, ok := decode[InspectionDone](f); ok {
			if t := s.task(v.Task); t != nil {
				t.Status = TaskPending
				if v.Findings == 0 {
					t.Status = TaskDone
				}
				s.Epoch++
				if s.in(PhaseBuilding) && s.allDone() {
					s.setPhase(PhaseIntegrating)
				}
			}
		}
	case KindTaskRetry:
		if v, ok := decode[TaskRetry](f); ok {
			if t := s.task(v.Task); t != nil {
				t.Status = TaskPending
				t.Retries++
				t.LastChange = v.Change
			}
		}
	case KindTasksSerialized:
		if v, ok := decode[TasksSerialized](f); ok {
			if t := s.task(v.B); t != nil {
				if !slices.Contains(t.DependsOn, v.A) {
					t.DependsOn = append(slices.Clone(t.DependsOn), v.A)
				}
				t.Status = TaskPending
				t.InspectionRounds, t.InspectedCommits = 0, t.Commits
			}
			for i := range s.Conflicts {
				if c := &s.Conflicts[i]; pairMatches(c.A, c.B, v.A, v.B) {
					c.Serialized = true
				}
			}
			if s.in(PhaseIntegrating) {
				s.setPhase(PhaseBuilding)
			}
		}
	case KindIntegrationConflict:
		if v, ok := decode[IntegrationConflict](f); ok {
			s.Conflicts = append(s.Conflicts, Conflict{IntegrationConflict: v})
			s.Epoch++
		}
	case KindEscalated:
		s.EscalatedFrom = s.Phase
		s.Phase = PhaseAwaitingHuman
		s.ConsecutiveRejections = 0
	case fact.KindVerifyPassed:
		if s.in(PhaseBuilding, PhaseIntegrating) {
			s.Epoch++
			s.setPhase(PhaseVerifying)
		}
	case fact.KindVerifyFailed:
		s.Epoch++
		s.setPhase(PhaseBuilding)
	case fact.KindWORequeued:
		s.Epoch++
		if r, ok := decode[fact.WORequeued](f); ok && r.Reason == ReasonNeedsRebase {
			s.setPhase(PhaseNeedsRebase)
		} else {
			s.setPhase(PhaseBuilding)
		}
	case fact.KindPROpened, fact.KindPRPushed:
		s.Epoch++
		s.prFacts = append(s.prFacts, f)
		s.setPhase(PhasePROpen)
	case fact.KindCheckObserved, fact.KindChecksStable:
		s.prFacts = append(s.prFacts, f)
		if s.in(PhasePROpen, PhaseCIFailed, PhaseCIGreen) {
			s.setPhase(Phase(fact.Derive(s.prFacts)))
		}
	case fact.KindPRMerged:
		s.Phase = PhaseMerged
	case fact.KindPRClosed:
		s.Phase = PhaseClosed
	case fact.KindWOCancelled:
		s.Phase = PhaseCancelled
	case fact.KindWOFailed:
		if v, ok := decode[fact.WOFailed](f); ok {
			s.FailureClass = v.Class
		}
		s.Phase = PhaseFailed
	}
}

func (s *State) applyDecision(f fact.Fact) {
	d, ok := decode[ForemanDecision](f)
	if !ok {
		return
	}
	if !d.Accepted {
		s.ConsecutiveRejections++
		return
	}
	s.ConsecutiveRejections = 0
	call, err := ParseCall(d.Tool, d.Args)
	if err != nil {
		return
	}
	s.Decisions = append(s.Decisions, Decision{Fact: f, Call: call, Epoch: s.Epoch})
	switch d.Tool {
	case ToolApproveSpec:
		s.setPhase(PhasePlanning)
	case ToolRejectSpec:
		s.SpecRejections++
		s.setPhase(PhaseSpecifying)
	case ToolApprovePlan:
		s.setPhase(PhaseBuilding)
	case ToolRejectPlan:
		s.PlanRejections++
		s.setPhase(PhasePlanning)
	}
}

func pairMatches(a, b, x, y string) bool { return (a == x && b == y) || (a == y && b == x) }

// Decided reports an accepted decision equivalent to call already recorded in the current epoch; the engine must not re-ask the Foreman.
func (s State) Decided(call Call) (fact.Fact, bool) {
	for i := len(s.Decisions) - 1; i >= 0; i-- {
		if d := s.Decisions[i]; d.Epoch == s.Epoch && d.Call.key() == call.key() {
			return d.Fact, true
		}
	}
	return fact.Fact{}, false
}
