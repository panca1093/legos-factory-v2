package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWatcherNeverWatchesThroughSymlinks(t *testing.T) {
	outside := t.TempDir()
	w, root := testWatcher(t, ".")
	if err := os.MkdirAll(filepath.Join(root, "real", "in"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"out": outside, "gl": ".git", "ln": "real"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Set([]string{".", "out", "gl", "ln", "ln/in", "real"}); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.dirs) != 2 || !w.dirs["."] || !w.dirs["real"] {
		t.Fatalf("watched %v", w.dirs)
	}
}
