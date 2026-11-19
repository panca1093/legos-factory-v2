package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

type editorFolders interface {
	Folder(ctx context.Context, u *model.User, id string) (*model.EditorFolder, error)
}

type editorServerPaths interface {
	ServerPaths(ctx context.Context, userID string) (map[string]string, error)
	SetServerPath(ctx context.Context, userID, server, path string) error
}

type editorRuns interface {
	RunningByWO(ctx context.Context, woID string) ([]model.RunningRun, error)
	List(ctx context.Context, woID string) ([]model.Run, error)
}

// goplsChecker is implemented by the sandbox host: gopls built with a Go older than the folder's go.mod needs.
type goplsChecker interface {
	GoplsOutdated(ctx context.Context, bin, root string) (outdated bool, builtWith, need string)
}

// EditorLive is the live half of the editor: change watching, actor attribution and language servers.
type EditorLive struct {
	Folders editorFolders
	FS      EditorFS
	Host    LSPHost
	Servers []model.LSPServerSpec
	Store   editorServerPaths
	Runs    editorRuns
	Reg     *LSPRegistry

	GitDebounce time.Duration
	GitMinGap   time.Duration
	GoneEvery   time.Duration
	Now         func() time.Time

	mu       sync.Mutex
	disc     map[string][]model.LSPServerState
	sessions map[[2]string]map[*ControlSession]struct{}
}

func (l *EditorLive) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *EditorLive) folder(ctx context.Context, u *model.User, id string) (model.EditorFolder, error) {
	f, err := l.Folders.Folder(ctx, u, id)
	if err != nil {
		return model.EditorFolder{}, err
	}
	if f == nil {
		return model.EditorFolder{}, model.ErrEditorFolderNotFound
	}
	return *f, nil
}

// OwnedFolder resolves a folder the user opened; another user's folder looks like a missing one.
func (l *EditorLive) OwnedFolder(ctx context.Context, u *model.User, folderID string) (model.EditorFolder, error) {
	return l.folder(ctx, u, folderID)
}

// ---- language servers ----

func (l *EditorLive) spec(id string) (model.LSPServerSpec, bool) {
	for _, s := range l.Servers {
		if s.ID == id {
			return s, true
		}
	}
	return model.LSPServerSpec{}, false
}

func (l *EditorLive) discover(ctx context.Context, userID string) ([]model.LSPServerState, error) {
	custom, err := l.Store.ServerPaths(ctx, userID)
	if err != nil {
		return nil, err
	}
	states := l.Host.Discover(ctx, l.Servers, custom)
	l.mu.Lock()
	if l.disc == nil {
		l.disc = map[string][]model.LSPServerState{}
	}
	l.disc[userID] = states
	l.mu.Unlock()
	return states, nil
}

// ServerStates lists the allowlisted servers for the user; discovery is cached until CheckServers.
func (l *EditorLive) ServerStates(ctx context.Context, userID string) ([]model.LSPServerState, error) {
	l.mu.Lock()
	c, ok := l.disc[userID]
	l.mu.Unlock()
	if !ok {
		return l.discover(ctx, userID)
	}
	return append([]model.LSPServerState(nil), c...), nil
}

// CheckServers rediscovers the servers (Check again).
func (l *EditorLive) CheckServers(ctx context.Context, userID string) ([]model.LSPServerState, error) {
	return l.discover(ctx, userID)
}

