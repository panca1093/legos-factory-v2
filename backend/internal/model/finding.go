package model

import "time"

const SeverityBlocker = "blocker"

type Finding struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	WOID      string    `json:"-"`
	Severity  string    `json:"severity"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	Location  string    `json:"location"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}
