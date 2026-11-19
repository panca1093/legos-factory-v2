package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"legos-factory/backend/internal/model"
)

const (
	MaxWatchedDirs = 256
	// TempPrefix names the temp files an atomic save writes next to its target; events for them are dropped.
	TempPrefix = ".legos-tmp-"

	suppressTTL  = 2 * time.Second
	hashMaxBytes = 10 << 20
)

// watchDebounce is a var so tests can shorten it.
var watchDebounce = 150 * time.Millisecond

type suppression struct {
	hash    string
	expires time.Time
}

type pendingEvent struct {
	created, dirty bool
	timer          *time.Timer
}

// FSWatcher watches a set of directories under one folder, non-recursively, and reports debounced events.
type FSWatcher struct {
	root   string
	w      *fsnotify.Watcher
	gitdir string // set when .git is a file pointing elsewhere (a linked worktree)

	mu       sync.Mutex
	dirs     map[string]bool // rel dirs, "." for the root
	suppress map[string]suppression
	pending  map[string]*pendingEvent // loop goroutine only

	out  chan model.FSEvent
	fire chan string
	done chan struct{}
	once sync.Once
}

// NewWatcher starts a watcher for root with no directories set.
func NewWatcher(root string) (model.Watcher, error) { return newFSWatcher(root) }

func newFSWatcher(root string) (*FSWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	fw := &FSWatcher{root: filepath.Clean(root), w: w, dirs: map[string]bool{}, suppress: map[string]suppression{},
		pending: map[string]*pendingEvent{}, out: make(chan model.FSEvent, 512), fire: make(chan string, 512), done: make(chan struct{})}
	fw.gitdir = gitdirOf(fw.root)
	go fw.loop()
	return fw, nil
}

func gitdirOf(root string) string {
	b, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil {
		return ""
	}
	line, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return ""
	}
	p := strings.TrimSpace(line)
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	return filepath.Clean(p)
}

func (fw *FSWatcher) Events() <-chan model.FSEvent { return fw.out }

// Count is the number of directories currently watched, not counting the git directory.
func (fw *FSWatcher) Count() int {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	return len(fw.dirs)
}

// Suppress drops the next event for rel whose file content hashes to hash, for 2 seconds.
func (fw *FSWatcher) Suppress(rel, hash string) {
	fw.mu.Lock()
	fw.suppress[filepath.ToSlash(rel)] = suppression{hash: hash, expires: time.Now().Add(suppressTTL)}
	fw.mu.Unlock()
}

