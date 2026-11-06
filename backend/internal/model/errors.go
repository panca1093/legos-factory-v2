package model

import "errors"

var (
	ErrWorkOrderNotFound = errors.New("work order not found")
	ErrProjectNotFound   = errors.New("project not found")
	ErrSessionNotFound   = errors.New("session not found")
	ErrUserNotFound      = errors.New("user not found")
	ErrKnowledgeNotFound = errors.New("knowledge record not found")
	ErrKBDocNotFound     = errors.New("knowledge document not found")
)

var (
	ErrGHMissing         = errors.New("the gh CLI is not installed; install it from https://cli.github.com")
	ErrGHUnauthenticated = errors.New("gh is not authenticated; run `gh auth login` and `gh auth setup-git`")
	ErrGHUnreachable     = errors.New("gh could not reach GitHub")
	ErrNotSupported      = errors.New("not supported on this platform")
	ErrCancelled         = errors.New("cancelled")
	ErrBusy              = errors.New("another folder chooser is already open")
)

// ErrStateConflict marks domain rejections that indicate an invalid
// operation for the current state (mapped to HTTP 409).
var ErrStateConflict = errors.New("STATE_CONFLICT")
