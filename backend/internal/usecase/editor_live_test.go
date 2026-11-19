package usecase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

type fakeRuns struct {
	running  []model.RunningRun
	finished []model.Run
	calls    atomic.Int32
}

func (f *fakeRuns) RunningByWO(_ context.Context, _ string) ([]model.RunningRun, error) {
	f.calls.Add(1)
	return f.running, nil
}
func (f *fakeRuns) List(context.Context, string) ([]model.Run, error) { return f.finished, nil }

func strp(s string) *string { return &s }

func TestAttributionTable(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) *time.Time { x := now.Add(-d); return &x }
	taskWT := model.EditorFolder{Kind: model.FolderWorktree, WOID: "wo_1", TaskID: "t3"}
	woWT := model.EditorFolder{Kind: model.FolderWorktree, WOID: "wo_1"}
	cases := []struct {
		name   string
		f      model.EditorFolder
		runs   *fakeRuns
		head   bool
		want   string
		isNull bool
	}{
		{"task worktree with its running builder", taskWT,
			&fakeRuns{running: []model.RunningRun{{WOID: "wo_1", TaskID: "t2", Kind: "builder"}, {WOID: "wo_1", TaskID: "t3", Kind: "builder"}}}, false, "Builder · wo_1 (task t3)", false},
		{"WO workspace with running architect", woWT,
			&fakeRuns{running: []model.RunningRun{{WOID: "wo_1", TaskID: "t1", Kind: "builder", StartedAt: now}, {WOID: "wo_1", Kind: "architect", StartedAt: now.Add(-time.Minute)}}}, false, "Architect · wo_1", false},
		{"WO workspace with only task runs takes the latest", woWT,
			&fakeRuns{running: []model.RunningRun{{WOID: "wo_1", TaskID: "t1", Kind: "builder", StartedAt: now.Add(-time.Minute)}, {WOID: "wo_1", TaskID: "t2", Kind: "builder", StartedAt: now}}}, false, "Builder · wo_1 (task t2)", false},
		{"finished 4 seconds ago", woWT,
			&fakeRuns{finished: []model.Run{{Kind: "inspector", FinishedAt: ago(4 * time.Second)}}}, false, "Inspector · wo_1", false},
		{"finished 30 seconds ago falls back to Factory", woWT,
			&fakeRuns{finished: []model.Run{{Kind: "inspector", FinishedAt: ago(30 * time.Second)}}}, false, "Factory · wo_1", false},
		{"nothing running", woWT, &fakeRuns{}, false, "Factory · wo_1", false},
		{"task worktree ignores another task's finished run", taskWT,
			&fakeRuns{finished: []model.Run{{Kind: "builder", TaskID: strp("t9"), FinishedAt: ago(time.Second)}}}, false, "Factory · wo_1", false},
		{"project folder has no Legos writer", model.EditorFolder{Kind: model.FolderProject}, &fakeRuns{}, false, "", true},
		{"plain folder", model.EditorFolder{Kind: model.FolderPlain}, &fakeRuns{}, false, "", true},
		{"project folder after a checkout", model.EditorFolder{Kind: model.FolderProject}, &fakeRuns{}, true, "after a git checkout", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Attributor{Runs: c.runs, Now: func() time.Time { return now }}
			got := a.Actor(context.Background(), c.f, now, c.head)
			if c.isNull {
				if got != nil {
					t.Fatalf("want nil, got %+v", got)
				}
				return
			}
			if got == nil || got.Label != c.want {
				t.Fatalf("want %q, got %+v", c.want, got)
			}
		})
	}
}

func TestAttributionCachesRunLookupsPerWorkOrder(t *testing.T) {
	now := time.Now()
	runs := &fakeRuns{}
	a := &Attributor{Runs: runs, Now: func() time.Time { return now }}
	f := model.EditorFolder{Kind: model.FolderWorktree, WOID: "wo_1"}
	a.Actor(context.Background(), f, now, false)
	a.Actor(context.Background(), f, now, false)
	if runs.calls.Load() != 1 {
		t.Fatalf("lookups %d", runs.calls.Load())
	}
	now = now.Add(3 * time.Second)
	a.Actor(context.Background(), f, now, false)
	if runs.calls.Load() != 2 {
		t.Fatalf("cache must expire after 2 s: %d", runs.calls.Load())
	}
}

// ---- fakes for sessions ----

type fakeWatcher struct {
	mu       sync.Mutex
	sets     [][]string
	ev       chan model.FSEvent
	closed   bool
	suppress [][2]string
}

