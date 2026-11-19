package usecase

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func railFixture(t *testing.T) (*Rail, *store.WorkOrderRepo, *model.User) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	users := &store.UserRepo{DB: db}
	for _, u := range []model.User{
		{ID: "ana", Email: "ana@t", Name: "Ana", Role: model.RoleEngineer},
		{ID: "bob", Email: "bob@t", Name: "Bob", Role: model.RoleEngineer},
	} {
		if err := users.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	projs, members := &store.ProjectRepo{DB: db}, &store.MemberRepo{DB: db}
	for _, p := range []model.Project{{ID: "p1", Name: "Alpha", RepoURL: "/work/alpha"}, {ID: "p2", Name: "Beta"}} {
		if err := projs.Create(ctx, &p); err != nil {
			t.Fatal(err)
		}
	}
	if err := members.Add(ctx, "p1", "ana"); err != nil {
		t.Fatal(err)
	}
	wos := &store.WorkOrderRepo{DB: db}
	r := &Rail{Projects: &Projects{Store: projs, Members: members, Users: users}, WO: wos}
	return r, wos, &model.User{ID: "ana", Role: model.RoleEngineer}
}

func railWO(t *testing.T, wos *store.WorkOrderRepo, id, project string, state model.State, age time.Duration, fields map[string]any) {
	t.Helper()
	ctx := context.Background()
	if err := wos.Create(ctx, &model.WorkOrder{ID: id, ProjectID: project, Title: id, Objective: "o", State: state, CreatedBy: "ana"}); err != nil {
		t.Fatal(err)
	}
	if len(fields) > 0 {
		if err := wos.UpdateFields(ctx, id, fields); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := wos.DB.ExecContext(ctx, `UPDATE work_orders SET updated_at = ? WHERE id = ?`, time.Now().Add(-age).Unix(), id); err != nil {
		t.Fatal(err)
	}
}

func railIDs(rows []model.RailWorkOrder) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRailSplitsOpenFromDoneAndSortsWaitingFirst(t *testing.T) {
	r, wos, ana := railFixture(t)
	ctx := context.Background()
	railWO(t, wos, "run_old", "p1", model.StateImplementing, 3*time.Hour, nil)
	railWO(t, wos, "run_new", "p1", model.StateVerifying, time.Hour, nil)
	railWO(t, wos, "ask", "p1", model.StateWaitingForHuman, 5*time.Hour, nil)
	railWO(t, wos, "blocked", "p1", model.StateBlocked, 4*time.Hour, nil)
	railWO(t, wos, "cancelled", "p1", model.StateCancelled, time.Minute, nil)
	railWO(t, wos, "merged", "p1", model.StateReady, time.Minute, map[string]any{"pr_ref": "o/r#1", "pr_merged_at": time.Now().Unix()})
	railWO(t, wos, "pr_open", "p1", model.StateReady, 2*time.Hour, map[string]any{"pr_ref": "o/r#2", "pr_url": "https://x/2"})
	railWO(t, wos, "pr_closed", "p1", model.StateReady, 6*time.Hour, map[string]any{"pr_ref": "o/r#3", "pr_url": "https://x/3", "pr_closed_at": time.Now().Unix()})
	railWO(t, wos, "other_project", "p2", model.StateImplementing, time.Minute, nil)

	res, err := r.Get(ctx, ana, "p1", "wo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"blocked", "ask", "pr_closed", "run_new", "pr_open", "run_old"}
	if got := railIDs(res.WorkOrders.Rows); !equalIDs(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if res.WorkOrders.Open != 6 || res.Project.NeedsYou != 3 {
		t.Fatalf("open=%d needs_you=%d", res.WorkOrders.Open, res.Project.NeedsYou)
	}
	for _, row := range res.WorkOrders.Rows {
		waiting := row.ID == "blocked" || row.ID == "ask" || row.ID == "pr_closed"
		if row.WaitingOnYou != waiting {
			t.Fatalf("%s waiting_on_you = %v", row.ID, row.WaitingOnYou)
		}
	}
	if res.Pulls != nil {
		t.Fatalf("part=wo must omit pulls: %+v", res.Pulls)
	}
	if res.Project.Repo != "alpha" {
		t.Fatalf("repo falls back to the folder name, got %q", res.Project.Repo)
	}
}

func TestRailPullsPartIsNoIdentityStub(t *testing.T) {
	r, wos, ana := railFixture(t)
	railWO(t, wos, "a", "p1", model.StateImplementing, time.Hour, nil)

	res, err := r.Get(context.Background(), ana, "p1", "pulls")
	if err != nil {
		t.Fatal(err)
	}
	if res.WorkOrders != nil {
		t.Fatal("part=pulls must omit work orders")
	}
	if res.Pulls == nil || res.Pulls.Error != "no_identity" || res.Pulls.Count != 0 || res.Pulls.Identity != nil || res.Pulls.Stale {
		t.Fatalf("pulls = %+v", res.Pulls)
	}

	all, err := r.Get(context.Background(), ana, "p1", "")
	if err != nil || all.WorkOrders == nil || all.Pulls == nil {
		t.Fatalf("default part is all: %+v %v", all, err)
	}
}

func TestRailAccessAndValidation(t *testing.T) {
	r, _, ana := railFixture(t)
	ctx := context.Background()
	if _, err := r.Get(ctx, ana, "p2", "all"); !errors.Is(err, model.ErrProjectNotFound) {
		t.Fatalf("non-member: %v", err)
	}
	if _, err := r.Get(ctx, ana, "nope", "all"); !errors.Is(err, model.ErrProjectNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	admin := &model.User{ID: "root", Role: model.RoleAdmin}
	if _, err := r.Get(ctx, admin, "p2", "all"); err != nil {
		t.Fatalf("admin: %v", err)
	}
	if _, err := r.Get(ctx, ana, "p1", "bogus"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad part: %v", err)
	}
}
