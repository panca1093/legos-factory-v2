package usecase

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

func infraVerifyRig(t *testing.T, d *fakeDocker) (*Factory, *model.WorkOrder, *store.WorkOrderRepo, *fakeQueue) {
	t.Helper()
	f, wo, wos := monitorFactory(t, nil)
	q := &fakeQueue{}
	f.Sched, f.Sandbox, f.DataDir, f.Docker = q, failSandbox{}, t.TempDir(), d
	if err := os.MkdirAll(f.workspaceDir(wo.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	f.Proj, f.Quality, f.Findings = &store.ProjectRepo{DB: wos.DB}, &store.QualityRepo{DB: wos.DB}, &store.FindingRepo{DB: wos.DB}
	return f, wo, wos, q
}

func eventKinds(t *testing.T, wos *store.WorkOrderRepo, id string) map[string]int {
	t.Helper()
	evs, err := wos.Events(context.Background(), id, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for _, e := range evs {
		n[e.Kind]++
	}
	return n
}

func TestLeaseFailureNeverSendsBuilderBack(t *testing.T) {
	ctx := context.Background()
	f, wo, wos, q := infraVerifyRig(t, &fakeDocker{missing: true})
	p := stackProfile()
	p.Checks = []model.Check{{Name: "api test", Command: "go test ./..."}}
	b, _ := json.Marshal(p)
	if err := (&store.ProjectRepo{DB: wos.DB}).SetQualityProfile(ctx, wo.ProjectID, string(b)); err != nil {
		t.Fatal(err)
	}
	if err := wos.UpdateState(ctx, wo.ID, model.StateVerifying); err != nil {
		t.Fatal(err)
	}
	if err := f.handleVerify(ctx, model.Job{WOID: wo.ID}); err != nil {
		t.Fatal(err)
	}
	got, _ := wos.Get(ctx, wo.ID)
	if got.Revision != wo.Revision || got.VerifyFailures != 0 || got.State == model.StateImplementing {
		t.Fatalf("infrastructure failure must not bump revision or return to the builder: %+v", got)
	}
	for i, pl := range q.payloads {
		if pl["reason"] == "fix_failed_verification" || strings.HasPrefix(q.enqueued[i], JobImplement) {
			t.Fatalf("enqueued %v", q.enqueued)
		}
	}
	kinds := eventKinds(t, wos, wo.ID)
	if kinds["verification_unavailable"] != 1 || kinds["verification_passed"] != 0 || kinds["check_unverified"] != 1 {
		t.Fatalf("events %v", kinds)
	}
}

func TestChecksFallbackEventOncePerOrder(t *testing.T) {
	ctx := context.Background()
	f, wo, wos, _ := infraVerifyRig(t, &fakeDocker{})
	for i := 0; i < 2; i++ {
		if err := wos.UpdateState(ctx, wo.ID, model.StateVerifying); err != nil {
			t.Fatal(err)
		}
		if err := f.handleVerify(ctx, model.Job{WOID: wo.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if n := eventKinds(t, wos, wo.ID)["checks_fallback"]; n != 1 {
		t.Fatalf("checks_fallback emitted %d times, want 1", n)
	}
}

func TestOptionalOnlyPassIsNotAllRequiredPassed(t *testing.T) {
	checks := []model.Check{{Name: "a", Optional: true}, {Name: "b"}}
	pass := model.CheckResult{Outcome: model.Pass}
	if anyRequiredPassed(checks, []model.CheckResult{pass, {Outcome: model.Unverified}}) {
		t.Fatal("an optional pass alone is not a required pass")
	}
	if !anyRequiredPassed(checks, []model.CheckResult{{Outcome: model.Fail}, pass}) {
		t.Fatal("a required pass must count")
	}
}
