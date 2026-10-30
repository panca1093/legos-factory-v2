package harness

import (
	"context"
	"time"

	"legos-factory/factory/internal/sandbox"
)

type Role string

const (
	RoleForeman   Role = "foreman"
	RoleCataloger Role = "cataloger"
	RoleArchitect Role = "architect"
	RoleBuilder   Role = "builder"
	RoleInspector Role = "inspector"
)

func (r Role) ReadOnly() bool { return r != RoleBuilder }

type Profile struct {
	Agent       string // "claude-agent-acp" | "opencode" | "codex-acp"
	Provider    string
	Model       string
	BillingKind string // "api_key" | "subscription"
}

type ToolPolicy struct {
	Deny []string
}

type Limits struct {
	MaxSteps  int
	MaxTokens int
	WallTime  time.Duration
}

type SessionSpec struct {
	Role         Role
	Profile      Profile
	AgentCommand []string
	Dir          string
	Env          map[string]string
	ToolPolicy   ToolPolicy
	Limits       Limits
	SessionID    string // non-empty: load this agent session instead of creating one
	Sandbox      sandbox.Sandbox
}

type EventKind string

const (
	EventStep       EventKind = "step"
	EventToolCall   EventKind = "tool_call"
	EventToolUpdate EventKind = "tool_update"
	EventUsage      EventKind = "usage"
	EventStop       EventKind = "stop"
	EventError      EventKind = "error"
)

type Usage struct {
	InputTokens       int
	OutputTokens      int
	CacheReadTokens   int
	CacheCreateTokens int
}

type Event struct {
	Kind       EventKind
	Text       string
	ToolCallID string
	Tool       string
	Status     string
	Usage      Usage
	StopReason string
	Err        error
	At         time.Time
}

type Harness interface {
	Start(ctx context.Context, s SessionSpec) (Session, error)
}

type Session interface {
	ID() string
	// Prompt blocks until the turn ends; its events stream on Events meanwhile.
	Prompt(ctx context.Context, text string) (stopReason string, err error)
	Events() <-chan Event
	Close() error
}
