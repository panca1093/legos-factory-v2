package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func TestKBDocsFactoryAllowListAndReadOnly(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/factory.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	projRepo := &store.ProjectRepo{DB: db}
	if err := projRepo.Create(ctx, &model.Project{ID: "p1", Name: "p1"}); err != nil {
		t.Fatal(err)
	}
	repo := &store.KBRepo{DB: db}
	put := func(source, path, typ string, meta map[string]any) {
		d := model.KBParsed{KBDoc: model.KBDoc{ID: source + ":p1:" + path, Source: source, ProjectID: "p1", Path: path, CommitSHA: "c", ContentHash: path,
			Type: typ, Status: model.KBStatusStable, Trust: model.KBTrustUnverified, Title: path, Meta: meta}, Body: "zebra"}
		if err := repo.Upsert(ctx, d, kb.HashEmbedder{}.Embed("zebra")); err != nil {
			t.Fatal(err)
		}
	}
	wo := map[string]any{"origin": "work_order"}
	put("factory", "brief/a.md", model.KBTypeBrief, wo)
	put("factory", "task/a-dag.md", model.KBTypeTaskDAG, wo)
	put("factory", "notes/n.md", model.KBTypeNote, map[string]any{"origin": "manual"})
	put("factory", "playbooks/p.md", model.KBTypePlaybook, nil)
	put("factory", "journal/j.md", model.KBTypeJournalEntry, nil)
	put("factory", "misc/x.md", "Whatever", nil)
	put("repo", "docs/r.md", "Arbitrary", nil)

	bundle, err := kb.OpenBundle(t.TempDir()+"/kb", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bundle.WriteMany([]kb.File{
		{Path: "journal/j.md", Front: map[string]any{"type": model.KBTypeJournalEntry}, Body: "# J\n"},
		{Path: "brief/a.md", Front: map[string]any{"type": model.KBTypeBrief, "origin": "work_order"}, Body: "# B\n"},
	}, "seed"); err != nil {
		t.Fatal(err)
	}
	s := &KBDocs{KB: &KB{Bundle: bundle, Idx: kb.Index(repo), Emb: kb.HashEmbedder{}}, Proj: projRepo,
		Projects: &Projects{Store: projRepo, Members: &store.MemberRepo{DB: db}, Users: &store.UserRepo{DB: db}}}
	u := &model.User{ID: "u1", Role: model.RoleOwner}
	want := strings.Fields("factory:p1:brief/a.md factory:p1:notes/n.md factory:p1:playbooks/p.md factory:p1:task/a-dag.md repo:p1:docs/r.md")
	for _, q := range []string{"", "zebra"} {
		docs, err := s.List(ctx, u, KBListParams{ProjectID: "p1", Q: q})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, d := range docs {
			got[d.ID] = true
		}
		if len(got) != len(want) {
			t.Fatalf("q=%q got %v", q, got)
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("q=%q missing %s in %v", q, id, got)
			}
		}
	}
	docs, _ := s.List(ctx, u, KBListParams{ProjectID: "p1", Type: model.KBTypeJournalEntry})
	if len(docs) != 0 {
		t.Fatalf("type filter must not bypass allow-list: %v", docs)
	}
	docs, _ = s.List(ctx, u, KBListParams{ProjectID: "p1", Type: model.KBTypeBrief})
	if len(docs) != 1 || !docs[0].ReadOnly {
		t.Fatalf("brief: %+v", docs)
	}

	if det, err := s.Detail(ctx, u, "factory:p1:journal/j.md"); err != nil || det.Doc.ReadOnly {
		t.Fatalf("deep link must still resolve: %+v %v", det, err)
	}
	det, err := s.Detail(ctx, u, "factory:p1:brief/a.md")
	if err != nil || !det.Doc.ReadOnly || det.Editable {
		t.Fatalf("detail: %+v %v", det, err)
	}
	if _, err := s.Update(ctx, u, "factory:p1:brief/a.md", map[string]any{"type": "Brief"}, "x"); !errors.Is(err, ErrKBReadOnly) {
		t.Fatalf("update: %v", err)
	}
	if err := s.Delete(ctx, u, "factory:p1:brief/a.md"); !errors.Is(err, ErrKBReadOnly) {
		t.Fatalf("delete: %v", err)
	}
}
