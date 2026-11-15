package model

import (
	"errors"
	"fmt"
	"time"
)

var ErrChatNotFound = errors.New("chat not found")

// ErrChatFilesUnavailable means a file meant for a message is gone or already belongs to another message.
var ErrChatFilesUnavailable = errors.New("an attached file is no longer available")

// ErrChatFileNotFound is also a not-found chat, so every file endpoint answers 404 for a stranger's chat or file.
var ErrChatFileNotFound = fmt.Errorf("%w: file not found", ErrChatNotFound)

type Chat struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ProjectID string    `json:"project_id,omitempty"`
	Title     string    `json:"title"`
	Model     string    `json:"model"` // "provider/model"; empty means auto
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ChatAttachment is what a message records of something sent with it: an uploaded
// file ("image", "pdf" or "text", with FileID) or a knowledge document ("kb").
// Rows from before uploads have kind "text" and no FileID.
type ChatAttachment struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Size      int    `json:"size,omitempty"`
	DocID     string `json:"doc_id,omitempty"`
	Quote     string `json:"quote,omitempty"`
	FileID    string `json:"file_id,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

// ChatFileRec is an uploaded file; its bytes live on disk under the chat's file directory.
type ChatFileRec struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Kind      string `json:"kind"` // image | pdf | text
	Size      int    `json:"size"`

	ChatID    string    `json:"-"`
	UserID    string    `json:"-"`
	MessageID *int64    `json:"-"` // nil until a message carries the file
	SHA256    string    `json:"-"`
	CreatedAt time.Time `json:"-"`
}

type ChatUsage struct {
	Model      string `json:"model,omitempty"`
	TokensIn   int64  `json:"tokens_in"`
	TokensOut  int64  `json:"tokens_out"`
	CostMicros int64  `json:"cost_micros"`

	CacheRead     int64          `json:"cache_read,omitempty"`
	CacheWrite    int64          `json:"cache_write,omitempty"`
	RepoSHA       string         `json:"repo_sha,omitempty"`
	NewSession    bool           `json:"new_session,omitempty"`
	PromptBytes   int            `json:"prompt_bytes,omitempty"`
	FactsBytes    int            `json:"facts_bytes,omitempty"`
	MCPToolBytes  int            `json:"mcp_tool_bytes,omitempty"`
	AttachBytes   int            `json:"attach_bytes,omitempty"`
	MCPCalls      map[string]int `json:"mcp_calls,omitempty"`
	ContextUsed   int64          `json:"context_used,omitempty"`
	ContextWindow int64          `json:"context_window,omitempty"`
	Steps         int            `json:"steps,omitempty"`
	RepoProject   string         `json:"repo_project,omitempty"`
}

type ChatMessage struct {
	ID          int64            `json:"id"`
	ChatID      string           `json:"chat_id"`
	Role        string           `json:"role"` // user | assistant
	Content     string           `json:"content"`
	Attachments []ChatAttachment `json:"attachments"`
	Usage       *ChatUsage       `json:"usage,omitempty"`
	Error       string           `json:"error,omitempty"` // set on an assistant reply that failed or was interrupted; Content holds what streamed before
	CreatedAt   time.Time        `json:"created_at"`
}
