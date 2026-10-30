package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/sandbox"
	"legos-factory/factory/internal/workflow"
)

// orch drives one work order from specification to an integrated branch: the Architect writes, the Foreman decides, the engine acts.
type orch struct {
	e         *Engine
	p         Project
	woID      string
	sb        sandbox.Sandbox
	dir       string // the work-order worktree on lf/<wo>
	objective string
	lim       workflow.Limits
}

// errWOFailed means the work order was already marked failed; callers stop without error.
var errWOFailed = errors.New("work order failed")

const maxLineSteps = 500

// foremanDeny removes every agent tool: the Foreman only reasons over the facts in its prompt (ADR-F15).
var foremanDeny = []string{
	"Bash", "BashOutput", "KillShell", "Read", "Glob", "Grep", "LS", "Edit", "MultiEdit", "Write", "NotebookRead", "NotebookEdit",
	"WebFetch", "WebSearch", "Task", "TodoWrite", "ExitPlanMode", "Skill", "SlashCommand",
	"read", "list", "glob", "grep", "edit", "write", "patch", "bash", "webfetch", "websearch", "task", "todowrite", "todoread", "skill",
}

// orchestrated reports whether the work order runs the SDLC line (Architect and Foreman) instead of the single-builder path.
func (e *Engine) orchestrated(ctx context.Context, p Project, facts []fact.Fact) bool {
	if p.Roles[string(harness.RoleForeman)].set() || p.Roles[string(harness.RoleArchitect)].set() {
		return true
	}
	if workflow.Fold(facts).Phase != workflow.PhaseQueued {
		return true
	}
	if e.Profiles != nil {
		for _, r := range []harness.Role{harness.RoleForeman, harness.RoleArchitect} {
			if _, err := e.Profiles.Resolve(ctx, p.ID, r); err == nil {
				return true
			}
		}
	}
	return false
}

// run loops on the folded state until the order is ready to verify (true) or parked, failed or finished (false).
func (o *orch) run(ctx context.Context) (ready bool, err error) {
	for range maxLineSteps {
		facts, err := o.e.Facts.List(ctx, o.woID)
		if err != nil {
			return false, err
		}
		st := workflow.Fold(facts)
		switch st.Phase {
		case workflow.PhaseQueued, workflow.PhaseCataloging, workflow.PhaseSpecifying:
			err = o.specify(ctx, facts)
		case workflow.PhaseSpecReady, workflow.PhasePlanReady:
			err = o.foreman(ctx, st, facts)
		case workflow.PhasePlanning:
			err = o.plan(ctx, facts)
		case workflow.PhaseBuilding:
			err = o.building(ctx, st, facts)
		case workflow.PhaseIntegrating:
			ready, err = o.integrate(ctx, st, facts)
		case workflow.PhaseVerifying:
			return true, nil
		default:
			return false, nil
		}
		if errors.Is(err, errWOFailed) {
			return false, nil
		}
		if err != nil || ready {
			return ready, err
		}
	}
	return false, o.escalate(ctx, "orchestration step limit reached")
}

func (o *orch) fail(ctx context.Context, f fact.WOFailed) error {
	if err := o.e.fail(ctx, o.woID, f); err != nil {
		return err
	}
	return errWOFailed
}

func (o *orch) escalate(ctx context.Context, reason string) error {
	_, err := o.e.Facts.Append(ctx, o.woID, workflow.KindEscalated, workflow.Escalated{Reason: reason})
	return err
}

// roleSession returns the agent session of the latest run of (role, task) and the id of that run when it never finished.
func roleSession(facts []fact.Fact, role harness.Role, task string) (sessionID, openRun string) {
	for _, f := range facts {
		switch f.Kind {
		case fact.KindRunStarted:
			if r, err := fact.Decode[fact.RunStarted](f); err == nil && r.Role == string(role) && r.Task == task {
				openRun = r.Run
				if r.SessionID != "" {
					sessionID = r.SessionID
				}
			}
		case fact.KindRunFinished:
			if r, err := fact.Decode[fact.RunFinished](f); err == nil && r.Run == openRun {
				openRun = ""
			}
		}
	}
	return sessionID, openRun
}

