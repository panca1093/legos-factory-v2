package workflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"legos-factory/factory/internal/fact"
)

type sim struct {
	t     *testing.T
	facts []fact.Fact
	lim   Limits
}

func newSim(t *testing.T) *sim {
	s := &sim{t: t, lim: DefaultLimits()}
	s.emit(fact.KindWOCreated, fact.WOCreated{Objective: "x", LevelCap: 3})
	return s
}

func (s *sim) emit(kind string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		s.t.Fatal(err)
	}
	s.facts = append(s.facts, fact.Fact{ID: int64(len(s.facts) + 1), Subject: "wo", Kind: kind, Payload: raw})
}

func (s *sim) state() State { return Fold(s.facts) }

func (s *sim) call(c Call) error {
	nf, err := s.state().Apply(c, s.lim)
	for _, n := range nf {
		s.emit(n.Kind, n.Payload)
	}
	return err
}

func (s *sim) must(c Call) {
	s.t.Helper()
	if err := s.call(c); err != nil {
		s.t.Fatalf("%s: %v", c.Tool, err)
	}
}

func (s *sim) phase(want Phase) {
	s.t.Helper()
	if got := s.state().Phase; got != want {
		s.t.Fatalf("phase = %s, want %s", got, want)
	}
}

func (s *sim) specReady() {
	s.emit(fact.KindRunStarted, fact.RunStarted{Run: "r1", Role: RoleCataloger})
	s.emit(fact.KindRunStarted, fact.RunStarted{Run: "r2", Role: RoleArchitect})
	s.emit(KindSpecReady, struct{}{})
}

func (s *sim) planReady(tasks ...PlanTask) {
	s.specReady()
	s.must(ApproveSpec("ok"))
	s.emit(KindPlanReady, PlanReady{Tasks: tasks})
}

func (s *sim) building(tasks ...PlanTask) {
	s.planReady(tasks...)
	s.must(ApprovePlan("ok"))
}

// forceApprovePlan records an approval the guards would refuse, to reach states only reachable by corrupt logs.
func (s *sim) forceApprovePlan() {
	s.emit(KindForemanDecision, ForemanDecision{Tool: ToolApprovePlan, Args: ApprovePlan("").Args(), Accepted: true})
}

func (s *sim) built(id string) {
	s.emit(KindTaskBuilt, TaskBuiltPayload{Task: id, SHA: "abc"})
}

func (s *sim) inspected(id string, findings int) {
	s.emit(KindInspectionDone, InspectionDone{Task: id, Findings: findings})
}

func pt(id string, deps []string, paths ...string) PlanTask {
	return PlanTask{ID: id, Description: id, Paths: paths, DependsOn: deps}
}

