package usecase

import (
	"strings"
	"testing"
)

func TestDecodeReplyPicksTheValidFinalAnswer(t *testing.T) {
	text := "For example a plan looks like {\"decision\":\"MAYBE\"}.\n" +
		"Here is mine:\n```json\n{\"decision\":\"APPROVE\",\"summary\":\"s\",\"spec\":\"sp\",\"tasks\":[{\"id\":\"t1\",\"title\":\"Add }{ parser\",\"objective\":\"handle \\\"{\\\" in strings\",\"role\":\"builder\"}]}\n```"
	var p planReply
	if err := decodeReply(text, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Decision != "APPROVE" || len(p.Tasks) != 1 || p.Tasks[0].Title != "Add }{ parser" {
		t.Fatalf("wrong candidate chosen: %+v", p)
	}
}

func TestDecodeReplyRejectsSchemaViolations(t *testing.T) {
	cases := map[string]string{
		"unknown decision":    `{"decision":"SHIP","tasks":[{"title":"a","objective":"b"}]}`,
		"approve w/o tasks":   `{"decision":"APPROVE","tasks":[]}`,
		"clarify w/o q":       `{"decision":"CLARIFY"}`,
		"blank task":          `{"decision":"APPROVE","tasks":[{"title":" ","objective":"b"}]}`,
		"path-like task id":   `{"decision":"APPROVE","tasks":[{"id":"../a","title":"a","objective":"b"}]}`,
		"git-ref task id":     `{"decision":"APPROVE","tasks":[{"id":"a b","title":"a","objective":"b"}]}`,
		"no json":             `I think it is fine.`,
		"unterminated object": `{"decision":"APPROVE"`,
	}
	for name, text := range cases {
		var p planReply
		if err := decodeReply(text, &p); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
		if p.Decision != "" || p.Tasks != nil {
			t.Errorf("%s: rejected reply must leave no partial values: %+v", name, p)
		}
	}
}

func TestIntakeAnswerAndSpecSchemas(t *testing.T) {
	var in intakeReply
	if err := decodeReply(`{"brief":" ","constraints":["a"]}`, &in); err == nil {
		t.Fatal("a blank brief must fail")
	}
	if err := decodeReply(`{"brief":"Build X","constraints":["keep Y"],"risks":["Z"],"suggested_task_shape":"small"}`, &in); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Build X", "Constraints:\n- keep Y", "Risks:\n- Z", "Suggested task shape: small"} {
		if !strings.Contains(in.text(), want) {
			t.Fatalf("brief text lacks %q:\n%s", want, in.text())
		}
	}
	var a foremanAnswerReply
	if err := decodeReply(`{"decision":"ANSWER"}`, &a); err == nil {
		t.Fatal("ANSWER without an answer must fail")
	}
	if err := decodeReply(`{"decision":"ESCALATE","reason":"product call"}`, &a); err != nil {
		t.Fatal(err)
	}
	var p planReply
	if err := decodeReply(`{"decision":"APPROVE","tasks":[{"title":"a","objective":"b"}]}`, &p); err == nil || !strings.Contains(err.Error(), "spec") {
		t.Fatalf("APPROVE without spec must fail: %v", err)
	}
	if err := decodeReply(`{"decision":"APPROVE","spec":"s","tasks":[{"title":"a","objective":"b","scope":["x.go"]}]}`, &p); err != nil || p.Tasks[0].Scope[0] != "x.go" {
		t.Fatalf("scope must decode: %v %+v", err, p)
	}
}

func TestReviewAndFindingsSchemas(t *testing.T) {
	var r reviewReply
	if err := decodeReply(`{"decision":"APPROVE"}`, &r); err == nil || !strings.Contains(err.Error(), "notes") {
		t.Fatalf("a review without notes must fail: %v", err)
	}
	if err := decodeReply(`{"notes":"ok","coverage":[{"criterion":"c1"}]}`, &r); err == nil || !strings.Contains(err.Error(), "coverage") {
		t.Fatalf("a coverage remark needs a criterion and a remark: %v", err)
	}
	if err := decodeReply(`{"notes":"Missing the failure path","coverage":[{"criterion":"c1","remark":"task t1"}]}`, &r); err != nil {
		t.Fatal(err)
	}
	if want := "Missing the failure path\n\nCoverage:\n- c1: task t1"; r.text() != want {
		t.Fatalf("text = %q", r.text())
	}
	var f findingsReply
	if err := decodeReply(`{"findings":[{"severity":"critical","title":"x"}]}`, &f); err == nil {
		t.Fatal("unknown severity must fail")
	}
	if err := decodeReply(`{"notes":"none"}`, &f); err == nil {
		t.Fatal("missing findings must fail")
	}
	if err := decodeReply(`{"findings":[]}`, &f); err != nil {
		t.Fatalf("empty findings are valid: %v", err)
	}
}

func TestVerifyReplySchema(t *testing.T) {
	var v verifyReply
	for name, text := range map[string]string{
		"no verdicts key":    `{"deviations":[]}`,
		"unknown status":     `{"verdicts":[{"criterion_id":"c1","status":"done"}]}`,
		"missing criterion":  `{"verdicts":[{"status":"met"}]}`,
		"missing status":     `{"verdicts":[{"criterion_id":"c1"}]}`,
		"verdicts not array": `{"verdicts":"met"}`,
	} {
		if err := decodeReply(text, &v); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	if err := decodeReply(`{"verdicts":[],"deviations":null}`, &v); err != nil {
		t.Fatalf("an empty verdict list is valid (the gate then asks for a fix): %v", err)
	}
	err := decodeReply(`{"verdicts":[
		{"criterion_id":" c1 ","status":"MET","evidence":"a.go"},
		{"criterion_id":"c2","status":"Not-Met","evidence":"b.go","gap":" caps too big ","fix":" per-section budgets "},
		{"criterion_id":"c3","status":"not met"},
		{"criterion_id":"c4","status":"Unclear"}],
		"deviations":["extra endpoint","  ",""]}`, &v)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range v.Verdicts {
		got = append(got, x.CriterionID+"="+x.Status)
	}
	if strings.Join(got, ",") != "c1=met,c2=not_met,c3=not_met,c4=unclear" {
		t.Fatalf("statuses must be normalised: %v", got)
	}
	if v.Verdicts[1].Gap != "caps too big" || v.Verdicts[1].Fix != "per-section budgets" {
		t.Fatalf("gap and fix must be kept and trimmed: %+v", v.Verdicts[1])
	}
	if len(v.Deviations) != 1 || v.Deviations[0] != "extra endpoint" {
		t.Fatalf("deviations: %q", v.Deviations)
	}
}

func TestValidationReplySchema(t *testing.T) {
	var v validationReply
	if err := decodeReply(`{"notes":" "}`, &v); err == nil {
		t.Fatal("blank notes must fail")
	}
	if err := decodeReply(`{"notes":"Criterion c2 is not met"}`, &v); err != nil || v.Notes != "Criterion c2 is not met" {
		t.Fatalf("%v %+v", err, v)
	}
}
