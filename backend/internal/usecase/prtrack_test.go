package usecase

import (
	"context"
	"errors"
	"fmt"
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

type prRig struct {
	f   *Factory
	wos *store.WorkOrderRepo
	git *statusGit
	at  time.Time
}

func newPRRig(t *testing.T) *prRig {
	t.Helper()
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
	for _, id := range []string{"p1", "unbound"} {
		if err := projs.Create(ctx, &model.Project{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	wos := &store.WorkOrderRepo{DB: db}
	return &prRig{
		f:   &Factory{WO: wos, Proj: projs, Bus: nopBus{}, Log: slog.Default()},
		wos: wos,
		git: &statusGit{status: map[string]model.PRStatus{}},
		at:  time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC),
	}
}

func (r *prRig) order(t *testing.T, id, project, state, ref string) {
	t.Helper()
	ctx := context.Background()
	if err := r.wos.Create(ctx, &model.WorkOrder{ID: id, ProjectID: project, Title: id, Objective: "o", State: model.State(state), CreatedBy: "u1"}); err != nil {
		t.Fatal(err)
	}
	if ref != "" {
		if err := r.wos.UpdateFields(ctx, id, map[string]any{"pr_ref": ref, "pr_url": "https://x/" + id}); err != nil {
			t.Fatal(err)
		}
	}
}

func (r *prRig) poll() {
	r.f.pollPRs(context.Background(), func(_ context.Context, p *model.Project) (GitProvider, bool, error) {
		return r.git, p.ID != "unbound", nil
	})
}

func (r *prRig) get(t *testing.T, id string) *model.WorkOrder {
	t.Helper()
	wo, err := r.wos.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return wo
}

func (r *prRig) events(t *testing.T, id, kind string) int {
	t.Helper()
	evs, err := r.wos.Events(context.Background(), id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == kind {
			if e.Actor != "factory" {
				t.Fatalf("%s actor: %s", kind, e.Actor)
			}
			n++
		}
	}
	return n
}

func TestPollPRsRecordsMergeAndClose(t *testing.T) {
	r := newPRRig(t)
	r.order(t, "merged", "p1", "READY", "github:a/b#1")
	r.order(t, "open", "p1", "READY", "github:a/b#3")
	r.order(t, "noref", "p1", "READY", "")
	r.order(t, "notready", "p1", "IMPLEMENTING", "github:a/b#5")
	r.git.status["github:a/b#1"] = model.PRStatus{State: model.PRStateMerged, At: r.at}
	r.git.status["github:a/b#3"] = model.PRStatus{State: model.PRStateOpen}

	r.poll()
	m := r.get(t, "merged")
	if m.PRMergedAt == nil || !m.PRMergedAt.Equal(r.at) || m.PRClosedAt != nil || r.events(t, "merged", "pr_merged") != 1 {
		t.Fatalf("merged order: %+v", m)
	}
	if o := r.get(t, "open"); o.PRMergedAt != nil || o.PRClosedAt != nil {
		t.Fatalf("open PR must stay untouched: %+v", o)
	}
	if r.git.calls != 2 {
		t.Fatalf("only READY orders with a ref are polled: %d calls", r.git.calls)
	}
	r.poll()
	if r.git.calls != 3 {
		t.Fatalf("merged orders must not be polled again: %d calls", r.git.calls)
	}
}

func TestPollPRsClosedThenReopenedAndMerged(t *testing.T) {
	r := newPRRig(t)
	r.order(t, "wo", "p1", "READY", "github:a/b#1")
	r.git.status["github:a/b#1"] = model.PRStatus{State: model.PRStateClosed, At: r.at}

	r.poll()
	r.poll()
	wo := r.get(t, "wo")
	if wo.PRClosedAt == nil || !wo.PRClosedAt.Equal(r.at) || wo.PRMergedAt != nil || r.events(t, "wo", "pr_closed") != 1 {
		t.Fatalf("closed must be recorded once: %+v", wo)
	}
	if r.git.calls != 2 {
		t.Fatalf("a closed PR keeps being polled: %d calls", r.git.calls)
	}

	r.git.status["github:a/b#1"] = model.PRStatus{State: model.PRStateOpen}
	r.poll()
	if wo = r.get(t, "wo"); wo.PRMergedAt != nil || r.events(t, "wo", "pr_closed") != 1 {
		t.Fatalf("reopened PR: %+v", wo)
	}

	later := r.at.Add(time.Hour)
	r.git.status["github:a/b#1"] = model.PRStatus{State: model.PRStateMerged, At: later}
	r.poll()
	wo = r.get(t, "wo")
	if wo.PRMergedAt == nil || !wo.PRMergedAt.Equal(later) || wo.PRClosedAt != nil || r.events(t, "wo", "pr_merged") != 1 {
		t.Fatalf("merged after reopen: %+v", wo)
	}
}

func TestPollPRsTransientErrorRetriesPermanentStops(t *testing.T) {
	r := newPRRig(t)
	r.order(t, "wo", "p1", "READY", "github:a/b#1")
	r.order(t, "unbound", "unbound", "READY", "github:a/b#2")
	r.git.status["github:a/b#1"] = model.PRStatus{State: model.PRStateMerged, At: r.at}

	r.git.err = errors.New("provider down")
	r.poll()
	wo := r.get(t, "wo")
	if wo.State != model.StateReady || wo.PRMergedAt != nil || wo.PRTrackFailedAt != nil {
		t.Fatalf("a transient error must not touch the order: %+v", wo)
	}
	if u := r.get(t, "unbound"); u.PRTrackFailedAt == nil || r.events(t, "unbound", "pr_track_failed") != 1 {
		t.Fatalf("an unbound project is permanent: %+v", u)
	}
	r.git.err = nil
	r.poll()
	if r.get(t, "wo").PRMergedAt == nil {
		t.Fatal("the next tick must retry")
	}
	calls := r.git.calls
	r.poll()
	if r.git.calls != calls || r.events(t, "unbound", "pr_track_failed") != 1 {
		t.Fatal("a failed order must not be polled or reported again")
	}
}

func TestPollPRsPermanentProviderErrors(t *testing.T) {
	for name, perm := range map[string]error{
		"wrong provider ref": fmt.Errorf("%w: %q is not a github pull request ref", model.ErrPermanent, "gitlab:a/b!1"),
		"404":                &model.ProviderError{Provider: "github", Status: 404, Message: "Not Found"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newPRRig(t)
			r.order(t, "wo", "p1", "READY", "gitlab:a/b!1")
			r.git.err = perm
			r.poll()
			wo := r.get(t, "wo")
			if wo.PRTrackFailedAt == nil || wo.PRMergedAt != nil || wo.State != model.StateReady || r.events(t, "wo", "pr_track_failed") != 1 {
				t.Fatalf("%+v", wo)
			}
			r.poll()
			if r.git.calls != 1 {
				t.Fatalf("polling must stop: %d calls", r.git.calls)
			}
		})
	}
	r := newPRRig(t)
	r.order(t, "wo", "p1", "READY", "github:a/b#1")
	r.git.err = &model.ProviderError{Provider: "github", Status: 502, Message: "bad gateway"}
	r.poll()
	if r.get(t, "wo").PRTrackFailedAt != nil {
		t.Fatal("a 5xx must be retried, not marked failed")
	}
}
