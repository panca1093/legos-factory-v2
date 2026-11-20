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
	// Resolved is the in-root path a write landed on after following symlinks; set by Write only.
	Resolved string `json:"-"`
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

// EditorActor is who most likely changed a file on disk; nil means unknown.
type EditorActor struct {
	Role   string `json:"role"`
	WOID   string `json:"wo_id,omitempty"`
	TaskID string `json:"task_id,omitempty"`
	Label  string `json:"label"`
}
