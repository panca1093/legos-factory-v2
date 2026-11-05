package usecase

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type statusGit struct {
	GitProvider
	status map[string]model.PRStatus
	err    error
	calls  int
}

func (g *statusGit) PullRequestStatus(_ context.Context, ref string) (model.PRStatus, error) {
	g.calls++
	return g.status[ref], g.err
}

func TestPollPRsRecordsMergeAndClose(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := (&store.UserRepo{DB: db}).Create(ctx, model.User{ID: "u1", Email: "u1@t", Name: "U", Role: model.RoleEngineer}); err != nil {
		t.Fatal(err)
	}
	projs := &store.ProjectRepo{DB: db}
	if err := projs.Create(ctx, &model.Project{ID: "p1", Name: "p"}); err != nil {
		t.Fatal(err)
	}
	wos := &store.WorkOrderRepo{DB: db}
	mk := func(id, state, ref string) {
		wo := &model.WorkOrder{ID: id, ProjectID: "p1", Title: id, Objective: "o", State: model.State(state), CreatedBy: "u1"}
		if err := wos.Create(ctx, wo); err != nil {
			t.Fatal(err)
		}
		if ref != "" {
			if err := wos.UpdateFields(ctx, id, map[string]any{"pr_ref": ref, "pr_url": "https://x/" + id}); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("merged", "READY", "github:a/b#1")
	mk("closed", "READY", "github:a/b#2")
	mk("open", "READY", "github:a/b#3")
	mk("noref", "READY", "")
	mk("notready", "IMPLEMENTING", "github:a/b#5")

	at := time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC)
	git := &statusGit{status: map[string]model.PRStatus{
		"github:a/b#1": {State: model.PRStateMerged, At: at},
		"github:a/b#2": {State: model.PRStateClosed, At: at},
		"github:a/b#3": {State: model.PRStateOpen},
	}}
	bound := func(context.Context, *model.Project) (GitProvider, bool, error) { return git, true, nil }
	f := &Factory{WO: wos, Proj: projs, Bus: nopBus{}, Log: slog.Default()}

	f.pollPRs(ctx, bound)
	m, _ := wos.Get(ctx, "merged")
	if m.PRMergedAt == nil || !m.PRMergedAt.Equal(at) || m.PRClosedAt != nil {
		t.Fatalf("merged order: %+v", m)
	}
	c, _ := wos.Get(ctx, "closed")
	if c.PRClosedAt == nil || c.PRMergedAt != nil {
		t.Fatalf("closed order: %+v", c)
	}
	if o, _ := wos.Get(ctx, "open"); o.PRMergedAt != nil || o.PRClosedAt != nil {
		t.Fatalf("open PR must stay untouched: %+v", o)
	}
	for id, kind := range map[string]string{"merged": "pr_merged", "closed": "pr_closed"} {
		ev, err := wos.LatestEvent(ctx, id, kind)
		if err != nil || ev == nil || ev.Actor != "factory" {
			t.Fatalf("%s event: %+v %v", kind, ev, err)
		}
	}

	if git.calls != 3 {
		t.Fatalf("only READY orders with a ref are polled: %d calls", git.calls)
	}
	f.pollPRs(ctx, bound)
	if git.calls != 4 {
		t.Fatalf("settled orders must not be polled again: %d calls", git.calls)
	}

	git.err = errors.New("provider down")
	git.status["github:a/b#3"] = model.PRStatus{State: model.PRStateMerged, At: at}
	f.pollPRs(ctx, bound)
	if o, _ := wos.Get(ctx, "open"); o.State != model.StateReady || o.PRMergedAt != nil {
		t.Fatalf("a provider error must not touch the order: %+v", o)
	}
	git.err = nil
	f.pollPRs(ctx, bound)
	if o, _ := wos.Get(ctx, "open"); o.PRMergedAt == nil {
		t.Fatalf("the next tick must retry: %+v", o)
	}
}
