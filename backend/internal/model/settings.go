package model

type Settings struct {
	BudgetLimitMicros  int64             `json:"budget_limit_micros"`
	ForemanModel       string            `json:"foreman_model"`
	ArchitectModel     string            `json:"architect_model"`
	BuilderModel       string            `json:"builder_model"`
	InspectorModel     string            `json:"inspector_model"`
	AllowedModels      []string          `json:"allowed_models"`
	CatalogerSummaries bool              `json:"cataloger_summaries"`
	Extra              map[string]string `json:"extra,omitempty"`
}
