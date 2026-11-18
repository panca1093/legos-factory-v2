package model

import (
	"errors"
	"time"
)

// ErrPermanent wraps handler errors that must not be retried (e.g. policy
// refusals); the scheduler marks the job FAILED immediately.
var ErrPermanent = errors.New("permanent failure")

// ErrToolCallAsText marks a turn whose tool call came back as plain text; retryable, and the text is not an answer.
var ErrToolCallAsText = errors.New("the model returned a tool call as plain text instead of calling the tool; retrying usually works")

// ErrRequeue asks the scheduler to run the job again with a fresh timeout,
// without spending a retry attempt.
var ErrRequeue = errors.New("requeue job")

type JobState string

const (
	JobPending   JobState = "PENDING"
	JobClaimed   JobState = "CLAIMED"
	JobRunning   JobState = "RUNNING"
	JobCompleted JobState = "COMPLETED"
	JobFailed    JobState = "FAILED"
	JobCancelled JobState = "CANCELLED"
)

type Job struct {
	ID          string
	Kind        string
	WOID        string
	TaskID      string
	Payload     map[string]any
	State       JobState
	Attempts    int
	MaxAttempts int
	RunAfter    time.Time
	Error       string
}
