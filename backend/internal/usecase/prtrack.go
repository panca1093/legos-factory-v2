package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"legos-factory/backend/internal/model"
)

const prPollEvery = 2 * time.Minute

type gitLookup func(ctx context.Context, p *model.Project) (GitProvider, bool, error)

// WatchPRs polls READY orders' PRs at once and then every prPollEvery until ctx ends.
func (f *Factory) WatchPRs(ctx context.Context) {
	go func() {
		f.pollPRs(ctx, f.Integrations.GitFor)
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

// pollPRs checks every unmerged PR; transient errors are retried next tick.
func (f *Factory) pollPRs(ctx context.Context, gitFor gitLookup) {
	wos, err := f.WO.PRTracking(ctx)
	if err != nil {
		f.Log.Warn("pr poll: list work orders failed", "err", err)
		return
	}
	for _, wo := range wos {
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
		return f.trackFailed(ctx, wo, "the project is no longer bound to a git connection")
	}
	if err != nil {
		return err
	}
	st, err := g.PullRequestStatus(ctx, wo.PRRef)
	var pe *model.ProviderError
	if errors.Is(err, model.ErrPermanent) || (errors.As(err, &pe) && pe.Permanent()) {
		return f.trackFailed(ctx, wo, err.Error())
	}
	if err != nil {
		return err
	}
	if st.At.IsZero() {
		st.At = time.Now()
	}
	switch {
	case st.State == model.PRStateMerged:
		return f.recordPR(ctx, wo, "pr_merged", "merged", st.At, map[string]any{"pr_merged_at": st.At.Unix(), "pr_closed_at": nil})
	case st.State == model.PRStateClosed && wo.PRClosedAt == nil:
		return f.recordPR(ctx, wo, "pr_closed", "closed without merging", st.At, map[string]any{"pr_closed_at": st.At.Unix()})
	}
	return nil
}

// recordPR appends the event before the timestamp: a crash in between repeats the event, never loses it.
func (f *Factory) recordPR(ctx context.Context, wo model.WorkOrder, kind, verb string, at time.Time, fields map[string]any) error {
	msg := fmt.Sprintf("Pull request %s %s", wo.PRRef, verb)
	if err := f.eventErr(ctx, wo.ID, kind, msg, map[string]any{"ref": wo.PRRef, "url": wo.PRURL, "at": at.UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	return f.WO.UpdateFields(ctx, wo.ID, fields)
}

func (f *Factory) trackFailed(ctx context.Context, wo model.WorkOrder, reason string) error {
	if err := f.eventErr(ctx, wo.ID, "pr_track_failed", "Pull request "+wo.PRRef+" can no longer be tracked: "+reason,
		map[string]any{"ref": wo.PRRef, "reason": reason}); err != nil {
		return err
	}
	return f.WO.UpdateFields(ctx, wo.ID, map[string]any{"pr_track_failed_at": time.Now().Unix()})
}

// eventErr is f.event with a returned error, for callers that must not lose the event.
func (f *Factory) eventErr(ctx context.Context, woID, kind, msg string, payload map[string]any) error {
	ev, err := f.WO.AppendEvent(ctx, woID, "factory", kind, msg, payload)
	if err != nil {
		return err
	}
	f.Bus.Publish(model.LiveEvent{WOID: woID, Actor: "factory", Kind: kind, Message: msg, Payload: map[string]any{"id": ev.ID, "payload": payload}})
	return nil
}