// SetServerPath probes a custom binary once and stores it for the user when it runs.
func (l *EditorLive) SetServerPath(ctx context.Context, userID, id, binPath string, confirm bool) (model.LSPServerState, error) {
	spec, ok := l.spec(id)
	if !ok {
		return model.LSPServerState{}, fmt.Errorf("%w: unknown language server %q", ErrInvalid, id)
	}
	if !confirm {
		return model.LSPServerState{}, fmt.Errorf("%w: confirm must be true to run %s", ErrInvalid, binPath)
	}
	if binPath != "" {
		if !filepath.IsAbs(binPath) || filepath.Clean(binPath) != binPath {
			return model.LSPServerState{}, fmt.Errorf("%w: path must be absolute and clean", ErrInvalid)
		}
		probe := l.Host.Discover(ctx, []model.LSPServerSpec{spec}, map[string]string{id: binPath})
		if len(probe) != 1 || (probe[0].State != model.LSPFound && probe[0].State != model.LSPOutdated) {
			msg := "the file did not run as " + spec.Binary
			if len(probe) == 1 && probe[0].Error != "" {
				msg = probe[0].Error
			}
			return model.LSPServerState{}, fmt.Errorf("%w: %s", ErrInvalid, msg)
		}
	}
	if err := l.Store.SetServerPath(ctx, userID, id, binPath); err != nil {
		return model.LSPServerState{}, err
	}
	states, err := l.discover(ctx, userID)
	if err != nil {
		return model.LSPServerState{}, err
	}
	for _, s := range states {
		if s.ID == id {
			return s, nil
		}
	}
	return model.LSPServerState{}, fmt.Errorf("%w: unknown language server %q", ErrInvalid, id)
}

// OpenLSP attaches the caller to a language server for a trusted folder, starting it when needed.
func (l *EditorLive) OpenLSP(ctx context.Context, u *model.User, folderID, server string) (*LSPClient, error) {
	userID := u.ID
	f, err := l.folder(ctx, u, folderID)
	if err != nil {
		return nil, err
	}
	if !f.Trusted {
		return nil, model.ErrUntrusted
	}
	spec, ok := l.spec(server)
	if !ok {
		return nil, ErrLSPUnknown
	}
	states, err := l.ServerStates(ctx, userID)
	if err != nil {
		return nil, err
	}
	bin := ""
	for _, s := range states {
		if s.ID == server && (s.State == model.LSPFound || s.State == model.LSPOutdated) {
			bin = s.Path
		}
	}
	if bin == "" {
		return nil, ErrLSPNotFound
	}
	c, err := l.Reg.Attach(ctx, LSPKey{User: userID, Folder: f.ID, Server: server}, spec, bin, f.Path)
	if err != nil {
		var crashed *LSPCrashedError
		if !errors.As(err, &crashed) && !errors.Is(err, model.ErrTooManyLSP) && ctx.Err() == nil {
			l.mu.Lock()
			delete(l.disc, userID)
			l.mu.Unlock()
		}
		return nil, err
	}
	return c, nil
}

// ---- actor attribution ----

// Attributor names who most likely wrote a file, per SPEC §Actor attribution. Run lookups are cached for TTL.
type Attributor struct {
	Runs editorRuns
	Now  func() time.Time
	TTL  time.Duration

	mu    sync.Mutex
	cache map[string]attrEntry
}

type attrEntry struct {
	at       time.Time
	running  []model.RunningRun
	finished []model.Run
}

func (a *Attributor) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Attributor) runs(ctx context.Context, woID string) attrEntry {
	ttl := a.TTL
	if ttl == 0 {
		ttl = 2 * time.Second
	}
	a.mu.Lock()
	if e, ok := a.cache[woID]; ok && a.now().Sub(e.at) < ttl {
		a.mu.Unlock()
		return e
	}
	a.mu.Unlock()
	e := attrEntry{at: a.now()}
	e.running, _ = a.Runs.RunningByWO(ctx, woID)
	if all, err := a.Runs.List(ctx, woID); err == nil {
		for _, r := range all {
			if r.FinishedAt != nil {
				e.finished = append(e.finished, r)
			}
		}
	}
	a.mu.Lock()
	if a.cache == nil {
		a.cache = map[string]attrEntry{}
	}
	a.cache[woID] = e
	a.mu.Unlock()
	return e
}

