package usecase

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestDocSlugAndCode(t *testing.T) {
	for _, c := range []struct{ title, want string }{
		{"Ganti jurnal verifikasi dengan log", "ganti-jurnal-verifikasi"},
		{"Café — déjà vu à Zürich", "cafe-deja-vu"},
		{"Fix: the (very) odd bug!", "fix-the-very"},
		{"  --  ", "untitled"},
		{"", "untitled"},
		{"日本語 only", "only"},
		{"Supercalifragilisticexpialidocious word", "supercalifragili-word"},
		{"A1 B2_C3", "a1-b2-c3"},
	} {
		if got := docSlug(c.title); got != c.want {
			t.Errorf("docSlug(%q) = %q, want %q", c.title, got, c.want)
		}
	}
	for id, want := range map[string]string{"wo_c6680cdd-92d": "cdd92d", "wo_ab-cd-ef-12-34": "ef1234", "abc": "abc"} {
		if got := docCode(id); got != want {
			t.Errorf("docCode(%q) = %q, want %q", id, got, want)
		}
	}
	at := time.Date(2026, 11, 6, 23, 30, 0, 0, time.FixedZone("WIB", 7*3600))
	if got := workDocPrefix(at, "Ganti jurnal verifikasi", "wo_c6680cdd-92d"); got != "2026-11-06-ganti-jurnal-verifikasi-cdd92d" {
		t.Fatalf("prefix %q", got)
	}
}

