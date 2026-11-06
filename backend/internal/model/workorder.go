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
	StateWaitingForHuman:  {StatePlanning, StateImplementing, StateCancelled},
	StatePlanning:         {StatePlanGeneration, StateWaitingForHuman, StateCancelled},
	StateQueued:           {StateImplementing, StateCancelled},
	StateImplementing:     {StateVerifying, StateWaitingForHuman, StateCancelled},
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
	ID              string     `json:"id"`
	ProjectID       string     `json:"project_id"`
	Title           string     `json:"title"`
	Objective       string     `json:"objective"`
	Acceptance      string     `json:"acceptance"`
	State           State      `json:"state"`
	BuilderModel    string     `json:"builder_model"`
	InspectorModel  string     `json:"inspector_model"`
	ForemanModel    string     `json:"foreman_model,omitempty"`
	ArchitectModel  string     `json:"architect_model,omitempty"`
	PlanReview      string     `json:"plan_review,omitempty"` // pending|approved
	ReviewRounds    int        `json:"review_rounds"`         // legacy: no longer written, kept for old rows
	PausedAt        *time.Time `json:"paused_at,omitempty"`
	PauseReason     string     `json:"pause_reason,omitempty"`
	ClarifQuestion  string     `json:"clarif_question,omitempty"`
	ClarifTask      string     `json:"-"` // plan task whose Builder question waits on a human; empty for a planning question
	ClarifAnswer    string     `json:"clarif_answer,omitempty"`
	Revision        int        `json:"revision"`
	CommitSHA       string     `json:"commit_sha,omitempty"`
	KBBaseSHA       string     `json:"-"`
	CheckProfile    string     `json:"-"`
	VerifyFailures  int        `json:"-"`
	ReviewFixes     int        `json:"-"`
	PlanRevisions   int        `json:"plan_revisions"`       // plan-gate revisions since the last human decision
	ArchitectReport string     `json:"-"`                    // JSON of the latest Architect verification
	SourceRef       string     `json:"source_ref,omitempty"` // e.g. jira:ABC-12
	SourceChatID    string     `json:"source_chat_id,omitempty"`
	PRURL           string     `json:"pr_url,omitempty"`
	PRRef           string     `json:"pr_ref,omitempty"`
	PRMergedAt      *time.Time `json:"pr_merged_at,omitempty"`
	PRClosedAt      *time.Time `json:"pr_closed_at,omitempty"`
	PRTrackFailedAt *time.Time `json:"pr_track_failed_at,omitempty"`
	CreatedBy       string     `json:"created_by"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`

	Brief          string          `json:"brief,omitempty"` // Foreman intake brief
	Spec           string          `json:"spec,omitempty"`  // Architect spec
	ClarifRounds   int             `json:"clarif_rounds"`
	IntakePending  bool            `json:"-"` // the brief waits on a human answer to an intake question
	Clarifications []Clarification `json:"clarifications,omitempty"` // Foreman answers to Architect questions
}

// Clarification is one question and its answer; By is "human" when a person answered.
type Clarification struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	By       string `json:"by,omitempty"`
	Task     string `json:"task,omitempty"` // plan task whose Builder asked it
}
