package usecase

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func planAnswers(over map[string]float64) map[string]float64 {
	a := map[string]float64{
		"covers_c1": 0.95, "covers_c2": 0.9, "verified_c1": 0.9, "verified_c2": 0.95, "critical_resolved": 0.9, "open_questions": 0.05, "scope_creep": 0.05, "touches_schema": 0.02,
		"touches_authentication": 0.01, "touches_authorization": 0.01, "touches_payments": 0.01, "touches_secrets": 0.01,
	}
	for k, v := range over {
		a[k] = v
	}
	return a
}

var testCriteria = []Criterion{{"c1", "add items to the cart"}, {"c2", "show the cart total"}}

var testFindings = []reviewFinding{
	{ID: "F1", Severity: "critical", Layer: "contract", Cites: "c2", Issue: "no error path", Fix: "add a rule"},
	{ID: "F2", Severity: "low", Layer: "contract", Cites: "spec: rules", Issue: "naming", Fix: "rename"},
}

func planInput(revisions int) PlanGateInput {
	return PlanGateInput{Request: "r", Criteria: testCriteria, Findings: testFindings, PlanRevisions: revisions}
}

func TestDecidePlanGate(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name      string
		answers   map[string]float64
		err       error
		over      bool
		revisions int
		outcome   string
		row       int
		noteHas   []string
	}{
		{"row1 judge error", nil, boom, false, 0, GateEscalate, 1, []string{"boom"}},
		{"row1 over limit", planAnswers(nil), nil, true, 0, GateEscalate, 1, []string{"context limit"}},
		{"row1 missing answers", map[string]float64{"covers_c1": 0.9}, nil, false, 0, GateEscalate, 1, nil},
		{"row1 beats row2", planAnswers(map[string]float64{"touches_schema": 0.9}), boom, false, 0, GateEscalate, 1, nil},
		{"row2 schema", planAnswers(map[string]float64{"touches_schema": 0.9}), nil, false, 0, GateEscalate, 2, []string{"schema"}},
		{"row2 authentication", planAnswers(map[string]float64{"touches_authentication": 0.71}), nil, false, 0, GateEscalate, 2, []string{"changes authentication"}},
		{"row2 authorization", planAnswers(map[string]float64{"touches_authorization": 0.9}), nil, false, 0, GateEscalate, 2, []string{"authorization"}},
		{"row2 payments", planAnswers(map[string]float64{"touches_payments": 0.9}), nil, false, 0, GateEscalate, 2, []string{"payment"}},
		{"row2 secrets", planAnswers(map[string]float64{"touches_secrets": 0.9}), nil, false, 0, GateEscalate, 2, []string{"secrets"}},
		{"row2 lists every risky area", planAnswers(map[string]float64{"touches_payments": 0.9, "touches_secrets": 0.9}), nil, false, 0, GateEscalate, 2, []string{"payment", "secrets"}},
		{"row2 beats row3", planAnswers(map[string]float64{"touches_schema": 0.9, "covers_c1": 0.1}), nil, false, 0, GateEscalate, 2, nil},
		{"row3 not covered", planAnswers(map[string]float64{"covers_c2": 0.1}), nil, false, 0, GateRevise, 3, []string{"criterion c2 (show the cart total) is not covered"}},
		{"row3 not verified", planAnswers(map[string]float64{"verified_c1": 0.1}), nil, false, 0, GateRevise, 3, []string{"c1 (add items to the cart) has no concrete test or check"}},
		{"row3 critical not resolved", planAnswers(map[string]float64{"critical_resolved": 0.1}), nil, false, 0, GateRevise, 3, []string{"critical finding is not addressed", "F1 [critical, contract] c2: no error path"}},
		{"row3 open questions", planAnswers(map[string]float64{"open_questions": 0.8}), nil, false, 1, GateRevise, 3, []string{"open question or a guessed behaviour"}},
		{"row3 scope creep", planAnswers(map[string]float64{"scope_creep": 0.8}), nil, false, 0, GateRevise, 3, []string{"does not ask for"}},
		{"row3 lists several", planAnswers(map[string]float64{"covers_c1": 0.0, "open_questions": 1}), nil, false, 0, GateRevise, 3, []string{"criterion c1 (add items to the cart) is not covered", "open question"}},
		{"row3 includes unsettled too", planAnswers(map[string]float64{"covers_c1": 0.1, "verified_c2": 0.5}), nil, false, 0, GateRevise, 3, []string{"c1 (add items to the cart) is not covered", "it is unclear whether acceptance criterion c2"}},
		{"row3 escalates at budget", planAnswers(map[string]float64{"covers_c1": 0.1}), nil, false, 2, GateEscalate, 3, []string{"c1"}},
		{"row5 unsettled covers", planAnswers(map[string]float64{"covers_c2": 0.5}), nil, false, 0, GateRevise, 5, []string{"it is unclear whether acceptance criterion c2 (show the cart total) is covered"}},
		{"row5 unsettled verified", planAnswers(map[string]float64{"verified_c2": 0.5}), nil, false, 1, GateRevise, 5, []string{"has a concrete test or check"}},
		{"row5 unsettled critical_resolved", planAnswers(map[string]float64{"critical_resolved": 0.5}), nil, false, 0, GateRevise, 5, []string{"every critical finding is addressed", "F1"}},
		{"row5 escalates at budget", planAnswers(map[string]float64{"critical_resolved": 0.4}), nil, false, 3, GateEscalate, 5, nil},
		{"row6 unsettled scope_creep is a note, not a revise", planAnswers(map[string]float64{"scope_creep": 0.5}), nil, false, 0, GateApprove, 6, nil},
		{"row6 unsettled open_questions is a note", planAnswers(map[string]float64{"open_questions": 0.5}), nil, false, 0, GateApprove, 6, nil},
		{"row4 unsettled touches escalates", planAnswers(map[string]float64{"touches_schema": 0.5}), nil, false, 0, GateEscalate, 4, []string{"it is unclear whether a task changes a database schema"}},
		{"row4 unsettled touches escalates even with revisions left", planAnswers(map[string]float64{"touches_payments": 0.4, "critical_resolved": 0.5}), nil, false, 0, GateEscalate, 4, nil},
		{"row3 beats row4", planAnswers(map[string]float64{"touches_secrets": 0.5, "covers_c1": 0.1}), nil, false, 0, GateRevise, 3, nil},
		{"row2 beats row4", planAnswers(map[string]float64{"touches_secrets": 0.5, "touches_schema": 0.9}), nil, false, 0, GateEscalate, 2, nil},
		{"row6 approve", planAnswers(nil), nil, false, 0, GateApprove, 6, nil},
		{"row6 approve with revisions spent", planAnswers(nil), nil, false, 2, GateApprove, 6, nil},
		{"edge covers 0.7 unsettled", planAnswers(map[string]float64{"covers_c1": 0.7}), nil, false, 0, GateRevise, 5, nil},
		{"edge covers 0.71 yes", planAnswers(map[string]float64{"covers_c1": 0.71}), nil, false, 0, GateApprove, 6, nil},
		{"edge covers 0.3 unsettled", planAnswers(map[string]float64{"covers_c1": 0.3}), nil, false, 0, GateRevise, 5, nil},
		{"edge covers 0.29 no", planAnswers(map[string]float64{"covers_c1": 0.29}), nil, false, 0, GateRevise, 3, nil},
		{"edge verified 0.7 unsettled", planAnswers(map[string]float64{"verified_c1": 0.7}), nil, false, 0, GateRevise, 5, nil},
		{"edge verified 0.29 no", planAnswers(map[string]float64{"verified_c1": 0.29}), nil, false, 0, GateRevise, 3, nil},
		{"edge open_questions 0.7 unsettled", planAnswers(map[string]float64{"open_questions": 0.7}), nil, false, 0, GateApprove, 6, nil},
		{"edge open_questions 0.71 yes", planAnswers(map[string]float64{"open_questions": 0.71}), nil, false, 0, GateRevise, 3, nil},
		{"edge scope_creep 0.7 unsettled", planAnswers(map[string]float64{"scope_creep": 0.7}), nil, false, 0, GateApprove, 6, nil},
		{"edge scope_creep 0.71 yes", planAnswers(map[string]float64{"scope_creep": 0.71}), nil, false, 0, GateRevise, 3, nil},
		{"edge secrets 0.7 unsettled is row 4, not 2", planAnswers(map[string]float64{"touches_secrets": 0.7}), nil, false, 0, GateEscalate, 4, nil},
		{"row1 no criteria", nil, nil, false, 0, GateEscalate, 1, []string{"no acceptance criteria to judge against"}},
		{"row1 no judge", nil, errNoJudge, false, 0, GateEscalate, 1, []string{"jev is not configured"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := planInput(c.revisions)
			if strings.Contains(c.name, "no criteria") {
				in.Criteria = nil
			}
			_, asked := planGateState(in)
			got := decidePlanGate(in, asked, c.answers, c.err, c.over)
			if got.Outcome != c.outcome || got.Row != c.row {
				t.Fatalf("got %+v, want %s row %d", got, c.outcome, c.row)
			}
			for _, s := range c.noteHas {
				if !strings.Contains(got.Note, s) {
					t.Errorf("note %q missing %q", got.Note, s)
				}
			}
		})
	}
}

