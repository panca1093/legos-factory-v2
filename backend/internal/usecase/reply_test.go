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
	if err := decodeReply(`{"decision":"REVISE"}`, &r); err == nil || !strings.Contains(err.Error(), "notes") {
		t.Fatalf("REVISE without notes must fail: %v", err)
	}
	if err := decodeReply(`{"decision":"CLARIFY","question_text":"Which window?"}`, &r); err != nil {
		t.Fatal(err)
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