func (r *rig) workDoc(t *testing.T, typ string) (model.KBDoc, map[string]any, string) {
	t.Helper()
	docs, err := r.know.Idx.List(context.Background(), model.KBQuery{ProjectID: "p1", Sources: []string{model.KBSourceFactory}, Types: []string{typ}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var mine []model.KBDoc
	for _, d := range docs {
		if d.Meta["origin_id"] == r.wo.ID {
			mine = append(mine, d)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("want exactly one %s document for %s, got %d", typ, r.wo.ID, len(mine))
	}
	front, body, err := r.know.Bundle.Read(mine[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	return mine[0], front, body
}

func (r *rig) hasWorkDoc(t *testing.T, typ string) bool {
	t.Helper()
	docs, err := r.know.Idx.List(context.Background(), model.KBQuery{ProjectID: "p1", Sources: []string{model.KBSourceFactory}, Types: []string{typ}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		if d.Meta["origin_id"] == r.wo.ID {
			return true
		}
	}
	return false
}

func requireFront(t *testing.T, front map[string]any, typ string, r *rig) {
	t.Helper()
	if _, has := front["status"]; has {
		t.Fatalf("%s must not carry a status: %v", typ, front)
	}
	for k, want := range map[string]any{"type": typ, "origin": "work_order", "origin_id": r.wo.ID, "project": "p1"} {
		if front[k] != want {
			t.Fatalf("%s frontmatter %s = %v, want %v", typ, k, front[k], want)
		}
	}
	title, _ := front["title"].(string)
	desc, _ := front["description"].(string)
	if !strings.HasSuffix(title, " — "+r.reload(t).Title) || !strings.Contains(desc, r.reload(t).Title) {
		t.Fatalf("%s title/description must carry the work-order title: %q / %q", typ, title, desc)
	}
	if created, _ := front["created_at"].(string); !strings.HasSuffix(created, "+07:00") {
		t.Fatalf("%s created_at %q not in Asia/Jakarta", typ, created)
	}
}

func TestWorkOrderDocumentsAreWrittenAtTheirStages(t *testing.T) {
	ctx := context.Background()
	r := sessionRig(t, &sessHarness{answer: answerJSON})
	if err := r.wos.UpdateFields(ctx, r.wo.ID, map[string]any{"title": "Ganti jurnal verifikasi dengan log"}); err != nil {
		t.Fatal(err)
	}
	r.f.Judge = &fakeJudge{}
	r.f.Bus = &liveBus{}

	r.plan(t)
	brief, front, body := r.workDoc(t, model.KBTypeBrief)
	requireFront(t, front, model.KBTypeBrief, r)
	if !strings.Contains(brief.Path, "/brief/") || !strings.HasSuffix(brief.Path, "-ganti-jurnal-verifikasi-"+docCode(r.wo.ID)+".md") {
		t.Fatalf("brief path %s", brief.Path)
	}
	for _, h := range []string{"## Request", "## Context", "## Constraints", "## Questions and answers"} {
		if !strings.Contains(body, h) {
			t.Fatalf("brief lacks %s:\n%s", h, body)
		}
	}
	if !strings.Contains(body, "Round money in integer rupiah") || !strings.Contains(body, "- keep API") || strings.Contains(body, "Related:") {
		t.Fatalf("brief body:\n%s", body)
	}
	if r.hasWorkDoc(t, model.KBTypeSpec) || r.hasWorkDoc(t, model.KBTypeTaskDAG) || r.hasWorkDoc(t, model.KBTypeTaskSummary) {
		t.Fatal("only the brief exists before the plan is approved")
	}

	wo := r.review(t)
	if wo.State != model.StateQueued {
		t.Fatalf("state %s", wo.State)
	}
	spec, front, body := r.workDoc(t, model.KBTypeSpec)
	requireFront(t, front, model.KBTypeSpec, r)
	base := strings.TrimSuffix(filepath.Base(brief.Path), ".md")
	if spec.Path != "projects/p1/spec/"+base+".md" {
		t.Fatalf("spec shares the brief prefix: %s vs %s", spec.Path, brief.Path)
	}
	if !strings.Contains(body, "[Brief](../brief/"+base+".md)") || !strings.Contains(body, "## Approach\n\nsp") {
		t.Fatalf("spec body:\n%s", body)
	}
	dag, front, body := r.workDoc(t, model.KBTypeTaskDAG)
	requireFront(t, front, model.KBTypeTaskDAG, r)
	if front["approved_by"] != "factory" || dag.Path != "projects/p1/task/"+base+"-dag.md" {
		t.Fatalf("dag %s %v", dag.Path, front)
	}
	for _, want := range []string{"[Spec](../spec/" + base + ".md)", "[Brief](../brief/" + base + ".md)", "## Graph\n\n```\n1\n```", "### 1. Round money", "Role: builder · Depends on: none · Scope: not set"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dag body lacks %q:\n%s", want, body)
		}
	}
	if r.hasWorkDoc(t, model.KBTypeTaskSummary) {
		t.Fatal("no summary before the work order ends")
	}

	if err := r.wos.UpdateFields(ctx, r.wo.ID, map[string]any{"title": "A completely different title"}); err != nil {
		t.Fatal(err)
	}
	r.f.completeWO(ctx, r.reload(t), "READY")
	sum, front, body := r.workDoc(t, model.KBTypeTaskSummary)
	if !strings.Contains(body, "Ganti") && !strings.Contains(body, "A completely different title") {
		t.Fatalf("written summary lacks the goal:\n%s", body)
	}
	if sum.Path != "projects/p1/task/"+base+"-summary.md" {
		t.Fatalf("a changed title must not rename files: %s", sum.Path)
	}
	if _, has := front["status"]; has || front["origin_id"] != r.wo.ID || front["type"] != model.KBTypeTaskSummary {
		t.Fatalf("summary front: %v", front)
	}
	for _, want := range []string{"[Brief](../brief/" + base + ".md)", "[Spec](../spec/" + base + ".md)", "[Task DAG](" + base + "-dag.md)", "## Goal", "## Main changes"} {
		if !strings.Contains(body, want) {
			t.Fatalf("summary body lacks %q:\n%s", want, body)
		}
	}
	for _, typ := range []string{model.KBTypeBrief, model.KBTypeSpec, model.KBTypeTaskDAG, model.KBTypeTaskSummary} {
		docs, _ := r.know.Idx.List(ctx, model.KBQuery{ProjectID: "p1", Sources: []string{model.KBSourceFactory}, Types: []string{typ}, Limit: 50})
		if len(docs) != 1 || !docs[0].ReadOnly {
			t.Fatalf("%s: want one read-only document, got %+v", typ, docs)
		}
	}
	log, _ := os.ReadFile(filepath.Join(r.know.Bundle.Root(), "projects", "p1", "log.md"))
	if !strings.Contains(string(log), "**work_order_completed** (cataloger) — Task summary: A completely different title (READY) — summary document: "+sum.Path) {
		t.Fatalf("log trail missing:\n%s", log)
	}

	assertNoLint(t, r.know.Bundle.Root())
}

func assertNoLint(t *testing.T, root string) {
	t.Helper()
	vs, err := kb.Lint(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("lint violations: %+v", vs)
	}
}

func TestCancelledWorkOrderStillGetsASummary(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateImplementing); err != nil {
		t.Fatal(err)
	}
	if err := r.f.Cancel(ctx, r.wo.ID); err != nil {
		t.Fatal(err)
	}
	sum, front, body := r.workDoc(t, model.KBTypeTaskSummary)
	if !strings.HasSuffix(sum.Path, "-summary.md") || front["origin"] != "work_order" || !strings.Contains(body, "Outcome: CANCELLED") {
		t.Fatalf("summary %s\n%v\n%s", sum.Path, front, body)
	}
	if strings.Contains(body, "Related:") {
		t.Fatalf("a work order that never reached its brief links nothing:\n%s", body)
	}
	assertNoLint(t, r.know.Bundle.Root())
}

func TestPlanRevisionRewritesSpecAndDAG(t *testing.T) {
	ctx := context.Background()
	second := `{"decision":"APPROVE","summary":"s2","spec":"second approach","tasks":[{"id":"t1","title":"First","objective":"o1","role":"builder","scope":["a/"]},{"id":"t2","title":"Second","objective":"o2","role":"builder","deps":["t1"]}]}`
	h := &sessHarness{answer: answerJSON, architect: []string{planJSON, second}}
	r := sessionRig(t, h)
	r.f.Judge = &fakeJudge{over: map[string]float64{"touches_schema": 0.9}}
	r.f.Bus = &liveBus{}

	r.plan(t)
	r.review(t)
	if r.hasWorkDoc(t, model.KBTypeSpec) {
		t.Fatal("an escalated plan is not approved yet")
	}
	if err := r.f.RevisePlan(ctx, r.wo.ID, "split it"); err != nil {
		t.Fatal(err)
	}
	r.plan(t)
	r.review(t)
	if err := r.f.ApprovePlan(ctx, r.wo.ID); err != nil {
		t.Fatal(err)
	}
	_, front, body := r.workDoc(t, model.KBTypeSpec)
	if !strings.Contains(body, "second approach") || front["type"] != model.KBTypeSpec {
		t.Fatalf("spec shows the approved result:\n%s", body)
	}
	_, front, body = r.workDoc(t, model.KBTypeTaskDAG)
	for _, want := range []string{"1\n2 ← 1\n", "### 2. Second", "Role: builder · Depends on: 1 · Scope: not set", "Scope: a/"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dag lacks %q:\n%s", want, body)
		}
	}
	if front["approved_by"] != "human" {
		t.Fatalf("approved_by %v", front["approved_by"])
	}

	files, _ := filepath.Glob(filepath.Join(r.know.Bundle.Root(), "projects", "p1", "spec", "2026-*.md"))
	if len(files) != 1 {
		t.Fatalf("rewrite must reuse the file: %v", files)
	}
	assertNoLint(t, r.know.Bundle.Root())
}

func TestBriefCarriesClarificationsOnApproval(t *testing.T) {
	r := reviewRig(t, &fakeJudge{})
	ctx := context.Background()
	if err := r.wos.UpdateFields(ctx, r.wo.ID, map[string]any{"clarif_log": `[{"question":"Round half up or down?","answer":"Half up."}]`}); err != nil {
		t.Fatal(err)
	}
	r.review(t)
	_, _, body := r.workDoc(t, model.KBTypeBrief)
	if !strings.Contains(body, "Q: Round half up or down?\nA: Half up.") {
		t.Fatalf("brief:\n%s", body)
	}
}

func TestWorkDocsStayCleanUnderLintWithAllFour(t *testing.T) {
	r := reviewRig(t, &fakeJudge{})
	r.review(t)
	r.f.completeWO(context.Background(), r.reload(t), "READY")
	assertNoLint(t, r.know.Bundle.Root())
	for _, typ := range []string{model.KBTypeBrief, model.KBTypeSpec, model.KBTypeTaskDAG, model.KBTypeTaskSummary} {
		r.workDoc(t, typ)
	}
}

const (
	leakPassword = "password=hunter2secret"
	leakToken    = "sk-abcdefghijklmnop1234567890"
)

func hostileEnv(t *testing.T) (summaryEnv, *model.WorkOrder, []model.Task) {
	t.Helper()
	env := newSummaryEnv(t, []string{"a.go"}, true)
	ctx := context.Background()
	title := "## Injected\n---\nstatus: draft\n[x](../../../etc/passwd) `tick` " + leakPassword + " " + leakToken
	obj := "## Fake heading\nuse " + leakPassword + " and " + leakToken + "\n---\n[[wiki]] [a](../../x.md) <file:///Users/x/y> a[0] ok\n```go\nunclosed"
	acc := "- [ ] one " + leakToken + "\n[ref]: /Users/x/secret.md\n# Top"
	wo := env.newWO(t, "wo_hostile", title, obj, acc, model.StateArchitectReview)
	err := env.wos.UpdateFields(ctx, wo.ID, map[string]any{
		"brief":      "Do it " + leakToken + "\n\nConstraints:\n- ## keep [api](x.md)\n\n~~~\nopen fence",
		"spec":       "# H1\nbody " + leakPassword + "\n## H2\n```\n# comment inside fence\n```\n### H3",
		"clarif_log": `[{"question":"## Q [x](y.md)?","answer":"A ` + leakToken + `"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env.seedFindings(t, wo.ID, 1, "finding "+leakPassword+" "+leakToken+" [z](q.md)")
	tasks := []model.Task{
		{ID: "t1", WOID: wo.ID, Seq: 0, Title: "## T1 " + leakToken, Objective: "# obj\n" + leakPassword, Role: "builder\n## x", Scope: []string{"[a](b.md)", "/Users/x " + leakToken}},
		{ID: "t2", WOID: wo.ID, Seq: 1, Title: "Two", Objective: "o", Role: "builder", Deps: []string{"t1"}},
	}
	return env, mustGet(t, env, wo.ID), tasks
}

func writeHostileDocs(t *testing.T) (summaryEnv, *model.WorkOrder) {
	env, wo, tasks := hostileEnv(t)
	ctx := context.Background()
	env.f.writeBriefDoc(ctx, wo)
	env.f.writePlanDocs(ctx, wo, tasks, "human")
	env.f.completeWO(ctx, wo, "READY")
	return env, wo
}

func TestWorkDocsNeverWriteSecrets(t *testing.T) {
	env, _ := writeHostileDocs(t)
	n := 0
	filepath.WalkDir(env.bundle.Root(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		n++
		data, _ := os.ReadFile(p)
		for _, leak := range []string{"hunter2secret", "abcdefghijklmnop1234567890"} {
			if strings.Contains(string(data), leak) {
				t.Errorf("%s leaks %q:\n%s", p, leak, data)
			}
		}
		return nil
	})
	if n < 4 {
		t.Fatalf("expected the written documents, saw %d files", n)
	}
}

func fenceLines(body string) (n int) {
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			n++
		}
	}
	return n
}

func fixedHeadings(body string) (h2 []string, other int) {
	fence := false
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			fence = !fence
			continue
		}
		switch {
		case fence:
		case strings.HasPrefix(l, "## "):
			h2 = append(h2, strings.TrimPrefix(l, "## "))
		case strings.HasPrefix(l, "# ") || strings.HasPrefix(l, "#### ") || (strings.HasPrefix(l, "### ") && !regexp.MustCompile(`^### \d+\. `).MatchString(l)):
			other++
		}
	}
	return h2, other
}

func TestHostileInputKeepsDocumentsLintCleanAndShaped(t *testing.T) {
	env, wo := writeHostileDocs(t)
	assertNoLint(t, env.bundle.Root())
	want := map[string][]string{
		model.KBTypeBrief:       {"Request", "Context", "Constraints", "Questions and answers"},
		model.KBTypeSpec:        {"Objective", "Acceptance criteria", "Out of scope", "Approach"},
		model.KBTypeTaskDAG:     {"Graph", "Tasks"},
		model.KBTypeTaskSummary: {"Goal", "Final verification", "Open items and follow-up", "Key decisions and rationale", "Main changes"},
	}
	for typ, headings := range want {
		docs, err := env.f.KB.Idx.List(context.Background(), model.KBQuery{ProjectID: "p1", Sources: []string{model.KBSourceFactory}, Types: []string{typ}, OriginID: wo.ID})
		if err != nil || len(docs) != 1 {
			t.Fatalf("%s: %v %v", typ, docs, err)
		}
		front, body, err := env.bundle.Read(docs[0].Path)
		if err != nil {
			t.Fatal(err)
		}
		if front["type"] != typ || front["origin_id"] != wo.ID || front["status"] != nil || front["origin"] != "work_order" {
			t.Fatalf("%s frontmatter: %v", typ, front)
		}
		h2, other := fixedHeadings(body)
		if fmt.Sprint(h2) != fmt.Sprint(headings) || other != 0 {
			t.Fatalf("%s headings %v (+%d stray), want %v:\n%s", typ, h2, other, headings, body)
		}
		if fenceLines(body)%2 != 0 {
			t.Fatalf("%s has an unbalanced fence:\n%s", typ, body)
		}
	}
}

func TestPrefixLookupSurvivesManyUnrelatedDocs(t *testing.T) {
	env := newSummaryEnv(t, nil, true)
	ctx := context.Background()
	wo := env.newWO(t, "wo_many", "Original title", "obj", "acc", model.StateArchitectReview)
	for i := 0; i < workDocLimit+20; i++ {
		rel := fmt.Sprintf("projects/p1/brief/2000-%04d.md", i)
		raw := fmt.Sprintf("---\ntype: Brief\ntitle: other %d\norigin: work_order\norigin_id: wo_other%d\n---\n\nbody\n", i, i)
		p, err := kb.Parse(model.KBSourceFactory, "p1", rel, []byte(raw), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := env.f.KB.Idx.(*store.KBRepo).Upsert(ctx, p, make([]float32, 512)); err != nil {
			t.Fatal(err)
		}
	}
	env.f.writeBriefDoc(ctx, wo)
	wo.Title = "Renamed entirely"
	env.f.writePlanDocs(ctx, wo, []model.Task{{ID: "t1", Title: "T", Objective: "o"}}, "factory")
	docs, _ := env.f.KB.Idx.List(ctx, model.KBQuery{ProjectID: "p1", OriginID: wo.ID, Limit: 10})
	if len(docs) != 3 {
		t.Fatalf("want brief, spec and dag, got %+v", docs)
	}
	var brief string
	for _, d := range docs {
		if d.Type == model.KBTypeBrief {
			brief = strings.TrimSuffix(filepath.Base(d.Path), ".md")
		}
	}
	for _, d := range docs {
		if !strings.Contains(d.Path, brief) {
			t.Fatalf("%s must reuse the brief prefix %s", d.Path, brief)
		}
	}
	_, body, _ := env.bundle.Read("projects/p1/spec/" + brief + ".md")
	if !strings.Contains(body, "[Brief](../brief/"+brief+".md)") {
		t.Fatalf("spec must link its brief:\n%s", body)
	}
}
