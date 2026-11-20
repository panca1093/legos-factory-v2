package usecase

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"legos-factory/backend/internal/model"
)

const (
	EditorEditableMax = 2 << 20  // larger or non-UTF-8 files open read-only
	EditorReadMax     = 10 << 20 // larger files are refused
	EditorQuickMax    = 50000
)

// Editor owns the rules of the code editor: which folders may be opened, what may be read or
// written inside them, and the write precondition. The filesystem work is behind FS.
type Editor struct {
	Store    EditorStore
	FS       EditorFS
	Projects *Projects
	Proj     ProjectStore
	WO       WorkOrderStore
	DataDir  string
	Home     string // the user's home directory; os.UserHomeDir when empty
	// Actor names who most likely changed rel on disk at the given time; nil result means unknown.
	Actor func(ctx context.Context, f *model.EditorFolder, rel string, at time.Time) *model.EditorActor

	writeLocks sync.Map // folder id + "\x00" + path -> *sync.Mutex
}

// WriteConflict is a write rejected because the disk no longer matches the caller's base hash.
type WriteConflict struct {
	model.EditorConflict
	Actor *model.EditorActor
}

func (c *WriteConflict) Unwrap() error { return &c.EditorConflict }

var systemRoots = []string{
	"/System", "/usr", "/bin", "/sbin", "/etc", "/Library", "/boot", "/dev", "/proc", "/sys",
	"/private/etc", "/private/var/db", "/private/var/root",
}

func realDir(p string) (string, error) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", model.ErrFolderGone
		}
		return "", err
	}
	st, err := os.Stat(real)
	if err != nil || !st.IsDir() {
		return "", model.ErrFolderGone
	}
	return real, nil
}

func under(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (e *Editor) home() string {
	h := e.Home
	if h == "" {
		h, _ = os.UserHomeDir()
	}
	if h == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(h); err == nil {
		return real
	}
	return h
}

// checkRoot refuses folders that would expose the machine or the factory's own data. A work-order
// worktree is the one place under DataDir that may be opened, and the caller builds that path itself.
func (e *Editor) checkRoot(real string, worktree bool) error {
	refuse := func(why string) error { return fmt.Errorf("%w: %s cannot be opened in the editor", ErrInvalid, why) }
	if real == string(filepath.Separator) {
		return refuse("the filesystem root")
	}
	if home := e.home(); home != "" && under(real, home) {
		return refuse("your home directory or one of its parents")
	}
	if data, err := filepath.EvalSymlinks(e.DataDir); err == nil {
		if under(real, data) {
			return refuse("a folder that contains the factory data directory")
		}
		if !worktree && under(data, real) {
			return refuse("the factory data directory")
		}
	}
	for _, s := range systemRoots {
		if under(s, real) {
			return refuse("a system directory")
		}
	}
	return nil
}

func (e *Editor) register(ctx context.Context, u *model.User, f model.EditorFolder) (*model.EditorFolder, error) {
	f.UserID = u.ID
	out, err := e.Store.UpsertFolder(ctx, f)
	if err != nil {
		return nil, err
	}
	if gs, err := e.FS.GitStatus(ctx, out.Path); err == nil && gs.Repo {
		out.Branch = gs.Branch
	}
	return out, nil
}

// ProjectFolder opens the project's local folder (its repo_url).
func (e *Editor) ProjectFolder(ctx context.Context, u *model.User, projectID string) (*model.EditorFolder, error) {
	if err := e.Projects.Authorize(ctx, u, projectID); err != nil {
		return nil, err
	}
	p, err := e.Proj.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	dir := expandPath(strings.TrimSpace(p.RepoURL))
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("%w: the project has no local folder", ErrInvalid)
	}
	real, err := realDir(dir)
	if err != nil {
		return nil, err
	}
	if err := e.checkRoot(real, false); err != nil {
		return nil, err
	}
	return e.register(ctx, u, model.EditorFolder{Path: real, Kind: model.FolderProject, ProjectID: projectID})
}

func validSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}

// WorktreeFolder opens a work order's worktree, or one of its task worktrees; ErrFolderGone when
// the worktree was cleaned up.
func (e *Editor) WorktreeFolder(ctx context.Context, u *model.User, woID, taskID string) (*model.EditorFolder, error) {
	if !validSegment(woID) || (taskID != "" && !validSegment(taskID)) {
		return nil, fmt.Errorf("%w: invalid work order or task id", ErrInvalid)
	}
	wo, err := e.WO.Get(ctx, woID)
	if err != nil {
		return nil, err
	}
	if err := e.Projects.Authorize(ctx, u, wo.ProjectID); err != nil {
		return nil, model.ErrWorkOrderNotFound
	}
	if !validSegment(wo.ProjectID) {
		return nil, fmt.Errorf("%w: invalid project id", ErrInvalid)
	}
	dir := model.WorktreeDir(e.DataDir, wo.ProjectID, woID)
	if taskID != "" {
		dir += ".tasks/" + taskID
	}
	real, err := realDir(dir)
	if err != nil {
		return nil, err
	}
	trees, err := filepath.EvalSymlinks(filepath.Join(e.DataDir, "worktrees"))
	if err != nil || !under(trees, real) || real == trees {
		return nil, fmt.Errorf("%w: not a work order worktree", ErrInvalid)
	}
	if err := e.checkRoot(real, true); err != nil {
		return nil, err
	}
	return e.register(ctx, u, model.EditorFolder{Path: real, Kind: model.FolderWorktree, ProjectID: wo.ProjectID, WOID: woID, TaskID: taskID})
}

// Folder returns the caller's registered folder when it still exists and the caller still has
// access to its project. Other users' folders are not found.
func (e *Editor) Folder(ctx context.Context, u *model.User, id string) (*model.EditorFolder, error) {
	f, err := e.Store.Folder(ctx, u.ID, id)
	if err != nil {
		return nil, err
	}
	if f.ProjectID != "" {
		if err := e.Projects.Authorize(ctx, u, f.ProjectID); err != nil {
			return nil, model.ErrEditorFolderNotFound
		}
	}
	st, err := os.Stat(f.Path)
	if err != nil || !st.IsDir() {
		return nil, model.ErrFolderGone
	}
	return f, nil
}

// Open returns the folder with its current branch, for reloads and deep links.
func (e *Editor) Open(ctx context.Context, u *model.User, id string) (*model.EditorFolder, error) {
	f, err := e.Folder(ctx, u, id)
	if err != nil {
		return nil, err
	}
	if gs, err := e.FS.GitStatus(ctx, f.Path); err == nil && gs.Repo {
		f.Branch = gs.Branch
	}
	return f, nil
}

// Tree lists one directory of the folder, flagging entries git ignores.
func (e *Editor) Tree(ctx context.Context, u *model.User, id, rel string) ([]model.FileEntry, int, error) {
	f, err := e.Folder(ctx, u, id)
	if err != nil {
		return nil, 0, err
	}
	rel = strings.Trim(rel, "/")
	entries, truncated, err := e.FS.List(f.Path, rel)
	if err != nil {
		return nil, 0, err
	}
	rels := make([]string, len(entries))
	for i, en := range entries {
		rels[i] = path.Join(rel, en.Name)
	}
	if ign, err := e.FS.Ignored(ctx, f.Path, rels); err == nil {
		for i := range entries {
			entries[i].Ignored = ign[rels[i]]
		}
	}
	return entries, truncated, nil
}

// Files lists the folder's files for quick open.
func (e *Editor) Files(ctx context.Context, u *model.User, id string) ([]string, error) {
	f, err := e.Folder(ctx, u, id)
	if err != nil {
		return nil, err
	}
	paths, err := e.FS.Files(ctx, f.Path, EditorQuickMax)
	if paths == nil {
		paths = []string{}
	}
	return paths, err
}

func countEOL(s string) (crlf, lf int) {
	crlf = strings.Count(s, "\r\n")
	return crlf, strings.Count(s, "\n") - crlf
}

