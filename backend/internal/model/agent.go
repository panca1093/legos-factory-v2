package model

// AgentRole identifies which persona a harness invocation serves.
type AgentRole string

const (
	AgentForeman   AgentRole = "foreman"
	AgentArchitect AgentRole = "architect"
	AgentBuilder   AgentRole = "builder"
	AgentInspector AgentRole = "inspector"
)

// AgentRequest is one logical agent invocation.
type AgentRequest struct {
	Role         AgentRole
	Prompt       string
	ProviderID   string // selected model, provider-qualified
	ModelID      string
	WorkspaceDir string // directory the agent may operate on (builder/inspector)
	Context      map[string]string
}

// AgentResult is the outcome of one invocation.
type AgentResult struct {
	Text       string
	SessionRef string
	TokensIn   int64
	TokensOut  int64
	CostMicros int64
	CostSource string // "measured" | "estimated" | "unknown"
}
