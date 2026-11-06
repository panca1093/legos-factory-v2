package usecase

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestCodeFactsStaleOnUncommittedEditAndRename(t *testing.T) {
	data := t.TempDir()
	ws, base := gitWorkspace(t, data, "wo1")
	f := &Factory{CodeFacts: &memFacts{}, DataDir: data}
	ctx := context.Background()
	wo := &model.WorkOrder{ID: "wo1", KBBaseSHA: base}
	f.recordCodeFacts(ctx, wo, model.AgentForeman, []string{"keep.go:1 — k", "change.go:1 — c"})

	if err := os.WriteFile(filepath.Join(ws, "change.go"), []byte("package z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := f.codeFactsText(ctx, wo)
	if strings.Contains(got, "change.go") || !strings.Contains(got, "keep.go") {
		t.Fatalf("uncommitted edit must stale only its file:\n%s", got)
	}

	gitIn(t, ws, "mv", "keep.go", "moved.go")
	gitIn(t, ws, "commit", "-qam", "rename")
	if got = f.codeFactsText(ctx, wo); strings.Contains(got, "keep.go") {
		t.Fatalf("renamed file's fact must be stale:\n%s", got)
	}
}
