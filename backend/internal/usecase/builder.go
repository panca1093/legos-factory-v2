package usecase

import (
	"context"
	"fmt"
	"strings"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/quality"
)

// Builder: baseline verification, task implementation and DAG advancement.

func (f *Foreman) handleImplement(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StateQueued, model.StateImplementing, "foreman", "Builder started implementation"); err != nil {
		if wo.State != model.StateImplementing {
			return err
		}
	}
	// first transition in: record the failing baseline before any change,
	// so verification evidence is revision-specific from the start
	if err := f.recordBaseline(ctx, wo); err != nil {
		return err
	}
	provider, modelID := f.Model(model.AgentBuilder, wo)
	res, runID, err := f.runAgent(ctx, wo, model.AgentBuilder, provider, modelID, implementPrompt(wo, job), nil, job.TaskID)
	if err != nil {
		return err
	}
	f.event(wo.ID, "builder", "task_completed", fmt.Sprintf("Task %s implemented", job.TaskID), nil)
	title := fmt.Sprintf("Task %s implemented", job.TaskID)
	if job.TaskID == "" {
		title = fmt.Sprintf("Remediation implemented (revision %d)", wo.Revision)
	}
	f.Catalog.Record(ctx, wo, model.KnowledgeImplementation, model.TrustAIInterpretation, "builder", title, res.Text, "run:"+runID)
	return f.maybeAdvanceAfterImplementation(ctx, wo)
}

// recordBaseline runs the deterministic checks once, before implementation,
// when no verification evidence exists yet. A passing baseline is recorded
// as PASS — it still counts as evidence for the original revision.
func (f *Foreman) recordBaseline(ctx context.Context, wo *model.WorkOrder) error {
	existing, err := f.Quality.Count(ctx, wo.ID)
	if err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	proj, err := f.Proj.Get(ctx, wo.ProjectID)
	if err != nil {
		return err
	}
	profile, err := model.ParseProfile(proj.QualityProfile, proj.Language)
	if err != nil {
		return err
	}
	if len(profile.Checks) == 0 {
		return nil
	}
	f.event(wo.ID, "builder", "baseline_started", "Recording baseline verification (pre-change)", nil)
	for _, c := range profile.Checks {
		r := quality.RunCheck(ctx, f.workspaceDir(wo.ID), c, f.QualTimeout)
		if err := f.saveCheck(ctx, wo, c, r); err != nil {
			return err
		}
		f.event(wo.ID, "builder", "baseline_"+strings.ToLower(string(r.Outcome)),
			fmt.Sprintf("baseline %s → %s", c.Name, r.Outcome), map[string]any{"command": c.Command})
	}
	return nil
}

// maybeAdvanceAfterImplementation enqueues newly ready tasks or, when all
// tasks are done, moves to verification.
func (f *Foreman) maybeAdvanceAfterImplementation(ctx context.Context, wo *model.WorkOrder) error {
	tasks, err := f.WO.Plan(ctx, wo.ID)
	if err != nil {
		return err
	}
	done, err := f.Runs.CompletedBuilderTasks(ctx, wo.ID)
	if err != nil {
		return err
	}
	ready := model.ReadyTasks(tasks, done)
	for _, t := range ready {
		// duplicate enqueues are rejected by the unique active-job index
		if _, err := f.Sched.Enqueue(ctx, JobImplement, wo.ID, t.ID, map[string]any{}); err != nil {
			continue // another worker already owns this task
		}
	}
	if len(tasks) > 0 && len(done) == len(tasks) {
		if err := f.step(ctx, wo, model.StateImplementing, model.StateVerifying, "foreman", "Implementation complete; verifying"); err != nil {
			if wo.State != model.StateVerifying {
				return err
			}
		}
		_, err := f.Sched.Enqueue(ctx, JobVerify, wo.ID, "", map[string]any{})
		return err
	}
	return nil
}
