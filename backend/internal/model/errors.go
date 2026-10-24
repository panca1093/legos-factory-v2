package model

import "errors"

var (
	ErrWorkOrderNotFound = errors.New("work order not found")
	ErrProjectNotFound   = errors.New("project not found")
	ErrSessionNotFound   = errors.New("session not found")
	ErrUserNotFound      = errors.New("user not found")
)

// ErrStateConflict marks domain rejections that indicate an invalid
// operation for the current state (mapped to HTTP 409).
var ErrStateConflict = errors.New("STATE_CONFLICT")
