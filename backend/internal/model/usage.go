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

type UsageDay struct {
	Date  string `json:"date"` // YYYY-MM-DD
	Count int    `json:"count"`
}

type UsageRole struct {
	Role       string `json:"role"`
	TokensIn   int64  `json:"tokens_in"`
	TokensOut  int64  `json:"tokens_out"`
	CostMicros int64  `json:"cost_micros"`
	Runs       int    `json:"runs"`
}

type UsagePeriod struct {
	TokensIn   int64 `json:"tokens_in"`
	TokensOut  int64 `json:"tokens_out"`
	CostMicros int64 `json:"cost_micros"`
	Runs       int   `json:"runs"`
}

// UsageSummary is one user's agent usage: daily activity, per-role totals, month-to-date.
type UsageSummary struct {
	Days        int         `json:"days"`
	From        string      `json:"from"`
	To          string      `json:"to"`
	Activity    []UsageDay  `json:"activity"`
	Roles       []UsageRole `json:"roles"`
	MonthToDate struct {
		Month string `json:"month"` // YYYY-MM
		UsagePeriod
	} `json:"month_to_date"`
}
