package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/google/uuid"

	"legos-factory/backend/internal/model"
)

// ErrWorkspace marks a work order whose isolated workspace could not be prepared.
var ErrWorkspace = errors.New("WORKSPACE_FAILED")

// WorkOrders creates work orders and their isolated workspaces: git worktrees of the project folder.
type WorkOrders struct {
	Store        WorkOrderStore
	Projects     *Projects
	Sandbox      Sandbox
	Integrations *Integrations
	Settings     SettingsReader
	// ResolveDefault gives the model a role resolves to under the workspace settings; nil snapshots the stored default as is.
	ResolveDefault func(role model.AgentRole, s *model.Settings) string
	// Chats resolves source_chat_id; nil rejects any chat link.
	Chats interface {
		Get(ctx context.Context, id string) (*model.Chat, error)
	}
}

func (w *WorkOrders) Create(ctx context.Context, u *model.User, req model.CreateWorkOrderRequest) (*model.WorkOrder, error) {
	if req.SourceChatID != "" {
		if w.Chats == nil {
			return nil, model.ErrChatNotFound
		}
		if ch, err := w.Chats.Get(ctx, req.SourceChatID); err != nil || ch.UserID != u.ID {
			if err == nil {
				err = model.ErrChatNotFound
			}
			return nil, err
		}
	}
	proj, err := w.Projects.Get(ctx, u, req.ProjectID)
	if err != nil {
		return nil, err
	}
	// governance snapshot: later default changes do not alter this order
	var foreman, architect string
	var roleAgents map[string]string
	if s, err := w.Settings.Get(ctx); err == nil {
		if len(s.RoleAgents) > 0 {
			roleAgents = maps.Clone(s.RoleAgents)
		}
		foreman, architect = s.ForemanModel, s.ArchitectModel
		if w.ResolveDefault != nil {
			foreman, architect = w.ResolveDefault(model.AgentForeman, s), w.ResolveDefault(model.AgentArchitect, s)
		}
	}
	wo := &model.WorkOrder{
		ID:             "wo_" + uuid.NewString()[:12],
		ProjectID:      req.ProjectID,
		Title:          req.Title,
		Objective:      req.Objective,
		Acceptance:     req.Acceptance,
		State:          model.StateCreated,
		BuilderModel:   req.BuilderModel,
		InspectorModel: req.InspectorModel,
		SourceRef:      req.SourceRef,
		SourceChatID:   req.SourceChatID,
		ForemanModel:   foreman,
		ArchitectModel: architect,
		RoleAgents:     roleAgents,
		CreatedBy:      u.ID,
	}
	// prepare before persisting: a stored CREATED order without a workspace gets resumed into planning on restart
	dir, base, err := w.Sandbox.Prepare(ctx, wo.ProjectID, wo.ID, proj.RepoURL, req.BaseBranch)
	if err != nil {
		return nil, fmt.Errorf("%w: could not prepare workspace: %v", ErrWorkspace, err)
	}
	wo.BaseBranch = base
	p, err := repoProfile(dir)
	if err != nil {
		w.cleanup(ctx, wo, proj)
		return nil, fmt.Errorf("%w: %v", ErrWorkspace, err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		w.cleanup(ctx, wo, proj)
		return nil, err
	}
	wo.CheckProfile = string(b)
	if p.Language != "" && p.Language != proj.Language {
		_ = w.Projects.Store.SetLanguage(ctx, proj.ID, p.Language)
	}
	if err := w.Store.Create(ctx, wo); err != nil {
		w.cleanup(ctx, wo, proj)
		return nil, err
	}
	if w.Sandbox.Dirty(ctx, proj.RepoURL) {
		wo.Warning = "Your project folder has uncommitted changes. They are not part of this work order, which starts from " + base + "."
	}
	if wo.SourceChatID != "" {
		_, _ = w.Store.AppendEvent(ctx, wo.ID, u.ID, "created_from_chat", "Created from a chat conversation", map[string]any{"chat_id": wo.SourceChatID})
	}
	return wo, nil
}

func (w *WorkOrders) cleanup(ctx context.Context, wo *model.WorkOrder, proj *model.Project) {
	_ = w.Sandbox.Cleanup(context.WithoutCancel(ctx), wo.ProjectID, wo.ID, proj.RepoURL)
}
