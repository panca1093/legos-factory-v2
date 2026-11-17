package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func TestRunCheckHonoursDirAndEnv(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := RunCheck(context.Background(), root, model.Check{Name: "pwd", Dir: "api", Command: `printf "%s %s" "$(basename "$PWD")" "$DATABASE_URL"`}, 0, []string{"DATABASE_URL=pg://x"})
	if r.Outcome != model.Pass || r.Output != "api pg://x" {
		t.Fatalf("outcome=%s output=%q", r.Outcome, r.Output)
	}
}

func TestRunCheckRejectsDirOutsideWorkspace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"..", "../other", "a/../.."} {
		r := RunCheck(context.Background(), root, model.Check{Name: "x", Dir: dir, Command: "touch escaped"}, 0, nil)
		if r.Outcome != model.Unverified || !strings.Contains(r.Output, "outside the workspace") {
			t.Fatalf("dir %q: outcome=%s output=%q", dir, r.Outcome, r.Output)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escaped")); err == nil {
		t.Fatal("command ran outside the workspace")
	}
}

func TestRunCheckTimeoutKillsTheWholeTree(t *testing.T) {
	start := time.Now()
	r := RunCheck(context.Background(), t.TempDir(), model.Check{Name: "slow", Command: "sleep 30 | cat"}, 500*time.Millisecond, nil)
	if r.Outcome != model.Unverified || time.Since(start) > 10*time.Second {
		t.Fatalf("outcome %s after %s", r.Outcome, time.Since(start))
	}
}