// runRole runs one turn of a read-only role in the work-order worktree and returns its final message. freshPrompt
// replaces prompt when the stored session cannot be resumed and must therefore carry the whole context.
func (o *orch) runRole(ctx context.Context, role harness.Role, task, prompt, freshPrompt string) (string, error) {
	e := o.e
	cfg, err := e.agentConfig(ctx, o.p, role)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", o.fail(ctx, fact.WOFailed{Class: "agent_error", Message: err.Error()})
	}
	defer cfg.release()
	facts, err := e.Facts.List(ctx, o.woID)
	if err != nil {
		return "", err
	}
	sid, open := roleSession(facts, role, task)
	if freshPrompt == "" {
		freshPrompt = prompt
	}
	var policy harness.ToolPolicy
	if role == harness.RoleForeman {
		policy.Deny = foremanDeny
	}
	note := ""
	if open != "" && role != harness.RoleForeman {
		note = "Your previous run was interrupted. "
	}
	reply, agentErr, err := e.converse(ctx, o.woID, o.sb, convo{
		role: role, task: task, dir: o.dir, cfg: cfg, policy: policy, sessionID: sid, openRun: open,
		prompt: func(fresh bool) string {
			if fresh {
				return note + freshPrompt
			}
			return note + prompt
		},
	})
	if err != nil {
		return "", err
	}
	if agentErr != nil {
		done, ferr := e.agentFailure(ctx, o.woID, agentErr, cfg.Profile)
		if ferr != nil {
			return "", ferr
		}
		if done {
			return "", errWOFailed
		}
	}
	return reply, nil
}

// --- Architect ---

func contextPack() string { return "" }

func specPrompt(objective string) string {
	s := "You are the Architect. Read the repository as needed (it is read-only) and write the specification for this objective: " +
		"behaviour, affected areas, constraints and acceptance criteria. Reply with the specification as your final message. Do not write a plan yet.\n\nObjective:\n" + objective
	if cp := contextPack(); cp != "" {
		s += "\n\nContext pack:\n" + cp
	}
	return s
}

const planFormat = "Reply with the plan as a JSON object in a final ```json block: " +
	`{"tasks":[{"id":"short-id","description":"what to build","owned_paths":["dir/or/file"],"depends_on":["other-id"]}]}. ` +
	"Task ids use letters, digits, '.', '_' or '-'. Tasks that can run in parallel must own disjoint paths; otherwise make one depend on the other."

func planPrompt(spec string) string {
	return "The Foreman approved the specification. Split the work into tasks that Builders can implement independently.\n\nSpecification:\n" + spec + "\n\n" + planFormat
}

// rejection returns the reason of the latest accepted rejectTool decision made after the latest readyKind fact.
func rejection(facts []fact.Fact, readyKind, rejectTool string) (string, bool) {
	reason, ok := "", false
	for _, f := range facts {
		switch f.Kind {
		case readyKind:
			reason, ok = "", false
		case workflow.KindForemanDecision:
			if d, err := fact.Decode[workflow.ForemanDecision](f); err == nil && d.Accepted && d.Tool == rejectTool {
				reason, ok = d.Reason, true
			}
		}
	}
	return reason, ok
}

func latestSpec(facts []fact.Fact) string {
	spec := ""
	for _, f := range facts {
		if f.Kind == workflow.KindSpecReady {
			if s, err := fact.Decode[workflow.SpecReady](f); err == nil {
				spec = s.Spec
			}
		}
	}
	return spec
}

func (o *orch) specify(ctx context.Context, facts []fact.Fact) error {
	prompt, fresh := specPrompt(o.objective), ""
	if reason, ok := rejection(facts, workflow.KindSpecReady, workflow.ToolRejectSpec); ok {
		fresh = prompt + "\n\nYour previous specification:\n" + latestSpec(facts) + "\n\nThe Foreman rejected it: " + reason + "\nReply with the complete revised specification."
		prompt = "The Foreman rejected your specification: " + reason + "\nRevise it and reply with the complete revised specification."
	}
	reply, err := o.runRole(ctx, harness.RoleArchitect, "architect", prompt, fresh)
	if err != nil {
		return err
	}
	_, err = o.e.Facts.Append(ctx, o.woID, workflow.KindSpecReady, workflow.SpecReady{Spec: strings.TrimSpace(reply)})
	return err
}

