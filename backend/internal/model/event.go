package model

import "time"

// Event is a persisted work-order activity entry.
type Event struct {
	ID        int64          `json:"id"`
	WOID      string         `json:"work_order_id"`
	Actor     string         `json:"actor"`
	Kind      string         `json:"kind"`
	Message   string         `json:"message"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// LiveEvent is the in-process fan-out shape delivered to SSE subscribers.
type LiveEvent struct {
	WOID    string
	Actor   string
	Kind    string
	Message string
	Payload map[string]any
}
