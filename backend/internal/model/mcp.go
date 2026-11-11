package model

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"time"
)

const (
	MCPStdio = "stdio"
	MCPHTTP  = "http"

	MCPAuthNone  = "none"
	MCPAuthToken = "token"

	MCPActive   = "active"
	MCPDisabled = "disabled"
)

// MCPNamePattern constrains a server name because it becomes the mcp__<name>__ tool prefix.
var MCPNamePattern = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// MCPRoles are the agent roles a server can be offered to.
var MCPRoles = []string{"foreman", "architect", "builder", "inspector", "chat"}

var ErrMCPNotFound = errors.New("mcp server not found")

// MCPTool is one tool a server advertised. Size is the byte length of its name, description and input schema.
type MCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"read_only"`
	Enabled     bool   `json:"enabled"`
	Size        int    `json:"size"`
}

// MCPServerRec is a stored server; its credentials live sealed beside it and never appear here.
type MCPServerRec struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Transport    string     `json:"transport"`
	Command      string     `json:"command"`
	Args         []string   `json:"args"`
	URL          string     `json:"url"`
	Auth         string     `json:"auth"`
	Roles        []string   `json:"roles"`
	Tools        []MCPTool  `json:"tools"`
	Status       string     `json:"status"`
	LastError    string     `json:"last_error"`
	DiscoveredAt *time.Time `json:"discovered_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// MCPServerView is the API shape of a server: tools with their descriptions, the names of the secrets that are set, never their values.
type MCPServerView struct {
	MCPServerRec
	SecretKeys   []string `json:"secret_keys"`
	ApproxTokens int      `json:"approx_tokens"`
}

// MCPServer is the ACP session/new wire object of one server, credentials included.
type MCPServer struct {
	Name      string
	Transport string
	Command   string
	Args      []string
	URL       string
	Secret    map[string]string // env for stdio, headers for http
}

func (s MCPServer) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(s.Secret))
	for k := range s.Secret {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kv := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		kv = append(kv, map[string]string{"name": k, "value": s.Secret[k]})
	}
	if s.Transport == MCPHTTP {
		return json.Marshal(map[string]any{"type": "http", "name": s.Name, "url": s.URL, "headers": kv})
	}
	args := s.Args
	if args == nil {
		args = []string{}
	}
	return json.Marshal(map[string]any{"name": s.Name, "command": s.Command, "args": args, "env": kv})
}

// MCPSelection is what one run gets: the servers to start, the tools it may call, and the discovered tools to remove.
type MCPSelection struct {
	Servers []MCPServer
	Allow   []string
	Deny    []string
}

type CreateMCPServer struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	URL       string            `json:"url"`
	Secret    map[string]string `json:"secret"`
}

// UpdateMCPServer leaves a nil field unchanged; a non-nil Secret replaces the whole map.
type UpdateMCPServer struct {
	Name    *string            `json:"name"`
	Command *string            `json:"command"`
	Args    *[]string          `json:"args"`
	URL     *string            `json:"url"`
	Secret  *map[string]string `json:"secret"`
	Status  *string            `json:"status"`
}

type MCPToolToggle struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}
