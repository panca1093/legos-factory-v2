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
}
