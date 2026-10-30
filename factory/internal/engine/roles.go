package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

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

	beforeAppend func() // test seam: runs between folding and appending a Foreman decision

	mu      sync.Mutex    // guards failed and serializes git worktree creation
	failed  bool          // wo.failed was recorded; builders racing to fail the order record it once
	builder chan struct{} // MaxBuilders slots, created on first use

	stopBuilders func() // cancels the running builders; set while building (guarded by mu)
}

// errWOFailed means the work order was already marked failed; callers stop without error.
var errWOFailed = errors.New("work order failed")

const (
	maxLineSteps      = 60
	maxConflictRounds = 2
)

// readOnlyDeny is the Architect's explicit tool policy, on top of what the harness denies for read-only roles.
var readOnlyDeny = []string{"Edit", "Write", "MultiEdit", "NotebookEdit", "Bash", "edit", "write", "patch", "apply_patch", "bash"}

// foremanDeny removes every agent tool: the Foreman only reasons over the facts in its prompt (ADR-F15).
var foremanDeny = []string{
	"Bash", "BashOutput", "KillShell", "Read", "Glob", "Grep", "LS", "Edit", "MultiEdit", "Write", "NotebookRead", "NotebookEdit",
	"WebFetch", "WebSearch", "Task", "TodoWrite", "ExitPlanMode", "Skill", "SlashCommand",
	"read", "list", "glob", "grep", "edit", "write", "patch", "bash", "webfetch", "websearch", "task", "todowrite", "todoread", "skill",
}

