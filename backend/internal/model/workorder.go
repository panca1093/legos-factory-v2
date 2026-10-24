// Package model holds every shared type: domain entities, workflow rules
// that operate purely on them, and HTTP request/response contracts. It
// imports nothing from the rest of the application.
package model

import (
	"fmt"
	"time"
)

// State is a persistent work order lifecycle state.
type State string

const (
	StateCreated          State = "CREATED"
	StateRequirement      State = "REQUIREMENT_ANALYSIS"
	StateDecomposition    State = "TASK_DECOMPOSITION"
	StatePlanGeneration   State = "SPEC_AND_PLAN_GENERATION"
	StateArchitectReview  State = "ARCHITECT_REVIEW"
	StateWaitingForHuman  State = "WAITING_FOR_HUMAN"
	StatePlanning         State = "PLANNING"
	StateQueued           State = "QUEUED"
	StateImplementing     State = "IMPLEMENTING"
	StateVerifying        State = "VERIFYING"
	StateSpecialistReview State = "SPECIALIST_REVIEW"
	StatePRPreparation    State = "PR_PREPARATION"
	StateMergeReadiness   State = "MERGE_READINESS"
	StateReady            State = "READY"
	StateNeedsHumanReview State = "NEEDS_HUMAN_REVIEW"
	StateBlocked          State = "BLOCKED"
	StateCancelled        State = "CANCELLED"
)

// Terminal reports whether no further automatic transitions are possible.
func (s State) Terminal() bool {
	return s == StateReady || s == StateNeedsHumanReview || s == StateBlocked || s == StateCancelled
}

// transitions maps state -> set of allowed target states. Deterministic
// workflow control: agents propose, this table decides.
var transitions = map[State][]State{
	StateCreated:          {StateRequirement, StateCancelled},
	StateRequirement:      {StateDecomposition, StateCancelled},
	StateDecomposition:    {StatePlanGeneration, StateCancelled},
	StatePlanGeneration:   {StateArchitectReview, StateCancelled},
	StateArchitectReview:  {StatePlanning, StateWaitingForHuman, StateQueued, StateCancelled},
	StateWaitingForHuman:  {StatePlanning, StateCancelled},
	StatePlanning:         {StatePlanGeneration, StateWaitingForHuman, StateCancelled},
	StateQueued:           {StateImplementing, StateCancelled},
	StateImplementing:     {StateVerifying, StateCancelled},
	StateVerifying:        {StateSpecialistReview, StateImplementing, StateCancelled},
	StateSpecialistReview: {StateImplementing, StatePRPreparation, StateWaitingForHuman, StateCancelled},
	StatePRPreparation:    {StateMergeReadiness, StateCancelled},
	StateMergeReadiness:   {StateReady, StateNeedsHumanReview, StateBlocked},
	StateNeedsHumanReview: {StateReady, StateImplementing, StateCancelled},
}

// CanTransition reports whether moving from s to next is permitted.
func CanTransition(s, next State) bool {
	for _, t := range transitions[s] {
		if t == next {
			return true
		}
	}
	return false
}

// MustTransition returns an error for invalid transitions.
func MustTransition(s, next State) error {
	if !CanTransition(s, next) {
		return fmt.Errorf("INVALID_STATE_TRANSITION: cannot move from %s to %s", s, next)
	}
	return nil
}

// AllowedTransitions exposes the transition table for introspection/tests.
func AllowedTransitions(s State) []State {
	out := make([]State, len(transitions[s]))
	copy(out, transitions[s])
	return out
}

type WorkOrder struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"project_id"`
	Title          string    `json:"title"`
	Objective      string    `json:"objective"`
	Acceptance     string    `json:"acceptance"`
	State          State     `json:"state"`
	Autonomy       string    `json:"autonomy"`
	BuilderModel   string    `json:"builder_model"`
	InspectorModel string    `json:"inspector_model"`
	ClarifQuestion string    `json:"clarif_question,omitempty"`
	ClarifAnswer   string    `json:"clarif_answer,omitempty"`
	Revision       int       `json:"revision"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
