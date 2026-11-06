package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"legos-factory/backend/internal/model"
)

// Builder: baseline verification, task implementation and DAG advancement.

func (f *Factory) handleImplement(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	if wo.State == model.StateCancelled {
		return nil
	}
	if wo.State == model.StateWaitingForHuman {
		return nil // another task's Builder question parked the order; the human's answer re-enqueues the ready tasks
	}
	if err := f.step(ctx, wo, model.StateQueued, model.StateImplementing, "factory", "Builder started implementation"); err != nil {
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
	_, _, err = f.runBuilder(ctx, wo, job, provider, modelID)
	var nc *needsContext
	if errors.As(err, &nc) {
		if f.cancelled(ctx, wo.ID) {
			return nil // a cancel does not stop a running job: do not ask or requeue
		}
		answered, err := f.askBack(ctx, wo, job, nc.question)
		if err != nil || !answered {
			return err
		}
		// the scheduler runs the task again with a fresh timeout and the answer in its prompt
		return model.ErrRequeue
	}
	if err != nil {
		return err
	}
	f.event(wo.ID, "builder", "task_completed", fmt.Sprintf("Task %s implemented", job.TaskID), nil)
	if job.TaskID == contractTaskID(wo.ID) {
		if proceed, err := f.contractRedProof(ctx, wo, job); err != nil || !proceed {
			return err
		}
	}
	return f.maybeAdvanceAfterImplementation(ctx, wo)
}

// runBuilder runs the builder agent and fails permanently when the workspace
// has no changes afterwards. It checks the whole diff, not this run's delta:
// a later task may legitimately add nothing when an earlier one did the work.
// A plan task in a git workspace runs in its own worktree and counts as done
// only once merged back.
func (f *Factory) runBuilder(ctx context.Context, wo *model.WorkOrder, job model.Job, provider, modelID string) (model.AgentResult, string, error) {
	woDir := f.workspaceDir(wo.ID)
	opts := runOpts{Dir: woDir, Hold: true}
	branch := ""
	if job.TaskID != "" {
		taskDir, b := f.taskDir(wo.ID, job.TaskID), "legos/task/"+job.TaskID
		ok, err := f.Sandbox.TaskWorktree(ctx, woDir, taskDir, b)
		if err != nil {
			return model.AgentResult{}, "", err
		}
		if ok {
			opts, branch = runOpts{Dir: taskDir, Hold: true}, b
			if profile, err := f.profile(ctx, wo); err == nil {
				f.runSetup(ctx, wo, profile, taskDir, "builder")
			}
		}
	}
	kbText, audit := f.KB.Context(ctx, wo.ProjectID, string(model.AgentBuilder), "", woQuery(wo)+" "+f.taskText(ctx, wo.ID, job.TaskID), woDir, wo.KBBaseSHA)
	opts.Audit = audit
	tasks, _ := f.WO.Plan(ctx, wo.ID) // without the plan the prompt just omits the task's scope
	res, runID, err := f.runAgent(ctx, wo, model.AgentBuilder, provider, modelID, implementPrompt(wo, job, tasks)+kbText, nil, job.TaskID, opts)
	if err != nil {
		return res, runID, err
	}
	if q, asked := builderQuestion(res.Text); asked && job.TaskID != "" {
		// the partial edits are not committed or counted: the task is not done until a run completes
		if q == "" {
			err := fmt.Errorf("%w: task %s ended with NEEDS_CONTEXT but no question", model.ErrPermanent, job.TaskID)
			f.failRun(ctx, wo.ID, model.AgentBuilder, runID, err)
			return res, runID, err
		}
		_ = f.Runs.Wait(context.WithoutCancel(ctx), runID, q, res)
		f.event(wo.ID, "builder", "agent_completed", "builder agent asked a question", map[string]any{"run_id": runID, "needs_context": true})
		return res, runID, &needsContext{q}
	}
	runs := []heldRun{{runID, res}}
	if err := f.requireChanges(ctx, wo, job, opts.Dir, runID); err != nil {
		f.failRuns(ctx, wo.ID, runs, err)
		return res, runID, err
	}
	if branch != "" {
		more, err := f.integrateTask(ctx, wo, job, opts.Dir, branch, provider, modelID)
		runs = append(runs, more...)
		if err != nil {
			f.failRuns(ctx, wo.ID, runs, err)
			return res, runID, err
		}
	}
	return res, runID, f.completeRuns(ctx, wo.ID, runs)
}

const (
	// maxBuilderQuestions is how many questions per task the Architect settles; the next goes to the Foreman, any later one to a human.
	maxBuilderQuestions = 2
	needsContextMarker  = "NEEDS_CONTEXT:"
	emphasis            = "[\\s*_`]*"
)

// needsContextLine matches the marker with any case and markdown emphasis around the word and the colon.
var needsContextLine = regexp.MustCompile(`(?i)^` + emphasis + `NEEDS_CONTEXT` + emphasis + `:?` + emphasis + `(.*?)` + emphasis + `$`)

// needsContext is a Builder run that ended on a behaviour question instead of finishing.
type needsContext struct{ question string }

func (e *needsContext) Error() string { return "builder needs context: " + e.question }

// builderQuestion reports whether the reply's last line is "NEEDS_CONTEXT: <question>"; a bare marker gives an empty question.
func builderQuestion(text string) (string, bool) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	m := needsContextLine.FindStringSubmatch(strings.TrimSpace(lines[len(lines)-1]))
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// askBack settles a Builder question and reports true when the task can run again with the answer.
// It reports false when the order is parked for a human, whose answer resumes the task.
func (f *Factory) askBack(ctx context.Context, wo *model.WorkOrder, job model.Job, question string) (bool, error) {
	f.event(wo.ID, "builder", "clarification_asked", question, map[string]any{"task_id": job.TaskID})
	// parallel tasks and earlier runs may have added answers since this job loaded the order
	if fresh, err := f.WO.Get(ctx, wo.ID); err == nil {
		wo.Clarifications, wo.Spec = fresh.Clarifications, fresh.Spec
	}
	asks, repeated := 0, false
	for _, c := range wo.Clarifications {
		if c.By != builderNotesBy && c.Task == job.TaskID {
			asks++
			repeated = repeated || strings.EqualFold(strings.TrimSpace(c.Question), question)
		}
	}
	actor, note := "builder", ""
	switch {
	case repeated || asks > maxBuilderQuestions: // a repeated question means its answer did not help
		return false, f.parkForHuman(ctx, wo, job, actor, question)
	case findClarification(wo.Clarifications, question) != nil:
		c := findClarification(wo.Clarifications, question)
		return true, f.recordBuildAnswer(ctx, wo, job.TaskID, question, c.Answer, "", c.By, "", false)
	case asks < maxBuilderQuestions:
		tasks, _ := f.WO.Plan(ctx, wo.ID)
		var r foremanAnswerReply
		_, runID, err := f.runStructured(ctx, wo, model.AgentArchitect, architectAnswerPrompt(wo, tasks, question), map[string]string{"stage": "answer"}, "", &r, runOpts{})
		if err != nil {
			return false, err
		}
		if r.Decision == "ANSWER" {
			return true, f.recordBuildAnswer(ctx, wo, job.TaskID, question, r.Answer, r.Reason, "architect", runID, true)
		}
		f.event(wo.ID, "architect", "clarification_escalated", r.Reason, map[string]any{"run_id": runID})
		actor, note = "architect", "\n(A Builder asked the Architect, who escalated: "+r.Reason+")"
	}
	r, runID, err := f.askForeman(ctx, wo, actor, question, note, repoFacts(ctx, f.workspaceDir(wo.ID)), false)
	if err != nil {
		return false, err
	}
	if r.Decision == "ANSWER" {
		return true, f.recordBuildAnswer(ctx, wo, job.TaskID, question, r.Answer, r.Reason, "foreman", runID, true)
	}
	f.event(wo.ID, "foreman", "clarification_escalated", r.Reason, map[string]any{"run_id": runID})
	return false, f.parkForHuman(ctx, wo, job, actor, question)
}

// parkForHuman asks a human unless the order already waits on another question; that task
// then stays ready and asks again after the pending answer, from the stored log.
func (f *Factory) parkForHuman(ctx context.Context, wo *model.WorkOrder, job model.Job, actor, question string) error {
	mu := f.wsLock(wo.ID)
	mu.Lock()
	defer mu.Unlock()
	fresh, err := f.WO.Get(ctx, wo.ID)
	if err != nil || fresh.State != model.StateImplementing {
		return err
	}
	wo.ClarifTask = job.TaskID
	return f.askHuman(ctx, wo, actor, question)
}

// recordBuildAnswer logs the answer and, when amend is set, amends the spec, which is otherwise read-only once approved.
// It rewrites the stored order's log and spec under the order lock: parallel tasks answer at the same time.
func (f *Factory) recordBuildAnswer(ctx context.Context, wo *model.WorkOrder, task, question, answer, reason, by, runID string, amend bool) error {
	mu := f.wsLock(wo.ID)
	mu.Lock()
	defer mu.Unlock()
	fresh, err := f.WO.Get(ctx, wo.ID)
	if err != nil {
		return err
	}
	log := append(append([]model.Clarification{}, fresh.Clarifications...), model.Clarification{Question: question, Answer: answer, By: by, Task: task})
	raw, _ := json.Marshal(log)
	fields, spec := map[string]any{"clarif_log": string(raw)}, fresh.Spec
	if amend {
		spec = amendSpec(spec, question, answer, reason, by)
		fields["spec"] = spec
	}
	if err := f.WO.UpdateFields(ctx, wo.ID, fields); err != nil {
		return err
	}
	wo.Clarifications, wo.Spec = log, spec
	f.event(wo.ID, by, "clarification_answered", answer, map[string]any{"question": question, "run_id": runID, "repeated": !amend})
	return nil
}

// resumeBuilder records a human's answer to a Builder question and re-enqueues the ready tasks,
// the asking task and any that were skipped while the order waited.
func (f *Factory) resumeBuilder(ctx context.Context, wo *model.WorkOrder, answer string) error {
	task, question := wo.ClarifTask, wo.ClarifQuestion
	mu := f.wsLock(wo.ID)
	mu.Lock()
	fresh, err := f.WO.Get(ctx, wo.ID)
	if err != nil {
		mu.Unlock()
		return err
	}
	log := append(append([]model.Clarification{}, fresh.Clarifications...), model.Clarification{Question: question, Answer: answer, By: "human", Task: task})
	raw, _ := json.Marshal(log)
	spec := amendSpec(fresh.Spec, question, answer, "decided by a human", "human")
	err = f.stepWith(ctx, wo, model.StateWaitingForHuman, model.StateImplementing, "human", "Builder question answered; task resumes",
		map[string]any{"clarif_answer": answer, "clarif_log": string(raw), "clarif_task": "", "spec": spec})
	mu.Unlock()
	if err != nil {
		return err
	}
	wo.ClarifAnswer, wo.Clarifications, wo.Spec, wo.ClarifTask = answer, log, spec, ""
	f.event(wo.ID, "human", "clarification_answered", answer, map[string]any{"question": question})
	f.Catalog.Record(ctx, wo, model.KnowledgeClarification, model.TrustHumanDecision, "human",
		"Clarification: "+question, fmt.Sprintf("Question: %s\nAnswer: %s", question, answer))
	f.draftFromClarification(ctx, wo, question, answer)
	return f.maybeAdvanceAfterImplementation(ctx, wo)
}

func amendSpec(spec, question, answer, reason, by string) string {
	if reason == "" {
		reason = "answered by the " + by
	}
	return spec + fmt.Sprintf("\n\nAmendment: %s → %s (reason: %s)", question, answer, reason)
}

// heldRun is a run kept open by runOpts.Hold together with its own result.
type heldRun struct {
	id  string
	res model.AgentResult
}

func (f *Factory) requireChanges(ctx context.Context, wo *model.WorkOrder, job model.Job, dir, runID string) error {
	changed, err := f.Sandbox.ChangedFiles(ctx, dir)
	if err != nil {
		return err
	}
	if len(changed) > 0 {
		return nil
	}
	what := "task " + job.TaskID
	if job.TaskID == "" {
		what = "remediation"
	}
	msg := fmt.Sprintf("Builder finished %s without changing any files", what)
	f.event(wo.ID, "builder", "task_no_changes", msg, map[string]any{"run_id": runID})
	return fmt.Errorf("%w: %s", model.ErrPermanent, msg)
}

// integrateTask commits the task worktree and merges it into the work order
// workspace. On a conflict the builder resolves it once in its worktree
// against the latest workspace HEAD; it returns the runs that resolution used.
func (f *Factory) integrateTask(ctx context.Context, wo *model.WorkOrder, job model.Job, dir, branch, provider, modelID string) ([]heldRun, error) {
	woDir := f.workspaceDir(wo.ID)
	msg := fmt.Sprintf("legos-factory: %s task %s", wo.ID, job.TaskID)
	if _, err := f.Sandbox.Commit(ctx, dir, msg); err != nil {
		return nil, err
	}
	conflict, err := f.mergeTask(ctx, wo.ID, woDir, branch, msg)
	if err != nil || !conflict {
		return nil, f.afterMerge(ctx, wo, job, woDir, dir, branch, err)
	}
	files, err := f.Sandbox.SyncTask(ctx, woDir, dir)
	if err != nil {
		return nil, err
	}
	f.event(wo.ID, "builder", "task_conflict",
		fmt.Sprintf("Task %s conflicts with merged work in %s; builder resolving", job.TaskID, strings.Join(files, ", ")), nil)
	res, runID, err := f.runAgent(ctx, wo, model.AgentBuilder, provider, modelID, resolvePrompt(wo, job, files), nil, job.TaskID, runOpts{Dir: dir, Hold: true})
	if err != nil {
		return nil, err
	}
	runs := []heldRun{{runID, res}}
	if left := withConflictMarkers(dir, files); len(left) > 0 {
		return runs, fmt.Errorf("%w: task %s still has conflict markers in %s", model.ErrPermanent, job.TaskID, strings.Join(left, ", "))
	}
	if _, err := f.Sandbox.Commit(ctx, dir, msg+" (conflicts resolved)"); err != nil {
		return runs, err
	}
	conflict, err = f.mergeTask(ctx, wo.ID, woDir, branch, msg)
	if err == nil && conflict {
		err = fmt.Errorf("%w: task %s still conflicts after the builder resolved it", model.ErrPermanent, job.TaskID)
	}
	return runs, f.afterMerge(ctx, wo, job, woDir, dir, branch, err)
}

func (f *Factory) mergeTask(ctx context.Context, woID, woDir, branch, msg string) (bool, error) {
	mu := f.wsLock(woID)
	mu.Lock()
	defer mu.Unlock()
	return f.Sandbox.MergeTask(ctx, woDir, branch, msg)
}

// afterMerge removes a merged task's worktree; a failed one is kept for inspection.
func (f *Factory) afterMerge(ctx context.Context, wo *model.WorkOrder, job model.Job, woDir, dir, branch string, err error) error {
	if err != nil {
		return err
	}
	f.event(wo.ID, "builder", "task_merged", fmt.Sprintf("Task %s merged into the work order workspace", job.TaskID), nil)
	if err := f.Sandbox.RemoveTaskWorktree(ctx, woDir, dir, branch); err != nil {
		f.Log.Warn("remove task worktree failed", "wo", wo.ID, "task", job.TaskID, "err", err)
	}
	return nil
}

func (f *Factory) completeRuns(ctx context.Context, woID string, runs []heldRun) error {
	for _, r := range runs {
		if err := f.Runs.Complete(context.WithoutCancel(ctx), r.id, r.res); err != nil {
			return err
		}
		f.event(woID, "builder", "agent_completed", "builder agent finished", map[string]any{"run_id": r.id})
	}
	return nil
}

func (f *Factory) failRuns(ctx context.Context, woID string, runs []heldRun, err error) {
	for _, r := range runs {
		f.failRun(ctx, woID, model.AgentBuilder, r.id, err)
	}
}

func (f *Factory) taskDir(woID, taskID string) string {
	return filepath.Join(f.DataDir, "workspaces", woID+".tasks", filepath.Base(taskID))
}

func (f *Factory) profile(ctx context.Context, wo *model.WorkOrder) (model.Profile, error) {
	proj, err := f.Proj.Get(ctx, wo.ProjectID)
	if err != nil {
		return model.Profile{}, err
	}
	if q := strings.TrimSpace(proj.QualityProfile); q != "" && q != "{}" && q != "[]" {
		return model.ParseProfile(proj.QualityProfile, proj.Language)
	}
	if wo.CheckProfile != "" {
		return model.ParseProfile(wo.CheckProfile, proj.Language)
	}
	// orders created before the profile was stored at creation
	return repoProfile(f.workspaceDir(wo.ID))
}

// runSetup installs dependencies in dir. A failure is reported, not fatal:
// the checks or the builder will surface it with their own evidence.
func (f *Factory) runSetup(ctx context.Context, wo *model.WorkOrder, profile model.Profile, dir, actor string) {
	if profile.Setup == "" {
		return
	}
	r := f.Sandbox.RunCheck(ctx, dir, model.Check{Name: "setup", Command: profile.Setup}, f.QualTimeout)
	if r.Outcome != model.Pass {
		out := r.Output
		if len(out) > 2000 {
			out = out[len(out)-2000:]
		}
		f.event(wo.ID, actor, "setup_failed", fmt.Sprintf("setup %q → %s", profile.Setup, r.Outcome), map[string]any{"output": out})
	}
}

func withConflictMarkers(dir string, files []string) []string {
	var left []string
	for _, name := range files {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil && (bytes.Contains(b, []byte("\n<<<<<<< ")) || bytes.HasPrefix(b, []byte("<<<<<<< "))) {
			left = append(left, name)
		}
	}
	return left
}

// recordBaseline runs the deterministic checks once, before implementation,
// when no verification evidence exists yet. A passing baseline is recorded
// as PASS — it still counts as evidence for the original revision.
func (f *Factory) recordBaseline(ctx context.Context, wo *model.WorkOrder) error {
	// parallel tasks arrive together; one records the baseline, apart from the merge lock so merges are not held up
	mu := f.baselineLock(wo.ID)
	mu.Lock()
	defer mu.Unlock()
	existing, err := f.Quality.Count(ctx, wo.ID)
	if err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	profile, err := f.profile(ctx, wo)
	if err != nil {
		return err
	}
	if len(profile.Checks) == 0 {
		return nil
	}
	f.event(wo.ID, "builder", "baseline_started", "Recording baseline verification (pre-change)", nil)
	f.runSetup(ctx, wo, profile, f.workspaceDir(wo.ID), "builder")
	for _, c := range profile.Checks {
		r := f.Sandbox.RunCheck(ctx, f.workspaceDir(wo.ID), c, f.QualTimeout)
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
func (f *Factory) maybeAdvanceAfterImplementation(ctx context.Context, wo *model.WorkOrder) error {
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
		if err := f.step(ctx, wo, model.StateImplementing, model.StateVerifying, "factory", "Implementation complete; verifying"); err != nil {
			if wo.State != model.StateVerifying {
				return err
			}
		}
		_, err := f.Sched.Enqueue(ctx, JobVerify, wo.ID, "", map[string]any{})
		return err
	}
	return nil
}
