package usecase

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
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
