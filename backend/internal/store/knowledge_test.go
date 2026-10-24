package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"legos-factory/backend/internal/model"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedProject(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if err := (&ProjectRepo{DB: db}).Create(context.Background(), &model.Project{ID: id, Name: id}); err != nil {
		t.Fatal(err)
	}
}

func TestKnowledgeSearchIsProjectScopedAndRanked(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	seedProject(t, db, "p1")
	seedProject(t, db, "p2")
	repo := &KnowledgeRepo{DB: db}
	for _, k := range []model.KnowledgeRecord{
		{ProjectID: "p1", Title: "Idempotency window decided", Body: "Retries use a 24 hour idempotency key", Trust: model.TrustHumanDecision},
		{ProjectID: "p1", Title: "Verification passed", Body: "go test ./... PASS", Trust: model.TrustVerified},
		{ProjectID: "p2", Title: "Idempotency in another project", Body: "must never leak into p1", Trust: model.TrustHumanDecision},
	} {
		k := k
		k.WorkspaceID, k.Kind, k.Actor = model.DefaultWorkspaceID, model.KnowledgeClarification, "human"
		if err := repo.Insert(ctx, &k); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.Search(ctx, model.KnowledgeQuery{ProjectIDs: []string{"p1"}, Text: "idempotency retries"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ProjectID != "p1" || got[0].Title != "Idempotency window decided" {
		t.Fatalf("expected only the p1 idempotency record, got %+v", got)
	}

	if got, _ := repo.Search(ctx, model.KnowledgeQuery{ProjectIDs: []string{}}); len(got) != 0 {
		t.Fatalf("empty project set must match nothing, got %d", len(got))
	}
	if got, _ := repo.Search(ctx, model.KnowledgeQuery{}); len(got) != 3 {
		t.Fatalf("unrestricted query should see all 3, got %d", len(got))
	}
	if got, _ := repo.Search(ctx, model.KnowledgeQuery{ProjectIDs: []string{"p1"}, Trust: model.TrustVerified}); len(got) != 1 {
		t.Fatalf("trust filter: expected 1, got %d", len(got))
	}
}

func TestKnowledgeSearchNeutralizesFTSOperators(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	seedProject(t, db, "p1")
	repo := &KnowledgeRepo{DB: db}
	k := &model.KnowledgeRecord{WorkspaceID: model.DefaultWorkspaceID, ProjectID: "p1", Kind: model.KnowledgeSummary,
		Trust: model.TrustAIInterpretation, Title: "Greeter summary", Body: "hello", Actor: "cataloger"}
	if err := repo.Insert(ctx, k); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{`greeter" OR body:*`, `NEAR(greeter`, `***`, `"`} {
		if _, err := repo.Search(ctx, model.KnowledgeQuery{ProjectIDs: []string{"p1"}, Text: text}); err != nil {
			t.Fatalf("query %q must not error: %v", text, err)
		}
	}
	got, err := repo.Get(ctx, k.ID)
	if err != nil || got.Title != "Greeter summary" || len(got.SourceRefs) != 0 {
		t.Fatalf("get: %+v %v", got, err)
	}
}
