package model

import "errors"

// AgentRole identifies which persona a harness invocation serves.
type AgentRole string

const (
	AgentForeman   AgentRole = "foreman"
	AgentArchitect AgentRole = "architect"
	AgentBuilder   AgentRole = "builder"
	AgentInspector AgentRole = "inspector"
	AgentCataloger AgentRole = "cataloger"
)

// AgentRequest is one logical agent invocation.
type AgentRequest struct {
	Role         AgentRole
	Prompt       string
	ProviderID   string // selected model, provider-qualified
	ModelID      string
	WorkspaceDir string // directory the agent may operate on (builder/inspector)
	Context      map[string]string
	OnStep       func(AgentStep) // optional; called once per finished tool-calling step
}

// AgentStep is one finished tool-calling step of a running agent.
type AgentStep struct {
	N         int         `json:"step"`
	Tools     []AgentTool `json:"tools"`
	TokensOut int64       `json:"tokens_out"`
}

// AgentTool is a tool call within a step, with its main input shortened for display.
type AgentTool struct {
	Name   string `json:"name"`
	Target string `json:"target,omitempty"`
	Failed bool   `json:"failed,omitempty"`
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

// AgentSession is a handle to an in-flight invocation.
type AgentSession interface {
	ID() string
}

// ErrAgentSessionNotFound is returned when a session handle is unknown or expired.
var ErrAgentSessionNotFound = errors.New("agent session not found")
