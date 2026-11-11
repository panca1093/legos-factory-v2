package model

type Settings struct {
	BudgetLimitMicros int64             `json:"budget_limit_micros"`
	ForemanModel      string            `json:"foreman_model"`
	ArchitectModel    string            `json:"architect_model"`
	BuilderModel      string            `json:"builder_model"`
	InspectorModel    string            `json:"inspector_model"`
	AllowedModels     []string          `json:"allowed_models"`
	ChatModel         string            `json:"chat_model"`
	Extra             map[string]string `json:"extra,omitempty"`
	JevEnabled        bool              `json:"jev_enabled"` // response only: true iff TYPESAFE_API_KEY is set
}

// SettingsPatch carries only the keys a PATCH sets; nil means untouched.
type SettingsPatch struct {
	BudgetLimitMicros *int64  `json:"budget_limit_micros"`
	ForemanModel      *string `json:"foreman_model"`
	ArchitectModel    *string `json:"architect_model"`
	BuilderModel      *string `json:"builder_model"`
	InspectorModel    *string `json:"inspector_model"`
	ChatModel         *string `json:"chat_model"`
}
