package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestCodeFactsRoundTripReplaceAndCap(t *testing.T) {
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

	a := model.CodeFact{Path: "a.go", Line: 3, Claim: "old", By: "foreman", SHA: "abc"}
	if err := repo.Add(ctx, wo.ID, []model.CodeFact{a}, 40); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.List(ctx, wo.ID)
	if !reflect.DeepEqual(got, []model.CodeFact{a}) {
		t.Fatalf("round trip = %+v", got)
	}

	b := model.CodeFact{Path: "a.go", Line: 3, Claim: "new", By: "architect", SHA: "def"}
	if err := repo.Add(ctx, wo.ID, []model.CodeFact{b}, 40); err != nil {
		t.Fatal(err)
	}
	if got, _ = repo.List(ctx, wo.ID); !reflect.DeepEqual(got, []model.CodeFact{b}) {
		t.Fatalf("same path:line must be replaced, got %+v", got)
	}

	var many []model.CodeFact
	for i := 0; i < 50; i++ {
		many = append(many, model.CodeFact{Path: fmt.Sprintf("f%02d.go", i), Claim: "c", By: "foreman", SHA: "abc"})
	}
	if err := repo.Add(ctx, wo.ID, many, 40); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.List(ctx, wo.ID)
	if len(got) != 40 || got[0].Path != "f10.go" || got[39].Path != "f49.go" {
		t.Fatalf("cap must keep the newest 40, got %d: first %s last %s", len(got), got[0].Path, got[len(got)-1].Path)
	}
}