// Set replaces the watched directories (rel, slash-separated, "" or "." for the root). Order is priority:
// entries past MaxWatchedDirs are not watched. Directories that do not exist are skipped.
func (fw *FSWatcher) Set(dirs []string) error {
	want := map[string]bool{}
	for _, d := range dirs {
		d = strings.Trim(filepath.ToSlash(filepath.Clean(d)), "/")
		if d == "" {
			d = "."
		}
		if strings.HasPrefix(d, "..") || d == ".git" || strings.HasPrefix(d, ".git/") {
			continue
		}
		if len(want) >= MaxWatchedDirs && !want[d] {
			break
		}
		if st, err := os.Stat(filepath.Join(fw.root, filepath.FromSlash(d))); err != nil || !st.IsDir() {
			continue
		}
		want[d] = true
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	select {
	case <-fw.done:
		return errors.New("watcher closed")
	default:
	}
	for d := range fw.dirs {
		if !want[d] {
			_ = fw.w.Remove(filepath.Join(fw.root, filepath.FromSlash(d)))
			delete(fw.dirs, d)
		}
	}
	for d := range want {
		if fw.dirs[d] {
			continue
		}
		if err := fw.w.Add(filepath.Join(fw.root, filepath.FromSlash(d))); err == nil {
			fw.dirs[d] = true
		}
	}
	gd := filepath.Join(fw.root, ".git")
	if fw.gitdir != "" {
		gd = fw.gitdir
	}
	if st, err := os.Stat(gd); err == nil && st.IsDir() {
		_ = fw.w.Add(gd)
	}
	return nil
}

func (fw *FSWatcher) Close() error {
	fw.once.Do(func() {
		close(fw.done)
		_ = fw.w.Close()
	})
	return nil
}

func (fw *FSWatcher) rel(name string) (string, bool) {
	if fw.gitdir != "" && filepath.Dir(name) == fw.gitdir {
		return ".git/" + filepath.Base(name), true
	}
	r, err := filepath.Rel(fw.root, name)
	if err != nil || strings.HasPrefix(r, "..") {
		return "", false
	}
	return filepath.ToSlash(r), true
}

func ignoredPath(rel string) bool {
	base := rel[strings.LastIndex(rel, "/")+1:]
	if base == ".DS_Store" || strings.HasPrefix(base, TempPrefix) {
		return true
	}
	if rel == ".git/HEAD" {
		return false
	}
	return rel == ".git" || strings.HasPrefix(rel, ".git/")
}

func (fw *FSWatcher) loop() {
	defer func() {
		for _, p := range fw.pending {
			p.timer.Stop()
		}
		close(fw.out)
	}()
	for {
		select {
		case <-fw.done:
			return
		case _, ok := <-fw.w.Errors:
			if !ok {
				return
			}
		case ev, ok := <-fw.w.Events:
			if !ok {
				return
			}
			fw.onEvent(ev)
		case rel := <-fw.fire:
			fw.emit(rel)
		}
	}
}

func (fw *FSWatcher) onEvent(ev fsnotify.Event) {
	rel, ok := fw.rel(ev.Name)
	if !ok || ignoredPath(rel) {
		return
	}
	structural := ev.Has(fsnotify.Create) || ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename)
	if !structural && !ev.Has(fsnotify.Write) {
		return
	}
	p := fw.pending[rel]
	if p == nil {
		p = &pendingEvent{}
		fw.pending[rel] = p
		p.timer = time.AfterFunc(watchDebounce, func() {
			select {
			case fw.fire <- rel:
			case <-fw.done:
			}
		})
	} else {
		p.timer.Reset(watchDebounce)
	}
	if ev.Has(fsnotify.Create) {
		p.created = true
	}
	if structural {
		p.dirty = true
	}
}

func (fw *FSWatcher) emit(rel string) {
	p := fw.pending[rel]
	delete(fw.pending, rel)
	if p == nil {
		return
	}
	full := filepath.Join(fw.root, filepath.FromSlash(rel))
	if rel == ".git/HEAD" {
		fw.send(model.FSEvent{Path: rel, Kind: "changed"})
		return
	}
	dir := "."
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		dir = rel[:i]
	}
	st, err := os.Lstat(full)
	switch {
	case err != nil:
		fw.send(model.FSEvent{Path: rel, Kind: "deleted"})
	case st.IsDir():
		// the entry itself is a directory: only its parent's listing changed
	default:
		if fw.suppressed(rel, full, st.Size()) {
			return
		}
		kind := "changed"
		if p.created {
			kind = "created"
		}
		fw.send(model.FSEvent{Path: rel, Kind: kind})
	}
	if p.dirty {
		fw.send(model.FSEvent{Path: dir, Kind: "dir"})
	}
}

func (fw *FSWatcher) suppressed(rel, full string, size int64) bool {
	fw.mu.Lock()
	s, ok := fw.suppress[rel]
	if ok && time.Now().After(s.expires) {
		delete(fw.suppress, rel)
		ok = false
	}
	fw.mu.Unlock()
	if !ok || size > hashMaxBytes {
		return false
	}
	h, err := hashFile(full)
	return err == nil && h == s.hash
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (fw *FSWatcher) send(e model.FSEvent) {
	select {
	case fw.out <- e:
	case <-fw.done:
	}
}
