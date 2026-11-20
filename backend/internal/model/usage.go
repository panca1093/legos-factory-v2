package model

import "time"

type UsageRecord struct {
	ID         string `json:"id"`
	WOID       string `json:"work_order_id,omitempty"`
	UserID     string `json:"user_id,omitempty"`
	Role       string `json:"role"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	TokensIn   int64  `json:"tokens_in"`
	TokensOut  int64  `json:"tokens_out"`
	CacheRead  int64  `json:"cache_read_tokens"`
	CacheWrite int64  `json:"cache_write_tokens"`
	CostMicros int64  `json:"cost_micros"`
	Source     string `json:"source"` // measured|estimated|unknown
	// Agent and AccountID name the account that served the run; both are empty on rows from before accounts were tracked.
	Agent        string    `json:"agent,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	ServedModel  string    `json:"served_model,omitempty"`
	ProviderHost string    `json:"provider_host,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
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

// UsageAccount is one account's usage in the period. CostMicros is nil when provider pricing is not tracked.
type UsageAccount struct {
	Agent        string   `json:"agent"`
	AccountID    string   `json:"account_id"`
	Label        string   `json:"label"`
	Kind         string   `json:"kind"` // subscription|api
	Host         string   `json:"host,omitempty"`
	Deleted      bool     `json:"deleted,omitempty"`
	Untracked    bool     `json:"untracked,omitempty"`
	ServedModels []string `json:"served_models"`
	TokensIn     int64    `json:"tokens_in"`
	TokensOut    int64    `json:"tokens_out"`
	CostMicros   *int64   `json:"cost_micros"`
	Runs         int      `json:"runs"`
}

type UsagePeriod struct {
	TokensIn   int64 `json:"tokens_in"`
	TokensOut  int64 `json:"tokens_out"`
	CostMicros int64 `json:"cost_micros"`
	Runs       int   `json:"runs"`
}

// UsageSummary is one user's agent usage: daily activity, per-role totals, month-to-date.
type UsageSummary struct {
	Days        int            `json:"days"`
	From        string         `json:"from"`
	To          string         `json:"to"`
	Activity    []UsageDay     `json:"activity"`
	Roles       []UsageRole    `json:"roles"`
	Accounts    []UsageAccount `json:"accounts"`
	MonthToDate struct {
		Month string `json:"month"` // YYYY-MM
		UsagePeriod
	} `json:"month_to_date"`
}

type UsageAccountLabel struct{ Agent, Label string }
