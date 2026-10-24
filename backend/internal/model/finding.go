package model

import (
	"errors"
	"time"
)

const SeverityBlocker = "blocker"

// Finding statuses. Open, confirmed and escalated blockers block readiness;
// a disputed finding does not, but keeps the human's reason.
const (
	FindingOpen      = "open"
	FindingConfirmed = "confirmed"
	FindingDisputed  = "disputed"
	FindingResolved  = "resolved"
	FindingEscalated = "escalated"
)

var ErrFindingNotFound = errors.New("finding not found")

type FindingDecisionRequest struct {
	Action string `json:"action"` // confirm|dispute|resolve|escalate
	Note   string `json:"note"`
}

type Finding struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	WOID      string    `json:"-"`
	Severity  string    `json:"severity"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	Location  string    `json:"location"`
	Status    string    `json:"status"`
	Specialty string    `json:"specialty,omitempty"`
	Note      string    `json:"note,omitempty"`
	DecidedBy string    `json:"decided_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
