package model

import "time"

type Run struct {
	ID         string    `json:"id"`
	WOID       string    `json:"-"`
	TaskID     *string   `json:"task_id"`
	Kind       string    `json:"kind"`
	State      string    `json:"state"`
	Harness    string    `json:"harness"`
	Output     *string   `json:"output,omitempty"`
	Error      *string   `json:"error,omitempty"`
	TokensIn   int64     `json:"tokens_in"`
	TokensOut  int64     `json:"tokens_out"`
	CostMicros int64     `json:"cost_micros"`
	CreatedAt  time.Time `json:"created_at"`
}