func TestLegalSequenceToMerged(t *testing.T) {
	s := newSim(t)
	s.phase(PhaseQueued)
	s.emit(fact.KindRunStarted, fact.RunStarted{Run: "r1", Role: RoleCataloger})
	s.phase(PhaseCataloging)
	s.emit(fact.KindRunStarted, fact.RunStarted{Run: "r2", Role: RoleArchitect})
	s.phase(PhaseSpecifying)
	s.emit(KindSpecReady, struct{}{})
	s.phase(PhaseSpecReady)
	s.must(RejectSpec("too vague"))
	s.phase(PhaseSpecifying)
	s.emit(KindSpecReady, struct{}{})
	s.must(ApproveSpec("good"))
	s.phase(PhasePlanning)
	s.emit(KindPlanReady, PlanReady{Tasks: []PlanTask{pt("a", nil, "x/"), pt("b", []string{"a"}, "x/y.go"), pt("c", nil, "z/*")}})
	s.phase(PhasePlanReady)
	s.must(RejectPlan("missing tests"))
	s.phase(PhasePlanning)
	s.emit(KindPlanReady, PlanReady{Tasks: []PlanTask{pt("a", nil, "x/"), pt("b", []string{"a"}, "x/y.go"), pt("c", nil, "z/*")}})
	s.must(ApprovePlan("fine"))
	s.phase(PhaseBuilding)

	s.must(DispatchBuild([]string{"a", "c"}))
	if got := s.state().ActiveBuilders(); len(got) != 2 {
		t.Fatalf("active = %v", got)
	}
	s.built("a")
	s.must(RequestInspection("a"))
	s.inspected("a", 2)
	if st, _ := s.state().Task("a"); st.Status != TaskPending {
		t.Fatalf("a = %s, want pending (fix needed)", st.Status)
	}
	s.must(DispatchBuild([]string{"a"}))
	s.built("a")
	s.must(RequestInspection("a"))
	s.inspected("a", 0)
	s.built("c")
	s.must(RequestInspection("c"))
	s.inspected("c", 0)
	s.must(DispatchBuild([]string{"b"}))
	s.built("b")
	s.must(RequestInspection("b"))
	s.phase(PhaseBuilding)
	s.inspected("b", 0)
	s.phase(PhaseIntegrating)

	s.emit(KindIntegrationConflict, IntegrationConflict{A: "a", B: "c", Files: []string{"go.mod"}})
	s.must(Serialize("a", "c"))
	s.phase(PhaseBuilding)
	s.must(DispatchBuild([]string{"c"}))
	s.built("c")
	s.must(RequestInspection("c"))
	s.inspected("c", 0)
	s.phase(PhaseIntegrating)

	s.emit(fact.KindVerifyPassed, struct{}{})
	s.phase(PhaseVerifying)
	s.emit(fact.KindPROpened, fact.PROpened{Number: 1, HeadSHA: "h1"})
	s.phase(PhasePROpen)
	s.emit(fact.KindCheckObserved, fact.CheckObserved{Name: "ci", State: "failure", HeadSHA: "h1"})
	s.phase(PhaseCIFailed)
	s.emit(fact.KindWORequeued, fact.WORequeued{Reason: "ci_failed"})
	s.phase(PhaseBuilding)
	s.emit(fact.KindPRPushed, fact.PRPushed{HeadSHA: "h2"})
	s.phase(PhasePROpen)
	s.emit(fact.KindCheckObserved, fact.CheckObserved{Name: "ci", State: "success", HeadSHA: "h2"})
	s.emit(fact.KindChecksStable, fact.ChecksStable{HeadSHA: "h2"})
	s.phase(PhaseCIGreen)
	s.emit(fact.KindPRMerged, struct{}{})
	s.phase(PhaseMerged)
	s.emit(KindSpecReady, struct{}{})
	s.phase(PhaseMerged)
}

func TestTerminalAndSideExits(t *testing.T) {
	s := newSim(t)
	s.specReady()
	s.must(EscalateToHuman("stuck"))
	s.phase(PhaseAwaitingHuman)
	if got := s.state().EscalatedFrom; got != PhaseSpecReady {
		t.Fatalf("escalated from %s", got)
	}
	s.emit(KindSpecReady, struct{}{})
	s.phase(PhaseAwaitingHuman)
	s.emit(fact.KindWOFailed, fact.WOFailed{Class: "out_of_funds"})
	s.phase(PhaseFailed)
	if s.state().FailureClass != "out_of_funds" {
		t.Fatal("failure class lost")
	}

	r := newSim(t)
	r.emit(fact.KindWORequeued, fact.WORequeued{Reason: ReasonNeedsRebase})
	r.phase(PhaseNeedsRebase)
	r.emit(fact.KindWOCancelled, struct{}{})
	r.phase(PhaseCancelled)
}

