package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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

type liveSession struct {
	woID    string
	harness Harness
	session string
}

// Steer sends a human message into a running agent turn without stopping it.
func (f *Foreman) Steer(ctx context.Context, woID, runID, message, actor string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("%w: message is required", ErrInvalid)
	}
	v, ok := f.live.Load(runID)
	if !ok || v.(liveSession).woID != woID {
		return stateConflict("run %s is not running", runID)
	}
	ls := v.(liveSession)
	s, ok := ls.harness.(Steerer)
	if !ok {
		return fmt.Errorf("%w: the %s harness cannot take messages mid-run", ErrInvalid, ls.harness.Name())
	}
	if err := s.Steer(ctx, ls.session, message); err != nil {
		switch {
		case errors.Is(err, model.ErrAgentSessionNotFound):
			return stateConflict("run %s has finished its turn", runID)
		case errors.Is(err, model.ErrSteeringUnsupported):
			return fmt.Errorf("%w: the %s harness cannot take messages mid-run", ErrInvalid, ls.harness.Name())
		}
		return err
	}
	f.event(woID, actor, "agent_steered", message, map[string]any{"run_id": runID})
	return nil
}

// RetryTask re-runs a plan task whose build failed, from a fresh worktree.
func (f *Foreman) RetryTask(ctx context.Context, woID, taskID, actor string) error {
	wo, err := f.WO.Get(ctx, woID)
	if err != nil {
		return err
	}
	if wo.State != model.StateImplementing {
		return stateConflict("WO %s is %s; tasks are retried while implementing", woID, wo.State)
	}
	tasks, err := f.WO.Plan(ctx, woID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(tasks, func(t model.Task) bool { return t.ID == taskID }) {
		return fmt.Errorf("%w: task %s is not in the plan", ErrInvalid, taskID)
	}
	done, err := f.Runs.CompletedBuilderTasks(ctx, woID)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(model.ReadyTasks(tasks, done), func(t model.Task) bool { return t.ID == taskID }) {
		return stateConflict("task %s is done or still waits on its dependencies", taskID)
	}
	if f.Sched.HasActive(ctx, woID, taskID, JobImplement) {
		return stateConflict("task %s is already queued or running", taskID)
	}
	if _, err := f.Sched.Enqueue(ctx, JobImplement, woID, taskID, map[string]any{"reason": "human_retry"}); err != nil {
		return err
	}
	f.event(woID, actor, "task_retry", fmt.Sprintf("Task %s retried", taskID), map[string]any{"task_id": taskID})
	return nil
}
