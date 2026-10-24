package usecase

import (
	"testing"

	"legos-factory/backend/internal/model"
)

func TestResolveModelPrecedence(t *testing.T) {
	discovered := map[string]string{"architect": "opencode/first"}
	s := &model.Settings{ForemanModel: "openai/gpt-5", ArchitectModel: "openai/gpt-5-mini", BuilderModel: "anthropic/sonnet"}
	wo := &model.WorkOrder{InspectorModel: "openai/o4", ForemanModel: "openai/gpt-5.1"}
	cases := []struct {
		role model.AgentRole
		want string
	}{
		{model.AgentForeman, "openai/gpt-5.1"},      // work-order snapshot wins
		{model.AgentArchitect, "openai/gpt-5-mini"}, // workspace default
		{model.AgentBuilder, "anthropic/sonnet"},    // workspace default
		{model.AgentInspector, "openai/o4"},         // per-order choice
		{model.AgentCataloger, "openai/gpt-5-mini"}, // follows the Architect
	}
	for _, c := range cases {
		if got := ResolveModel(c.role, wo, s, discovered); got != c.want {
			t.Errorf("%s: got %q want %q", c.role, got, c.want)
		}
	}
	if got := ResolveModel(model.AgentBuilder, &model.WorkOrder{BuilderModel: "mock-local"}, nil, discovered); got != "opencode/first" {
		t.Errorf("mock-local must fall through to discovery, got %q", got)
	}
	if p, m := SplitModelRef("openai/gpt-5"); p != "openai" || m != "gpt-5" {
		t.Errorf("split: %s %s", p, m)
	}
}
