package usecase

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type memFacts struct{ facts []model.CodeFact }

func (m *memFacts) Add(_ context.Context, _ string, fs []model.CodeFact, max int) error {
	m.facts = append(m.facts, fs...)
	if len(m.facts) > max {
		m.facts = m.facts[len(m.facts)-max:]
	}
	return nil
}
func (m *memFacts) List(context.Context, string) ([]model.CodeFact, error) { return m.facts, nil }

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestParseCodeFact(t *testing.T) {
	cases := []struct {
		in   string
		want model.CodeFact
		ok   bool
	}{
		{"a/b.go:12 — BodyLimit is 1MB", model.CodeFact{Path: "a/b.go", Line: 12, Claim: "BodyLimit is 1MB"}, true},
		{"a/b.go - no line", model.CodeFact{Path: "a/b.go", Claim: "no line"}, true},
		{"no separator here", model.CodeFact{}, false},
		{"a.go:x — bad line", model.CodeFact{}, false},
		{"two words — claim", model.CodeFact{}, false},
	}
	for _, c := range cases {
		got, ok := parseCodeFact(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseCodeFact(%q) = %+v, %v", c.in, got, ok)
		}
	}
	long, _ := parseCodeFact("a.go:1 — " + strings.Repeat("x", 500))
	if n := len([]rune(long.Claim)) + len([]rune("a.go:1 — ")); n > maxCodeFactChars {
		t.Errorf("fact is %d chars, cap %d", n, maxCodeFactChars)
	}
}

func gitWorkspace(t *testing.T, data, woID string) (ws, base string) {
	t.Helper()
	ws = filepath.Join(data, "workspaces", woID)
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, ws, "init", "-q")
	for _, n := range []string{"keep.go", "change.go"} {
		if err := os.WriteFile(filepath.Join(ws, n), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, ws, "add", ".")
	gitIn(t, ws, "commit", "-qm", "base")
	return ws, gitIn(t, ws, "rev-parse", "HEAD")
}

func TestCodeFactsSectionHidesStaleFacts(t *testing.T) {
	data := t.TempDir()
	ws, base := gitWorkspace(t, data, "wo1")
	mem := &memFacts{}
	f := &Factory{CodeFacts: mem, DataDir: data}
	ctx := context.Background()
	wo := &model.WorkOrder{ID: "wo1", KBBaseSHA: base}
	f.recordCodeFacts(ctx, wo, model.AgentForeman, []string{"keep.go:1 — stays valid", "change.go:1 — will go stale", "junk"})
	if len(mem.facts) != 2 || mem.facts[0].SHA != base || mem.facts[0].By != "foreman" {
		t.Fatalf("recorded = %+v", mem.facts)
	}

	got := f.codeFactsText(ctx, wo)
	if !strings.Contains(got, "Code facts (verified at "+base[:8]) || !strings.Contains(got, "keep.go:1 — stays valid") || !strings.Contains(got, "change.go:1") {
		t.Fatalf("fresh facts missing:\n%s", got)
	}

	if err := os.WriteFile(filepath.Join(ws, "change.go"), []byte("package y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, ws, "commit", "-qam", "edit")
	got = f.codeFactsText(ctx, wo)
	if !strings.Contains(got, "keep.go:1") || strings.Contains(got, "change.go") {
		t.Fatalf("only the untouched fact may remain:\n%s", got)
	}

	f.recordCodeFacts(ctx, &model.WorkOrder{ID: "wo1", KBBaseSHA: "deadbeef"}, model.AgentArchitect, []string{"keep.go:2 — unknown sha"})
	if strings.Contains(f.codeFactsText(ctx, wo), "unknown sha") {
		t.Fatal("a sha git cannot resolve must count as stale")
	}
}

func TestRecordCodeFactsCapsAtForty(t *testing.T) {
	mem := &memFacts{}
	f := &Factory{CodeFacts: mem, DataDir: t.TempDir()}
	var items []string
	for i := 0; i < 50; i++ {
		items = append(items, "f"+strings.Repeat("a", i)+".go — c")
	}
	f.recordCodeFacts(context.Background(), &model.WorkOrder{ID: "wo1", KBBaseSHA: "abc"}, model.AgentForeman, items)
	if len(mem.facts) != maxCodeFacts {
		t.Fatalf("kept %d facts, want %d", len(mem.facts), maxCodeFacts)
	}
}

func TestAgentPromptsCarryCodeFacts(t *testing.T) {
	r := newRig(t)
	ws, base := gitWorkspace(t, r.f.DataDir, r.wo.ID)
	_ = ws
	r.f.CodeFacts = &store.CodeFactRepo{DB: r.db}
	ctx := context.Background()
	wo := *r.wo
	wo.KBBaseSHA = base
	r.f.recordCodeFacts(ctx, &wo, model.AgentForeman, []string{"keep.go:1 — the sentinel claim"})
	var rep foremanAnswerReply
	if _, _, err := r.f.runStructured(ctx, &wo, model.AgentForeman, "hello", nil, "", &rep, runOpts{}); err != nil && !strings.Contains(err.Error(), "invalid") {
		t.Fatal(err)
	}
	ps := r.h.prompts["foreman"]
	if len(ps) == 0 || !strings.Contains(ps[0], "Code facts (verified at "+base[:8]) || !strings.Contains(ps[0], "the sentinel claim") {
		t.Fatalf("foreman prompt lacks the code facts section: %v", ps)
	}
}
