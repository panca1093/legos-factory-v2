package store

import (
	"context"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestCodeFactsWithoutLineOnlyReplaceSameClaim(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	seedProject(t, db, "p1")
	if err := (&UserRepo{DB: db}).EnsureUser(ctx, model.User{ID: "u1", Email: "u1@example.test"}); err != nil {
		t.Fatal(err)
	}
	wo := &model.WorkOrder{ProjectID: "p1", Title: "t", State: model.StateCreated, CreatedBy: "u1"}
	if err := (&WorkOrderRepo{DB: db}).Create(ctx, wo); err != nil {
		t.Fatal(err)
	}
	repo := &CodeFactRepo{DB: db}
	for _, claim := range []string{"one", "two", "one"} {
		if err := repo.Add(ctx, wo.ID, []model.CodeFact{{Path: "a.go", Claim: claim, By: "foreman", SHA: "abc"}}, 40); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := repo.List(ctx, wo.ID)
	if len(got) != 2 || got[0].Claim != "two" || got[1].Claim != "one" {
		t.Fatalf("got %+v", got)
	}
}
