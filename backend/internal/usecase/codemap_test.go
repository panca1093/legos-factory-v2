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
	if !strings.Contains(body, "- keep.go:1 @"+base+" — first claim") || strings.Contains(body, "change.go") {
		t.Fatalf("stale fact must be skipped:\n%s", body)
	}
	if strings.Contains(body, "gone.go") {
		t.Fatalf("a fact on a missing file must be dropped:\n%s", body)
	}

	mem.facts = nil
	r.f.recordCodeFacts(ctx, r.wo, model.AgentArchitect, []string{"keep.go:1 — second claim", "keep.go — whole file"})
	r.f.portCodeMap(ctx, r.wo)
	_, body = readCodeMap(t, r)
	if strings.Contains(body, "first claim") || !strings.Contains(body, "- keep.go:1 @"+base+" — second claim") || !strings.Contains(body, "- keep.go @"+base+" — whole file") {
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
}

func TestSpecDecisionsFeedSummary(t *testing.T) {
	spec := "## Behaviour rules\n1. x\n\n## Decisions\n\n| # | Decision | Why | Rejected alternative |\n|---|---|---|---|\n| 1 | Use integer rupiah | no float drift | float64 |\n\n## Out of scope\n| 9 | not | a | decision |\n"
	got := specDecisions(spec)
	if len(got) != 1 || got[0] != "- Use integer rupiah — no float drift (rejected: float64)" {
		t.Fatalf("got %q", got)
	}
}

func TestSpecDecisionsEscapedPipeNumberedHeadingAndFence(t *testing.T) {
	spec := "## 5. Decisions\n| # | Decision | Why | Rejected alternative |\n|---|---|---|---|\n| 1 | a \\| b | because | c |\n```\n| 2 | fenced | no | no |\n```\n"
	got := specDecisions(spec)
	if len(got) != 1 || got[0] != "- a | b — because (rejected: c)" {
		t.Fatalf("got %q", got)
	}
}

func TestSeedCodeFactsFromCodeMap(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	mem := &memFacts{}
	r.f.CodeFacts = mem
	mem.facts = []model.CodeFact{{Path: "a.go", Line: 1, Claim: "from map", SHA: "abc"}, {Path: "b.go", Line: 2, Claim: "other", SHA: "abc"}}
	r.f.portCodeMap(ctx, r.wo)
	if _, body := readCodeMap(t, r); !strings.Contains(body, "- a.go:1 @abc — from map") {
		t.Fatalf("map line lacks its sha:\n%s", body)
	}

	mem.facts = []model.CodeFact{{Path: "a.go", Line: 1, Claim: "own fact", SHA: "def", By: "builder"}}
	r.f.seedCodeFacts(ctx, r.wo)
	if len(mem.facts) != 2 || mem.facts[0].Claim != "own fact" || mem.facts[1].Path != "b.go" || mem.facts[1].SHA != "abc" || mem.facts[1].By != codeMapSeeder {
		t.Fatalf("seed must add only what the order lacks, with the map's sha: %+v", mem.facts)
	}

	opts := runOpts{Audit: map[string]any{"kb_docs": []string{"x"}}}
	r.f.auditCodeMap(ctx, r.wo, "no seeded line here", &opts)
	if len(opts.Audit["kb_docs"].([]string)) != 1 {
		t.Fatal("no seeded fact shown, no audit entry")
	}
	r.f.auditCodeMap(ctx, r.wo, "- b.go:2 — other\n", &opts)
	if ids := opts.Audit["kb_docs"].([]string); len(ids) != 2 || !strings.HasSuffix(ids[1], codeMapPath("p1")) {
		t.Fatalf("audit: %v", opts.Audit)
	}
}

func TestPortCodeMapDropsMapLinesOnEditedFiles(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	mem := &memFacts{}
	r.f.CodeFacts = mem
	ws, base := gitWorkspace(t, r.f.DataDir, r.wo.ID)
	r.wo.KBBaseSHA = base
	mem.facts = []model.CodeFact{{Path: "change.go", Line: 1, Claim: "old", SHA: "zzz"}, {Path: "keep.go", Line: 1, Claim: "kept", SHA: "zzz"}}
	r.f.portCodeMap(ctx, &model.WorkOrder{ID: "other", ProjectID: "p1"}) // no workspace: stores both
	if err := os.WriteFile(filepath.Join(ws, "change.go"), []byte("package z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mem.facts = []model.CodeFact{{Path: "keep.go", Line: 5, Claim: "new", SHA: base}}
	r.f.portCodeMap(ctx, r.wo)
	_, body := readCodeMap(t, r)
	if strings.Contains(body, "change.go") || !strings.Contains(body, "keep.go:1 @zzz") || !strings.Contains(body, "keep.go:5 @"+base) {
		t.Fatalf("map lines on edited files must go:\n%s", body)
	}
}
