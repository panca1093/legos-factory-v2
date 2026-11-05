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
		"covers_c1": 0.95, "covers_c2": 0.9, "scope_creep": 0.05, "touches_schema": 0.02,
		"touches_sensitive": 0.01, "task_overlap": 0.1, "review_unresolved": 0.05,
	}
	for k, v := range over {
		a[k] = v
	}
	return a
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
		{"row2 sensitive", planAnswers(map[string]float64{"touches_sensitive": 0.71}), nil, false, 0, GateEscalate, 2, []string{"authentication"}},
		{"row2 beats row3", planAnswers(map[string]float64{"touches_schema": 0.9, "covers_c1": 0.1}), nil, false, 0, GateEscalate, 2, nil},
		{"row3 not covered", planAnswers(map[string]float64{"covers_c2": 0.1}), nil, false, 0, GateRevise, 3, []string{"criterion c2 is not covered"}},
		{"row3 scope creep", planAnswers(map[string]float64{"scope_creep": 0.8}), nil, false, 0, GateRevise, 3, []string{"does not ask for"}},
		{"row3 overlap", planAnswers(map[string]float64{"task_overlap": 0.8}), nil, false, 1, GateRevise, 3, []string{"tasks overlap"}},
		{"row3 review unresolved", planAnswers(map[string]float64{"review_unresolved": 0.8}), nil, false, 0, GateRevise, 3, []string{"Foreman review"}},
		{"row3 lists several", planAnswers(map[string]float64{"covers_c1": 0.0, "task_overlap": 1}), nil, false, 0, GateRevise, 3, []string{"criterion c1 is not covered", "tasks overlap"}},
		{"row3 includes unsettled too", planAnswers(map[string]float64{"covers_c1": 0.1, "scope_creep": 0.5}), nil, false, 0, GateRevise, 3, []string{"c1 is not covered", "unsettled"}},
		{"row3 escalates at budget", planAnswers(map[string]float64{"covers_c1": 0.1}), nil, false, 2, GateEscalate, 3, []string{"c1"}},
		{"row4 unsettled covers", planAnswers(map[string]float64{"covers_c2": 0.5}), nil, false, 0, GateRevise, 4, []string{"unclear whether acceptance criterion c2"}},
		{"row4 unsettled schema", planAnswers(map[string]float64{"touches_schema": 0.5}), nil, false, 1, GateRevise, 4, []string{"touches_schema"}},
		{"row4 escalates at budget", planAnswers(map[string]float64{"scope_creep": 0.4}), nil, false, 3, GateEscalate, 4, nil},
		{"row5 approve", planAnswers(nil), nil, false, 0, GateApprove, 5, nil},
		{"row5 approve with revisions spent", planAnswers(nil), nil, false, 2, GateApprove, 5, nil},
		{"edge covers 0.7 unsettled", planAnswers(map[string]float64{"covers_c1": 0.7}), nil, false, 0, GateRevise, 4, nil},
		{"edge covers 0.71 yes", planAnswers(map[string]float64{"covers_c1": 0.71}), nil, false, 0, GateApprove, 5, nil},
		{"edge covers 0.3 unsettled", planAnswers(map[string]float64{"covers_c1": 0.3}), nil, false, 0, GateRevise, 4, nil},
		{"edge covers 0.29 no", planAnswers(map[string]float64{"covers_c1": 0.29}), nil, false, 0, GateRevise, 3, nil},
		{"edge scope_creep 0.7 unsettled", planAnswers(map[string]float64{"scope_creep": 0.7}), nil, false, 0, GateRevise, 4, nil},
		{"edge scope_creep 0.3 unsettled", planAnswers(map[string]float64{"scope_creep": 0.3}), nil, false, 0, GateRevise, 4, nil},
		{"edge scope_creep 0.29 no", planAnswers(map[string]float64{"scope_creep": 0.29}), nil, false, 0, GateApprove, 5, nil},
		{"edge sensitive 0.7 unsettled not escalate-2", planAnswers(map[string]float64{"touches_sensitive": 0.7}), nil, false, 0, GateRevise, 4, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decidePlanGate(c.answers, c.err, c.over, c.revisions)
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

func doneAnswers(over map[string]float64) map[string]float64 {
	a := map[string]float64{"met_c1": 0.9, "met_c2": 0.95, "deviates": 0.05, "notes_unresolved": 0.1}
	for k, v := range over {
		a[k] = v
	}
	return a
}

func TestDecideDoneGate(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name     string
		answers  map[string]float64
		err      error
		over     bool
		failed   bool
		blockers int
		budget   bool
		outcome  string
		row      int
		noteHas  []string
	}{
		{"row1 checks failed", doneAnswers(nil), nil, false, true, 0, true, GateLoop, 1, nil},
		{"row1 blockers", doneAnswers(nil), nil, false, false, 2, true, GateLoop, 1, nil},
		{"row1 beats judge error", nil, boom, true, true, 0, true, GateLoop, 1, nil},
		{"row2 judge error", nil, boom, false, false, 0, true, GateEscalate, 2, []string{"boom"}},
		{"row2 over limit", doneAnswers(nil), nil, true, false, 0, true, GateEscalate, 2, []string{"context limit"}},
		{"row2 missing answers", map[string]float64{"met_c1": 0.9}, nil, false, false, 0, true, GateEscalate, 2, nil},
		{"row3 not met", doneAnswers(map[string]float64{"met_c2": 0.1}), nil, false, false, 0, true, GateFix, 3, []string{"criterion c2"}},
		{"row3 deviates", doneAnswers(map[string]float64{"deviates": 0.9}), nil, false, false, 0, true, GateFix, 3, []string{"spec does not describe"}},
		{"row3 notes unresolved", doneAnswers(map[string]float64{"notes_unresolved": 0.9}), nil, false, false, 0, true, GateFix, 3, []string{"Foreman notes"}},
		{"row3 no budget escalates", doneAnswers(map[string]float64{"deviates": 0.9}), nil, false, false, 0, false, GateEscalate, 3, nil},
		{"row4 unsettled met", doneAnswers(map[string]float64{"met_c1": 0.5}), nil, false, false, 0, true, GateFix, 4, []string{"met_c1"}},
		{"row4 unsettled deviates", doneAnswers(map[string]float64{"deviates": 0.5}), nil, false, false, 0, true, GateFix, 4, nil},
		{"row4 no budget escalates", doneAnswers(map[string]float64{"notes_unresolved": 0.5}), nil, false, false, 0, false, GateEscalate, 4, nil},
		{"row5 done", doneAnswers(nil), nil, false, false, 0, true, GateDone, 5, nil},
		{"row5 done without budget", doneAnswers(nil), nil, false, false, 0, false, GateDone, 5, nil},
		{"edge met 0.7 unsettled", doneAnswers(map[string]float64{"met_c1": 0.7}), nil, false, false, 0, true, GateFix, 4, nil},
		{"edge met 0.71 yes", doneAnswers(map[string]float64{"met_c1": 0.71}), nil, false, false, 0, true, GateDone, 5, nil},
		{"edge met 0.3 unsettled", doneAnswers(map[string]float64{"met_c1": 0.3}), nil, false, false, 0, true, GateFix, 4, nil},
		{"edge met 0.29 no", doneAnswers(map[string]float64{"met_c1": 0.29}), nil, false, false, 0, true, GateFix, 3, nil},
		{"edge deviates 0.7 unsettled", doneAnswers(map[string]float64{"deviates": 0.7}), nil, false, false, 0, true, GateFix, 4, nil},
		{"edge deviates 0.3 unsettled", doneAnswers(map[string]float64{"deviates": 0.3}), nil, false, false, 0, true, GateFix, 4, nil},
		{"edge deviates 0.29 no", doneAnswers(map[string]float64{"deviates": 0.29}), nil, false, false, 0, true, GateDone, 5, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decideDoneGate(c.answers, c.err, c.over, c.failed, c.blockers, c.budget)
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
		Request:       "Cart\nadd totals",
		Criteria:      []Criterion{{"c2", "b"}, {"c1", "a"}},
		Brief:         "brief",
		Spec:          "spec",
		Tasks:         []PlanTask{{ID: "t2", Title: "T2"}, {ID: "t1", Title: "T1", Scope: []string{"cart.go"}, Deps: []string{"t2"}}},
		ForemanReview: "ok",
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
	for _, k := range []string{"request", "brief", "spec", "plan", "foreman_review"} {
		if _, ok := state[k]; !ok {
			t.Errorf("state missing %s", k)
		}
	}
	want := []string{"covers_c1", "covers_c2", "scope_creep", "touches_schema", "touches_sensitive", "task_overlap", "review_unresolved"}
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

func TestDoneGateState(t *testing.T) {
	state, qs := doneGateState(DoneGateInput{
		Criteria: []Criterion{{"c1", "a"}, {"c2", "b"}},
		Diff:     []FileDiff{{Path: "a.go", Added: 1, Hunks: "+x\n"}},
	})
	for _, k := range []string{"acceptance_criteria", "spec", "diff", "diff_trimmed", "checks", "open_findings", "architect_report", "foreman_notes"} {
		if _, ok := state[k]; !ok {
			t.Errorf("state missing %s", k)
		}
	}
	for _, id := range []string{"met_c1", "met_c2", "deviates", "notes_unresolved"} {
		q, ok := qs[id]
		if !ok || q.Criteria == nil {
			t.Errorf("question %s missing or without criteria", id)
		}
	}
	if len(qs) != 4 {
		t.Errorf("questions: %d", len(qs))
	}
	raw, _ := json.Marshal(state)
	if !strings.Contains(string(raw), `"checks":[]`) || !strings.Contains(string(raw), `"diff_trimmed":[]`) {
		t.Errorf("nil slices must marshal as []: %s", raw)
	}
}

func TestTrimHunks(t *testing.T) {
	files := []FileDiff{
		{Path: "a", Hunks: "l1\nl2\n"},
		{Path: "b", Hunks: "m1\nm2\nm3\n"},
		{Path: "c", Hunks: "n1\n"},
	}
	t.Run("fits", func(t *testing.T) {
		out, trimmed := trimHunks(files, 100)
		if len(trimmed) != 0 || !reflect.DeepEqual(out, files) {
			t.Fatalf("out=%+v trimmed=%v", out, trimmed)
		}
	})
	t.Run("cuts at line boundary and drops the rest", func(t *testing.T) {
		out, trimmed := trimHunks(files, 10) // a=6, 4 left: "m1\nm" -> "m1\n"
		if out[0].Hunks != "l1\nl2\n" || out[1].Hunks != "m1\n" || out[2].Hunks != "" {
			t.Fatalf("out=%+v", out)
		}
		if !reflect.DeepEqual(trimmed, []string{"b", "c"}) {
			t.Fatalf("trimmed=%v", trimmed)
		}
	})
	t.Run("keeps counts and does not mutate input", func(t *testing.T) {
		in := []FileDiff{{Path: "a", Added: 3, Removed: 1, Hunks: "xxxx\nyyyy\n"}}
		out, _ := trimHunks(in, 5)
		if out[0].Added != 3 || out[0].Removed != 1 || out[0].Hunks != "xxxx\n" || in[0].Hunks != "xxxx\nyyyy\n" {
			t.Fatalf("out=%+v in=%+v", out, in)
		}
	})
	t.Run("zero budget", func(t *testing.T) {
		out, trimmed := trimHunks(files[:1], 0)
		if out[0].Hunks != "" || len(trimmed) != 1 {
			t.Fatalf("out=%+v trimmed=%v", out, trimmed)
		}
	})
	t.Run("through doneGateState", func(t *testing.T) {
		big := []FileDiff{{Path: "big.go", Hunks: strings.Repeat("0123456789\n", diffHunkBudget/11+100)}}
		state, _ := doneGateState(DoneGateInput{Criteria: []Criterion{{"c1", "a"}}, Diff: big})
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
	if overLimit(map[string]any{"a": "b"}, small) {
		t.Fatal("small request flagged")
	}
	// 32k tokens ≈ 128k bytes of state.
	if !overLimit(map[string]any{"a": strings.Repeat("x", 4*maxQuestionTokens)}, small) {
		t.Fatal("state + question over 32k not flagged")
	}
	if overLimit(map[string]any{"a": strings.Repeat("x", 4*(maxQuestionTokens-100))}, small) {
		t.Fatal("state just under 32k flagged")
	}
	// Many questions: each under 32k with the state, total over 64k.
	many := map[string]Noul{}
	for i := 0; i < 40; i++ {
		many[string(rune('a'+i%26))+strings.Repeat("k", i)] = Noul{Instructions: strings.Repeat("y", 6000)}
	}
	if !overLimit(map[string]any{"a": strings.Repeat("x", 4*20000)}, many) {
		t.Fatal("request over 64k not flagged")
	}
}

func TestGateEvent(t *testing.T) {
	state := map[string]any{"request": "r"}
	qs := map[string]Noul{"scope_creep": {Instructions: "q"}}
	ans := map[string]float64{"scope_creep": 0.9}
	ev := gateEvent("plan", state, qs, ans, "jev-1.14.0", GateOutcome{GateRevise, 3, "tasks overlap"}, nil)
	req := ev["request"].(map[string]any)
	if !reflect.DeepEqual(req["state"], state) || !reflect.DeepEqual(req["questions"], qs) || !reflect.DeepEqual(ev["answers"], ans) {
		t.Fatalf("payload: %+v", ev)
	}
	if ev["gate"] != "plan" || ev["model"] != "jev-1.14.0" || ev["row"] != 3 || ev["outcome"] != GateRevise || ev["note"] != "tasks overlap" {
		t.Fatalf("payload: %+v", ev)
	}
	if _, ok := ev["error"]; ok {
		t.Fatal("no error expected")
	}
	if _, err := json.Marshal(ev); err != nil {
		t.Fatal(err)
	}
	ev = gateEvent("done", state, qs, nil, "", GateOutcome{GateEscalate, 2, "x"}, errors.New("boom"))
	if ev["error"] != "boom" {
		t.Fatalf("error not recorded: %+v", ev)
	}
}
