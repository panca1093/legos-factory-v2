package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func testWatcher(t *testing.T, dirs ...string) (*FSWatcher, string) {
	t.Helper()
	old := watchDebounce
	watchDebounce = 30 * time.Millisecond
	t.Cleanup(func() { watchDebounce = old })
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	w, err := newFSWatcher(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	for _, d := range dirs {
		if d != "." {
			if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Set(dirs); err != nil {
		t.Fatal(err)
	}
	return w, root
}

func collect(w *FSWatcher, quiet time.Duration) []model.FSEvent {
	var got []model.FSEvent
	for {
		select {
		case e, ok := <-w.Events():
			if !ok {
				return got
			}
			got = append(got, e)
		case <-time.After(quiet):
			return got
		}
	}
}

func has(evs []model.FSEvent, path, kind string) bool {
	for _, e := range evs {
		if e.Path == path && e.Kind == kind {
			return true
		}
	}
	return false
}

func TestWatcherDebouncesBurstIntoOneEvent(t *testing.T) {
	w, root := testWatcher(t, ".")
	p := filepath.Join(root, "a.txt")
	os.WriteFile(p, []byte("1"), 0o644)
	time.Sleep(60 * time.Millisecond)
	collect(w, 150*time.Millisecond)
	for i := 0; i < 5; i++ {
		os.WriteFile(p, []byte(fmt.Sprint(i)), 0o644)
		time.Sleep(5 * time.Millisecond)
	}
	got := collect(w, 200*time.Millisecond)
	n := 0
	for _, e := range got {
		if e.Path == "a.txt" && e.Kind == "changed" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one debounced change, got %d in %+v", n, got)
	}
}

func TestWatcherCreateDeleteAndDirEvents(t *testing.T) {
	w, root := testWatcher(t, ".", "sub")
	os.WriteFile(filepath.Join(root, "sub", "n.go"), []byte("x"), 0o644)
	got := collect(w, 200*time.Millisecond)
	if !has(got, "sub/n.go", "created") || !has(got, "sub", "dir") {
		t.Fatalf("create: %+v", got)
	}
	os.Remove(filepath.Join(root, "sub", "n.go"))
	got = collect(w, 200*time.Millisecond)
	if !has(got, "sub/n.go", "deleted") || !has(got, "sub", "dir") {
		t.Fatalf("delete: %+v", got)
	}
}

func TestWatcherSeesTempThenRenameSaveAndDropsTempFile(t *testing.T) {
	w, root := testWatcher(t, ".")
	target := filepath.Join(root, "f.txt")
	os.WriteFile(target, []byte("old"), 0o644)
	collect(w, 150*time.Millisecond)
	tmp := filepath.Join(root, TempPrefix+"123")
	os.WriteFile(tmp, []byte("new"), 0o644)
	os.Rename(tmp, target)
	got := collect(w, 200*time.Millisecond)
	if !has(got, "f.txt", "created") && !has(got, "f.txt", "changed") {
		t.Fatalf("rename save not seen: %+v", got)
	}
	for _, e := range got {
		if e.Path == TempPrefix+"123" {
			t.Fatalf("temp file leaked: %+v", got)
		}
	}
}

func TestWatcherIgnoresGitExceptHEADAndDSStore(t *testing.T) {
	w, root := testWatcher(t, ".")
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	w.Set([]string{"."}) // picks up .git now that it exists
	os.WriteFile(filepath.Join(root, ".git", "index"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/x"), 0o644)
	os.WriteFile(filepath.Join(root, ".DS_Store"), []byte("x"), 0o644)
	got := collect(w, 250*time.Millisecond)
	if !has(got, ".git/HEAD", "changed") {
		t.Fatalf("HEAD change missing: %+v", got)
	}
	for _, e := range got {
		if e.Path == ".git/index" || e.Path == ".DS_Store" {
			t.Fatalf("ignored path leaked: %+v", got)
		}
	}
}

func TestWatcherOnlyReportsWatchedDirs(t *testing.T) {
	w, root := testWatcher(t, ".", "node_modules/x")
	os.MkdirAll(filepath.Join(root, "node_modules", "x"), 0o755)
	w.Set([]string{"."})
	os.WriteFile(filepath.Join(root, "node_modules", "x", "i.js"), []byte("x"), 0o644)
	got := collect(w, 200*time.Millisecond)
	for _, e := range got {
		if e.Path == "node_modules/x/i.js" {
			t.Fatalf("unwatched node_modules reported: %+v", got)
		}
	}
	w.Set([]string{".", "node_modules/x"})
	os.WriteFile(filepath.Join(root, "node_modules", "x", "j.js"), []byte("x"), 0o644)
	if got = collect(w, 200*time.Millisecond); !has(got, "node_modules/x/j.js", "created") {
		t.Fatalf("explicitly set node_modules dir not reported: %+v", got)
	}
}

func TestWatcherCapsDirsKeepingPriorityOrder(t *testing.T) {
	w, root := testWatcher(t, ".")
	var dirs []string
	for i := 0; i < MaxWatchedDirs+40; i++ {
		d := fmt.Sprintf("d%03d", i)
		os.Mkdir(filepath.Join(root, d), 0o755)
		dirs = append(dirs, d)
	}
	w.Set(append([]string{"."}, dirs...))
	if n := w.Count(); n != MaxWatchedDirs {
		t.Fatalf("count %d", n)
	}
	os.WriteFile(filepath.Join(root, "d000", "in.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, dirs[len(dirs)-1], "out.txt"), []byte("x"), 0o644)
	got := collect(w, 250*time.Millisecond)
	if !has(got, "d000/in.txt", "created") || has(got, dirs[len(dirs)-1]+"/out.txt", "created") {
		t.Fatalf("cap eviction order: %+v", got)
	}
}

func TestWatcherDropsOwnWriteWithMatchingHashOnly(t *testing.T) {
	w, root := testWatcher(t, ".")
	p := filepath.Join(root, "s.txt")
	os.WriteFile(p, []byte("mine"), 0o644)
	h, _ := hashFile(p)
	collect(w, 150*time.Millisecond)

	w.Suppress("s.txt", h)
	os.WriteFile(p, []byte("mine"), 0o644)
	if got := collect(w, 200*time.Millisecond); has(got, "s.txt", "changed") || has(got, "s.txt", "created") {
		t.Fatalf("own write not suppressed: %+v", got)
	}

	w.Suppress("s.txt", h)
	os.WriteFile(p, []byte("someone else"), 0o644)
	if got := collect(w, 200*time.Millisecond); !has(got, "s.txt", "changed") {
		t.Fatalf("foreign write must pass: %+v", got)
	}
}

func TestWatcherCloseStopsAndClosesChannel(t *testing.T) {
	w, root := testWatcher(t, ".")
	w.Close()
	select {
	case _, ok := <-w.Events():
		if ok {
			t.Fatal("unexpected event")
		}
	case <-time.After(time.Second):
		t.Fatal("events channel not closed")
	}
	os.WriteFile(filepath.Join(root, "after.txt"), []byte("x"), 0o644)
	if err := w.Set([]string{"."}); err == nil {
		t.Fatal("Set after Close must fail")
	}
}
