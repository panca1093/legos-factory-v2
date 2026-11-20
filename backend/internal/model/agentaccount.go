package model

import "time"

// AgentAccount is one signed-in identity of a user for an agent. Its HomeDir
// holds the agent's own login state; the implicit "system" account has none.
type AgentAccount struct {
	ID        string
	UserID    string
	Agent     string
	Label     string
	HomeDir   string
	Active    bool
	CreatedAt time.Time

	// An API-key account talks to an Anthropic-compatible provider; the key is sealed and never leaves the usecase.
	APIProvider  string
	APIBaseURL   string
	APIKeySealed []byte
	APIModels    string // JSON {opus,sonnet,haiku}
	APIKeyHeader string // auth_token | api_key: which env var carries the key
}