func (w *fakeWatcher) Set(d []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sets = append(w.sets, append([]string(nil), d...))
	return nil
}
func (w *fakeWatcher) Events() <-chan model.FSEvent { return w.ev }
func (w *fakeWatcher) Close() error                 { w.mu.Lock(); w.closed = true; w.mu.Unlock(); return nil }
func (w *fakeWatcher) Suppress(rel, h string) {
	w.mu.Lock()
	w.suppress = append(w.suppress, [2]string{rel, h})
	w.mu.Unlock()
}
func (w *fakeWatcher) lastSet() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sets[len(w.sets)-1]
}

type fakeEditorFS struct {
	EditorFS
	w   *fakeWatcher
	git atomic.Int32
}

func (f *fakeEditorFS) Watch(string) (model.Watcher, error) { return f.w, nil }
func (f *fakeEditorFS) GitStatus(context.Context, string) (model.GitStatus, error) {
	n := f.git.Add(1)
	return model.GitStatus{Repo: true, Branch: fmt.Sprintf("b%d", n)}, nil
}

type fakeFolders map[string]*model.EditorFolder

func (f fakeFolders) Folder(_ context.Context, u *model.User, id string) (*model.EditorFolder, error) {
	if x, ok := f[u.ID+"/"+id]; ok {
		if st, err := os.Stat(x.Path); err != nil || !st.IsDir() {
			return nil, model.ErrFolderGone
		}
		return x, nil
	}
	return nil, model.ErrEditorFolderNotFound
}

func usr(id string) *model.User { return &model.User{ID: id} }

type fakePaths struct {
	mu sync.Mutex
	m  map[string]string
}

func (p *fakePaths) ServerPaths(context.Context, string) (map[string]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]string{}
	for k, v := range p.m {
		out[k] = v
	}
	return out, nil
}
func (p *fakePaths) SetServerPath(_ context.Context, _, id, path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]string{}
	}
	p.m[id] = path
	return nil
}

type probeHost struct {
	lspFakeHost
	discoverCalls atomic.Int32
	outdated      bool
}

func (h *probeHost) Discover(_ context.Context, servers []model.LSPServerSpec, custom map[string]string) []model.LSPServerState {
	h.discoverCalls.Add(1)
	var out []model.LSPServerState
	for _, s := range servers {
		st := model.LSPServerState{ID: s.ID, State: model.LSPFound, Path: "/bin/" + s.Binary, Version: "1.0.0", Install: s.Install}
		if p := custom[s.ID]; p != "" {
			st.Path = p
			if p == "/bad" {
				st.State, st.Error = model.LSPNotFound, "custom path is not an executable file"
			}
		} else if s.ID == "tsls" && os.Getenv("NO_TSLS") != "" {
			st.State = model.LSPNotFound
		}
		out = append(out, st)
	}
	return out
}

func (h *probeHost) GoplsOutdated(context.Context, string, string) (bool, string, string) {
	return h.outdated, "go1.24.0", "go1.26.0"
}

var testServers = []model.LSPServerSpec{{ID: "gopls", Binary: "gopls", Install: "go install x"}, {ID: "tsls", Binary: "typescript-language-server", Install: "npm i x"}}

func newLive(t *testing.T, trusted bool) (*EditorLive, *fakeEditorFS, *probeHost, string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	fs := &fakeEditorFS{w: &fakeWatcher{ev: make(chan model.FSEvent, 16)}}
	host := &probeHost{}
	l := &EditorLive{
		Folders: fakeFolders{"u1/f1": {ID: "f1", Path: root, Kind: model.FolderProject, Trusted: trusted}},
		FS:      fs, Host: host, Servers: testServers, Store: &fakePaths{}, Runs: &fakeRuns{},
		Reg:         &LSPRegistry{Host: host},
		GitDebounce: 20 * time.Millisecond, GitMinGap: 60 * time.Millisecond, GoneEvery: time.Hour,
	}
	t.Cleanup(l.KillAll)
	return l, fs, host, root
}

func nextEvent[T any](t *testing.T, s *ControlSession, match func(T) bool) T {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if v, ok := ev.(T); ok && match(v) {
				return v
			}
		case <-deadline:
			var zero T
			t.Fatalf("no %T event", zero)
			return zero
		}
	}
}

func always[T any](T) bool { return true }

