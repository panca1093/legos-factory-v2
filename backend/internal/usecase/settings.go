package usecase

import (
	"context"
	"fmt"
	"strings"

	"legos-factory/backend/internal/model"
)

type SettingsStore interface {
	Get(ctx context.Context) (*model.Settings, error)
	PatchSealed(ctx context.Context, p model.SettingsPatch, sealedJevKey []byte) error
}

const (
	minWorkers = 1
	maxWorkers = 16
)

// WorkspaceSettings applies the workspace policy: it validates a PATCH, seals
// the Jev key, fills the defaults a response shows and tells the running
// process (worker count, Jev judge) about the change.
type WorkspaceSettings struct {
	Store SettingsStore
	// Secrets seals the Jev key; nil refuses to store one.
	Secrets Sealer
	// EnvJevKey is TYPESAFE_API_KEY; it wins over a stored key.
	EnvJevKey string
	// DefaultAgent is the agent a role uses until settings say otherwise.
	DefaultAgent string
	// DefaultWorkers is the first-boot worker count (FACTORY_MAX_CONCURRENT_RUNS).
	DefaultWorkers int
	// OnChange runs after a successful PATCH with the settings now in force.
	OnChange func(*model.Settings)
}

func (w *WorkspaceSettings) defaultAgent() string {
	if _, ok := AgentByID(w.DefaultAgent); ok {
		return w.DefaultAgent
	}
	return "claude"
}

// Get returns the stored settings with the defaults filled in. The sealed key never leaves.
func (w *WorkspaceSettings) Get(ctx context.Context) (*model.Settings, error) {
	s, err := w.Store.Get(ctx)
	if err != nil {
		return nil, err
	}
	agents := map[string]string{}
	for _, r := range allRoles {
		agents[string(r)] = w.defaultAgent()
		if a, ok := s.RoleAgents[string(r)]; ok && a != "" {
			agents[string(r)] = a
		}
	}
	s.RoleAgents = agents
	perms := map[string]string{"chat": "ask"}
	if v := s.RolePermissions["chat"]; v == "ask" || v == "read" {
		perms["chat"] = v
	}
	s.RolePermissions = perms
	if s.MaxConcurrentRuns < minWorkers {
		s.MaxConcurrentRuns = min(max(w.DefaultWorkers, minWorkers), maxWorkers)
	}
	s.JevKeySet = len(s.JevKeySealed) > 0
	if !s.JevEnabledSet {
		s.JevEnabled = w.EnvJevKey != ""
	}
	return s, nil
}

// JevKey is the key the Jev judge runs on, or "" when Jev is off or has no key.
func (w *WorkspaceSettings) JevKey(ctx context.Context) string {
	s, err := w.Get(ctx)
	if err != nil || !s.JevEnabled {
		return ""
	}
	if w.EnvJevKey != "" {
		return w.EnvJevKey
	}
	if len(s.JevKeySealed) == 0 || w.Secrets == nil {
		return ""
	}
	plain, err := w.Secrets.Open(s.JevKeySealed)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(plain))
}

// Patch validates and stores the keys p sets, then returns the settings in force.
func (w *WorkspaceSettings) Patch(ctx context.Context, p model.SettingsPatch) (*model.Settings, error) {
	for role, id := range p.RoleAgents {
		spec, ok := AgentByID(id)
		if !ok {
			return nil, fmt.Errorf("%w: unknown agent %q", ErrInvalid, id)
		}
		valid := false
		for _, r := range allRoles {
			valid = valid || string(r) == role
		}
		if !valid {
			return nil, fmt.Errorf("%w: unknown role %q", ErrInvalid, role)
		}
		if !spec.Allows(model.AgentRole(role)) {
			return nil, fmt.Errorf("%w: %s cannot run the %s role", ErrInvalid, spec.Name, role)
		}
	}
	for role, v := range p.RolePermissions {
		if role != "chat" || (v != "ask" && v != "read") {
			return nil, fmt.Errorf("%w: only role_permissions.chat may be set, to ask or read", ErrInvalid)
		}
	}
	if p.MaxConcurrentRuns != nil && (*p.MaxConcurrentRuns < minWorkers || *p.MaxConcurrentRuns > maxWorkers) {
		return nil, fmt.Errorf("%w: max_concurrent_runs must be between %d and %d", ErrInvalid, minWorkers, maxWorkers)
	}
	var sealed []byte
	if p.JevKey != nil {
		key := strings.TrimSpace(*p.JevKey)
		sealed = []byte{}
		if key != "" {
			if w.Secrets == nil {
				return nil, model.ErrSecretsUnavailable
			}
			var err error
			if sealed, err = w.Secrets.Seal([]byte(key)); err != nil {
				return nil, fmt.Errorf("could not seal the Jev key")
			}
		}
	}
	if err := w.Store.PatchSealed(ctx, p, sealed); err != nil {
		return nil, err
	}
	s, err := w.Get(ctx)
	if err != nil {
		return nil, err
	}
	if w.OnChange != nil {
		w.OnChange(s)
	}
	return s, nil
}

// RoleAgent is the agent a role runs on for wo: the work order's snapshot, else the workspace setting.
func RoleAgent(role model.AgentRole, wo *model.WorkOrder, s *model.Settings, fallback string) string {
	if role == model.AgentCataloger {
		role = model.AgentArchitect
	}
	if wo != nil {
		if a := wo.RoleAgents[string(role)]; a != "" {
			return a
		}
	}
	if s != nil {
		if a := s.RoleAgents[string(role)]; a != "" {
			return a
		}
	}
	return fallback
}
