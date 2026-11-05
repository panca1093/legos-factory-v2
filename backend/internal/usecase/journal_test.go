package usecase

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func newJournal(t *testing.T) (*JournalStore, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projects := &store.ProjectRepo{DB: db}
	for _, id := range []string{"p1", "p2"} {
		if err := projects.Create(context.Background(), &model.Project{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := kb.OpenBundle(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	know := &KB{Bundle: bundle, Idx: &store.KBRepo{DB: db}, Emb: kb.HashEmbedder{}, Log: slog.Default()}
	return &JournalStore{KB: know, Proj: projects}, db
}

func commitCount(t *testing.T, root string) int {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "rev-list", "--count", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func TestJournalRoundTripTrustAndIsolation(t *testing.T) {
	j, _ := newJournal(t)
	ctx := context.Background()
	recs := []*model.KnowledgeRecord{
		{ProjectID: "p1", WOID: "wo1", Kind: "plan_approved", Trust: model.TrustVerified, Actor: "policy", Title: "Plan approved", Body: "Rounding uses integer rupiah", SourceRefs: []string{"run:1"}, CommitSHA: "abc", RepoURL: "/r/p1", WorkspaceID: "ws"},
		{ProjectID: "p1", WOID: "wo1", Kind: "clarification", Trust: model.TrustHumanDecision, Actor: "human", Title: "Clarification: tax", Body: "Tax is inclusive"},
		{ProjectID: "p1", WOID: "wo2", Kind: "architect_review", Trust: model.TrustAIInterpretation, Actor: "architect", Title: "Architect plan", Body: "two tasks"},
		{ProjectID: "p2", WOID: "wo3", Kind: "plan_approved", Trust: model.TrustVerified, Actor: "policy", Title: "Other project plan", Body: "secret rounding"},
	}
	for _, r := range recs {
		if err := j.Insert(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := j.Get(ctx, recs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != "p1" || got.WOID != "wo1" || got.Kind != "plan_approved" || got.Trust != model.TrustVerified || got.Actor != "policy" ||
		got.Title != "Plan approved" || got.Body != "Rounding uses integer rupiah" || got.CommitSHA != "abc" || got.RepoURL != "/r/p1" ||
		got.WorkspaceID != "ws" || len(got.SourceRefs) != 1 || got.SourceRefs[0] != "run:1" || got.CreatedAt.IsZero() {
		t.Fatalf("round trip: %+v", got)
	}
	for _, c := range []struct {
		r    *model.KnowledgeRecord
		want model.Trust
		key  string
		by   string
	}{
		{recs[0], model.TrustVerified, "verified", "process:legos-factory"},
		{recs[1], model.TrustHumanDecision, "verified", "human:human"},
		{recs[2], model.TrustAIInterpretation, "generated", "architect"},
	} {
		r, err := j.Get(ctx, c.r.ID)
		if err != nil || r.Trust != c.want {
			t.Fatalf("trust of %s: %+v %v", c.r.Kind, r, err)
		}
		front, _, err := j.KB.Bundle.Read(journalPath("p1", c.r.ID))
		if err != nil {
			t.Fatal(err)
		}
		m, _ := front[c.key].(map[string]any)
		if front["type"] != model.KBTypeJournalEntry || m["by"] != c.by {
			t.Fatalf("%s frontmatter: %v", c.r.Kind, front)
		}
		if c.key == "generated" && front["verified"] != nil {
			t.Fatal("an AI interpretation must not be verified")
		}
		if d, _ := j.KB.Idx.Get(ctx, "factory:p1:"+journalPath("p1", c.r.ID)); d == nil || (c.want == model.TrustAIInterpretation) != (d.Trust == model.KBTrustUnverified) {
			t.Fatalf("index trust of %s: %+v", c.r.Kind, d)
		}
	}
	if _, err := j.Get(ctx, "kn_missing"); err != model.ErrKnowledgeNotFound {
		t.Fatalf("missing: %v", err)
	}
	if _, err := j.Get(ctx, "../x"); err != model.ErrKnowledgeNotFound {
		t.Fatalf("unsafe id: %v", err)
	}

	all, _ := j.Search(ctx, model.KnowledgeQuery{})
	if len(all) != 4 {
		t.Fatalf("unrestricted search: %d", len(all))
	}
	p1, _ := j.Search(ctx, model.KnowledgeQuery{ProjectIDs: []string{"p1"}})
	if len(p1) != 3 {
		t.Fatalf("p1 only: %d", len(p1))
	}
	for _, r := range p1 {
		if r.ProjectID != "p1" {
			t.Fatalf("leak: %+v", r)
		}
	}
	if none, err := j.Search(ctx, model.KnowledgeQuery{ProjectIDs: []string{}}); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("empty scope must match nothing: %v %v", none, err)
	}
	if wo, _ := j.Search(ctx, model.KnowledgeQuery{WOID: "wo1"}); len(wo) != 2 {
		t.Fatalf("wo filter: %d", len(wo))
	}
	if hd, _ := j.Search(ctx, model.KnowledgeQuery{Trust: model.TrustHumanDecision}); len(hd) != 1 || hd[0].Kind != "clarification" {
		t.Fatalf("trust filter: %+v", hd)
	}
	txt, _ := j.Search(ctx, model.KnowledgeQuery{ProjectIDs: []string{"p1"}, Text: "rounding rupiah", Limit: 1})
	if len(txt) != 1 || txt[0].Title != "Plan approved" {
		t.Fatalf("text search: %+v", txt)
	}
	for _, r := range txt {
		if r.Body == "" {
			t.Fatal("search must return the body from the file")
		}
	}
}

func TestJournalExportOnce(t *testing.T) {
	j, db := newJournal(t)
	ctx := context.Background()
	legacy := &store.KnowledgeRepo{DB: db}
	var ids []string
	for _, title := range []string{"first", "second", "third"} {
		k := &model.KnowledgeRecord{WorkspaceID: model.DefaultWorkspaceID, ProjectID: "p1", Kind: "plan_approved", Trust: model.TrustHumanDecision, Actor: "human", Title: title, Body: "body " + title}
		if err := legacy.Insert(ctx, k); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, k.ID)
	}
	before := commitCount(t, j.KB.Bundle.Root())
	n, err := j.Export(ctx, legacy)
	if err != nil || n != 3 {
		t.Fatalf("export: %d %v", n, err)
	}
	if after := commitCount(t, j.KB.Bundle.Root()); after != before+1 {
		t.Fatalf("export must be one commit, went %d → %d", before, after)
	}
	for i, id := range ids {
		r, err := j.Get(ctx, id)
		if err != nil || r.Body != "body "+[]string{"first", "second", "third"}[i] || r.Trust != model.TrustHumanDecision {
			t.Fatalf("exported %s: %+v %v", id, r, err)
		}
	}
	if found, _ := j.Search(ctx, model.KnowledgeQuery{ProjectIDs: []string{"p1"}, Text: "second"}); len(found) == 0 || found[0].Title != "second" {
		t.Fatalf("exported rows must be indexed: %+v", found)
	}
	if n, err := j.Export(ctx, legacy); err != nil || n != 0 {
		t.Fatalf("second export must be a no-op: %d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(j.KB.Bundle.Root(), "projects", "p1", "journal", ids[0]+".md")); err != nil {
		t.Fatal(err)
	}
}
