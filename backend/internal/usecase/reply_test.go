package usecase

import (
	"strings"
	"testing"
)

func TestDecodeReplyPicksTheValidFinalAnswer(t *testing.T) {
	text := "For example a plan looks like {\"decision\":\"MAYBE\"}.\n" +
		"Here is mine:\n```json\n{\"decision\":\"APPROVE\",\"summary\":\"s\",\"tasks\":[{\"id\":\"t1\",\"title\":\"Add }{ parser\",\"objective\":\"handle \\\"{\\\" in strings\",\"role\":\"builder\"}]}\n```"
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