func TestControlSessionReportsOpenFileChangesWithHashAndActor(t *testing.T) {
	l, fs, _, root := newLive(t, true)
	s, err := l.OpenControl(context.Background(), usr("u1"), "f1")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Watch(nil, []string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	fs.w.ev <- model.FSEvent{Path: "a.go", Kind: "changed"}
	ev := nextEvent(t, s, func(FileChanged) bool { return true })
	if ev.Path != "a.go" || ev.Kind != "changed" || len(ev.Hash) != 64 || ev.MTime.IsZero() || ev.Type != "file_changed" {
		t.Fatalf("%+v", ev)
	}
	if ev.Actor != nil {
		t.Fatalf("project folder attributes nobody: %+v", ev.Actor)
	}
	os.Remove(filepath.Join(root, "a.go"))
	fs.w.ev <- model.FSEvent{Path: "a.go", Kind: "changed"}
	if ev = nextEvent(t, s, func(FileChanged) bool { return true }); ev.Kind != "deleted" || ev.Hash != "" {
		t.Fatalf("vanished file must report deleted: %+v", ev)
	}
}

func TestControlSessionAttributesGitCheckoutAndIgnoresUnopenedFiles(t *testing.T) {
	l, fs, _, root := newLive(t, true)
	s, _ := l.OpenControl(context.Background(), usr("u1"), "f1")
	defer s.Close()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644)
	s.Watch(nil, []string{"a.txt"})
	fs.w.ev <- model.FSEvent{Path: ".git/HEAD", Kind: "changed"}
	fs.w.ev <- model.FSEvent{Path: "other.txt", Kind: "changed"}
	fs.w.ev <- model.FSEvent{Path: "a.txt", Kind: "changed"}
	ev := nextEvent(t, s, func(FileChanged) bool { return true })
	if ev.Path != "a.txt" || ev.Actor == nil || ev.Actor.Label != "after a git checkout" {
		t.Fatalf("%+v", ev)
	}
}

func TestControlSessionDirChangedRootIsEmptyString(t *testing.T) {
	l, fs, _, _ := newLive(t, true)
	s, _ := l.OpenControl(context.Background(), usr("u1"), "f1")
	defer s.Close()
	fs.w.ev <- model.FSEvent{Path: ".", Kind: "dir"}
	fs.w.ev <- model.FSEvent{Path: "pkg/x", Kind: "dir"}
	if ev := nextEvent(t, s, func(DirChanged) bool { return true }); ev.Dir != "" || ev.Type != "dir_changed" {
		t.Fatalf("%+v", ev)
	}
	if ev := nextEvent(t, s, func(DirChanged) bool { return true }); ev.Dir != "pkg/x" {
		t.Fatalf("%+v", ev)
	}
}

func TestControlSessionGitStatusIsDebouncedAndRateLimited(t *testing.T) {
	l, fs, _, _ := newLive(t, true)
	s, _ := l.OpenControl(context.Background(), usr("u1"), "f1")
	defer s.Close()
	first := nextEvent(t, s, always[GitStatusMsg])
	if first.Type != "git_status" || first.Branch != "b1" {
		t.Fatalf("initial status: %+v", first)
	}
	start := time.Now()
	for i := 0; i < 20; i++ {
		fs.w.ev <- model.FSEvent{Path: fmt.Sprintf("f%d", i), Kind: "changed"}
	}
	second := nextEvent(t, s, always[GitStatusMsg])
	if second.Branch != "b2" {
		t.Fatalf("burst collapsed into one refresh: %+v", second)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatalf("refresh within the rate limit: %v", time.Since(start))
	}
	time.Sleep(150 * time.Millisecond)
	if n := fs.git.Load(); n != 2 {
		t.Fatalf("git status ran %d times for one burst", n)
	}
}

func TestControlSessionWatchSetOrderingAndCap(t *testing.T) {
	l, fs, _, _ := newLive(t, true)
	s, _ := l.OpenControl(context.Background(), usr("u1"), "f1")
	defer s.Close()
	var dirs []string
	for i := 0; i < 300; i++ {
		dirs = append(dirs, fmt.Sprintf("d%03d", i))
	}
	if err := s.Watch(dirs, []string{"x/y/open.go", "../escape.go", "/abs.go"}); err != nil {
		t.Fatal(err)
	}
	set := fs.w.lastSet()
	if len(set) != MaxWatchedDirs || set[0] != "." || set[1] != "x/y" {
		t.Fatalf("root and open-file dirs first: %v", set[:3])
	}
	if set[2] != "d299" || set[len(set)-1] != "d046" {
		t.Fatalf("newest expanded dirs kept, oldest dropped: %s .. %s", set[2], set[len(set)-1])
	}
	for _, d := range set {
		if d == ".." || d == "../escape.go" || d == "/abs.go" {
			t.Fatalf("unsafe path accepted: %s", d)
		}
	}
}

func TestControlSessionReportsFolderGoneAndClosesWatcher(t *testing.T) {
	l, fs, _, root := newLive(t, true)
	l.GoneEvery = 20 * time.Millisecond
	s, _ := l.OpenControl(context.Background(), usr("u1"), "f1")
	os.RemoveAll(root)
	nextEvent(t, s, always[FolderGoneMsg])
	s.Close()
	fs.w.mu.Lock()
	defer fs.w.mu.Unlock()
	if !fs.w.closed {
		t.Fatal("watcher left open")
	}
}

