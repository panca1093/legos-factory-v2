package usecase

import (
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestPromptsCarryClarificationAnswer(t *testing.T) {
	wo := &model.WorkOrder{Title: "t", ClarifQuestion: "round what?", ClarifAnswer: "the discount"}
	for name, p := range map[string]string{
		"plan":        planPrompt(wo, ""),
		"review":      foremanReviewPrompt(wo, nil),
		"implement":   implementPrompt(wo, model.Job{TaskID: "task-1"}),
		"remediation": implementPrompt(wo, model.Job{Payload: map[string]any{"reason": "fix"}}),
		"inspect":     inspectPrompt(wo, SpecialistPick{Specialty: "testing"}, nil),
	} {
		if !strings.Contains(p, "Q: round what?\nA: the discount") {
			t.Errorf("%s prompt lacks the clarification:\n%s", name, p)
		}
	}
}
