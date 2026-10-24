package usecase

import (
	"context"
	"time"

	"legos-factory/backend/internal/model"
)

// Pause stops a work order after its current step: the scheduler no longer
// claims its jobs, so queued work waits and nothing is lost. Stop is Cancel.
func (f *Foreman) Pause(ctx context.Context, woID, actor, reason string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if wo.State == model.StateReady || wo.State == model.StateCancelled || wo.State == model.StateBlocked {
		return stateConflict("WO %s is %s; there is nothing to pause", woID, wo.State)
	}
	if wo.PausedAt != nil {
		return stateConflict("WO %s is already paused", woID)
	}
	return f.pause(ctx, wo, actor, reason)
}

func (f *Foreman) pause(ctx context.Context, wo *model.WorkOrder, actor, reason string) error {
	now := time.Now()
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"paused_at": now.Unix(), "pause_reason": reason}); err != nil {
		return err
	}
	wo.PausedAt, wo.PauseReason = &now, reason
	f.event(wo.ID, actor, "paused", reason, nil)
	return nil
}

// Resume lets the scheduler pick up the order's queued work again.
func (f *Foreman) Resume(ctx context.Context, woID, actor string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if wo.PausedAt == nil {
		return stateConflict("WO %s is not paused", woID)
	}
	if err := f.WO.UpdateFields(ctx, woID, map[string]any{"paused_at": nil, "pause_reason": ""}); err != nil {
		return err
	}
	f.event(woID, actor, "resumed", "Work resumed", nil)
	return nil
}
