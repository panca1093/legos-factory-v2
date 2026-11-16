package usecase

import (
	"slices"
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
	case model.AgentChat:
		chain = []string{s.ChatModel, s.ArchitectModel}
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

// ResolveAgentModel is ResolveModel held to the role's agent: a ref the agent's catalog lacks (a model of another
// agent, or one it no longer offers) gives way to the catalog's first model. An empty catalog trusts the stored ref.
func ResolveAgentModel(role model.AgentRole, wo *model.WorkOrder, s *model.Settings, catalog []string) string {
	if wo == nil {
		wo = &model.WorkOrder{}
	}
	ref := ResolveModel(role, wo, s, nil)
	if len(catalog) == 0 || slices.Contains(catalog, ref) {
		return ref
	}
	return catalog[0]
}

// SplitModelRef splits "provider/model" into its parts.
func SplitModelRef(ref string) (provider, modelID string) {
	if p, m, ok := strings.Cut(ref, "/"); ok {
		return p, m
	}
	return "", ref
}

// ChatAuto is the model a chat on "auto" runs: the workspace chat model when set, else fallback.
func ChatAuto(s *model.Settings, fallback string) string {
	if s != nil && s.ChatModel != "" {
		return s.ChatModel
	}
	return fallback
}