func TestControlSessionSendsServerStatesIncludingOutdatedGopls(t *testing.T) {
	l, _, host, _ := newLive(t, true)
	host.outdated = true
	s, _ := l.OpenControl(context.Background(), usr("u1"), "f1")
	defer s.Close()
	g := nextEvent(t, s, func(m ServerStateMsg) bool { return m.Server == "gopls" })
	if g.State != model.LSPOutdated || g.Error == "" {
		t.Fatalf("%+v", g)
	}
	ts := nextEvent(t, s, func(m ServerStateMsg) bool { return m.Server == "tsls" })
	if ts.State != model.LSPFound {
		t.Fatalf("%+v", ts)
	}
}

func TestOpenControlRejectsOtherUsersAndMissingFolders(t *testing.T) {
	l, _, _, root := newLive(t, true)
	if _, err := l.OpenControl(context.Background(), usr("u2"), "f1"); !errors.Is(err, model.ErrEditorFolderNotFound) {
		t.Fatalf("other user's folder: %v", err)
	}
	os.RemoveAll(root)
	if _, err := l.OpenControl(context.Background(), usr("u1"), "f1"); !errors.Is(err, model.ErrFolderGone) {
		t.Fatalf("gone folder: %v", err)
	}
}

func TestNoteWriteReachesTheWatcherOfThatFolderOnly(t *testing.T) {
	l, fs, _, _ := newLive(t, true)
	s, _ := l.OpenControl(context.Background(), usr("u1"), "f1")
	defer s.Close()
	l.NoteWrite("u1", "f1", "a.go", "abc")
	l.NoteWrite("u2", "f1", "b.go", "def")
	fs.w.mu.Lock()
	defer fs.w.mu.Unlock()
	if len(fs.w.suppress) != 1 || fs.w.suppress[0] != [2]string{"a.go", "abc"} {
		t.Fatalf("%v", fs.w.suppress)
	}
}

func TestOpenLSPRefusesUntrustedUnknownAndMissingServers(t *testing.T) {
	l, _, _, _ := newLive(t, false)
	if _, err := l.OpenLSP(context.Background(), usr("u1"), "f1", "gopls"); !errors.Is(err, model.ErrUntrusted) {
		t.Fatalf("untrusted: %v", err)
	}
	l, _, _, _ = newLive(t, true)
	if _, err := l.OpenLSP(context.Background(), usr("u1"), "f1", "rust-analyzer"); !errors.Is(err, ErrLSPUnknown) {
		t.Fatalf("outside the allowlist: %v", err)
	}
	if _, err := l.OpenLSP(context.Background(), usr("u2"), "f1", "gopls"); !errors.Is(err, model.ErrEditorFolderNotFound) {
		t.Fatalf("other user's folder: %v", err)
	}
	t.Setenv("NO_TSLS", "1")
	l.disc = nil
	if _, err := l.OpenLSP(context.Background(), usr("u1"), "f1", "tsls"); !errors.Is(err, ErrLSPNotFound) {
		t.Fatalf("not installed: %v", err)
	}
	c, err := l.OpenLSP(context.Background(), usr("u1"), "f1", "gopls")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestServerListIsCachedUntilCheckAndCustomPathProbesOnce(t *testing.T) {
	l, _, host, _ := newLive(t, true)
	ctx := context.Background()
	if _, err := l.ServerStates(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ServerStates(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if host.discoverCalls.Load() != 1 {
		t.Fatalf("list not cached: %d", host.discoverCalls.Load())
	}
	l.CheckServers(ctx, "u1")
	if host.discoverCalls.Load() != 2 {
		t.Fatalf("check must rediscover")
	}

	if _, err := l.SetServerPath(ctx, "u1", "gopls", "/opt/mine/gopls", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("confirm required: %v", err)
	}
	if _, err := l.SetServerPath(ctx, "u1", "gopls", "relative/gopls", true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("absolute path required: %v", err)
	}
	if _, err := l.SetServerPath(ctx, "u1", "nope", "/opt/x", true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("allowlist: %v", err)
	}
	if _, err := l.SetServerPath(ctx, "u1", "gopls", "/bad", true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a path that does not run must not be stored: %v", err)
	}
	if got, _ := l.Store.ServerPaths(ctx, "u1"); got["gopls"] != "" {
		t.Fatalf("stored a bad path: %v", got)
	}
	st, err := l.SetServerPath(ctx, "u1", "gopls", "/opt/mine/gopls", true)
	if err != nil || st.Path != "/opt/mine/gopls" || st.State != model.LSPFound {
		t.Fatalf("%+v %v", st, err)
	}
	if got, _ := l.Store.ServerPaths(ctx, "u1"); got["gopls"] != "/opt/mine/gopls" {
		t.Fatalf("not stored: %v", got)
	}
}
