package usecase

import (
	"context"
	"fmt"
	"time"

	"legos-factory/backend/internal/model"
)

const prPollEvery = 2 * time.Minute

type gitLookup func(ctx context.Context, p *model.Project) (GitProvider, bool, error)

// WatchPRs polls the PRs of READY work orders until ctx ends. Ship is complete
// only when the PR is merged, so the poll records merge and close as events.
func (f *Factory) WatchPRs(ctx context.Context) {
	go func() {
		t := time.NewTicker(prPollEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				f.pollPRs(ctx, f.Integrations.GitFor)
			}
		}
	}()
}

// pollPRs checks every READY order with an open PR; a provider error is
// logged and the order is checked again on the next tick.
func (f *Factory) pollPRs(ctx context.Context, gitFor gitLookup) {
	wos, err := f.WO.List(ctx)
	if err != nil {
		f.Log.Warn("pr poll: list work orders failed", "err", err)
		return
	}
	for _, wo := range wos {
		if wo.State != model.StateReady || wo.PRRef == "" || wo.PRMergedAt != nil || wo.PRClosedAt != nil {
			continue
		}
		if err := f.checkPR(ctx, gitFor, wo); err != nil {
			f.Log.Warn("pr poll: status check failed", "wo", wo.ID, "ref", wo.PRRef, "err", err)
		}
	}
}

func (f *Factory) checkPR(ctx context.Context, gitFor gitLookup, wo model.WorkOrder) error {
	proj, err := f.Proj.Get(ctx, wo.ProjectID)
	if err != nil {
		return err
	}
	g, bound, err := gitFor(ctx, proj)
	if !bound {
		return nil
	}
	if err != nil {
		return err
	}
	st, err := g.PullRequestStatus(ctx, wo.PRRef)
	if err != nil {
		return err
	}
	if st.At.IsZero() {
		st.At = time.Now()
	}
	var col, kind, verb string
	switch st.State {
	case model.PRStateMerged:
		col, kind, verb = "pr_merged_at", "pr_merged", "merged"
	case model.PRStateClosed:
		col, kind, verb = "pr_closed_at", "pr_closed", "closed without merging"
	default:
		return nil
	}
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{col: st.At.Unix()}); err != nil {
		return err
	}
	f.event(wo.ID, "factory", kind, fmt.Sprintf("Pull request %s %s", wo.PRRef, verb),
		map[string]any{"ref": wo.PRRef, "url": wo.PRURL, "at": st.At.UTC().Format(time.RFC3339)})
	return nil
}
