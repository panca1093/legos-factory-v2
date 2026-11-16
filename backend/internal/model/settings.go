package model

// Settings is the workspace policy. Agent ids are claude, codex or opencode;
// RolePermissions only holds roles that offer a choice (chat: ask | read).
type Settings struct {
	ForemanModel      string            `json:"foreman_model"`
	ArchitectModel    string            `json:"architect_model"`
	BuilderModel      string            `json:"builder_model"`
	InspectorModel    string            `json:"inspector_model"`
	AllowedModels     []string          `json:"allowed_models"`
	ChatModel         string            `json:"chat_model"`
	RoleAgents        map[string]string `json:"role_agents"`
	RolePermissions   map[string]string `json:"role_permissions"`
	MaxConcurrentRuns int               `json:"max_concurrent_runs"`
	JevEnabled        bool              `json:"jev_enabled"`
	JevKeySet         bool              `json:"jev_key_set"`
	Extra             map[string]string `json:"extra,omitempty"`

	// JevEnabledSet is false while the toggle was never stored; JevKeySealed is
	// the sealed TypeSafe key. Neither leaves the process.
	JevEnabledSet bool   `json:"-"`
	JevKeySealed  []byte `json:"-"`
}

// SettingsPatch carries only the keys a PATCH sets; nil means untouched.
// RoleAgents and RolePermissions merge per role.
type SettingsPatch struct {
	ForemanModel      *string           `json:"foreman_model"`
	ArchitectModel    *string           `json:"architect_model"`
	BuilderModel      *string           `json:"builder_model"`
	InspectorModel    *string           `json:"inspector_model"`
	ChatModel         *string           `json:"chat_model"`
	RoleAgents        map[string]string `json:"role_agents"`
	RolePermissions   map[string]string `json:"role_permissions"`
	MaxConcurrentRuns *int              `json:"max_concurrent_runs"`
	JevEnabled        *bool             `json:"jev_enabled"`
	JevKey            *string           `json:"jev_key"` // write-only; the empty string clears it
}

// ChatReadOnly reports whether chat is limited to reading (role_permissions.chat == "read").
func (s *Settings) ChatReadOnly() bool { return s != nil && s.RolePermissions["chat"] == "read" }