// lineConfigured reports whether the project has a Foreman or Architect configured or bound; it is recorded at creation.
func (e *Engine) lineConfigured(ctx context.Context, p Project) bool {
	if p.Roles[string(harness.RoleForeman)].set() || p.Roles[string(harness.RoleArchitect)].set() {
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
			err = o.building(ctx)
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
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.failed {
		if o.stopBuilders != nil {
			o.stopBuilders()
		}
		if err := o.e.fail(context.WithoutCancel(ctx), o.woID, f); err != nil {
			return err
		}
		o.failed = true
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
		return "", o.fail(ctx, fact.WOFailed{Class: fact.ClassTask, Reason: "no_profile", Message: err.Error()})
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
	dir := o.dir
	switch role {
	case harness.RoleForeman:
		policy = harness.ToolPolicy{Deny: foremanDeny, AllowNone: true}
		dir, err = o.foremanDir(ctx)
	case harness.RoleArchitect:
		policy = harness.ToolPolicy{Deny: readOnlyDeny}
		dir, err = o.architectDir(ctx)
	}
	if err != nil {
		return "", err
	}
	note := ""
	if open != "" && role != harness.RoleForeman {
		note = "Your previous run was interrupted. "
	}
	reply, agentErr, err := e.converse(ctx, o.woID, o.sb, convo{
		role: role, task: task, dir: dir, cfg: cfg, policy: policy, sessionID: sid, openRun: open,
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

// foremanDir is an empty directory, so the repo's CLAUDE.md/AGENTS.md never reach the tool-less Foreman.
func (o *orch) foremanDir(ctx context.Context) (string, error) {
	dir := path.Join(o.sb.Workspace().HomeDir(), "foreman-cwd")
	if _, stderr, code, err := o.sb.Exec(ctx, "", nil, "mkdir", "-p", dir); err != nil || code != 0 {
		return "", fmt.Errorf("create %s: %v %s", dir, err, stderr)
	}
	return dir, nil
}

// architectDir is a detached worktree at the base commit, reset on every use, so nothing the Architect writes can reach the PR.
func (o *orch) architectDir(ctx context.Context) (string, error) {
	ws := o.sb.Workspace()
	dir := ws.WorktreeDir("_architect")
	if _, err := o.git(ctx, dir, "rev-parse", "--git-dir"); err != nil {
		if _, err := o.git(ctx, ws.RepoDir(), "worktree", "prune"); err != nil {
			return "", err
		}
		if _, err := o.git(ctx, ws.RepoDir(), "worktree", "add", "--detach", "--force", dir, o.p.DefaultBranch); err != nil {
			return "", err
		}
	}
	if _, err := o.git(ctx, dir, "reset", "--hard", o.p.DefaultBranch); err != nil {
		return "", err
	}
	if _, err := o.git(ctx, dir, "clean", "-fdxq"); err != nil {
		return "", err
	}
	return dir, nil
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
		paths := make([]string, len(t.OwnedPaths))
		for i, p := range t.OwnedPaths {
			paths[i] = p
			if c, err := workflow.CleanPath(p); err == nil {
				paths[i] = c // invalid ones stay as written so approve_plan names them to the Foreman
			}
		}
		out.Tasks = append(out.Tasks, workflow.PlanTask{ID: t.ID, Description: t.Description, Paths: paths, DependsOn: t.DependsOn})
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
				out[v.Task] = v.Class + "/" + v.Reason + ": " + firstLine(v.Message, 300)
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
	if sid, _ := roleSession(facts, harness.RoleForeman, "foreman"); sid != "" {
		prompt = foremanDelta(st, facts)
	}
	for {
		reply, err := o.runRole(ctx, harness.RoleForeman, "foreman", prompt, full)
		if err != nil {
			return err
		}
		cerr, err := o.decide(ctx, reply)
		if err != nil {
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

// decide records the Foreman's reply against the facts it was folded from; when another writer got in first it re-folds
// and judges the same call against the newer state. cerr is the tool error to hand back to the Foreman.
func (o *orch) decide(ctx context.Context, reply string) (cerr, err error) {
	call, perr := parseForemanReply(reply)
	for range 3 {
		facts, err := o.e.Facts.List(ctx, o.woID)
		if err != nil {
			return nil, err
		}
		st := workflow.Fold(facts)
		var nf []fact.NewFact
		if perr != nil {
			cerr, nf = perr, st.Rejection("", nil, perr, o.lim)
		} else {
			nf, cerr = st.Apply(call, o.lim)
		}
		if o.beforeAppend != nil {
			o.beforeAppend()
		}
		_, err = o.e.Facts.AppendBatch(ctx, o.woID, facts[len(facts)-1].ID, nf)
		if errors.Is(err, fact.ErrConflict) {
			continue
		}
		return cerr, err
	}
	return nil, fact.ErrConflict
}

// foremanDelta is the prompt for a resumed Foreman session: what changed since its last turn, not the whole context again.
func foremanDelta(st workflow.State, facts []fact.Fact) string {
	var since int64
	for _, f := range facts {
		if f.Kind == fact.KindRunStarted {
			if r, err := fact.Decode[fact.RunStarted](f); err == nil && r.Role == string(harness.RoleForeman) {
				since = f.ID
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Update since your last turn. Phase: %s; rejected calls in a row: %d.\n", st.Phase, st.ConsecutiveRejections)
	if len(st.Tasks) > 0 {
		b.WriteString("Tasks:")
		for _, t := range st.Tasks {
			fmt.Fprintf(&b, " %s[%s]", t.ID, t.Status)
		}
		b.WriteString("\n")
	}
	b.WriteString("New facts:\n")
	n := 0
	for _, f := range facts {
		if f.ID <= since {
			continue
		}
		switch f.Kind {
		case fact.KindRunStarted, fact.KindRunStep, fact.KindRunFinished, fact.KindSandboxStarted, fact.KindSandboxStopped, workflow.KindForemanDecision:
			continue
		case workflow.KindSpecReady:
			fmt.Fprintf(&b, "- spec.ready:\n%s\n", latestSpec([]fact.Fact{f}))
		case workflow.KindPlanReady:
			fmt.Fprintf(&b, "- plan.ready: %s\n", f.Payload)
		default:
			fmt.Fprintf(&b, "- %s %s\n", f.Kind, firstLine(string(f.Payload), 600))
		}
		n++
	}
	if n == 0 {
		b.WriteString("- none\n")
	}
	b.WriteString("\nEnd your reply with exactly one JSON object and nothing after it: {\"tool\": \"<name>\", \"args\": {...}}.")
	return b.String()
}

// --- building and integration ---

type buildResult struct {
	id  string
	err error
}

// building runs every task the Foreman dispatched concurrently, one goroutine each, and asks the Foreman again as soon as
// anything needs a decision, without waiting for the other builders. It returns when the phase leaves building.
func (o *orch) building(ctx context.Context) error {
	if o.builder == nil {
		o.builder = make(chan struct{}, max(o.lim.MaxBuilders, 1))
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	o.mu.Lock()
	o.stopBuilders = cancel
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		o.stopBuilders = nil
		o.mu.Unlock()
	}()
	results := make(chan buildResult)
	active := map[string]bool{}
	var firstErr error
	finish := func(r buildResult) {
		delete(active, r.id)
		stopped := cctx.Err() != nil && !errors.Is(r.err, errWOFailed) // errors after our own cancel are fallout
		if r.err != nil && firstErr == nil && !stopped && !errors.Is(r.err, context.Canceled) {
			firstErr = r.err
		}
	}
	drain := func() error {
		cancel()
		for len(active) > 0 {
			finish(<-results)
		}
		if firstErr == nil {
			firstErr = ctx.Err()
		}
		return firstErr
	}
	for range maxLineSteps {
		facts, err := o.e.Facts.List(ctx, o.woID)
		if err != nil {
			drain()
			return err
		}
		st := workflow.Fold(facts)
		if st.Phase != workflow.PhaseBuilding {
			return drain()
		}
		for _, t := range st.Tasks {
			if t.Status == workflow.TaskBuilding && !active[t.ID] {
				active[t.ID] = true
				go func() { results <- buildResult{t.ID, o.runBuilder(cctx, t, facts)} }()
			}
		}
		select {
		case r := <-results:
			finish(r)
			if firstErr != nil {
				return drain()
			}
			continue
		default:
		}
		switch {
		case hasInspecting(st):
			if err := o.inspect(cctx, st); err != nil {
				drain()
				return err
			}
		case splitPending(st) != nil:
			if err := o.split(cctx, st, *splitPending(st), facts); err != nil {
				drain()
				return err
			}
		case needsDecision(st, o.lim):
			if err := o.foreman(cctx, st, facts); err != nil {
				drain()
				return err
			}
		case len(active) == 0:
			return o.escalate(ctx, "no task can make progress")
		default:
			select {
			case r := <-results:
				finish(r)
				if firstErr != nil {
					return drain()
				}
			case <-cctx.Done():
				return drain()
			}
		}
	}
	drain()
	return o.escalate(ctx, "orchestration step limit reached")
}

func hasInspecting(st workflow.State) bool {
	return slices.ContainsFunc(st.Tasks, func(t workflow.Task) bool { return t.Status == workflow.TaskInspecting })
}

// inspect completes pending inspections; the Inspector arrives with M5 and until then finds nothing.
func (o *orch) inspect(ctx context.Context, st workflow.State) error {
	for _, t := range st.Tasks {
		if t.Status == workflow.TaskInspecting {
			if _, err := o.e.Facts.Append(ctx, o.woID, workflow.KindInspectionDone, workflow.InspectionDone{Task: t.ID}); err != nil {
				return err
			}
		}
	}
	return nil
}

func splitPending(st workflow.State) *workflow.Task {
	for i := range st.Tasks {
		if st.Tasks[i].SplitPending {
			return &st.Tasks[i]
		}
	}
	return nil
}

func needsDecision(st workflow.State, lim workflow.Limits) bool {
	for _, t := range st.Tasks {
		switch t.Status {
		case workflow.TaskFailed:
			return true
		case workflow.TaskBuilt:
			if t.Commits > t.InspectedCommits {
				return true
			}
		case workflow.TaskPending:
			if !t.SplitPending && depsDone(st, t) && st.CanDispatch(t.ID, lim) {
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

// lastFailure is the latest task.failed of a task that has not built successfully since.
func lastFailure(facts []fact.Fact, id string) (fact.TaskFailed, bool) {
	var out fact.TaskFailed
	found := false
	for _, f := range facts {
		switch f.Kind {
		case fact.KindTaskFailed:
			if v, err := fact.Decode[fact.TaskFailed](f); err == nil && v.Task == id {
				out, found = v, true
			}
		case workflow.KindTaskBuilt:
			if v, err := fact.Decode[workflow.TaskBuiltPayload](f); err == nil && v.Task == id {
				found = false
			}
		}
	}
	return out, found
}

func taskPrompt(objective, spec string, t workflow.Task, facts []fact.Fact) string {
	s := objective + "\n\nSpecification:\n" + spec + "\n\nYour task (" + t.ID + "): " + t.Description
	if len(t.Paths) > 0 {
		s += "\nYou own these paths; change files only there: " + strings.Join(t.Paths, ", ")
	}
	if tf, ok := lastFailure(facts, t.ID); ok {
		s += "\nA previous attempt failed (" + tf.Class + "): " + tail(tf.Message, 3000) + "\nFix the cause and try again."
	}
	return s
}

// runBuilder holds one of the MaxBuilders slots and a PORT for the duration of one task.
func (o *orch) runBuilder(ctx context.Context, t workflow.Task, facts []fact.Fact) error {
	select {
	case o.builder <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-o.builder }()
	key := o.woID + "/" + t.ID
	port, err := o.e.ports().Acquire(key)
	if err != nil {
		return o.taskFailed(ctx, t.ID, fact.ClassInfra, "no_port", err.Error(), harness.Profile{})
	}
	defer o.e.ports().Release(key)
	return o.buildTask(ctx, t, facts, port)
}

// buildTask runs one dispatched task in its own worktree on top of its dependencies and records task.built or task.failed.
func (o *orch) buildTask(ctx context.Context, t workflow.Task, facts []fact.Fact, port int) error {
	e := o.e
	branch := taskBranch(o.woID, t.ID)
	o.mu.Lock()
	dir, err := sandbox.AddWorktree(ctx, o.sb, t.ID, branch, o.p.DefaultBranch)
	o.mu.Unlock()
	if err != nil {
		return err
	}
	for _, dep := range t.DependsOn {
		db := taskBranch(o.woID, dep)
		if _, merr := o.git(ctx, dir, "merge", "--no-edit", db); merr != nil {
			_, _ = o.git(ctx, dir, "merge", "--abort")
			if !serializedOnto(facts, dep, t.ID) {
				return o.taskFailed(ctx, t.ID, fact.ClassIntegration, "dependency_conflict", "merging dependency "+dep+" into "+t.ID+": "+firstLine(merr.Error(), 300), harness.Profile{})
			}
			// serialized re-run: start over on top of the dependency
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
	cfg, err := e.agentConfigN(ctx, o.p, harness.RoleBuilder, t.ModelChanges)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return o.taskFailed(ctx, t.ID, fact.ClassTask, "no_profile", err.Error(), harness.Profile{})
	}
	defer cfg.release()
	sid, open := roleSession(facts, harness.RoleBuilder, t.ID)
	prompt := taskPrompt(o.objective, latestSpec(facts), t, facts)
	note := ""
	if open != "" {
		note = "Your previous run was interrupted and the worktree may hold partial work. "
	}
	_, agentErr, err := e.converse(ctx, o.woID, o.sb, convo{
		role: harness.RoleBuilder, task: t.ID, dir: dir, cfg: cfg, sessionID: sid, openRun: open,
		env:    map[string]string{"PORT": strconv.Itoa(port)},
		prompt: func(bool) string { return note + prompt },
	})
	if err != nil {
		return o.transientTaskFailure(ctx, t.ID, err, cfg.Profile)
	}
	if agentErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch f := Classify(agentErr, false); {
		case f.Transient:
			return o.transientTaskFailure(ctx, t.ID, agentErr, cfg.Profile)
		case f.Class == fact.ClassProvider:
			return o.fail(ctx, fact.WOFailed{Class: fact.ClassProvider, Message: agentErr.Error(), Model: cfg.Profile.Model, Provider: cfg.Profile.Provider})
		}
		return o.taskFailed(ctx, t.ID, fact.ClassTask, "agent_error", agentErr.Error(), cfg.Profile)
	}
	sha, _, err := sandbox.CommitAll(ctx, o.sb, dir, "lf: "+firstLine(t.Description, 60))
	if err != nil {
		return err
	}
	own := []string{"HEAD", "^" + o.p.DefaultBranch}
	for _, dep := range t.DependsOn {
		own = append(own, "^"+taskBranch(o.woID, dep))
	}
	n, err := o.git(ctx, dir, append([]string{"rev-list", "--no-merges", "--count"}, own...)...)
	if err != nil {
		return err
	}
	if n == "0" {
		return o.taskFailed(ctx, t.ID, fact.ClassTask, "no_changes", "builder produced no changes", cfg.Profile)
	}
	changed, err := o.git(ctx, dir, append([]string{"log", "--no-merges", "--name-only", "--pretty=format:"}, own...)...)
	if err != nil {
		return err
	}
	var stray []string
	for _, f := range strings.Split(changed, "\n") {
		if f != "" && !workflow.Owned(f, t.Paths) && !slices.Contains(stray, f) {
			stray = append(stray, f)
		}
	}
	if len(stray) > 0 {
		return o.taskFailed(ctx, t.ID, fact.ClassTask, "path_violation", fmt.Sprintf("changed files outside the owned paths %v: %s", t.Paths, strings.Join(stray, ", ")), cfg.Profile)
	}
	_, err = e.Facts.Append(ctx, o.woID, workflow.KindTaskBuilt, workflow.TaskBuiltPayload{Task: t.ID, SHA: sha})
	return err
}

// transientTaskFailure fails only this task when its retries ran out, leaving siblings running and the decision to the Foreman;
// other errors, and a spent work-order budget, stop the line.
func (o *orch) transientTaskFailure(ctx context.Context, id string, err error, prof harness.Profile) error {
	if !IsTransient(err) || ctx.Err() != nil {
		return err
	}
	if wf, over := o.e.overBudget(ctx, o.woID); over {
		wf.Model, wf.Provider = prof.Model, prof.Provider
		return o.fail(ctx, wf)
	}
	return o.taskFailed(ctx, id, fact.ClassProvider, "transient_exhausted", err.Error(), prof)
}

func (o *orch) taskFailed(ctx context.Context, id, class, reason, msg string, prof harness.Profile) error {
	_, err := o.e.Facts.Append(ctx, o.woID, fact.KindTaskFailed, fact.TaskFailed{Task: id, Class: class, Reason: reason, Message: msg, Model: prof.Model, Provider: prof.Provider})
	return err
}

const splitFormat = "Reply with the sub-plan as a JSON object in a final ```json block, in the same format as the plan: " +
	`{"tasks":[{"id":"new-id","description":"...","owned_paths":["..."],"depends_on":[]}]}. `

// split asks the Architect to replace a failed task with smaller ones that own paths within its own, and records plan.split.
// An unusable sub-plan fails the task again so the Foreman can choose another change.
func (o *orch) split(ctx context.Context, st workflow.State, t workflow.Task, facts []fact.Fact) error {
	var others []string
	for _, x := range st.Tasks {
		others = append(others, x.ID)
	}
	rules := fmt.Sprintf("Task %q (%s, owning %v) failed and the Foreman wants it split. Replace it with at least 2 smaller tasks. "+
		"Their owned paths must lie within %v. New ids must differ from the existing ones %v. Tasks that can run in parallel must own disjoint paths; "+
		"otherwise make one depend on the other.\n", t.ID, t.Description, t.Paths, t.Paths, others)
	if tf, ok := lastFailure(facts, t.ID); ok {
		rules += "Why it failed: " + tail(tf.Message, 1500) + "\n"
	}
	fresh := "Specification:\n" + latestSpec(facts) + "\n\n" + rules + splitFormat
	prompt := rules + splitFormat
	var subs []workflow.PlanTask
	var perr error
	for attempt := 0; attempt < 2; attempt++ {
		reply, err := o.runRole(ctx, harness.RoleArchitect, "architect", prompt, fresh)
		if err != nil {
			return err
		}
		var plan workflow.PlanReady
		if plan, perr = parsePlan(reply); perr == nil {
			subs = plan.Tasks
			cur, err := o.e.Facts.List(ctx, o.woID)
			if err != nil {
				return err
			}
			perr = workflow.Fold(cur).ValidateSplit(t.ID, subs)
		}
		if perr == nil {
			break
		}
		prompt = "Your sub-plan was rejected: " + perr.Error() + "\n" + splitFormat
		fresh += "\n\n" + prompt
	}
	if perr != nil {
		return o.taskFailed(ctx, t.ID, fact.ClassTask, "split_failed", "the Architect produced no valid split: "+perr.Error(), harness.Profile{})
	}
	_, err := o.e.Facts.Append(ctx, o.woID, workflow.KindPlanSplit, workflow.PlanSplit{Task: t.ID, Tasks: subs})
	return err
}

// serializedOnto reports whether the Foreman serialized task onto dep, which licenses discarding its earlier work to rebuild on dep.
func serializedOnto(facts []fact.Fact, dep, task string) bool {
	for _, f := range facts {
		if f.Kind == workflow.KindTasksSerialized {
			if v, err := fact.Decode[workflow.TasksSerialized](f); err == nil && v.A == dep && v.B == task {
				return true
			}
		}
	}
	return false
}

// integrate merges the task branches into lf/<wo> one by one (M4 refines conflict handling); ready is true when all merged.
func (o *orch) integrate(ctx context.Context, st workflow.State, facts []fact.Fact) (ready bool, err error) {
	for _, c := range st.Conflicts {
		if !c.Serialized {
			return false, o.foreman(ctx, st, facts)
		}
	}
	order, oerr := topoOrder(st.Tasks)
	if oerr != nil {
		return false, o.escalate(ctx, oerr.Error())
	}
	_, _ = o.git(ctx, o.dir, "merge", "--abort")
	// start from the base every time so a rebuilt task is merged once, never on top of its stale earlier merge
	if _, err := o.git(ctx, o.dir, "reset", "--hard", o.p.DefaultBranch); err != nil {
		return false, err
	}
	var merged []workflow.Task
	for _, t := range order {
		_, merr := o.git(ctx, o.dir, "merge", "--no-ff", "--no-edit", "-m", "lf: merge "+t.ID, taskBranch(o.woID, t.ID))
		if merr == nil {
			merged = append(merged, t)
			continue
		}
		files, ferr := o.git(ctx, o.dir, "diff", "--name-only", "--diff-filter=U")
		if ferr != nil {
			return false, ferr
		}
		_, _ = o.git(ctx, o.dir, "merge", "--abort")
		if strings.TrimSpace(files) == "" {
			return false, merr
		}
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
			return false, o.fail(ctx, fact.WOFailed{Class: fact.ClassIntegration, Reason: "base_conflict", Message: "task " + t.ID + " conflicts with the base branch: " + files})
		}
		rounds := 0
		for _, c := range st.Conflicts {
			if c.A == other && c.B == t.ID {
				rounds++
			}
		}
		if rounds >= maxConflictRounds {
			return false, o.escalate(ctx, fmt.Sprintf("tasks %s and %s still conflict after %d serialize rounds (files %v)", other, t.ID, rounds, conflicted))
		}
		_, err := o.e.Facts.Append(ctx, o.woID, workflow.KindIntegrationConflict, workflow.IntegrationConflict{A: other, B: t.ID, Files: conflicted})
		return false, err
	}
	return true, nil
}

func topoOrder(tasks []workflow.Task) ([]workflow.Task, error) {
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
			return nil, fmt.Errorf("tasks cannot be ordered: dependency cycle or unknown dependency among %d tasks", len(tasks)-len(out))
		}
	}
	return out, nil
}
