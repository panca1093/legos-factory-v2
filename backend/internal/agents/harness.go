package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Role identifies which persona a harness invocation serves.
type Role string

const (
	RoleForeman   Role = "foreman"
	RoleArchitect Role = "architect"
	RoleBuilder   Role = "builder"
	RoleInspector Role = "inspector"
)

// AgentRequest is one logical agent invocation.
type AgentRequest struct {
	Role        Role
	Prompt      string
	ProviderID  string // selected model, provider-qualified
	ModelID     string
	WorkspaceDir string // directory the agent may operate on (builder/inspector)
	Context     map[string]string
}

// AgentResult is the outcome of one invocation.
type AgentResult struct {
	Text        string
	SessionRef  string
	TokensIn    int64
	TokensOut   int64
	CostMicros  int64
	CostSource  string // "measured" | "estimated" | "unknown"
}

// AgentSession is a handle to an in-flight invocation.
type AgentSession interface {
	ID() string
}

// Harness abstracts the coding-agent runtime (OpenCode, mock, ...).
type Harness interface {
	Start(ctx context.Context, req AgentRequest) (AgentSession, error)
	Cancel(ctx context.Context, sessionID string) error
	Result(ctx context.Context, sessionID string) (AgentResult, error)
	Name() string
}

// ErrSessionNotFound is returned when a session handle is unknown or expired.
var ErrSessionNotFound = errors.New("agent session not found")

// StructuredJSON extracts the first JSON object/array from agent text and
// unmarshals it into v. Agent replies are prose+JSON; this normalizes the
// extraction. Callers must still validate semantics before acting on it.
func StructuredJSON(text string, v any) error {
	start := strings.Index(text, "{")
	if start < 0 {
		return fmt.Errorf("no JSON object found in agent response")
	}
	// find matching closing brace from start
	depth := 0
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return json.Unmarshal([]byte(text[start:i+1]), v)
			}
		}
	}
	return fmt.Errorf("unterminated JSON object in agent response")
}

var jsonFence = regexp.MustCompile("(?s)```(?:json)?\\s*")

// StripFences removes markdown code fences (agents often fence their JSON).
func StripFences(s string) string {
	return jsonFence.ReplaceAllString(s, "")
}