func (o *orch) plan(ctx context.Context, facts []fact.Fact) error {
	spec := latestSpec(facts)
	prompt, fresh := planPrompt(spec), ""
	if reason, ok := rejection(facts, workflow.KindPlanReady, workflow.ToolRejectPlan); ok {
		fresh = prompt + "\n\nThe Foreman rejected your previous plan: " + reason
		prompt = "The Foreman rejected your plan: " + reason + "\nRevise it. " + planFormat
	}
	if fresh == "" {
		fresh = prompt
	}
	reply, err := o.runRole(ctx, harness.RoleArchitect, "architect", prompt, fresh)
	if err != nil {
		return err
	}
	plan, perr := parsePlan(reply)
	if perr != nil {
		retry := "Your reply could not be used as a plan: " + perr.Error() + "\n" + planFormat
		if reply, err = o.runRole(ctx, harness.RoleArchitect, "architect", retry, fresh+"\n\n"+retry); err != nil {
			return err
		}
		if plan, perr = parsePlan(reply); perr != nil {
			plan = workflow.PlanReady{} // the Foreman's approve_plan guard rejects an empty plan
		}
	}
	_, err = o.e.Facts.Append(ctx, o.woID, workflow.KindPlanReady, plan)
	return err
}

var taskIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func parsePlan(reply string) (workflow.PlanReady, error) {
	raw, err := extractJSON(reply)
	if err != nil {
		return workflow.PlanReady{}, err
	}
	var in struct {
		Tasks []struct {
			ID          string   `json:"id"`
			Description string   `json:"description"`
			OwnedPaths  []string `json:"owned_paths"`
			DependsOn   []string `json:"depends_on"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return workflow.PlanReady{}, fmt.Errorf("plan JSON: %w", err)
	}
	if len(in.Tasks) == 0 {
		return workflow.PlanReady{}, errors.New(`plan JSON has no "tasks"`)
	}
	var out workflow.PlanReady
	for _, t := range in.Tasks {
		if !taskIDRe.MatchString(t.ID) || t.ID == task || strings.HasSuffix(t.ID, ".lock") {
			return workflow.PlanReady{}, fmt.Errorf("task id %q is not allowed (letters, digits, '.', '_', '-'; not %q)", t.ID, task)
		}
		out.Tasks = append(out.Tasks, workflow.PlanTask{ID: t.ID, Description: t.Description, Paths: t.OwnedPaths, DependsOn: t.DependsOn})
	}
	return out, nil
}

var fenceRe = regexp.MustCompile("(?s)```[A-Za-z]*[ \t]*\r?\n(.*?)```")

// extractJSON returns the last fenced block holding a JSON object, else the last top-level JSON object in text.
func extractJSON(text string) ([]byte, error) {
	ms := fenceRe.FindAllStringSubmatch(text, -1)
	for i := len(ms) - 1; i >= 0; i-- {
		if b := strings.TrimSpace(ms[i][1]); strings.HasPrefix(b, "{") && json.Valid([]byte(b)) {
			return []byte(b), nil
		}
	}
	var last []byte
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		if end := objectEnd(text, i); end > 0 && json.Valid([]byte(text[i:end])) {
			last = []byte(text[i:end])
			i = end - 1
		}
	}
	if last == nil {
		return nil, errors.New("no JSON object found in the reply")
	}
	return last, nil
}

// objectEnd returns the index just past the brace-balanced object starting at text[start], or -1.
func objectEnd(text string, start int) int {
	depth, inStr := 0, false
	for i := start; i < len(text); i++ {
		c := text[i]
		switch {
		case inStr && c == '\\':
			i++
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// --- Foreman ---

func parseForemanReply(reply string) (workflow.Call, error) {
	raw, err := extractJSON(reply)
	if err != nil {
		return workflow.Call{}, fmt.Errorf("reply must end with one JSON object {\"tool\": ..., \"args\": {...}}: %w", err)
	}
	var in struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return workflow.Call{}, fmt.Errorf("invalid call: %w", err)
	}
	return workflow.ParseCall(in.Tool, in.Args)
}

func (o *orch) foremanPrompt(st workflow.State, facts []fact.Fact) string {
	var b strings.Builder
	b.WriteString("You are the Foreman of a software work order. You write no code and have no tools except the transition calls listed below; decide the next transition from the facts here.\n\n")
	fmt.Fprintf(&b, "Objective: %s\nPhase: %s\nAutonomy level cap: %d\n", o.objective, st.Phase, st.LevelCap)
	if spec := latestSpec(facts); spec != "" {
		fmt.Fprintf(&b, "\nSpecification:\n%s\n", spec)
	}
	if len(st.Tasks) > 0 {
		b.WriteString("\nPlan and task status:\n")
		failures := lastFailures(facts)
		for _, t := range st.Tasks {
			fmt.Fprintf(&b, "- %s [%s] %s | owns %v | depends_on %v | commits %d, inspected %d, inspection rounds %d, retries %d",
				t.ID, t.Status, t.Description, t.Paths, t.DependsOn, t.Commits, t.InspectedCommits, t.InspectionRounds, t.Retries)
			if m := failures[t.ID]; m != "" {
				fmt.Fprintf(&b, " | last failure: %s", m)
			}
			b.WriteString("\n")
		}
	}
	for _, c := range st.Conflicts {
		fmt.Fprintf(&b, "\nIntegration conflict between %s and %s (files %v), serialized: %v\n", c.A, c.B, c.Files, c.Serialized)
	}
	l := o.lim
	fmt.Fprintf(&b, "\nLimits: %d builders at once; spec rejections %d of %d; plan rejections %d of %d; %d inspection rounds and %d retries per task; %d rejected calls in a row escalate (currently %d).\n",
		l.MaxBuilders, st.SpecRejections, l.MaxSpecRounds, st.PlanRejections, l.MaxPlanRounds, l.MaxInspectionRounds, l.MaxRetries, l.MaxConsecutiveRejections, st.ConsecutiveRejections)
	tools, _ := json.Marshal(workflow.Tools())
	fmt.Fprintf(&b, "\nTransition tools (JSON schema):\n%s\n", tools)
	b.WriteString("\nThink briefly, then end your reply with exactly one JSON object and nothing after it: {\"tool\": \"<name>\", \"args\": {...}}. An illegal call is answered with \"tool error: ...\".")
	return b.String()
}

func lastFailures(facts []fact.Fact) map[string]string {
	out := map[string]string{}
	for _, f := range facts {
		switch f.Kind {
		case fact.KindTaskFailed:
			if v, err := fact.Decode[fact.TaskFailed](f); err == nil {
				out[v.Task] = v.Class + ": " + firstLine(v.Message, 300)
			}
		case workflow.KindTaskBuilt:
			if v, err := fact.Decode[workflow.TaskBuiltPayload](f); err == nil {
				delete(out, v.Task)
			}
		}
	}
	return out
}

// foreman asks for one transition and applies it; a refused call is answered with a tool error in the same session until accepted or escalated.
func (o *orch) foreman(ctx context.Context, st workflow.State, facts []fact.Fact) error {
	full := o.foremanPrompt(st, facts)
	prompt := full
	for {
		reply, err := o.runRole(ctx, harness.RoleForeman, "foreman", prompt, full)
		if err != nil {
			return err
		}
		if facts, err = o.e.Facts.List(ctx, o.woID); err != nil {
			return err
		}
		st = workflow.Fold(facts)
		var nf []fact.NewFact
		call, cerr := parseForemanReply(reply)
		if cerr != nil {
			nf = st.Rejection("", nil, cerr, o.lim)
		} else {
			if _, dup := st.Decided(call); dup {
				return nil
			}
			nf, cerr = st.Apply(call, o.lim)
		}
		if _, err := o.e.Facts.AppendBatch(ctx, o.woID, nf); err != nil {
			return err
		}
		if cerr == nil {
			return nil
		}
		if facts, err = o.e.Facts.List(ctx, o.woID); err != nil {
			return err
		}
		if workflow.Fold(facts).Phase == workflow.PhaseAwaitingHuman {
			return nil
		}
		prompt = "tool error: " + cerr.Error()
		full = full + "\n\n" + prompt
	}
}

// --- building and integration ---

func (o *orch) building(ctx context.Context, st workflow.State, facts []fact.Fact) error {
	for _, t := range st.Tasks {
		if t.Status == workflow.TaskBuilding {
			return o.buildTask(ctx, t, facts)
		}
	}
	for _, t := range st.Tasks {
		if t.Status == workflow.TaskInspecting {
			// the Inspector arrives with M5; until then an inspection finds nothing
			_, err := o.e.Facts.Append(ctx, o.woID, workflow.KindInspectionDone, workflow.InspectionDone{Task: t.ID})
			return err
		}
	}
	if needsDecision(st) {
		return o.foreman(ctx, st, facts)
	}
	return o.escalate(ctx, "no task can make progress")
}

func needsDecision(st workflow.State) bool {
	for _, t := range st.Tasks {
		switch t.Status {
		case workflow.TaskFailed:
			return true
		case workflow.TaskBuilt:
			if t.Commits > t.InspectedCommits {
				return true
			}
		case workflow.TaskPending:
			if depsDone(st, t) {
				return true
			}
		}
	}
	return false
}

func depsDone(st workflow.State, t workflow.Task) bool {
	for _, d := range t.DependsOn {
		if dt, ok := st.Task(d); !ok || dt.Status != workflow.TaskDone {
			return false
		}
	}
	return true
}

// taskBranch cannot be lf/<wo>/<task>: git refuses a branch below another branch's name.
func taskBranch(woID, taskID string) string { return "lf/" + woID + "-" + taskID }

func (o *orch) git(ctx context.Context, dir string, args ...string) (string, error) {
	argv := append([]string{"git", "-c", "user.name=legos-factory", "-c", "user.email=factory@localhost"}, args...)
	stdout, stderr, code, err := o.sb.Exec(ctx, dir, nil, argv...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	if code != 0 {
		return "", fmt.Errorf("git %s: exit %d: %s", args[0], code, strings.TrimSpace(stderr+stdout))
	}
	return strings.TrimSpace(stdout), nil
}

func taskPrompt(objective, spec string, t workflow.Task) string {
	s := objective + "\n\nSpecification:\n" + spec + "\n\nYour task (" + t.ID + "): " + t.Description
	if len(t.Paths) > 0 {
		s += "\nYou own these paths; change files only there: " + strings.Join(t.Paths, ", ")
	}
	if t.LastFailureClass != "" {
		s += "\nA previous attempt failed (" + t.LastFailureClass + "); try again."
	}
	return s
}

// buildTask runs one dispatched task in its own worktree on top of its dependencies and records task.built or task.failed.
func (o *orch) buildTask(ctx context.Context, t workflow.Task, facts []fact.Fact) error {
	e := o.e
	branch := taskBranch(o.woID, t.ID)
	dir, err := sandbox.AddWorktree(ctx, o.sb, t.ID, branch, o.p.DefaultBranch)
	if err != nil {
		return err
	}
	for _, dep := range t.DependsOn {
		db := taskBranch(o.woID, dep)
		if _, err := o.git(ctx, dir, "merge", "--no-edit", db); err != nil {
			// serialized re-run: start over on top of the dependency
			_, _ = o.git(ctx, dir, "merge", "--abort")
			if _, err := o.git(ctx, dir, "reset", "--hard", db); err != nil {
				return err
			}
		}
	}
	if failed, err := e.setup(ctx, o.p, o.woID, o.sb, dir, t.ID); err != nil {
		return err
	} else if failed {
		return errWOFailed
	}
	cfg, err := e.agentConfig(ctx, o.p, harness.RoleBuilder)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return o.fail(ctx, fact.WOFailed{Class: "agent_error", Message: err.Error()})
	}
	defer cfg.release()
	sid, open := roleSession(facts, harness.RoleBuilder, t.ID)
	prompt := taskPrompt(o.objective, latestSpec(facts), t)
	note := ""
	if open != "" {
		note = "Your previous run was interrupted and the worktree may hold partial work. "
	}
	_, agentErr, err := e.converse(ctx, o.woID, o.sb, convo{
		role: harness.RoleBuilder, task: t.ID, dir: dir, cfg: cfg, sessionID: sid, openRun: open,
		prompt: func(bool) string { return note + prompt },
	})
	if err != nil {
		return err
	}
	if agentErr != nil {
		if done, ferr := e.agentFailure(ctx, o.woID, agentErr, cfg.Profile); ferr != nil {
			return ferr
		} else if done {
			return errWOFailed
		}
	}
	sha, _, err := sandbox.CommitAll(ctx, o.sb, dir, "lf: "+firstLine(t.Description, 60))
	if err != nil {
		return err
	}
	args := []string{"rev-list", "--no-merges", "--count", "HEAD", "^" + o.p.DefaultBranch}
	for _, dep := range t.DependsOn {
		args = append(args, "^"+taskBranch(o.woID, dep))
	}
	n, err := o.git(ctx, dir, args...)
	if err != nil {
		return err
	}
	if n == "0" {
		_, err = e.Facts.Append(ctx, o.woID, fact.KindTaskFailed, fact.TaskFailed{Task: t.ID, Class: "task_no_changes", Message: "builder produced no changes"})
		return err
	}
	_, err = e.Facts.Append(ctx, o.woID, workflow.KindTaskBuilt, workflow.TaskBuiltPayload{Task: t.ID, SHA: sha})
	return err
}

// integrate merges the task branches into lf/<wo> one by one (M4 refines conflict handling); ready is true when all merged.
func (o *orch) integrate(ctx context.Context, st workflow.State, facts []fact.Fact) (ready bool, err error) {
	for _, c := range st.Conflicts {
		if !c.Serialized {
			return false, o.foreman(ctx, st, facts)
		}
	}
	_, _ = o.git(ctx, o.dir, "merge", "--abort")
	var merged []workflow.Task
	for _, t := range topoOrder(st.Tasks) {
		if _, err := o.git(ctx, o.dir, "merge", "--no-ff", "--no-edit", "-m", "lf: merge "+t.ID, taskBranch(o.woID, t.ID)); err == nil {
			merged = append(merged, t)
			continue
		}
		files, ferr := o.git(ctx, o.dir, "diff", "--name-only", "--diff-filter=U")
		if ferr != nil {
			return false, ferr
		}
		_, _ = o.git(ctx, o.dir, "merge", "--abort")
		conflicted := strings.Fields(files)
		other := ""
		for _, m := range merged {
			out, derr := o.git(ctx, o.dir, "diff", "--name-only", o.p.DefaultBranch+"..."+taskBranch(o.woID, m.ID))
			if derr != nil {
				return false, derr
			}
			if slices.ContainsFunc(strings.Fields(out), func(f string) bool { return slices.Contains(conflicted, f) }) {
				other = m.ID
				break
			}
		}
		if other == "" && len(merged) > 0 {
			other = merged[len(merged)-1].ID
		}
		if other == "" {
			return false, o.fail(ctx, fact.WOFailed{Class: "integration_failed", Message: "task " + t.ID + " conflicts with the base branch: " + files})
		}
		_, err := o.e.Facts.Append(ctx, o.woID, workflow.KindIntegrationConflict, workflow.IntegrationConflict{A: other, B: t.ID, Files: conflicted})
		return false, err
	}
	return true, nil
}

func topoOrder(tasks []workflow.Task) []workflow.Task {
	var out []workflow.Task
	done := map[string]bool{}
	for len(out) < len(tasks) {
		progressed := false
		for _, t := range tasks {
			if done[t.ID] || slices.ContainsFunc(t.DependsOn, func(d string) bool { return !done[d] }) {
				continue
			}
			done[t.ID], progressed = true, true
			out = append(out, t)
		}
		if !progressed {
			break
		}
	}
	return out
}