func roleLabel(kind string) string {
	if kind == "" {
		return "Agent"
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}

func actorFor(kind, woID, taskID string) *model.EditorActor {
	label := roleLabel(kind) + " · " + woID
	if taskID != "" {
		label += " (task " + taskID + ")"
	}
	return &model.EditorActor{Role: kind, WOID: woID, TaskID: taskID, Label: label}
}

// Actor returns nil when no Legos writer exists for the folder.
func (a *Attributor) Actor(ctx context.Context, f model.EditorFolder, at time.Time, headChanged bool) *model.EditorActor {
	if f.Kind != model.FolderWorktree || f.WOID == "" {
		if headChanged {
			return &model.EditorActor{Role: "git", Label: "after a git checkout"}
		}
		return nil
	}
	e := a.runs(ctx, f.WOID)
	latest := func(match func(model.RunningRun) bool) *model.RunningRun {
		var best *model.RunningRun
		for i := range e.running {
			if r := &e.running[i]; match(*r) && (best == nil || !r.StartedAt.Before(best.StartedAt)) {
				best = r
			}
		}
		return best
	}
	var pick *model.RunningRun
	if f.TaskID != "" {
		pick = latest(func(r model.RunningRun) bool { return r.TaskID == f.TaskID })
	} else if pick = latest(func(r model.RunningRun) bool { return r.TaskID == "" }); pick == nil {
		pick = latest(func(model.RunningRun) bool { return true })
	}
	if pick != nil {
		return actorFor(pick.Kind, f.WOID, pick.TaskID)
	}
	var recent *model.Run
	for i := range e.finished {
		r := &e.finished[i]
		if f.TaskID != "" && (r.TaskID == nil || *r.TaskID != f.TaskID) {
			continue
		}
		if d := at.Sub(*r.FinishedAt); d >= -time.Second && d <= 10*time.Second && (recent == nil || r.FinishedAt.After(*recent.FinishedAt)) {
			recent = r
		}
	}
	if recent != nil {
		task := ""
		if recent.TaskID != nil {
			task = *recent.TaskID
		}
		return actorFor(recent.Kind, f.WOID, task)
	}
	return &model.EditorActor{Role: "factory", WOID: f.WOID, TaskID: f.TaskID, Label: "Factory · " + f.WOID}
}

// ---- control socket session ----

type FileChanged struct {
	Type  string             `json:"type"`
	Path  string             `json:"path"`
	Kind  string             `json:"kind"`
	Hash  string             `json:"hash"`
	MTime time.Time          `json:"mtime"`
	Actor *model.EditorActor `json:"actor"`
}

type DirChanged struct {
	Type string `json:"type"`
	Dir  string `json:"dir"`
}

type GitStatusMsg struct {
	Type string `json:"type"`
	model.GitStatus
}

type ServerStateMsg struct {
	Type     string `json:"type"`
	Server   string `json:"server"`
	State    string `json:"state"`
	Version  string `json:"version,omitempty"`
	Progress *int   `json:"progress,omitempty"` // percent
	Error    string `json:"error,omitempty"`
}

type FolderGoneMsg struct {
	Type string `json:"type"`
}

const MaxWatchedDirs = 256

// ControlSession turns watcher events into the control socket's messages.
type ControlSession struct {
	live   *EditorLive
	user   string
	folder model.EditorFolder
	w      model.Watcher
	attr   *Attributor
	out    chan any
	done   chan struct{}
	once   sync.Once
	cancel context.CancelFunc
	ctx    context.Context
	unsub  func()

	mu     sync.Mutex
	files  map[string]bool
	headAt time.Time

	versions map[string]string // server id -> version, from the initial state; registry events carry none
}

// OpenControl starts watching the folder for the user.
func (l *EditorLive) OpenControl(ctx context.Context, u *model.User, folderID string) (*ControlSession, error) {
	userID := u.ID
	f, err := l.folder(ctx, u, folderID)
	if err != nil {
		return nil, err
	}
	w, err := l.FS.Watch(f.Path)
	if err != nil {
		return nil, err
	}
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &ControlSession{live: l, user: userID, folder: f, w: w, ctx: sctx, cancel: cancel,
		attr: &Attributor{Runs: l.Runs, Now: l.Now}, out: make(chan any, 256), done: make(chan struct{}), files: map[string]bool{}}
	if err := w.Set([]string{"."}); err != nil {
		cancel()
		_ = w.Close()
		return nil, err
	}
	var stateCh <-chan LSPStateEvent
	stateCh, s.unsub = l.Reg.Watch(userID, f.ID)
	l.mu.Lock()
	if l.sessions == nil {
		l.sessions = map[[2]string]map[*ControlSession]struct{}{}
	}
	k := [2]string{userID, f.ID}
	if l.sessions[k] == nil {
		l.sessions[k] = map[*ControlSession]struct{}{}
	}
	l.sessions[k][s] = struct{}{}
	l.mu.Unlock()
	go s.run(stateCh)
	go s.initialServerStates()
	return s, nil
}

func (s *ControlSession) Events() <-chan any         { return s.out }
func (s *ControlSession) Folder() model.EditorFolder { return s.folder }

func (s *ControlSession) Close() {
	s.once.Do(func() {
		close(s.done)
		s.cancel()
		s.unsub()
		_ = s.w.Close()
		l := s.live
		l.mu.Lock()
		delete(l.sessions[[2]string{s.user, s.folder.ID}], s)
		l.mu.Unlock()
	})
}

func (s *ControlSession) emit(ev any) {
	select {
	case s.out <- ev:
	case <-s.done:
	}
}

func cleanRel(p string) (string, bool) {
	if p == "" || strings.ContainsRune(p, 0) {
		return "", false
	}
	p = path.Clean(filepath.ToSlash(p))
	if p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
		return "", false
	}
	return p, true
}

