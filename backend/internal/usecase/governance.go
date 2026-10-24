package usecase

import (
	"strings"

	"legos-factory/backend/internal/model"
)

// ResolveModel picks the provider/model ref for a role: the work order's own
// choice (Builder/Inspector picked at creation, Foreman/Architect
// snapshotted from workspace policy), then the workspace default, then the
// first discovered model. "mock-local" never overrides a real model.
func ResolveModel(role model.AgentRole, wo *model.WorkOrder, s *model.Settings, discovered map[string]string) string {
	if s == nil {
		s = &model.Settings{}
	}
	var chain []string
	switch role {
	case model.AgentBuilder:
		chain = []string{wo.BuilderModel, s.BuilderModel}
	case model.AgentInspector:
		chain = []string{wo.InspectorModel, s.InspectorModel}
	case model.AgentForeman:
		chain = []string{wo.ForemanModel, s.ForemanModel}
	default: // architect, cataloger
		chain = []string{wo.ArchitectModel, s.ArchitectModel}
	}
	chain = append(chain, discovered[string(role)], discovered[string(model.AgentArchitect)])
	for _, ref := range chain {
		if ref != "" && ref != "mock-local" {
			return ref
		}
	}
	return ""
}

// SplitModelRef splits "provider/model" into its parts.
func SplitModelRef(ref string) (provider, modelID string) {
	if p, m, ok := strings.Cut(ref, "/"); ok {
		return p, m
	}
	return "", ref
}
