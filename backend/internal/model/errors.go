package model

import (
	"errors"
	"fmt"
)

var (
	ErrWorkOrderNotFound = errors.New("work order not found")
	ErrProjectNotFound   = errors.New("project not found")
	ErrSessionNotFound   = errors.New("session not found")
	ErrUserNotFound      = errors.New("user not found")
	ErrKnowledgeNotFound = errors.New("knowledge record not found")
	ErrKBDocNotFound     = errors.New("knowledge document not found")

	ErrAgentAccountNotFound = errors.New("agent account not found")
	ErrAgentUnknown         = errors.New("unknown agent")
)

var (
	ErrGHMissing         = errors.New("the gh CLI is not installed; install it from https://cli.github.com")
	ErrGHUnauthenticated = errors.New("gh is not authenticated; run `gh auth login` and `gh auth setup-git`")
	ErrGHUnreachable     = errors.New("gh could not reach GitHub")
	ErrNotSupported      = errors.New("not supported on this platform")
	ErrCancelled         = errors.New("cancelled")
	ErrBusy              = errors.New("another folder chooser is already open")
)

// ErrNoGitIdentity means the repository has no user.name/user.email configured, so commits cannot be made.
var ErrNoGitIdentity = fmt.Errorf("%w: no git identity for this repository: set user.name and user.email (for example with `git config --global user.name \"Your Name\"` and `git config --global user.email you@example.com`)", ErrPermanent)

// HookError is a commit or merge that git rejected, almost always a repository hook (pre-commit,
// commit-msg); Output is what git and the hook printed, so the Builder can fix it.
type HookError struct {
	Op     string // commit | merge
	Output string
	Hook   string // the hook that rejected it when only one could have ("commit-msg"), else ""
}

func (e *HookError) Error() string {
	return "git " + e.Op + " was rejected by a repository hook: " + e.Output
}

// SigningError is a commit or merge that failed because git could not sign it (the repository sets
// commit.gpgsign). The Builder cannot fix it: the user must make the signing key available.
type SigningError struct {
	Op     string
	Output string
}

func (e *SigningError) Error() string {
	return "git " + e.Op + " could not be signed: " + e.Output + " (commit.gpgsign is on for this repository; make the signing key available without a prompt, for example add it to ssh-agent, or turn signing off)"
}

func (e *SigningError) Unwrap() error { return ErrPermanent }

// WorktreeDir is where a work order's worktree lives, outside the project folder.
func WorktreeDir(dataDir, projectID, woID string) string {
	return dataDir + "/worktrees/" + projectID + "/" + woID
}

// ErrStateConflict marks domain rejections that indicate an invalid
// operation for the current state (mapped to HTTP 409).
var ErrStateConflict = errors.New("STATE_CONFLICT")

// Editor errors; the HTTP layer maps each to its code (SPEC §HTTP API).
var (
	ErrOutsideFolder = errors.New("path is outside this folder")
	ErrTooLarge      = errors.New("file is too large to open")
	ErrBinary        = errors.New("binary file")
	ErrFolderGone    = errors.New("folder no longer exists")
)

// EditorConflict is returned by a write whose base hash no longer matches the disk.
type EditorConflict struct{ Current FileStat }

func (e *EditorConflict) Error() string { return "file changed on disk" }
