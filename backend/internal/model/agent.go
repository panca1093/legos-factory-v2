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
	AgentChat      AgentRole = "chat"
)

// AgentRequest is one logical agent invocation.
type AgentRequest struct {
	Role         AgentRole
	Prompt       string
	Images       []AgentImage // optional; sent with this turn only, before the text
	ProviderID   string       // selected model, provider-qualified
	ModelID      string
	WorkspaceDir string // directory the agent may operate on (builder/inspector)
	Context      map[string]string
	OnStep       func(AgentStep) // optional; called once per finished tool-calling step
	OnText       func(string)    // optional; called with each chunk of the reply as it streams, by harnesses that stream
	Keep         bool            // keep the session open after the run, for harnesses that can continue it
	RepoAccess   bool            // chat only: the agent may read and search WorkspaceDir
	MCP          []MCPServer     // MCP servers to start with the session (claude only)
	MCPAllow     []string        // mcp__<server>__<tool> names the session may call
	MCPDeny      []string        // mcp__<server>__<tool> names to remove from the session
}

// AgentImage is one image of a turn; MediaType is image/png, image/jpeg, image/gif or image/webp.
type AgentImage struct {
	MediaType string
	Data      []byte
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
	TokensIn   int64 // total input, cache reads and writes included
	TokensOut  int64
	CacheRead  int64
	CacheWrite int64
	CostMicros int64
	CostSource string // "measured" | "estimated" | "unknown"
}

// AgentSession is a handle to an in-flight invocation.
type AgentSession interface {
	ID() string
}

// ErrAgentSessionNotFound is returned when a session handle is unknown or expired.
var ErrAgentSessionNotFound = errors.New("agent session not found")

// ErrSteeringUnsupported is returned when the agent cannot take a message mid-turn.
var ErrSteeringUnsupported = errors.New("agent does not support steering")