// Read returns the file as the editor shows it: BOM stripped, line endings normalized to LF, with
// the original BOM and EOL reported so a write restores them.
func (e *Editor) Read(ctx context.Context, u *model.User, id, rel string) (model.FileContent, error) {
	f, err := e.Folder(ctx, u, id)
	if err != nil {
		return model.FileContent{}, err
	}
	fc, err := e.FS.Read(f.Path, rel, EditorReadMax)
	if err != nil {
		return model.FileContent{}, err
	}
	text, bom := strings.CutPrefix(fc.Content, "\xEF\xBB\xBF")
	fc.BOM = bom
	fc.ReadOnly = fc.Size > EditorEditableMax || !utf8.ValidString(text)
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	fc.EOL = "LF"
	if crlf, lf := countEOL(text); crlf > lf {
		fc.EOL = "CRLF"
	}
	fc.Content = strings.ReplaceAll(text, "\r\n", "\n")
	return fc, nil
}

// Write saves content. An existing file needs baseHash to equal the disk's hash; create needs the
// file to be absent. The file's BOM and line endings are preserved.
func (e *Editor) Write(ctx context.Context, u *model.User, id, rel, content, baseHash string, create bool) (model.FileStat, error) {
	f, err := e.Folder(ctx, u, id)
	if err != nil {
		return model.FileStat{}, err
	}
	if len(content) > EditorEditableMax {
		return model.FileStat{}, &model.SizedError{Err: model.ErrTooLarge, Size: int64(len(content))}
	}
	mu, _ := e.writeLocks.LoadOrStore(id+"\x00"+path.Clean(rel), &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	body := []byte(content)
	if create {
		return e.write(ctx, f, rel, body, "", true)
	}
	cur, err := e.FS.Read(f.Path, rel, EditorReadMax)
	if err != nil {
		return model.FileStat{}, err
	}
	text, bom := strings.CutPrefix(cur.Content, "\xEF\xBB\xBF")
	if cur.Size > EditorEditableMax || !utf8.ValidString(text) {
		return model.FileStat{}, fmt.Errorf("%w: this file is read-only (over %d MB or not UTF-8)", ErrInvalid, EditorEditableMax>>20)
	}
	if cur.Hash != baseHash {
		return model.FileStat{}, e.conflict(ctx, f, rel, &model.EditorConflict{Current: model.FileStat{Hash: cur.Hash, MTime: cur.MTime, Size: cur.Size}})
	}
	out := strings.ReplaceAll(content, "\r\n", "\n")
	if crlf, lf := countEOL(text); crlf > lf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	if bom {
		out = "\xEF\xBB\xBF" + out
	}
	return e.write(ctx, f, rel, []byte(out), baseHash, false)
}

func (e *Editor) write(ctx context.Context, f *model.EditorFolder, rel string, body []byte, baseHash string, create bool) (model.FileStat, error) {
	st, err := e.FS.Write(f.Path, rel, body, baseHash, create)
	if c, ok := err.(*model.EditorConflict); ok {
		return model.FileStat{}, e.conflict(ctx, f, rel, c)
	}
	return st, err
}

func (e *Editor) conflict(ctx context.Context, f *model.EditorFolder, rel string, c *model.EditorConflict) error {
	wc := &WriteConflict{EditorConflict: *c}
	if e.Actor != nil {
		wc.Actor = e.Actor(ctx, f, rel, c.Current.MTime)
	}
	return wc
}

func (e *Editor) Mkdir(ctx context.Context, u *model.User, id, rel string) error {
	f, err := e.Folder(ctx, u, id)
	if err != nil {
		return err
	}
	return e.FS.Mkdir(f.Path, rel)
}

func (e *Editor) Rename(ctx context.Context, u *model.User, id, from, to string) error {
	f, err := e.Folder(ctx, u, id)
	if err != nil {
		return err
	}
	return e.FS.Rename(f.Path, from, to)
}