func TestDecidePlanGateAskedQuestions(t *testing.T) {
	in := planInput(0)
	_, asked := planGateState(in)
	t.Run("asked id without answer escalates row 1", func(t *testing.T) {
		for id := range asked {
			a := planAnswers(nil)
			delete(a, id)
			got := decidePlanGate(in, asked, a, nil, false)
			if got.Outcome != GateEscalate || got.Row != 1 || !strings.Contains(got.Note, id) {
				t.Errorf("without %s: %+v", id, got)
			}
		}
	})
	t.Run("unasked extra answers are ignored", func(t *testing.T) {
		a := planAnswers(map[string]float64{"covers_c9": 0.0, "bogus": 1})
		if got := decidePlanGate(in, asked, a, nil, false); got.Outcome != GateApprove {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("a criterion that was never asked is missing", func(t *testing.T) {
		withExtra := in
		withExtra.Criteria = append(append([]Criterion{}, testCriteria...), Criterion{"c3", "x"})
		a := planAnswers(map[string]float64{"covers_c3": 0.95})
		if got := decidePlanGate(withExtra, asked, a, nil, false); got.Outcome != GateEscalate || got.Row != 1 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("revise note is numbered, quotes full criterion text and names tasks", func(t *testing.T) {
		long := planInput(0)
		text := strings.Repeat("word ", 40)
		long.Criteria = []Criterion{{"c1", text}}
		long.Tasks = []PlanTask{{ID: "t1", Title: "Add cart endpoint", Scope: []string{"api/cart.go"}}, {ID: "t2", Title: "Show total"}}
		_, q := planGateState(long)
		a := planAnswers(map[string]float64{"covers_c1": 0.1, "open_questions": 0.9, "scope_creep": 0.9})
		for _, id := range []string{"covers_c2", "verified_c2"} {
			delete(a, id)
		}
		got := decidePlanGate(long, q, a, nil, false)
		for _, want := range []string{"1. ", "2. ", "3. ", "c1 (" + text + ")", `"Add cart endpoint" (api/cart.go)`, `"Show total"`} {
			if !strings.Contains(got.Note, want) {
				t.Errorf("note %q missing %q", got.Note, want)
			}
		}
	})
}

var (
	gapMissing  = convergeGap{ID: "C1-G1", Type: "missing", Layer: "code", Cites: "c1", Evidence: "cart.go:10", Fix: "add AddItem"}
	gapPartial  = convergeGap{ID: "C1-G2", Type: "partial", Layer: "code", Cites: "spec §2", Evidence: "cart.go:20", Fix: "tidy"}
	gapContract = convergeGap{ID: "C1-G3", Type: "contradicts", Layer: "contract", Cites: "spec §3", Evidence: "cart.go:30", Fix: "reword"}
	gapACOnly   = convergeGap{ID: "C1-G4", Type: "partial", Layer: "code", Cites: "c2, spec §1", Evidence: "total.go:1", Fix: "sum"}
)

func convergeAnswers(over map[string]float64) map[string]float64 {
	a := map[string]float64{"met_c1": 0.9, "met_c2": 0.95, "gap_valid_C1-G1": 0.05, "gap_valid_C1-G4": 0.05}
	for k, v := range over {
		a[k] = v
	}
	return a
}

func convergeInput(gaps ...convergeGap) ConvergeGateInput {
	return ConvergeGateInput{Criteria: testCriteria, Gaps: gaps}
}

func TestBlockingGaps(t *testing.T) {
	got := blockingGaps([]convergeGap{gapMissing, gapPartial, gapContract, gapACOnly,
		{ID: "x", Type: "unrequested", Layer: "code", Cites: "C2"}, {ID: "y", Type: "contradicts", Layer: "requirement", Cites: "spec"}}, testCriteria)
	var ids []string
	for _, g := range got {
		ids = append(ids, g.ID)
	}
	if !reflect.DeepEqual(ids, []string{"C1-G1", "C1-G4", "x"}) {
		t.Fatalf("a code missing/contradicts gap or any gap citing a criterion blocks: %v", ids)
	}
}

func TestDecideConvergeGate(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name     string
		gaps     []convergeGap
		answers  map[string]float64
		mod      func(*ConvergeGateInput)
		err      error
		over     bool
		failed   bool
		blockers int
		budget   bool
		outcome  string
		row      int
		noteHas  []string
	}{
		{"row1 checks failed", nil, convergeAnswers(nil), nil, nil, false, true, 0, true, GateLoop, 1, nil},
		{"row1 blockers", nil, convergeAnswers(nil), nil, nil, false, false, 2, true, GateLoop, 1, nil},
		{"row1 beats judge error", nil, nil, nil, boom, true, true, 0, true, GateLoop, 1, nil},
		{"row2 judge error", nil, nil, nil, boom, false, false, 0, true, GateEscalate, 2, []string{"boom"}},
		{"row2 no judge", nil, nil, nil, errNoJudge, false, false, 0, true, GateEscalate, 2, []string{"jev is not configured"}},
		{"row2 over limit", nil, convergeAnswers(nil), nil, nil, true, false, 0, true, GateEscalate, 2, []string{"context limit"}},
		{"row2 unanswered met", nil, map[string]float64{"met_c1": 0.9}, nil, nil, false, false, 0, true, GateEscalate, 2, []string{"met_c2"}},
		{"row2 unanswered gap_valid", []convergeGap{gapMissing}, map[string]float64{"met_c1": 0.9, "met_c2": 0.9}, nil, nil, false, false, 0, true, GateEscalate, 2, []string{"gap_valid_C1-G1"}},
		{"row2 no criteria", nil, convergeAnswers(nil), func(in *ConvergeGateInput) { in.Criteria = nil }, nil, false, false, 0, true, GateEscalate, 2, []string{"no acceptance criteria to judge against"}},
		{"row3 valid gap appends", []convergeGap{gapMissing}, convergeAnswers(map[string]float64{"gap_valid_C1-G1": 0.9}), nil, nil, false, false, 0, true, GateAppend, 3, []string{"gap C1-G1"}},
		{"row3 valid gap citing a criterion appends", []convergeGap{gapACOnly}, convergeAnswers(map[string]float64{"gap_valid_C1-G4": 0.9}), nil, nil, false, false, 0, true, GateAppend, 3, nil},
		{"row3 unmet criterion appends", nil, convergeAnswers(map[string]float64{"met_c2": 0.1}), nil, nil, false, false, 0, true, GateAppend, 3, []string{"does not implement acceptance criterion c2 (show the cart total)"}},
		{"row3 beats row4", nil, convergeAnswers(map[string]float64{"met_c1": 0.5, "met_c2": 0.1}), nil, nil, false, false, 0, true, GateAppend, 3, nil},
		{"row3 no budget escalates", []convergeGap{gapMissing}, convergeAnswers(map[string]float64{"gap_valid_C1-G1": 0.9}), nil, nil, false, false, 0, false, GateEscalate, 3, []string{"gap C1-G1"}},
		{"row3 non-blocking gaps are not asked", []convergeGap{gapPartial, gapContract}, convergeAnswers(nil), nil, nil, false, false, 0, true, GateConverged, 5, nil},
		{"row4 unsettled met escalates", nil, convergeAnswers(map[string]float64{"met_c1": 0.5}), nil, nil, false, false, 0, true, GateEscalate, 4, []string{"it is unclear whether the diff implements acceptance criterion c1 (add items to the cart)"}},
		{"row4 unsettled met escalates even without budget", nil, convergeAnswers(map[string]float64{"met_c1": 0.5}), nil, nil, false, false, 0, false, GateEscalate, 4, nil},
		{"unsettled gap_valid is a note, not an append", []convergeGap{gapMissing}, convergeAnswers(map[string]float64{"gap_valid_C1-G1": 0.5}), nil, nil, false, false, 0, true, GateConverged, 5, nil},
		{"row5 converged", nil, convergeAnswers(nil), nil, nil, false, false, 0, true, GateConverged, 5, nil},
		{"row5 gap judged not real", []convergeGap{gapMissing}, convergeAnswers(nil), nil, nil, false, false, 0, true, GateConverged, 5, nil},
		{"row5 converged without budget", nil, convergeAnswers(nil), nil, nil, false, false, 0, false, GateConverged, 5, nil},
		{"edge met 0.7 unsettled", nil, convergeAnswers(map[string]float64{"met_c1": 0.7}), nil, nil, false, false, 0, true, GateEscalate, 4, nil},
		{"edge met 0.71 yes", nil, convergeAnswers(map[string]float64{"met_c1": 0.71}), nil, nil, false, false, 0, true, GateConverged, 5, nil},
		{"edge met 0.3 unsettled", nil, convergeAnswers(map[string]float64{"met_c1": 0.3}), nil, nil, false, false, 0, true, GateEscalate, 4, nil},
		{"edge met 0.29 no", nil, convergeAnswers(map[string]float64{"met_c1": 0.29}), nil, nil, false, false, 0, true, GateAppend, 3, nil},
		{"edge gap_valid 0.71 yes", []convergeGap{gapMissing}, convergeAnswers(map[string]float64{"gap_valid_C1-G1": 0.71}), nil, nil, false, false, 0, true, GateAppend, 3, nil},
		{"edge gap_valid 0.7 unsettled", []convergeGap{gapMissing}, convergeAnswers(map[string]float64{"gap_valid_C1-G1": 0.7}), nil, nil, false, false, 0, true, GateConverged, 5, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := convergeInput(c.gaps...)
			if c.mod != nil {
				c.mod(&in)
			}
			_, asked := convergeGateState(in)
			got := decideConvergeGate(in, asked, c.answers, c.err, c.over, c.failed, c.blockers, c.budget)
			if got.Outcome != c.outcome || got.Row != c.row {
				t.Fatalf("got %+v, want %s row %d", got, c.outcome, c.row)
			}
			for _, s := range c.noteHas {
				if !strings.Contains(got.Note, s) {
					t.Errorf("note %q missing %q", got.Note, s)
				}
			}
		})
	}
}

func TestDecideConvergeGateReportsWhatIsValid(t *testing.T) {
	in := convergeInput(gapMissing, gapACOnly)
	_, asked := convergeGateState(in)
	got := decideConvergeGate(in, asked, convergeAnswers(map[string]float64{"gap_valid_C1-G1": 0.9, "gap_valid_C1-G4": 0.5, "met_c2": 0.1}), nil, false, false, 0, true)
	if !reflect.DeepEqual(got.Valid, []string{"C1-G1"}) || !reflect.DeepEqual(got.Unmet, []string{"c2"}) || len(got.Builder) != 1 || !strings.Contains(got.Builder[0], "C1-G4") {
		t.Fatalf("valid, unmet and the unsettled gap's note: %+v", got)
	}
}

func TestDecideConvergeGateAskedQuestions(t *testing.T) {
	in := convergeInput(gapMissing)
	_, asked := convergeGateState(in)
	for id := range asked {
		a := convergeAnswers(nil)
		delete(a, id)
		got := decideConvergeGate(in, asked, a, nil, false, false, 0, true)
		if got.Outcome != GateEscalate || got.Row != 2 || !strings.Contains(got.Note, id) {
			t.Errorf("without %s: %+v", id, got)
		}
	}
	a := convergeAnswers(map[string]float64{"met_c9": 0, "bogus": 1})
	if got := decideConvergeGate(in, asked, a, nil, false, false, 0, true); got.Outcome != GateConverged {
		t.Fatalf("extra answers must be ignored: %+v", got)
	}
}

func TestJudgeThresholds(t *testing.T) {
	for p, want := range map[float64]verdict{0: vNo, 0.29: vNo, 0.3: vUnsettled, 0.5: vUnsettled, 0.7: vUnsettled, 0.71: vYes, 1: vYes} {
		if got := judge(p); got != want {
			t.Errorf("judge(%v) = %v, want %v", p, got, want)
		}
	}
}

func TestSplitCriteria(t *testing.T) {
	cases := []struct {
		name, in string
		want     []Criterion
	}{
		{"dashes", "- add item\n- remove item", []Criterion{{"c1", "add item"}, {"c2", "remove item"}}},
		{"stars and blanks", "* a\n\n* b\n", []Criterion{{"c1", "a"}, {"c2", "b"}}},
		{"numbered", "1. first\n2) second\n10. tenth", []Criterion{{"c1", "first"}, {"c2", "second"}, {"c3", "tenth"}}},
		{"indented mixed", "  - a\n  1. b", []Criterion{{"c1", "a"}, {"c2", "b"}}},
		{"continuation", "- a\n  more a\n- b", []Criterion{{"c1", "a more a"}, {"c2", "b"}}},
		{"preamble dropped", "Must:\n- a", []Criterion{{"c1", "a"}}},
		{"plain text fallback", "Cart totals update when items change.", []Criterion{{"c1", "Cart totals update when items change."}}},
		{"multi-line plain fallback", "line one\nline two", []Criterion{{"c1", "line one\nline two"}}},
		{"dash without space is not a bullet", "-5 degrees is the minimum", []Criterion{{"c1", "-5 degrees is the minimum"}}},
		{"empty", "  \n ", []Criterion{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := splitCriteria(c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestPlanGateState(t *testing.T) {
	state, qs := planGateState(PlanGateInput{
		Request:  "Cart\nadd totals",
		Criteria: []Criterion{{"c2", "b"}, {"c1", "a"}},
		Brief:    "brief",
		Spec:     "spec",
		Tasks:    []PlanTask{{ID: "t2", Title: "T2"}, {ID: "t1", Title: "T1", Scope: []string{"cart.go"}, Deps: []string{"t2"}}},
		Findings: testFindings,
	})
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"acceptance_criteria":{"c2":"b","c1":"a"}`) {
		t.Errorf("criteria order lost: %s", s)
	}
	if strings.Index(s, `"t2"`) > strings.Index(s, `"t1"`) || !strings.Contains(s, `"scope":[]`) {
		t.Errorf("plan order or empty scope wrong: %s", s)
	}
	for _, k := range []string{"request", "brief", "spec", "plan", "analysis"} {
		if _, ok := state[k]; !ok {
			t.Errorf("state missing %s", k)
		}
	}
	want := []string{"covers_c1", "covers_c2", "verified_c1", "verified_c2", "critical_resolved", "open_questions", "scope_creep", "touches_schema", "touches_authentication", "touches_authorization", "touches_payments", "touches_secrets"}
	if len(qs) != len(want) {
		t.Fatalf("questions: %d, want %d", len(qs), len(want))
	}
	for _, id := range want {
		q, ok := qs[id]
		if !ok || q.Instructions == "" || q.Criteria == nil || q.Criteria.True == "" || q.Criteria.False == "" {
			t.Errorf("question %s incomplete: %+v", id, q)
		}
	}
	if !strings.Contains(qs["covers_c1"].Instructions, "acceptance_criteria.c1") {
		t.Errorf("covers wording: %s", qs["covers_c1"].Instructions)
	}
}

func TestConvergeGateState(t *testing.T) {
	state, qs := convergeGateState(ConvergeGateInput{
		Criteria: []Criterion{{"c1", "a"}, {"c2", "b"}},
		Gaps:     []convergeGap{gapMissing, gapPartial},
		Diff:     []FileDiff{{Path: "a.go", Added: 1, Hunks: "+x\n"}},
	})
	for _, k := range []string{"acceptance_criteria", "spec", "plan", "diff", "diff_trimmed", "checks", "open_findings", "gaps"} {
		if _, ok := state[k]; !ok {
			t.Errorf("state missing %s", k)
		}
	}
	for _, id := range []string{"met_c1", "met_c2", "gap_valid_C1-G1"} {
		if q, ok := qs[id]; !ok || q.Criteria == nil || q.Criteria.True == "" || q.Criteria.False == "" {
			t.Errorf("question %s missing or without criteria", id)
		}
	}
	if len(qs) != 3 {
		t.Errorf("one question per blocking gap and per criterion, got %d", len(qs))
	}
	if q := qs["met_c1"]; !strings.Contains(q.Instructions, "diff implement") {
		t.Errorf("met_ must judge the diff: %+v", q)
	}
	raw, _ := json.Marshal(state)
	for _, want := range []string{`"checks":[]`, `"open_findings":[]`, `"diff_trimmed":[]`, `"plan":{}`, `"id":"C1-G2"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("state lacks %s: %s", want, raw)
		}
	}
}

func TestTrimHunks(t *testing.T) {
	t.Run("fits", func(t *testing.T) {
		files := []FileDiff{{Path: "a", Hunks: "l1\nl2\n"}, {Path: "b", Hunks: "m1\n"}}
		out, trimmed := trimHunks(files, 100)
		if len(trimmed) != 0 || !reflect.DeepEqual(out, files) {
			t.Fatalf("out=%+v trimmed=%v", out, trimmed)
		}
	})
	t.Run("a cut file takes the leftover budget, cut at a line boundary", func(t *testing.T) {
		line := strings.Repeat("x", 9) + "\n" // 10 bytes
		big := FileDiff{Path: "big", Added: 50, Hunks: strings.Repeat(line, 50)}
		out, trimmed := trimHunks([]FileDiff{big}, 105) // cap 26 first, then the whole budget
		if out[0].Hunks != strings.Repeat(line, 10) || out[0].Added != 50 || !reflect.DeepEqual(trimmed, []string{"big"}) {
			t.Fatalf("out=%+v trimmed=%v", out, trimmed)
		}
	})
	t.Run("the leftover goes to cut files in order and stops at the budget", func(t *testing.T) {
		line := strings.Repeat("x", 9) + "\n"
		files := []FileDiff{
			{Path: "big1", Hunks: strings.Repeat(line, 30)},
			{Path: "tiny", Hunks: "t\n"},
			{Path: "big2", Hunks: strings.Repeat(line, 30)},
		}
		out, trimmed := trimHunks(files, 200) // cap 50: big1 50, tiny 2, big2 50; 98 left
		if len(out[0].Hunks) != 140 || len(out[2].Hunks) != 50 || out[1].Hunks != "t\n" || !reflect.DeepEqual(trimmed, []string{"big1", "big2"}) {
			t.Fatalf("lens=%d/%d/%d trimmed=%v", len(out[0].Hunks), len(out[1].Hunks), len(out[2].Hunks), trimmed)
		}
		if usedHunks(out) > 200 {
			t.Fatalf("over budget: %d", usedHunks(out))
		}
	})
	t.Run("a file the leftover fully covers is no longer trimmed", func(t *testing.T) {
		out, trimmed := trimHunks([]FileDiff{{Path: "a", Hunks: "xxxx\nyyyy\n"}}, 20)
		if out[0].Hunks != "xxxx\nyyyy\n" || len(trimmed) != 0 {
			t.Fatalf("out=%+v trimmed=%v", out, trimmed)
		}
	})
	t.Run("one large file does not starve the rest", func(t *testing.T) {
		line := strings.Repeat("x", 9) + "\n"
		files := []FileDiff{
			{Path: "big1", Hunks: strings.Repeat(line, 100)},
			{Path: "big2", Hunks: strings.Repeat(line, 100)},
			{Path: "big3", Hunks: strings.Repeat(line, 100)},
			{Path: "big4", Hunks: strings.Repeat(line, 100)},
			{Path: "small", Hunks: "keep me\n"},
		}
		out, trimmed := trimHunks(files, 400) // cap 100 each; four fill the budget exactly
		if out[4].Hunks != "" {
			t.Fatalf("budget should be spent: %q", out[4].Hunks)
		}
		files[3].Hunks = "tiny\n"
		out, trimmed = trimHunks(files, 400)
		if out[4].Hunks != "keep me\n" || len(out[0].Hunks) != 180 || len(out[1].Hunks) != 100 || !reflect.DeepEqual(trimmed, []string{"big1", "big2", "big3"}) {
			t.Fatalf("out=%+v trimmed=%v", out, trimmed)
		}
	})
	t.Run("lock and generated files keep path and counts but lose hunks", func(t *testing.T) {
		files := []FileDiff{
			{Path: "web/package-lock.json", Added: 900, Removed: 3, Hunks: "+lock\n"},
			{Path: "go.sum", Added: 2, Hunks: "+h1\n"},
			{Path: "yarn.lock", Added: 1, Hunks: "+y\n"},
			{Path: "pnpm-lock.yaml", Added: 1, Hunks: "+p\n"},
			{Path: "Cargo.lock", Added: 1, Hunks: "+c\n"},
			{Path: "vendor/x/y.go", Added: 1, Hunks: "+v\n"},
			{Path: "web/dist/app.js", Added: 1, Hunks: "+d\n"},
			{Path: "src/real.go", Added: 1, Hunks: "+r\n"},
			{Path: "notvendor/a.go", Added: 1, Hunks: "+n\n"},
		}
		out, trimmed := trimHunks(files, 1000)
		for i := 0; i < 7; i++ {
			if out[i].Hunks != "" || out[i].Path != files[i].Path || out[i].Added != files[i].Added {
				t.Errorf("file %d not stripped: %+v", i, out[i])
			}
		}
		if out[0].Added != 900 || out[0].Removed != 3 || out[7].Hunks != "+r\n" || out[8].Hunks != "+n\n" {
			t.Fatalf("out=%+v", out)
		}
		if len(trimmed) != 7 || trimmed[0] != "web/package-lock.json" {
			t.Fatalf("trimmed=%v", trimmed)
		}
	})
	t.Run("keeps counts and does not mutate input", func(t *testing.T) {
		in := []FileDiff{{Path: "a", Added: 3, Removed: 1, Hunks: "xxxx\nyyyy\nzzzz\n"}}
		out, _ := trimHunks(in, 8) // cap 2 keeps nothing; the leftover keeps one line
		if out[0].Added != 3 || out[0].Removed != 1 || out[0].Hunks != "xxxx\n" || in[0].Hunks != "xxxx\nyyyy\nzzzz\n" {
			t.Fatalf("out=%+v in=%+v", out, in)
		}
	})
	t.Run("zero budget", func(t *testing.T) {
		out, trimmed := trimHunks([]FileDiff{{Path: "a", Hunks: "l1\n"}}, 0)
		if out[0].Hunks != "" || len(trimmed) != 1 {
			t.Fatalf("out=%+v trimmed=%v", out, trimmed)
		}
	})
	t.Run("through convergeGateState", func(t *testing.T) {
		big := []FileDiff{{Path: "big.go", Hunks: strings.Repeat("0123456789\n", diffHunkBudget/11+100)}}
		state, _ := convergeGateState(ConvergeGateInput{Criteria: []Criterion{{"c1", "a"}}, Diff: big})
		if tr := state["diff_trimmed"].([]string); len(tr) != 1 || tr[0] != "big.go" {
			t.Fatalf("diff_trimmed=%v", tr)
		}
		if n := len(state["diff"].([]FileDiff)[0].Hunks); n > diffHunkBudget {
			t.Fatalf("hunks %d > budget", n)
		}
	})
}

func TestOverLimit(t *testing.T) {
	small := map[string]Noul{"q": {Instructions: "short"}}
	questionCap := maxQuestionTokens * limitMarginPct / 100
	if overLimit(map[string]any{"a": "b"}, small) {
		t.Fatal("small request flagged")
	}
	if !overLimit(map[string]any{"a": strings.Repeat("x", 4*questionCap)}, small) {
		t.Fatal("state + question over 90% of 32k not flagged")
	}
	if overLimit(map[string]any{"a": strings.Repeat("x", 4*(questionCap-100))}, small) {
		t.Fatal("state just under the cap flagged")
	}
	many := map[string]Noul{}
	for i := 0; i < 40; i++ {
		many[string(rune('a'+i%26))+strings.Repeat("k", i)] = Noul{Instructions: strings.Repeat("y", 6000)}
	}
	if !overLimit(map[string]any{"a": strings.Repeat("x", 4*20000)}, many) {
		t.Fatal("request over the cap not flagged")
	}
}

func TestEstimateTokensDoesNotCountHTMLEscaping(t *testing.T) {
	code := strings.Repeat("<>&\t", 20000) // 80k chars; json.Marshal would escape to ~400k bytes
	state := map[string]any{"diff": code}
	if got := estimateTokens(state); got > 80000*5/4/4+10 {
		t.Fatalf("estimate %d counts escaping", got)
	}
	if overLimit(state, map[string]Noul{"q": {Instructions: "short"}}) {
		t.Fatal("diff with <, > & and tabs flagged although it fits")
	}
}

func TestGateEvent(t *testing.T) {
	state := map[string]any{"request": "r"}
	qs := map[string]Noul{"scope_creep": {Instructions: "q"}}
	wire := json.RawMessage(`{"model":"jev-1.14.0","state":{"request":"r"},"questions":{"scope_creep":{"type":"noul","instructions":"q"}}}`)
	res := JudgeResult{
		Answers: map[string]float64{"scope_creep": 0.9}, Model: "jev-1.14.0-r2", RequestedModel: "jev-1.14.0",
		Request: wire, Usage: Usage{InputTokens: 360, OutputTokens: 39},
	}
	ev := gateEvent("plan", state, qs, res, GateOutcome{Outcome: GateRevise, Row: 3, Note: "tasks overlap"}, nil)
	if !reflect.DeepEqual(ev["request"], wire) || !reflect.DeepEqual(ev["answers"], res.Answers) {
		t.Fatalf("payload: %+v", ev)
	}
	if _, ok := ev["unsent_request"]; ok {
		t.Fatal("sent request must not also be recorded as unsent")
	}
	if ev["gate"] != "plan" || ev["requested_model"] != "jev-1.14.0" || ev["model"] != "jev-1.14.0-r2" || ev["usage"] != res.Usage ||
		ev["row"] != 3 || ev["outcome"] != GateRevise || ev["note"] != "tasks overlap" {
		t.Fatalf("payload: %+v", ev)
	}
	if _, ok := ev["error"]; ok {
		t.Fatal("no error expected")
	}
	raw, err := json.Marshal(ev)
	if err != nil || !strings.Contains(string(raw), `"request":`+string(wire)) {
		t.Fatalf("request not embedded verbatim: %s (%v)", raw, err)
	}
	ev = gateEvent("done", state, qs, JudgeResult{}, GateOutcome{Outcome: GateEscalate, Row: 2, Note: "x"}, errNoJudge)
	if ev["error"] != "jev is not configured" {
		t.Fatalf("error not recorded: %+v", ev)
	}
	if _, ok := ev["request"]; ok {
		t.Fatal("nothing was sent")
	}
	un := ev["unsent_request"].(map[string]any)
	if !reflect.DeepEqual(un["state"], state) || !reflect.DeepEqual(un["questions"], qs) {
		t.Fatalf("unsent_request: %+v", un)
	}
	if _, err := json.Marshal(ev); err != nil {
		t.Fatal(err)
	}
}

func TestNoulJSONShape(t *testing.T) {
	raw, _ := json.Marshal(map[string]Noul{"a": {Instructions: "q", Criteria: &NoulCriteria{True: "t", False: "f"}}, "b": {Instructions: "q"}})
	if string(raw) != `{"a":{"instructions":"q","criteria":{"true":"t","false":"f"}},"b":{"instructions":"q"}}` {
		t.Fatalf("shape: %s", raw)
	}
}

func TestUsageJSONTags(t *testing.T) {
	raw, _ := json.Marshal(Usage{InputTokens: 3, OutputTokens: 4})
	if string(raw) != `{"input_tokens":3,"output_tokens":4}` {
		t.Fatalf("usage json: %s", raw)
	}
}
