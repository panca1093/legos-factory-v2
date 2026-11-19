package model

import "time"

// EditorFolder is a folder a user opened in the editor. Path is absolute, cleaned and symlink-resolved.
type EditorFolder struct {
	ID        string    `json:"id"`
	UserID    string    `json:"-"`
	Path      string    `json:"path"`
	Kind      string    `json:"kind"` // project|worktree|folder
	ProjectID string    `json:"project_id,omitempty"`
	WOID      string    `json:"wo_id,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	Trusted   bool      `json:"trusted"`
	Branch    string    `json:"branch,omitempty"`
	OpenedAt  time.Time `json:"opened_at"`
}

const (
	FolderProject  = "project"
	FolderWorktree = "worktree"
	FolderPlain    = "folder"
)

// FileEntry is one row of a directory listing.
type FileEntry struct {
	Name         string    `json:"name"`
	Kind         string    `json:"kind"` // file|dir|symlink
	Size         int64     `json:"size"`
	MTime        time.Time `json:"mtime"`
	TargetInside *bool     `json:"target_inside,omitempty"` // symlinks only
	Ignored      bool      `json:"ignored"`
}

// FileContent is a file read for the editor.
type FileContent struct {
	Content  string    `json:"content"`
	Hash     string    `json:"hash"` // sha256 hex of the disk bytes
	MTime    time.Time `json:"mtime"`
	Size     int64     `json:"size"`
	EOL      string    `json:"eol"` // LF|CRLF
	BOM      bool      `json:"bom"`
	ReadOnly bool      `json:"readonly"` // 2–10 MB or not UTF-8
}

type FileStat struct {
	Hash  string    `json:"hash"`
	MTime time.Time `json:"mtime"`
	Size  int64     `json:"size"`
}

// GitStatusEntry is one porcelain v2 entry; X and Y are the index and work-tree codes.
type GitStatusEntry struct {
	Path string `json:"path"`
	X    string `json:"x"`
	Y    string `json:"y"`
}

type GitStatus struct {
	Repo    bool             `json:"repo"`
	Branch  string           `json:"branch"`
	Ahead   int              `json:"ahead"`
	Behind  int              `json:"behind"`
	Entries []GitStatusEntry `json:"entries"`
	TooMany bool             `json:"too_many"`
}

// FSEvent is a debounced change under a watched directory; Path is relative to the folder.
type FSEvent struct {
	Path string // relative, slash-separated
	Kind string // changed|created|deleted|dir
}

// Watcher watches a set of directories (non-recursive) under one folder.
type Watcher interface {
	Set(dirs []string) error
	Events() <-chan FSEvent
	Close() error
}

// LSPServerSpec is one allowlisted language server.
type LSPServerSpec struct {
	ID        string   `json:"id"`
	Languages []string `json:"languages"`
	Binary    string   `json:"binary"`
	Args      []string `json:"args"`
	Install   string   `json:"install"`
	NeedsNode bool     `json:"needs_node"`
}

const (
	LSPFound    = "found"
	LSPNotFound = "not_found"
	LSPOutdated = "outdated"
	LSPRunning  = "running"
	LSPCrashed  = "crashed"
)

type LSPServerState struct {
	ID        string   `json:"id"`
	Languages []string `json:"languages"`
	State     string   `json:"state"`
	Version   string   `json:"version,omitempty"`
	Path      string   `json:"path,omitempty"`
	Install   string   `json:"install"`
	Error     string   `json:"error,omitempty"`
}

// LSPProcess is a running language server speaking JSON-RPC with Content-Length framing on stdio.
type LSPProcess interface {
	ReadMessage() ([]byte, error)
	WriteMessage(msg []byte) error
	Wait() error
	Kill() error
	StderrTail() string
}

// EditorActor is who most likely changed a file on disk; nil means unknown.
type EditorActor struct {
	Role   string `json:"role"`
	WOID   string `json:"wo_id,omitempty"`
	TaskID string `json:"task_id,omitempty"`
	Label  string `json:"label"`
}
