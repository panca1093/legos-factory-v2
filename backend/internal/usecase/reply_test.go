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
	if err := decodeReply(`{"outcome":" ","constraints":["a"]}`, &in); err == nil {
		t.Fatal("a blank outcome must fail")
	}
	if err := decodeReply(`{"outcome":"Build X","open_questions":[{"question":"Which?"}]}`, &in); err == nil {
		t.Fatal("an open question without a recommended answer must fail")
	}
	if err := decodeReply(`{"outcome":"Build X","why":"W","domain_invariants":["I"],"surfaces_touched":["S"],"constraints":["keep Y"],"out_of_scope":["O"],"assumptions":["A"],
		"open_questions":[{"question":"q1","recommended":"r"},{"question":"q2","recommended":"r"},{"question":"q3","recommended":"r"},{"question":"q4","recommended":"r"}]}`, &in); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Build X", "Why: W", "Domain invariants:\n- I", "Surfaces touched:\n- S", "Constraints:\n- keep Y",
		"Out of scope:\n- O", "Assumptions:\n- A", "Open questions:\n- q1 Recommended: r.\n- q2"} {
		if !strings.Contains(in.text(), want) {
			t.Fatalf("brief text lacks %q:\n%s", want, in.text())
		}
	}
	if len(in.OpenQuestions) != 3 || strings.Contains(in.text(), "q4") {
		t.Fatalf("open questions are capped at 3: %+v", in.OpenQuestions)
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
	if err := decodeReply(`{"coverage":[]}`, &r); err == nil || !strings.Contains(err.Error(), "findings") {
		t.Fatalf("an analysis without findings must fail: %v", err)
	}
	finding := func(sev, layer, cites string) string {
		return `{"findings":[{"id":"F1","severity":"` + sev + `","layer":"` + layer + `","cites":"` + cites + `","issue":"no error path","fix":"add a rule"}]}`
	}
	for name, text := range map[string]string{
		"unknown severity":        finding("blocker", "contract", "c1"),
		"unknown layer":           finding("high", "code", "c1"),
		"critical without a cite": finding("critical", "contract", " "),
		"bad coverage":            `{"findings":[],"coverage":[{"criterion":"c1","verification":"maybe"}]}`,
		"coverage without id":     `{"findings":[],"coverage":[{"verification":"test"}]}`,
		"bad resolved status":     `{"findings":[],"resolved":[{"id":"F1","status":"fixed"}]}`,
		"eleven findings":         `{"findings":[` + strings.TrimSuffix(strings.Repeat(`{"id":"F","severity":"low","layer":"contract","issue":"x"},`, 11), ",") + `]}`,
	} {
		if err := decodeReply(text, &r); err == nil {
			t.Errorf("%s must fail", name)
		}
	}
	if err := decodeReply(finding("high", "contract", ""), &r); err != nil {
		t.Fatalf("a high finding needs no cite: %v", err)
	}
	if err := decodeReply(`{"findings":[{"id":"F1","severity":"critical","layer":"requirement","cites":"brief: constraint 1","issue":"silent on limits","fix":"cap at 5MB","question":"Which cap?"}],"coverage":[{"criterion":"c1","verification":"test"},{"criterion":"c2","verification":"missing"}],"resolved":[{"id":"F0","status":"resolved"}]}`, &r); err != nil {
		t.Fatal(err)
	}
	if want := "- F1 [critical, requirement] brief: constraint 1: silent on limits Fix: cap at 5MB\nCoverage: c1=test c2=missing"; r.text() != want {
		t.Fatalf("text = %q", r.text())
	}
	if err := decodeReply(`{"findings":[],"coverage":[]}`, &r); err != nil || r.text() != "No findings." {
		t.Fatalf("empty analysis is valid: %v %q", err, r.text())
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

func TestConvergeReplySchema(t *testing.T) {
	var v convergeReply
	for name, text := range map[string]string{
		"no gaps key":    `{"converged":true}`,
		"unknown type":   `{"gaps":[{"type":"wrong","layer":"code","evidence":"a.go"}]}`,
		"unknown layer":  `{"gaps":[{"type":"missing","layer":"ui","evidence":"a.go"}]}`,
		"no evidence":    `{"gaps":[{"type":"missing","layer":"code"}]}`,
		"gaps not array": `{"gaps":"none"}`,
		"over the cap":   `{"gaps":[` + strings.Repeat(`{"type":"missing","layer":"code","evidence":"e"},`, maxConvergeGaps) + `{"type":"missing","layer":"code","evidence":"e"}]}`,
	} {
		if err := decodeReply(text, &v); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	if err := decodeReply(`{"gaps":[],"converged":true}`, &v); err != nil || !v.Converged {
		t.Fatalf("an empty gap list is valid: %v", err)
	}
	if err := decodeReply(`{"gaps":[{"type":" Missing ","layer":"CODE","cites":" c1 ","evidence":" a.go:1 ","fix":" f "}]}`, &v); err != nil {
		t.Fatal(err)
	}
	if g := v.Gaps[0]; g.Type != "missing" || g.Layer != "code" || g.Cites != "c1" || g.Evidence != "a.go:1" || g.Fix != "f" {
		t.Fatalf("gap must be normalised: %+v", g)
	}
}
