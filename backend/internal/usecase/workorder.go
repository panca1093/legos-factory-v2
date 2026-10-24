package usecase

import (
	"context"
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
}

func (w *WorkOrders) Create(ctx context.Context, u *model.User, req model.CreateWorkOrderRequest) (*model.WorkOrder, error) {
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
		Autonomy:       req.Autonomy,
		BuilderModel:   req.BuilderModel,
		InspectorModel: req.InspectorModel,
		SourceRef:      req.SourceRef,
		ForemanModel:   foreman,
		ArchitectModel: architect,
		CreatedBy:      u.ID,
	}
	if err := w.Store.Create(ctx, wo); err != nil {
		return nil, err
	}
	var auth *model.GitAuth
	if g, bound, err := w.Integrations.GitFor(ctx, proj); bound {
		if err != nil {
			return wo, fmt.Errorf("%w: repository connection unavailable: %v", ErrWorkspace, err)
		}
		a := g.GitAuth()
		auth = &a
	}
	// isolated workspace: builders never touch the project source dir
	if _, err := w.Sandbox.Prepare(ctx, wo.ID, proj.RepoURL, auth); err != nil {
		return wo, fmt.Errorf("%w: could not prepare workspace: %v", ErrWorkspace, err)
	}
	return wo, nil
}
