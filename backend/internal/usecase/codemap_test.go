package usecase

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

func readCodeMap(t *testing.T, r *rig) (map[string]any, string) {
	t.Helper()
	front, body, err := r.know.Bundle.Read(codeMapPath("p1"))
	if err != nil {
		t.Fatal(err)
	}
	return front, body
}

func TestPortCodeMapMergesReplacesAndSkipsStale(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	mem := &memFacts{}
	r.f.CodeFacts = mem
	ws, base := gitWorkspace(t, r.f.DataDir, r.wo.ID)
	r.wo.KBBaseSHA = base
	r.f.recordCodeFacts(ctx, r.wo, model.AgentForeman, []string{"keep.go:1 — first claim", "change.go:1 — goes stale", "gone.go:2 — file removed"})
	if err := os.WriteFile(filepath.Join(ws, "change.go"), []byte("package z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.f.portCodeMap(ctx, r.wo)

	front, body := readCodeMap(t, r)
	if front["type"] != model.KBTypeCodeMap || front["status"] != model.KBStatusStable {
		t.Fatalf("front: %v", front)
	}
	if !strings.Contains(body, "- keep.go:1 — first claim") || strings.Contains(body, "change.go") {
		t.Fatalf("stale fact must be skipped:\n%s", body)
	}
	if strings.Contains(body, "gone.go") {
		t.Fatalf("a fact on a missing file must be dropped:\n%s", body)
	}

	mem.facts = nil
	r.f.recordCodeFacts(ctx, r.wo, model.AgentArchitect, []string{"keep.go:1 — second claim", "keep.go — whole file"})
	r.f.portCodeMap(ctx, r.wo)
	_, body = readCodeMap(t, r)
	if strings.Contains(body, "first claim") || !strings.Contains(body, "- keep.go:1 — second claim") || !strings.Contains(body, "- keep.go — whole file") {
		t.Fatalf("same path and line must be replaced:\n%s", body)
	}
	assertNoLint(t, r.know.Bundle.Root())
	if docs, _ := r.know.Idx.List(ctx, model.KBQuery{ProjectID: "p1", Types: []string{model.KBTypeCodeMap}}); len(docs) != 1 {
		t.Fatalf("want one indexed Code Map, got %+v", docs)
	}
}

func TestPortCodeMapWithoutWorkspaceKeepsStoredAndCaps(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	mem := &memFacts{}
	r.f.CodeFacts = mem
	for i := 0; i < maxCodeMapLines+5; i++ {
		mem.facts = append(mem.facts, model.CodeFact{Path: fmt.Sprintf("f%03d.go", i), Line: 1, Claim: "c", SHA: "abc"})
	}
	r.f.portCodeMap(ctx, r.wo)
	_, body := readCodeMap(t, r)
	if n := strings.Count(body, "\n- "); n != maxCodeMapLines {
		t.Fatalf("want %d lines, got %d", maxCodeMapLines, n)
	}
	if strings.Contains(body, "f000.go") || !strings.Contains(body, fmt.Sprintf("f%03d.go", maxCodeMapLines+4)) {
		t.Fatal("the oldest lines must be the ones dropped")
	}
	assertNoLint(t, r.know.Bundle.Root())

	text, audit := r.know.Context(ctx, "p1", string(model.AgentForeman), "", "anything", "", "")
	want := filepath.Join(r.know.Bundle.Root(), codeMapPath("p1"))
	if !strings.Contains(text, want) {
		t.Fatalf("Foreman context lacks the Code Map pointer:\n%s", text)
	}
	if strings.Contains(text, "f003.go") {
		t.Fatal("the pointer must not inline the document")
	}
	if ids, _ := audit["kb_docs"].([]string); len(ids) == 0 {
		t.Fatalf("audit lacks the doc: %v", audit)
	}
	if other, _ := r.know.Context(ctx, "other", string(model.AgentForeman), "", "x", "", ""); strings.Contains(other, "Code Map") {
		t.Fatal("another project must not see this Code Map")
	}
}

func TestSpecDecisionsFeedSummary(t *testing.T) {
	spec := "## Behaviour rules\n1. x\n\n## Decisions\n\n| # | Decision | Why | Rejected alternative |\n|---|---|---|---|\n| 1 | Use integer rupiah | no float drift | float64 |\n\n## Out of scope\n| 9 | not | a | decision |\n"
	got := specDecisions(spec)
	if len(got) != 1 || got[0] != "- Use integer rupiah — no float drift (rejected: float64)" {
		t.Fatalf("got %q", got)
	}
}