func TestIllegalCallsRejectedAndStateUnchanged(t *testing.T) {
	fan := []PlanTask{pt("a", nil, "a/"), pt("b", nil, "b/"), pt("c", nil, "c/"), pt("d", nil, "d/")}
	cases := []struct {
		name  string
		setup func(s *sim)
		call  Call
		want  string
	}{
		{"approve_spec when queued", func(s *sim) {}, ApproveSpec("n"), "needs spec_ready"},
		{"approve_spec when planning", func(s *sim) { s.specReady(); s.must(ApproveSpec("n")) }, ApproveSpec("n"), "needs spec_ready"},
		{"reject_spec when queued", func(s *sim) {}, RejectSpec("r"), "needs spec_ready"},
		{"reject_spec without reason", func(s *sim) { s.specReady() }, RejectSpec(""), "reason is required"},
		{"reject_spec rounds exhausted", func(s *sim) {
			s.specReady()
			for i := 0; i < 3; i++ {
				s.must(RejectSpec("again"))
				s.emit(KindSpecReady, struct{}{})
			}
		}, RejectSpec("again"), "revision limit"},
		{"approve_plan when spec_ready", func(s *sim) { s.specReady() }, ApprovePlan("n"), "needs plan_ready"},
		{"approve_plan overlapping parallel paths", func(s *sim) {
			s.planReady(pt("a", nil, "pkg/"), pt("b", nil, "pkg/x.go"))
		}, ApprovePlan("n"), "overlapping paths"},
		{"approve_plan glob overlaps dir", func(s *sim) {
			s.planReady(pt("a", nil, "pkg/*"), pt("b", nil, "pkg/sub/x.go"))
		}, ApprovePlan("n"), "overlapping paths"},
		{"approve_plan equal paths", func(s *sim) {
			s.planReady(pt("a", nil, "x.go"), pt("b", nil, "x.go"))
		}, ApprovePlan("n"), "overlapping paths"},
		{"approve_plan indirect deps still parallel", func(s *sim) {
			s.planReady(pt("a", nil, "p/"), pt("b", []string{"a"}, "q/"), pt("c", nil, "p/z.go"))
		}, ApprovePlan("n"), "overlapping paths"},
		{"approve_plan unknown dep", func(s *sim) { s.planReady(pt("a", []string{"zz"}, "a/")) }, ApprovePlan("n"), "unknown task zz"},
		{"approve_plan cycle", func(s *sim) {
			s.planReady(pt("a", []string{"b"}, "a/"), pt("b", []string{"a"}, "b/"))
		}, ApprovePlan("n"), "cycle"},
		{"approve_plan empty", func(s *sim) { s.planReady() }, ApprovePlan("n"), "no tasks"},
		{"reject_plan when queued", func(s *sim) {}, RejectPlan("r"), "needs plan_ready"},
		{"reject_plan rounds exhausted", func(s *sim) {
			s.planReady(fan...)
			for i := 0; i < 3; i++ {
				s.must(RejectPlan("again"))
				s.emit(KindPlanReady, PlanReady{Tasks: fan})
			}
		}, RejectPlan("again"), "revision limit"},
		{"dispatch before building", func(s *sim) { s.planReady(fan...) }, DispatchBuild([]string{"a"}), "needs building"},
		{"dispatch empty", func(s *sim) { s.building(fan...) }, DispatchBuild(nil), "empty"},
		{"dispatch unknown task", func(s *sim) { s.building(fan...) }, DispatchBuild([]string{"nope"}), "unknown task"},
		{"dispatch duplicate id", func(s *sim) { s.building(fan...) }, DispatchBuild([]string{"a", "a"}), "twice"},
		{"dispatch deps not done", func(s *sim) {
			s.building(pt("a", nil, "a/"), pt("b", []string{"a"}, "b/"))
		}, DispatchBuild([]string{"b"}), "not done"},
		{"dispatch over builder limit", func(s *sim) {
			s.building(fan...)
			s.must(DispatchBuild([]string{"a", "b", "c"}))
		}, DispatchBuild([]string{"d"}), "exceeds the limit"},
		{"dispatch already building", func(s *sim) {
			s.building(fan...)
			s.must(DispatchBuild([]string{"a"}))
		}, DispatchBuild([]string{"a"}), "only pending"},
		{"dispatch overlaps active", func(s *sim) {
			s.planReady(pt("a", nil, "x/"), pt("b", []string{"c"}, "x/y.go"), pt("c", nil, "w/"))
			s.forceApprovePlan()
			s.must(DispatchBuild([]string{"a", "c"}))
			s.built("c")
			s.must(RequestInspection("c"))
			s.inspected("c", 0)
		}, DispatchBuild([]string{"b"}), "overlaps"},
		{"dispatch batch overlaps itself", func(s *sim) {
			s.planReady(pt("a", nil, "m/"), pt("b", nil, "m/n"))
			s.forceApprovePlan()
		}, DispatchBuild([]string{"a", "b"}), "overlaps"},
		{"inspect before built", func(s *sim) {
			s.building(fan...)
			s.must(DispatchBuild([]string{"a"}))
		}, RequestInspection("a"), "needs a built task"},
		{"inspect unknown task", func(s *sim) { s.building(fan...) }, RequestInspection("q"), "unknown task"},
		{"inspect without new commits", func(s *sim) {
			s.building(fan...)
			s.must(DispatchBuild([]string{"a"}))
			s.built("a")
			s.must(RequestInspection("a"))
		}, RequestInspection("a"), "needs a built task"},
		{"inspect rounds exhausted", func(s *sim) {
			s.building(fan...)
			for i := 0; i < 3; i++ {
				s.must(DispatchBuild([]string{"a"}))
				s.built("a")
				s.must(RequestInspection("a"))
				s.inspected("a", 1)
			}
			s.must(DispatchBuild([]string{"a"}))
			s.built("a")
		}, RequestInspection("a"), "inspection rounds"},
		{"retry task not failed", func(s *sim) { s.building(fan...) }, RetryTask("a", ChangeSame), "only failed"},
		{"retry unknown task", func(s *sim) { s.building(fan...) }, RetryTask("q", ChangeSame), "unknown task"},
		{"retry bad change", func(s *sim) {
			s.building(fan...)
			s.must(DispatchBuild([]string{"a"}))
			s.emit(fact.KindTaskFailed, fact.TaskFailed{Task: "a", Class: "timeout"})
		}, RetryTask("a", "bigger_hammer"), "invalid"},
		{"retry budget exhausted", func(s *sim) {
			s.building(fan...)
			s.must(DispatchBuild([]string{"a"}))
			for i := 0; i < 2; i++ {
				s.emit(fact.KindTaskFailed, fact.TaskFailed{Task: "a", Class: "timeout"})
				s.must(RetryTask("a", ChangeSame))
				s.must(DispatchBuild([]string{"a"}))
			}
			s.emit(fact.KindTaskFailed, fact.TaskFailed{Task: "a", Class: "timeout"})
		}, RetryTask("a", ChangeOtherModel), "retries"},
		{"serialize without conflict", func(s *sim) { s.building(fan...) }, Serialize("a", "b"), "no unresolved"},
		{"serialize self", func(s *sim) { s.building(fan...) }, Serialize("a", "a"), "itself"},
		{"serialize wrong phase", func(s *sim) {
			s.building(fan...)
			s.emit(KindIntegrationConflict, IntegrationConflict{A: "a", B: "b"})
		}, Serialize("a", "b"), "needs integrating"},
		{"retry other_model without fallback", func(s *sim) {
			s.building(fan...)
			s.must(DispatchBuild([]string{"a"}))
			s.emit(fact.KindTaskFailed, fact.TaskFailed{Task: "a", Class: "timeout"})
		}, RetryTask("a", ChangeOtherModel), "no fallback model"},
		{"serialize reversed order", func(s *sim) {
			s.integrating(pt("a", nil, "a/"), pt("b", nil, "b/"))
			s.emit(KindIntegrationConflict, IntegrationConflict{A: "a", B: "b"})
		}, Serialize("b", "a"), "recorded order"},
		{"serialize cycle", func(s *sim) {
			s.integrating(pt("a", nil, "a/"), pt("b", []string{"a"}, "b/"))
			s.emit(KindIntegrationConflict, IntegrationConflict{A: "b", B: "a"})
		}, Serialize("b", "a"), "cycle"},
		{"empty owned paths", func(s *sim) { s.planReady(pt("a", nil)) }, ApprovePlan(""), "no owned_paths"},
		{"owned path escapes repo", func(s *sim) { s.planReady(pt("a", nil, "src/../../x")) }, ApprovePlan(""), "escapes"},
		{"owned path absolute", func(s *sim) { s.planReady(pt("a", nil, "/etc")) }, ApprovePlan(""), "absolute"},
		{"serialize twice", func(s *sim) {
			s.building(pt("a", nil, "a/"), pt("b", nil, "b/"))
			s.must(DispatchBuild([]string{"a", "b"}))
			for _, id := range []string{"a", "b"} {
				s.built(id)
				s.must(RequestInspection(id))
				s.inspected(id, 0)
			}
			s.emit(KindIntegrationConflict, IntegrationConflict{A: "a", B: "b"})
			s.must(Serialize("a", "b"))
		}, Serialize("a", "b"), "no unresolved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSim(t)
			tc.setup(s)
			before := s.state()
			nf, err := before.Apply(tc.call, s.lim)
			if err == nil {
				t.Fatal("call accepted, want rejection")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q lacks %q", err, tc.want)
			}
			if nf[0].Kind != KindForemanDecision || nf[0].Payload.(ForemanDecision).Accepted {
				t.Fatalf("first fact must be a rejected decision, got %+v", nf[0])
			}
			s.emit(nf[0].Kind, nf[0].Payload)
			after := s.state()
			if after.ConsecutiveRejections != before.ConsecutiveRejections+1 {
				t.Fatalf("rejections = %d", after.ConsecutiveRejections)
			}
			after.ConsecutiveRejections = before.ConsecutiveRejections
			after.Epoch = before.Epoch
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("state changed by rejection:\nbefore %+v\nafter  %+v", before, after)
			}
		})
	}
}

