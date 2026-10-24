package model

import "time"

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
