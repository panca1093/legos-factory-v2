package usecase

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

const contractSpec = `## Acceptance
| AC | Verification | Kind |
|---|---|---|
| c1 | ` + "`TestRoundsToRupiah`" + ` | test |
| c2 | reviewer reads the diff | check |
`

const goRed = "$ go test ./...\n--- FAIL: TestRoundsToRupiah (0.00s)\n    round_test.go:9: got 1.5 want 2\nFAIL\n"

// scriptSandbox returns the next scripted check output per RunCheck call.
type scriptSandbox struct {
	diffSandbox
	outs *[]string
	runs *int
}

func (s scriptSandbox) RunCheck(_ context.Context, _ string, c model.Check, _ time.Duration) model.CheckResult {
	out := (*s.outs)[min(*s.runs, len(*s.outs)-1)]
	*s.runs++
	return model.CheckResult{Name: c.Name, Command: c.Command, Output: out, Outcome: model.Fail}
}

func contractRig(t *testing.T, j *fakeJudge, outs ...string) (*rig, *int) {
	t.Helper()
	r := newRig(t)
	ctx := context.Background()
	dir := r.workspace(t, map[string]string{"README.md": "x\n"})
	_ = dir
	if err := os.WriteFile(filepath.Join(r.f.workspaceDir(r.wo.ID), "round_test.go"), []byte("func TestRoundsToRupiah(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runs := 0
	r.f.Sandbox = scriptSandbox{diffSandbox{filesSandbox{files: []string{"round_test.go"}}, nil}, &outs, &runs}
	if j != nil {
		r.f.Judge = j
	}
	r.f.Bus = &liveBus{}
	if _, err := r.db.Exec(`UPDATE projects SET contract_tests = 'on', quality_profile = ? WHERE id = 'p1'`, `{"checks":[{"name":"go-test","command":"go test ./..."}]}`); err != nil {
		t.Fatal(err)
	}
	if err := r.wos.UpdateFields(ctx, r.wo.ID, map[string]any{"acceptance": "- rounds to rupiah\n- docs updated", "spec": contractSpec}); err != nil {
		t.Fatal(err)
	}
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateImplementing); err != nil {
		t.Fatal(err)
	}
	return r, &runs
}

func (r *rig) redProof(t *testing.T) (bool, error) {
	t.Helper()
	wo := r.reload(t)
	return r.f.contractRedProof(context.Background(), wo, model.Job{WOID: wo.ID, TaskID: contractTaskID(wo.ID)})
}

func TestRedProofClassification(t *testing.T) {
	names := []string{"TestRoundsToRupiah"}
	cases := map[string]struct {
		out  string
		want string
	}{
		"go assertion":      {goRed, ""},
		"go compile error":  {"FAIL\tpkg [build failed]\n./round_test.go:5: undefined: Round\n--- FAIL: TestRoundsToRupiah\n", "build or import error"},
		"go already passes": {"ok  \tpkg\t0.1s\n", "did not fail"},
		"vitest assertion":  {" FAIL  src/round.test.ts > rounds\n × rounds money 0ms\n", ""},
		"vitest import":     {" FAIL  src/round.test.ts\nError: Failed to resolve import \"./round\"\n", "build or import error"},
	}
	for name, c := range cases {
		got := redProofFailure(c.out, names)
		if name == "vitest assertion" {
			got = redProofFailure(c.out, []string{"rounds money"})
		}
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestContractRowsReadTestRowsOnly(t *testing.T) {
	rows := contractRows(contractSpec)
	if len(rows) != 1 || rows[0].Criterion != "c1" || !slices.Equal(rows[0].Names, []string{"TestRoundsToRupiah"}) {
		t.Fatalf("rows: %+v", rows)
	}
}

func TestContractTaskIsRootOfThePlan(t *testing.T) {
	tasks := []model.Task{{ID: "wo1_t1", Deps: []string{"wo1_t0"}}, {ID: "wo1_t0", Deps: []string{"wo1_t1"}}, {ID: "wo1_t2", Deps: []string{"wo1_t1"}}}
	if err := requireContractTask("wo1", tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks[1].Deps) != 0 || !slices.Contains(tasks[2].Deps, "wo1_t0") || len(tasks[2].Deps) != 2 || len(tasks[0].Deps) != 1 {
		t.Fatalf("deps: %+v", tasks)
	}
	if err := model.ValidateDAG(tasks); err != nil {
		t.Fatal(err)
	}
	if requireContractTask("wo1", []model.Task{{ID: "wo1_t1"}}) == nil {
		t.Fatal("a plan without t0 must be refused")
	}
}

func TestPlanPromptRequiresT0OnlyWhenOn(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.workspace(t, map[string]string{"README.md": "x\n"})
	if err := r.f.handlePlan(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.h.prompts["architect"][0], "Contract tests are ON") {
		t.Fatal("off must not change the plan prompt")
	}

	r = newRig(t)
	r.workspace(t, map[string]string{"README.md": "x\n"})
	if _, err := r.db.Exec(`UPDATE projects SET contract_tests = 'on'`); err != nil {
		t.Fatal(err)
	}
	err := r.f.handlePlan(ctx, model.Job{WOID: r.wo.ID})
	if !strings.Contains(r.h.prompts["architect"][0], `The first task has id "t0"`) {
		t.Fatal("on must add the t0 rule")
	}
	if !errors.Is(err, model.ErrPermanent) {
		t.Fatalf("a plan without t0 must fail permanently when on, got %v", err)
	}
}

func TestRedProofOffDoesNothing(t *testing.T) {
	r, runs := contractRig(t, nil, goRed)
	if _, err := r.db.Exec(`UPDATE projects SET contract_tests = 'off'`); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.redProof(t); !ok || err != nil || *runs != 0 || len(r.events(t, "red_proof")) != 0 {
		t.Fatalf("off: ok=%v err=%v runs=%d", ok, err, *runs)
	}
}

func TestRedProofPassLocksFilesAndDoesNotRunTwice(t *testing.T) {
	j := &fakeJudge{over: map[string]float64{"test_checks_c1": 0.95}}
	r, runs := contractRig(t, j, goRed)
	if ok, err := r.redProof(t); !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	ev := eventByKind(t, r, "red_proof")
	locks, _ := ev.Payload["locks"].([]any)
	if ev.Payload["pass"] != true || len(locks) != 1 || locks[0].(map[string]any)["path"] != "round_test.go" || len(locks[0].(map[string]any)["sha256"].(string)) != 64 {
		t.Fatalf("event: %+v", ev.Payload)
	}
	if _, ok := j.calls[0].qs["test_checks_c1"]; !ok || len(j.calls[0].qs) != 1 {
		t.Fatalf("one question per criterion with a test: %v", j.calls[0].qs)
	}
	if ok, err := r.redProof(t); !ok || err != nil || *runs != 1 || len(r.events(t, "red_proof")) != 1 || len(j.calls) != 1 {
		t.Fatalf("a restart must not prove twice: runs=%d events=%d", *runs, len(r.events(t, "red_proof")))
	}
}

func TestRedProofFailureSendsT0BackOnceThenEscalates(t *testing.T) {
	for name, out := range map[string]string{"already passes": "ok pkg 0.1s\n", "compile error": "FAIL pkg [build failed]\n"} {
		t.Run(name, func(t *testing.T) {
			r, _ := contractRig(t, nil, out)
			ok, err := r.redProof(t)
			if ok || !errors.Is(err, model.ErrRequeue) {
				t.Fatalf("first failure: ok=%v err=%v", ok, err)
			}
			if wo := r.reload(t); !strings.Contains(planNotesSection(wo), "REQUIRED FIX for the contract tests") {
				t.Fatalf("t0 must get the output as a note: %+v", wo.Clarifications)
			}
			ok, err = r.redProof(t)
			if ok || err != nil {
				t.Fatalf("second failure: ok=%v err=%v", ok, err)
			}
			if wo := r.reload(t); wo.State != model.StateWaitingForHuman || wo.ClarifTask != contractTaskID(wo.ID) {
				t.Fatalf("must park for a human: %s %q", wo.State, wo.ClarifTask)
			}
			evs := r.events(t, "red_proof")
			if len(evs) != 2 || evs[0].Payload["retry"] != true || evs[1].Payload["escalated"] != true || evs[1].Payload["pass"] != false {
				t.Fatalf("events: %+v", evs)
			}
		})
	}
}

func TestJevNoOnTestChecksCountsTowardTheSameRetry(t *testing.T) {
	j := &fakeJudge{} // test_checks_* default to no
	r, _ := contractRig(t, j, goRed)
	if ok, err := r.redProof(t); ok || !errors.Is(err, model.ErrRequeue) {
		t.Fatalf("first no: ok=%v err=%v", ok, err)
	}
	if ok, err := r.redProof(t); ok || err != nil || r.reload(t).State != model.StateWaitingForHuman {
		t.Fatalf("second no must escalate: ok=%v err=%v", ok, err)
	}
	if got := len(r.events(t, "red_proof")); got != 2 {
		t.Fatalf("events %d", got)
	}
}

// lockedRig seeds a passed red proof for round_test.go and an Architect report that claims c1 unmet.
func lockedRig(t *testing.T, j *fakeJudge, report string) *rig {
	t.Helper()
	r := convergeRig(t, j, report)
	dir := r.f.workspaceDir(r.wo.ID)
	if err := os.WriteFile(filepath.Join(dir, "round_test.go"), []byte("func TestRoundsToRupiah(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`UPDATE projects SET contract_tests = 'on' WHERE id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	sha, _ := fileSHA(dir, "round_test.go")
	r.f.event(r.wo.ID, "factory", "red_proof", "ok", map[string]any{"pass": true, "locks": []contractLock{{Path: "round_test.go", SHA: sha, Criteria: []string{"c1"}}}})
	return r
}

func TestGreenUnchangedTestMeansMetIsNotAsked(t *testing.T) {
	j := &fakeJudge{}
	r := lockedRig(t, j, cleanReport)
	if wo := r.evaluate(t); wo.State != model.StateNeedsHumanReview {
		t.Fatalf("state %s", wo.State)
	}
	if len(j.calls) != 0 {
		t.Fatalf("no question is left for Jev: %v", j.calls[0].qs)
	}
	if ev := eventByKind(t, r, "converge_gate"); ev.Payload["outcome"] != GateConverged {
		t.Fatalf("gate: %+v", ev.Payload)
	}
}

func TestChangedContractTestIsFlaggedAndMetIsAskedAgain(t *testing.T) {
	j := &fakeJudge{over: map[string]float64{"gap_valid_H1": 0.9}}
	r := lockedRig(t, j, cleanReport)
	if err := os.WriteFile(filepath.Join(r.f.workspaceDir(r.wo.ID), "round_test.go"), []byte("func TestRoundsToRupiah(t *testing.T) { t.Skip() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.builtPlan(t)
	wo := r.evaluate(t)
	qs := j.calls[0].qs
	if _, ok := qs["gap_valid_H1"]; !ok {
		t.Fatalf("the changed test must be flagged as a contract gap: %v", qs)
	}
	if _, ok := qs["met_c1"]; !ok {
		t.Fatalf("a changed test no longer proves c1: %v", qs)
	}
	plan, _ := r.wos.Plan(context.Background(), r.wo.ID)
	if wo.State != model.StateImplementing || len(plan) != 2 || !strings.HasPrefix(plan[1].Title, "Close gap H1:") || !slices.Contains(plan[1].Scope, "round_test.go") {
		t.Fatalf("state %s plan %+v", wo.State, plan)
	}
	if !strings.Contains(wo.Spec, "contract test changed") {
		t.Fatalf("the spec must record the amendment: %s", wo.Spec)
	}
}
