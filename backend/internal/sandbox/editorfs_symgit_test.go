package sandbox

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestEditorSymlinksIntoGitAreRefused(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	writeFile(t, root, "ok.txt", "ok")
	writeFile(t, root, "sub/keep", "k")
	links := map[string]string{"gl": ".git", "glc": ".git/config", "sub/deep": "../gl", "chain": "sub/deep"}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	var e EditorFS
	cfg, _ := os.ReadFile(filepath.Join(root, ".git", "config"))
	for _, p := range []string{"gl/config", "glc", "chain/config", "sub/deep/config", "chain"} {
		if _, err := e.Read(root, p, 1<<20); !errors.Is(err, model.ErrOutsideFolder) {
			t.Errorf("read %s: %v", p, err)
		}
		if _, err := e.Write(root, p, []byte("[core]\n\tfsmonitor = x\n"), "", false); !errors.Is(err, model.ErrOutsideFolder) {
			t.Errorf("write %s: %v", p, err)
		}
		if _, err := e.Write(root, p+"/new", []byte("x"), "", true); !errors.Is(err, model.ErrOutsideFolder) {
			t.Errorf("create %s/new: %v", p, err)
		}
		if err := e.Rename(root, "ok.txt", p+"/x"); !errors.Is(err, model.ErrOutsideFolder) {
			t.Errorf("rename to %s: %v", p, err)
		}
		if err := e.Rename(root, p+"/config", "moved"); !errors.Is(err, model.ErrOutsideFolder) {
			t.Errorf("rename from %s: %v", p, err)
		}
		if err := e.Mkdir(root, p+"/d"); !errors.Is(err, model.ErrOutsideFolder) {
			t.Errorf("mkdir %s: %v", p, err)
		}
	}
	for _, p := range []string{"gl", "chain"} {
		if _, _, err := e.List(root, p); !errors.Is(err, model.ErrOutsideFolder) {
			t.Errorf("list %s: %v", p, err)
		}
	}
	after, _ := os.ReadFile(filepath.Join(root, ".git", "config"))
	if !bytes.Equal(cfg, after) {
		t.Fatal(".git/config was modified")
	}
	if _, err := os.Stat(filepath.Join(root, "moved")); err == nil {
		t.Fatal("a file moved out of .git")
	}
}