// Watch replaces the watched set from the client's full list: the root and the directories of open files first
// (never dropped), then expanded directories, newest first, until the cap.
func (s *ControlSession) Watch(dirs, files []string) error {
	var ordered []string
	seen := map[string]bool{}
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			ordered = append(ordered, d)
		}
	}
	add(".")
	open := map[string]bool{}
	for _, f := range files {
		if rel, ok := cleanRel(f); ok {
			open[rel] = true
			add(path.Dir(rel))
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if rel, ok := cleanRel(dirs[i]); ok {
			add(rel)
		}
	}
	if len(ordered) > MaxWatchedDirs {
		ordered = ordered[:MaxWatchedDirs]
	}
	s.mu.Lock()
	s.files = open
	s.mu.Unlock()
	return s.w.Set(ordered)
}

func (s *ControlSession) isOpen(rel string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.files[rel]
}

func (s *ControlSession) run(states <-chan LSPStateEvent) {
	debounce := orDefaultDur(s.live.GitDebounce, 500*time.Millisecond)
	minGap := orDefaultDur(s.live.GitMinGap, 2*time.Second)
	goneEvery := orDefaultDur(s.live.GoneEvery, 5*time.Second)
	gone := time.NewTicker(goneEvery)
	defer gone.Stop()
	var gitTimer *time.Timer
	var gitC <-chan time.Time
	var lastGit time.Time
	schedule := func() {
		if gitC != nil {
			return
		}
		d := debounce
		if wait := minGap - time.Since(lastGit); wait > d {
			d = wait
		}
		gitTimer = time.NewTimer(d)
		gitC = gitTimer.C
	}
	defer func() {
		if gitTimer != nil {
			gitTimer.Stop()
		}
	}()
	go s.sendGit()
	lastGit = time.Now()
	events := s.w.Events()
	for {
		select {
		case <-s.done:
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			s.onFSEvent(ev)
			schedule()
		case ev := <-states:
			s.mu.Lock()
			ver := s.versions[ev.Server]
			s.mu.Unlock()
			s.emit(ServerStateMsg{Type: "server_state", Server: ev.Server, State: ev.State, Version: ver, Error: ev.Error})
		case <-gitC:
			gitC = nil
			lastGit = time.Now()
			go s.sendGit()
		case <-gone.C:
			if st, err := os.Stat(s.folder.Path); err != nil || !st.IsDir() {
				s.emit(FolderGoneMsg{Type: "folder_gone"})
				return
			}
		}
	}
}

