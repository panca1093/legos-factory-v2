package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"legos-factory/backend/internal/model"
)

// ErrWorkspace marks a work order whose isolated workspace could not be prepared.
var ErrWorkspace = errors.New("WORKSPACE_FAILED")

// WorkOrders creates work orders and their isolated workspaces. A project
// bound to a git connection is cloned with that connection's credential.
type WorkOrders struct {
	Store        WorkOrderStore
	Projects     *Projects
	Sandbox      Sandbox
	Integrations *Integrations
	Settings     SettingsReader
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
	if s, err := w.Settings.Get(ctx); err == nil {
		foreman, architect = s.ForemanModel, s.ArchitectModel
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
		CreatedBy:      u.ID,
	}
	var auth *model.GitAuth
	if g, bound, err := w.Integrations.GitFor(ctx, proj); bound {
		switch {
		case errors.Is(err, model.ErrGHMissing), errors.Is(err, model.ErrGHUnauthenticated), errors.Is(err, model.ErrGHUnreachable):
			// a missing gh login must not block local work; the open-PR job reports it
		case err != nil:
			return nil, fmt.Errorf("%w: repository connection unavailable: %v", ErrWorkspace, err)
		default:
			a := g.GitAuth()
			auth = &a
		}
	}
	// prepare before persisting: a stored CREATED order without a workspace gets resumed into planning on restart
	dir, err := w.Sandbox.Prepare(ctx, wo.ID, proj.RepoURL, auth)
	if err != nil {
		return nil, fmt.Errorf("%w: could not prepare workspace: %v", ErrWorkspace, err)
	}
	p, err := repoProfile(dir)
	if err != nil {
		_ = w.Sandbox.Destroy(ctx, wo.ID)
		return nil, fmt.Errorf("%w: %v", ErrWorkspace, err)
	}
	b, err := json.Marshal(p)
	if err != nil {
		_ = w.Sandbox.Destroy(ctx, wo.ID)
		return nil, err
	}
	wo.CheckProfile = string(b)
	if p.Language != "" && p.Language != proj.Language {
		_ = w.Projects.Store.SetLanguage(ctx, proj.ID, p.Language)
	}
	if err := w.Store.Create(ctx, wo); err != nil {
		_ = w.Sandbox.Destroy(ctx, wo.ID)
		return nil, err
	}
	if wo.SourceChatID != "" {
		_, _ = w.Store.AppendEvent(ctx, wo.ID, u.ID, "created_from_chat", "Created from a chat conversation", map[string]any{"chat_id": wo.SourceChatID})
	}
	return wo, nil
}