func TestParallelOverlapVersusDependent(t *testing.T) {
	s := newSim(t)
	s.planReady(pt("a", nil, "pkg/"), pt("b", []string{"a"}, "pkg/x.go"), pt("c", []string{"b"}, "pkg/*"))
	if err := s.call(ApprovePlan("n")); err != nil {
		t.Fatalf("dependent tasks may overlap: %v", err)
	}
	s.phase(PhaseBuilding)
}

func TestPathOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"a/b.go", "a/b.go", true},
		{"a/", "a/b.go", true},
		{"a", "a/b.go", true},
		{"a/*", "a/b/c.go", true},
		{"./a/", "a/b.go", true},
		{"a/*.go", "a/b/c.go", true},
		{"ab/", "a/b.go", false},
		{"a/b.go", "a/bc.go", false},
		{"a/", "b/", false},
		{"*", "a/b.go", true},
	}
	for _, c := range cases {
		if got := pathsOverlap(c.a, c.b); got != c.want || pathsOverlap(c.b, c.a) != c.want {
			t.Errorf("overlap(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestConsecutiveRejectionsEscalate(t *testing.T) {
	s := newSim(t)
	s.specReady()
	bad := ApprovePlan("n")
	for i := 1; i <= 2; i++ {
		nf, err := s.state().Apply(bad, s.lim)
		if err == nil || len(nf) != 1 {
			t.Fatalf("call %d: err=%v facts=%d", i, err, len(nf))
		}
		s.call(bad)
		s.phase(PhaseSpecReady)
	}
	nf, _ := s.state().Apply(bad, s.lim)
	if len(nf) != 2 || nf[1].Kind != KindEscalated {
		t.Fatalf("third rejection must escalate, got %+v", nf)
	}
	s.call(bad)
	s.phase(PhaseAwaitingHuman)
	if s.state().ConsecutiveRejections != 0 {
		t.Fatal("counter not reset by escalation")
	}
}

func TestAcceptedCallResetsRejections(t *testing.T) {
	s := newSim(t)
	s.specReady()
	s.call(ApprovePlan("n"))
	s.call(ApprovePlan("n"))
	if got := s.state().ConsecutiveRejections; got != 2 {
		t.Fatalf("rejections = %d", got)
	}
	s.must(ApproveSpec("ok"))
	if got := s.state().ConsecutiveRejections; got != 0 {
		t.Fatalf("rejections = %d after accepted call", got)
	}
	s.call(ApprovePlan("n"))
	s.call(ApprovePlan("n"))
	s.phase(PhasePlanning)
}

func TestDecidedReplay(t *testing.T) {
	s := newSim(t)
	s.specReady()
	if _, ok := s.state().Decided(ApproveSpec("")); ok {
		t.Fatal("nothing decided yet")
	}
	s.must(ApproveSpec("first note"))
	f, ok := s.state().Decided(ApproveSpec("different note"))
	if !ok || f.Kind != KindForemanDecision {
		t.Fatalf("approve_spec not detected as decided: %v", ok)
	}
	if _, ok := s.state().Decided(RejectSpec("x")); ok {
		t.Fatal("a different tool must not match")
	}

	s.emit(KindPlanReady, PlanReady{Tasks: []PlanTask{pt("a", nil, "a/"), pt("b", nil, "b/")}})
	if _, ok := s.state().Decided(ApproveSpec("")); ok {
		t.Fatal("decision from an earlier round must not match")
	}
	s.must(ApprovePlan("ok"))
	s.must(DispatchBuild([]string{"a", "b"}))
	if _, ok := s.state().Decided(DispatchBuild([]string{"b", "a"})); !ok {
		t.Fatal("dispatch with reordered ids should match")
	}
	if _, ok := s.state().Decided(DispatchBuild([]string{"a"})); ok {
		t.Fatal("different ids must not match")
	}
	s.built("a")
	if _, ok := s.state().Decided(DispatchBuild([]string{"a", "b"})); ok {
		t.Fatal("a new builder result starts a new epoch")
	}
}

func TestToolsSchemas(t *testing.T) {
	want := []string{ToolApproveSpec, ToolRejectSpec, ToolApprovePlan, ToolRejectPlan, ToolDispatchBuild, ToolRequestInspection, ToolRetryTask, ToolSerialize, ToolEscalateToHuman}
	tools := Tools()
	if len(tools) != len(want) {
		t.Fatalf("%d tools", len(tools))
	}
	for i, td := range tools {
		if td.Name != want[i] || td.Description == "" {
			t.Errorf("tool %d = %q", i, td.Name)
		}
		var m map[string]any
		if err := json.Unmarshal(td.InputSchema, &m); err != nil {
			t.Errorf("%s schema: %v", td.Name, err)
		}
		if _, err := ParseCall(td.Name, nil); err != nil {
			t.Errorf("%s not parseable: %v", td.Name, err)
		}
	}
	if _, err := ParseCall("nope", nil); err == nil {
		t.Error("unknown tool accepted")
	}
}

func TestLevelZeroPlanGateWaitsForHuman(t *testing.T) {
	s := newSim(t)
	s.facts[0].Payload = []byte(`{"objective":"x","level_cap":0}`)
	s.planReady(PlanTask{ID: "a", Paths: []string{"a/"}})
	s.must(ApprovePlan("ok"))
	s.phase(PhaseAwaitingHuman)
	s.emit(KindHumanApproved, HumanApproved{Gate: "spec"})
	s.phase(PhaseAwaitingHuman)
	s.emit(KindHumanApproved, HumanApproved{Gate: GatePlan})
	s.phase(PhaseBuilding)
	s.must(DispatchBuild([]string{"a"}))
}

func TestMissingLevelCapDefaultsToThree(t *testing.T) {
	if got := Fold([]fact.Fact{{Kind: fact.KindWOCreated, Payload: []byte(`{"objective":"x"}`)}}).LevelCap; got != DefaultLevelCap {
		t.Fatalf("level cap = %d", got)
	}
}

func TestUnknownToolRejectedWithDecision(t *testing.T) {
	s := newSim(t)
	nf, err := s.state().Apply(Call{Tool: "rm_rf"}, s.lim)
	if err == nil || nf[0].Kind != KindForemanDecision {
		t.Fatalf("err=%v facts=%v", err, nf)
	}
}

func (s *sim) integrating(tasks ...PlanTask) {
	s.building(tasks...)
	for _, t := range tasks {
		s.must(DispatchBuild([]string{t.ID}))
		s.built(t.ID)
		s.must(RequestInspection(t.ID))
		s.inspected(t.ID, 0)
	}
}

func TestDuplicateOfAppliedDecisionIsRejected(t *testing.T) {
	s := newSim(t)
	s.building(pt("a", nil, "a/"))
	s.must(EscalateToHuman("first"))
	nf, err := s.state().Apply(EscalateToHuman("second"), s.lim)
	if err == nil || !strings.Contains(err.Error(), "already decided: escalate_to_human") {
		t.Fatalf("err = %v", err)
	}
	if nf[0].Payload.(ForemanDecision).Accepted {
		t.Fatal("duplicate recorded as accepted")
	}
}

func TestEscalationApprovalReturnsToEscalatedPhase(t *testing.T) {
	s := newSim(t)
	s.specReady()
	s.call(ApprovePlan("n"))
	s.call(ApprovePlan("n"))
	s.call(ApprovePlan("n"))
	st := s.state()
	if st.Phase != PhaseAwaitingHuman || st.Gate != GateEscalation || st.EscalatedFrom != PhaseSpecReady {
		t.Fatalf("state = %+v", st)
	}
	s.emit(KindHumanApproved, HumanApproved{Gate: GatePlan})
	s.phase(PhaseAwaitingHuman)
	before := st.Epoch
	s.emit(KindHumanApproved, HumanApproved{Gate: GateEscalation, Note: "go on"})
	st = s.state()
	if st.Phase != PhaseSpecReady || st.Gate != "" || st.ConsecutiveRejections != 0 || st.Epoch <= before {
		t.Fatalf("after approval: %+v", st)
	}
	s.must(ApproveSpec("ok"))
	s.phase(PhasePlanning)
}

func TestEscalationWhileParkedKeepsGate(t *testing.T) {
	s := newSim(t)
	s.facts[0].Payload = []byte(`{"objective":"x","level_cap":0}`)
	s.planReady(pt("a", nil, "a/"))
	s.must(ApprovePlan("ok"))
	s.emit(KindEscalated, Escalated{Reason: "late"})
	if st := s.state(); st.Gate != GatePlan {
		t.Fatalf("gate = %q", st.Gate)
	}
}