func (s *ControlSession) onFSEvent(ev model.FSEvent) {
	switch {
	case ev.Path == ".git/HEAD":
		s.mu.Lock()
		s.headAt = time.Now()
		s.mu.Unlock()
	case ev.Kind == "dir":
		dir := ev.Path
		if dir == "." {
			dir = ""
		}
		s.emit(DirChanged{Type: "dir_changed", Dir: dir})
	case s.isOpen(ev.Path):
		msg := FileChanged{Type: "file_changed", Path: ev.Path, Kind: ev.Kind}
		if ev.Kind != "deleted" {
			if st, h, err := statHash(s.folder.Path, ev.Path); err == nil {
				msg.Hash, msg.MTime = h, st.ModTime()
			} else if errors.Is(err, os.ErrNotExist) {
				msg.Kind = "deleted"
			}
		}
		at := time.Now()
		s.mu.Lock()
		head := !s.headAt.IsZero() && at.Sub(s.headAt) <= time.Second
		s.mu.Unlock()
		msg.Actor = s.attr.Actor(s.ctx, s.folder, at, head)
		s.emit(msg)
	}
}

const hashLimit = 10 << 20

// statHash hashes a file inside root without following links out of it; files over 10 MB are not hashed.
func statHash(root, rel string) (os.FileInfo, string, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", err
	}
	defer r.Close()
	f, err := r.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return st, "", err
	}
	if st.Size() > hashLimit {
		return st, "", nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return st, "", err
	}
	return st, hex.EncodeToString(h.Sum(nil)), nil
}

func (s *ControlSession) sendGit() {
	st, err := s.live.FS.GitStatus(s.ctx, s.folder.Path)
	if err != nil {
		return
	}
	if st.Entries == nil {
		st.Entries = []model.GitStatusEntry{}
	}
	s.emit(GitStatusMsg{Type: "git_status", GitStatus: st})
}

func (s *ControlSession) initialServerStates() {
	l := s.live
	states, err := l.ServerStates(s.ctx, s.user)
	if err != nil {
		return
	}
	running := l.Reg.Running(s.user, s.folder.ID)
	chk, _ := l.Host.(goplsChecker)
	vers := map[string]string{}
	for _, st := range states {
		vers[st.ID] = st.Version
	}
	s.mu.Lock()
	s.versions = vers
	s.mu.Unlock()
	for _, st := range states {
		msg := ServerStateMsg{Type: "server_state", Server: st.ID, State: st.State, Version: st.Version, Error: st.Error}
		if running[st.ID] {
			msg.State = model.LSPRunning
		} else if st.ID == "gopls" && st.State == model.LSPFound && chk != nil {
			if out, built, need := chk.GoplsOutdated(s.ctx, st.Path, s.folder.Path); out {
				msg.State = model.LSPOutdated
				msg.Error = fmt.Sprintf("gopls is built with %s but this module needs %s", built, need)
			}
		}
		s.emit(msg)
	}
}

type hashSuppressor interface{ Suppress(rel, hash string) }

// NoteWrite tells the user's open control sockets for the folder that the editor itself just wrote rel, so the
// resulting disk event is not reported back as an external change.
func (l *EditorLive) NoteWrite(userID, folderID, rel, hash string) {
	l.mu.Lock()
	var ss []*ControlSession
	for s := range l.sessions[[2]string{userID, folderID}] {
		ss = append(ss, s)
	}
	l.mu.Unlock()
	for _, s := range ss {
		if w, ok := s.w.(hashSuppressor); ok {
			w.Suppress(rel, hash)
		}
	}
}

// KillAll stops every language server; the app calls it on shutdown.
func (l *EditorLive) KillAll() { l.Reg.Shutdown() }
