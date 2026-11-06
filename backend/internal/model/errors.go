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
	ErrNotSupported      = errors.New("not supported on this platform")
	ErrCancelled         = errors.New("cancelled")
	ErrBusy              = errors.New("another folder chooser is already open")
)

// LocalRepo describes a folder on the host: the folder itself, or when it is
// not a git repo, the git repos among its direct children.
type LocalRepo struct {
	Path          string      `json:"path"`
	Name          string      `json:"name"`
	IsGit         bool        `json:"is_git"`
	GitHubRepo    string      `json:"github_repo"`
	DefaultBranch string      `json:"default_branch"`
	Language      string      `json:"language"`
	Repos         []LocalRepo `json:"repos"`
}

// ErrStateConflict marks domain rejections that indicate an invalid
// operation for the current state (mapped to HTTP 409).
var ErrStateConflict = errors.New("STATE_CONFLICT")
