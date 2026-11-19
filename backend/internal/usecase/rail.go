package usecase

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"legos-factory/backend/internal/model"
)

// Rail builds the project workspace rail summary.
type Rail struct {
	Projects     *Projects
	WO           WorkOrderStore
	Integrations *Integrations
	Reviews      *Reviews // nil: the pull request part reports no identity
}

const (
	RailPartAll   = "all"
	RailPartWO    = "wo"
	RailPartPulls = "pulls"
)

func (r *Rail) Get(ctx context.Context, u *model.User, projectID, part string) (*model.RailResponse, error) {
	if part == "" {
		part = RailPartAll
	}
	if part != RailPartAll && part != RailPartWO && part != RailPartPulls {
		return nil, fmt.Errorf("%w: part must be all, wo or pulls", ErrInvalid)
	}
	p, err := r.Projects.Get(ctx, u, projectID)
	if err != nil {
		return nil, err
	}
	res := &model.RailResponse{Project: model.RailProject{ID: p.ID, Name: p.Name, Repo: r.repoLabel(ctx, p)}}
	if part != RailPartPulls {
		all, err := r.WO.List(ctx)
		if err != nil {
			return nil, err
		}
		wos := &model.RailWorkOrders{Rows: []model.RailWorkOrder{}}
		for _, wo := range all {
			if wo.ProjectID != p.ID || !railOpen(&wo) {
				continue
			}
			waiting := waitingOnYou(&wo)
			if waiting {
				res.Project.NeedsYou++
			}
			wos.Rows = append(wos.Rows, model.RailWorkOrder{ID: wo.ID, Title: wo.Title, State: wo.State, Stage: railStage(wo.State), WaitingOnYou: waiting, UpdatedAt: wo.UpdatedAt})
		}
		sort.SliceStable(wos.Rows, func(i, j int) bool {
			a, b := wos.Rows[i], wos.Rows[j]
			if a.WaitingOnYou != b.WaitingOnYou {
				return a.WaitingOnYou
			}
			return a.UpdatedAt.After(b.UpdatedAt)
		})
		wos.Open = len(wos.Rows)
		res.WorkOrders = wos
	}
	if part != RailPartWO {
		if r.Reviews != nil {
			res.Pulls = r.Reviews.RailPulls(ctx, u, p)
		} else {
			res.Pulls = &model.RailPulls{Rows: []model.RailPullRow{}, Error: &model.ProviderErr{Code: model.ErrCodeNoIdentity, Message: ErrNoReviewIdentity.Error()}}
		}
	}
	return res, nil
}

func (r *Rail) repoLabel(ctx context.Context, p *model.Project) string {
	if label := r.Integrations.RepoLabel(ctx, p); label != "" {
		return label
	}
	if p.RepoURL == "" {
		return ""
	}
	return filepath.Base(p.RepoURL)
}

func railOpen(wo *model.WorkOrder) bool {
	return wo.State != model.StateCancelled && wo.PRMergedAt == nil
}

// waitingOnYou mirrors the frontend's isNeedsYou so the rail and the nav dot agree.
func waitingOnYou(wo *model.WorkOrder) bool {
	switch wo.State {
	case model.StateWaitingForHuman, model.StateNeedsHumanReview, model.StateBlocked:
		return true
	case model.StateArchitectReview:
		return wo.PlanReview == "approved"
	case model.StateReady:
		return wo.PRMergedAt == nil && (wo.PRClosedAt != nil || wo.PRTrackFailedAt != nil || (wo.PRURL == "" && wo.PROpenFailedAt != nil))
	}
	return false
}

func railStage(s model.State) string {
	switch s {
	case model.StateCreated, model.StateRequirement:
		return "brief"
	case model.StateDecomposition, model.StatePlanGeneration, model.StatePlanning, model.StateArchitectReview:
		return "plan"
	case model.StateQueued, model.StateImplementing, model.StateVerifying:
		return "build"
	case model.StateSpecialistReview, model.StatePRPreparation, model.StateMergeReadiness, model.StateBlocked, model.StateWaitingForHuman:
		return "review"
	case model.StateNeedsHumanReview, model.StateReady:
		return "ship"
	}
	return ""
}
