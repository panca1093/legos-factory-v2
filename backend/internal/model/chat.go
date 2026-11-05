package model

import (
	"errors"
	"time"
)

var ErrChatNotFound = errors.New("chat not found")

type Chat struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ProjectID string    `json:"project_id,omitempty"`
	Title     string    `json:"title"`
	Model     string    `json:"model"` // "provider/model"; empty means auto
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ChatAttachment is what is kept of something sent with a message: a text file
// ("file") or a knowledge document ("kb"). The content itself is not stored.
type ChatAttachment struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Size  int    `json:"size,omitempty"`
	DocID string `json:"doc_id,omitempty"`
	Quote string `json:"quote,omitempty"`
}

type ChatUsage struct {
	Model      string `json:"model,omitempty"`
	TokensIn   int64  `json:"tokens_in"`
	TokensOut  int64  `json:"tokens_out"`
	CostMicros int64  `json:"cost_micros"`
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
