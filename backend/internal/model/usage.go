package model

import "time"

type UsageRecord struct {
	ID         string    `json:"id"`
	WOID       string    `json:"work_order_id,omitempty"`
	UserID     string    `json:"user_id,omitempty"`
	Role       string    `json:"role"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	TokensIn   int64     `json:"tokens_in"`
	TokensOut  int64     `json:"tokens_out"`
	CostMicros int64     `json:"cost_micros"`
	Source     string    `json:"source"` // measured|estimated|unknown
	CreatedAt  time.Time `json:"created_at"`
}

type UsageTotals struct {
	CostMicros int64 `json:"cost_micros"`
	TokensIn   int64 `json:"tokens_in"`
	TokensOut  int64 `json:"tokens_out"`
}
