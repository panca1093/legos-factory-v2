package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/workflow"
)

// scripted answers each role from its own reply queue; an exhausted Foreman queue escalates so tests end cleanly.
type scripted struct {
	mu       sync.Mutex
	replies  map[harness.Role][]string
	prompts  map[harness.Role][]string
	specs    []harness.SessionSpec
	builds   map[string]int
	injected map[harness.Role]int
	build    func(task string, n int, dir string) error
	onRole   func(spec harness.SessionSpec)                    // runs while a non-builder role is prompted
	inject   func(role harness.Role, task string, n int) error // fails the n-th prompt of (role, task) before it does anything
	started  int
}

func newScripted() *scripted {
	return &scripted{replies: map[harness.Role][]string{}, prompts: map[harness.Role][]string{}, builds: map[string]int{}, injected: map[harness.Role]int{}}
}

func (h *scripted) say(role harness.Role, replies ...string) *scripted {
	h.replies[role] = append(h.replies[role], replies...)
	return h
}

func (h *scripted) Start(_ context.Context, s harness.SessionSpec) (harness.Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.specs = append(h.specs, s)
	h.started++
	id := s.SessionID
	if id == "" {
		id = fmt.Sprintf("s-%s-%d", s.Role, h.started)
	}
	return &scriptedSession{h: h, id: id, spec: s, events: make(chan harness.Event, 16)}, nil
}

func (h *scripted) promptsFor(role harness.Role) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.prompts[role]...)
}

type scriptedSession struct {
	h      *scripted
	id     string
	spec   harness.SessionSpec
	events chan harness.Event
}

func (s *scriptedSession) ID() string                   { return s.id }
func (s *scriptedSession) Events() <-chan harness.Event { return s.events }
func (s *scriptedSession) Close() error                 { close(s.events); return nil }

func (s *scriptedSession) Prompt(_ context.Context, text string) (string, error) {
	h := s.h
	role := s.spec.Role
	h.mu.Lock()
	h.prompts[role] = append(h.prompts[role], text)
	var reply string
	if q := h.replies[role]; len(q) > 0 {
		reply, h.replies[role] = q[0], q[1:]
	} else if role == harness.RoleForeman {
		reply = `{"tool":"escalate_to_human","args":{"reason":"script exhausted"}}`
	}
	task := filepath.Base(s.spec.Dir)
	n := 0
	if role == harness.RoleBuilder {
		h.builds[task]++
		n = h.builds[task]
	}
	build, onRole, inject := h.build, h.onRole, h.inject
	h.mu.Unlock()
	if inject != nil {
		h.mu.Lock()
		h.injected[role+"/"+harness.Role(task)]++
		k := h.injected[role+"/"+harness.Role(task)]
		h.mu.Unlock()
		if err := inject(role, task, k); err != nil {
			return "", err
		}
	}
	if role != harness.RoleBuilder && onRole != nil {
		onRole(s.spec)
	}
	if role == harness.RoleBuilder {
		if build != nil {
			if err := build(task, n, s.spec.Dir); err != nil {
				return "", err
			}
		}
		reply = "done"
	}
	s.events <- harness.Event{Kind: harness.EventToolCall, Tool: "think", Text: "x"}
	s.events <- harness.Event{Kind: harness.EventStep, Text: reply}
	return "end_turn", nil
}

func call(tool, args string) string {
	return fmt.Sprintf("Reasoning first.\n{\"tool\":%q,\"args\":%s}", tool, args)
}

func planJSON(tasks string) string {
	return "Here is the plan.\n```json\n{\"tasks\":[" + tasks + "]}\n```"
}

const (
	taskA = `{"id":"a","description":"build a","owned_paths":["a/"],"depends_on":[]}`
	taskB = `{"id":"b","description":"build b","owned_paths":["b/"],"depends_on":[]}`
)

func writeIn(rel, content string) func(string, int, string) error {
	return func(_ string, _ int, dir string) error {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(content), 0o644)
	}
}

func lineEnv(t *testing.T, h *scripted, verify ...string) *env {
	t.Helper()
	ev := newEnv(t, verify...)
	ev.e.Harness = h
	cfg := BuilderConfig{Profile: harness.Profile{Agent: "claude-agent-acp", Model: "opus"}, AgentCommand: []string{"claude-agent-acp"}}
	ev.p.Roles = map[string]BuilderConfig{"foreman": cfg, "architect": cfg}
	return ev
}

func decisions(t *testing.T, ev *env, id string) (accepted, rejected []string) {
	t.Helper()
	facts, _ := ev.e.Facts.List(context.Background(), id)
	for _, f := range facts {
		if f.Kind == workflow.KindForemanDecision {
			d, _ := fact.Decode[workflow.ForemanDecision](f)
			if d.Accepted {
				accepted = append(accepted, d.Tool)
			} else {
				rejected = append(rejected, d.Tool)
			}
		}
	}
	return
}

func phase(t *testing.T, ev *env, id string) workflow.Phase {
	t.Helper()
	facts, _ := ev.e.Facts.List(context.Background(), id)
	return workflow.Fold(facts).Phase
}

func TestLineHappyPathToPR(t *testing.T) {
	ctx := context.Background()
	h := newScripted()
	h.build = writeIn("pkg/out.txt", "built\n")
	h.say(harness.RoleArchitect, "The spec: add pkg/out.txt.", planJSON(`{"id":"t1","description":"add out","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman,
		call("approve_spec", `{"note":"ok"}`), call("approve_plan", `{"note":"ok"}`),
		call("dispatch_build", `{"task_ids":["t1"]}`), call("request_inspection", `{"task_id":"t1"}`))
	ev := lineEnv(t, h, "test -f pkg/out.txt")
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	k := ev.kinds(t, ev.e, id)
	for _, want := range []string{workflow.KindSpecReady, workflow.KindPlanReady, workflow.KindTaskDispatched, workflow.KindTaskBuilt,
		workflow.KindInspectionRequested, workflow.KindInspectionDone, fact.KindVerifyPassed, fact.KindPROpened} {
		if count(k, want) == 0 {
			t.Errorf("missing %s in %v", want, k)
		}
	}
	if acc, rej := decisions(t, ev, id); len(acc) != 4 || len(rej) != 0 {
		t.Errorf("decisions = %v rejected %v", acc, rej)
	}
	if got := sh(t, ev.p.RepoURL, "git", "show", "lf/"+id+":pkg/out.txt"); got != "built" {
		t.Errorf("pushed content = %q", got)
	}
	if !strings.Contains(sh(t, ev.p.RepoURL, "git", "log", "--format=%s", "lf/"+id), "lf: merge t1") {
		t.Error("task branch not merged with --no-ff")
	}

	roles := map[harness.Role]harness.SessionSpec{}
	for _, s := range h.specs {
		roles[s.Role] = s
	}
	fm := roles[harness.RoleForeman]
	if !strings.HasSuffix(fm.Env["CLAUDE_CONFIG_DIR"], "/foreman-foreman") || !strings.HasSuffix(roles[harness.RoleArchitect].Env["CLAUDE_CONFIG_DIR"], "/architect-architect") ||
		!strings.HasSuffix(roles[harness.RoleBuilder].Env["CLAUDE_CONFIG_DIR"], "/builder-t1") {
		t.Errorf("config dirs: %v / %v / %v", fm.Env, roles[harness.RoleArchitect].Env, roles[harness.RoleBuilder].Env)
	}
	for _, tool := range []string{"Bash", "Read", "Grep", "WebFetch", "Task"} {
		if !contains(fm.ToolPolicy.Deny, tool) {
			t.Errorf("foreman does not deny %s", tool)
		}
	}
	if !contains(roles[harness.RoleArchitect].ToolPolicy.Deny, "Edit") || !contains(roles[harness.RoleArchitect].ToolPolicy.Deny, "Bash") || roles[harness.RoleArchitect].ToolPolicy.AllowNone || !harness.RoleArchitect.ReadOnly() {
		t.Errorf("architect policy = %+v", roles[harness.RoleArchitect].ToolPolicy)
	}
	if p := h.promptsFor(harness.RoleBuilder); len(p) != 1 || !strings.Contains(p[0], "add out.txt") || !strings.Contains(p[0], "The spec: add pkg/out.txt.") || !strings.Contains(p[0], "pkg/") {
		t.Errorf("builder prompt = %q", p)
	}
	fp := h.promptsFor(harness.RoleForeman)[0]
	for _, want := range []string{"Phase: spec_ready", "The spec: add pkg/out.txt.", "approve_spec", "input_schema", "Autonomy level cap: 3"} {
		if !strings.Contains(fp, want) {
			t.Errorf("foreman prompt lacks %q", want)
		}
	}
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindRunStarted {
			r, _ := fact.Decode[fact.RunStarted](f)
			if r.Role == "builder" && r.Task != "t1" {
				t.Errorf("builder run task = %q", r.Task)
			}
		}
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func mustList(t *testing.T, ev *env, id string) []fact.Fact {
	t.Helper()
	facts, err := ev.e.Facts.List(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func TestForemanIllegalCallGetsToolErrorThenLegalAccepted(t *testing.T) {
	h := newScripted()
	h.build = writeIn("pkg/out.txt", "built\n")
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman,
		call("dispatch_build", `{"task_ids":["t1"]}`), // illegal in spec_ready
		call("approve_spec", `{"note":"ok"}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`),
		call("request_inspection", `{"task_id":"t1"}`))
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	fp := h.promptsFor(harness.RoleForeman)
	if len(fp) < 2 || !strings.HasPrefix(fp[1], "tool error: dispatch_build rejected") {
		t.Fatalf("foreman prompts = %q", fp)
	}
	acc, rej := decisions(t, ev, id)
	if len(rej) != 1 || rej[0] != "dispatch_build" || acc[0] != "approve_spec" {
		t.Errorf("accepted %v rejected %v", acc, rej)
	}
	var sess []string
	for _, s := range h.specs {
		if s.Role == harness.RoleForeman {
			sess = append(sess, s.SessionID)
		}
	}
	if sess[0] != "" || sess[1] == "" {
		t.Errorf("foreman sessions not resumed after the tool error: %q", sess)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s", st)
	}
}

func TestConsecutiveRejectionsEscalateToHuman(t *testing.T) {
	h := newScripted()
	h.say(harness.RoleArchitect, "spec")
	h.say(harness.RoleForeman, call("dispatch_build", `{"task_ids":["x"]}`), "no json at all", call("approve_plan", `{}`))
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusAwaitingHuman {
		t.Fatalf("status = %s", st)
	}
	if n := len(h.promptsFor(harness.RoleForeman)); n != 3 {
		t.Errorf("foreman asked %d times, want 3", n)
	}
	if k := ev.kinds(t, ev.e, id); count(k, workflow.KindEscalated) != 1 || count(k, fact.KindPROpened) != 0 {
		t.Errorf("kinds = %v", k)
	}
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil || len(h.promptsFor(harness.RoleForeman)) != 3 {
		t.Errorf("run on a parked order asked the Foreman again: %v", err)
	}
}

func TestOverlappingParallelPlanRejectedAndArchitectRevises(t *testing.T) {
	h := newScripted()
	h.build = func(task string, _ int, dir string) error { return writeIn(task+"/f.txt", task)(task, 0, dir) }
	h.say(harness.RoleArchitect, "spec",
		planJSON(`{"id":"a","description":"a","owned_paths":["src/"]},{"id":"b","description":"b","owned_paths":["src/x/"]}`),
		planJSON(`{"id":"a","description":"a","owned_paths":["src/"]},{"id":"b","description":"b","owned_paths":["src/x/"],"depends_on":["a"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`),
		call("reject_plan", `{"reason":"src/ and src/x/ overlap"}`), call("approve_plan", `{}`))
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	acc, rej := decisions(t, ev, id)
	if len(rej) != 1 || rej[0] != "approve_plan" || strings.Join(acc, ",") != "approve_spec,reject_plan,approve_plan,escalate_to_human" {
		t.Errorf("accepted %v rejected %v", acc, rej)
	}
	ap := h.promptsFor(harness.RoleArchitect)
	if len(ap) != 3 || !strings.Contains(ap[2], "src/ and src/x/ overlap") {
		t.Fatalf("architect prompts = %q", ap)
	}
	var arch []string
	for _, s := range h.specs {
		if s.Role == harness.RoleArchitect {
			arch = append(arch, s.SessionID)
		}
	}
	if arch[2] == "" || arch[2] != arch[1] && arch[1] != "" {
		t.Errorf("architect revision did not resume the session: %q", arch)
	}
	fp := h.promptsFor(harness.RoleForeman)
	if !strings.Contains(fp[2], "tool error: approve_plan rejected") || !strings.Contains(fp[2], "overlapping") {
		t.Errorf("tool error prompt = %q", fp[2])
	}
	if got := phase(t, ev, id); got != workflow.PhaseAwaitingHuman {
		t.Errorf("phase = %s", got)
	}
}

func TestUnparseablePlanAskedOnceThenEmptyPlanReachesForeman(t *testing.T) {
	h := newScripted()
	h.say(harness.RoleArchitect, "spec", "I forgot the JSON", "still no JSON")
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`))
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	ap := h.promptsFor(harness.RoleArchitect)
	if len(ap) != 3 || !strings.Contains(ap[2], "could not be used as a plan") {
		t.Fatalf("architect prompts = %q", ap)
	}
	var plan workflow.PlanReady
	for _, f := range mustList(t, ev, id) {
		if f.Kind == workflow.KindPlanReady {
			plan, _ = fact.Decode[workflow.PlanReady](f)
		}
	}
	if len(plan.Tasks) != 0 {
		t.Errorf("plan = %+v", plan)
	}
	fp := h.promptsFor(harness.RoleForeman)
	if !strings.Contains(fp[2], "plan has no tasks") {
		t.Errorf("foreman was not told: %q", fp[2])
	}
}

func TestLevelZeroPlanGateWaitsForHumanApproval(t *testing.T) {
	ctx := context.Background()
	h := newScripted()
	h.build = writeIn("pkg/out.txt", "built\n")
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{"note":"fine"}`),
		call("dispatch_build", `{"task_ids":["t1"]}`), call("request_inspection", `{"task_id":"t1"}`))
	ev := lineEnv(t, h)
	id, err := ev.e.CreateAt(ctx, ev.p, "add out.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusAwaitingHuman {
		t.Fatalf("status = %s", st)
	}
	if len(h.promptsFor(harness.RoleBuilder)) != 0 {
		t.Fatal("built before the human approved the plan")
	}
	if err := ev.e.Run(ctx, ev.p, id); err != nil || len(h.promptsFor(harness.RoleForeman)) != 2 {
		t.Fatalf("parked order was driven: %v", err)
	}
	if _, err := ev.e.Facts.Append(ctx, id, workflow.KindHumanApproved, workflow.HumanApproved{Gate: workflow.GatePlan}); err != nil {
		t.Fatal(err)
	}
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
}

func TestRestartExecutesRecordedDecisionWithoutAskingForeman(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h1 := newScripted()
	h1.build = func(string, int, string) error { cancel(); return context.Canceled }
	h1.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h1.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`))
	ev := lineEnv(t, h1)
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err == nil {
		t.Fatal("run survived the kill")
	}
	if k := ev.kinds(t, ev.e, id); count(k, workflow.KindTaskDispatched) != 1 || count(k, workflow.KindTaskBuilt) != 0 {
		t.Fatalf("decision not recorded before the kill: %v", k)
	}

	h2 := newScripted()
	h2.build = writeIn("pkg/out.txt", "built\n")
	h2.say(harness.RoleForeman, call("request_inspection", `{"task_id":"t1"}`))
	e2 := ev.engine(t)
	e2.Harness = h2
	if err := e2.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if fp := h2.promptsFor(harness.RoleForeman); len(fp) != 1 || !strings.Contains(fp[0], "[built]") {
		t.Fatalf("restarted engine asked the Foreman again: %q", fp)
	}
	if len(h2.promptsFor(harness.RoleArchitect)) != 0 {
		t.Error("restart re-ran the Architect")
	}
	if p := h2.promptsFor(harness.RoleBuilder); len(p) != 1 || !strings.Contains(p[0], "interrupted") {
		t.Errorf("builder prompt = %q", p)
	}
	if st := status(t, e2, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, e2, id))
	}
	if acc, rej := decisions(t, ev, id); len(rej) != 0 || len(acc) != 4 {
		t.Errorf("accepted %v rejected %v", acc, rej)
	}
}

func waitBuilt(t *testing.T, ev *env, id, task string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		facts, _ := ev.e.Facts.List(context.Background(), id)
		for _, f := range facts {
			if v, _ := fact.Decode[workflow.TaskBuiltPayload](f); f.Kind == workflow.KindTaskBuilt && v.Task == task {
				return
			}
		}
	}
	t.Errorf("task %s never built", task)
}

func TestIntegrationConflictSerializeRebuildsOnDependency(t *testing.T) {
	h := newScripted()
	var ev *env
	var id string
	// owned-path enforcement makes a genuine git conflict unreachable, so b's first build records one the way integrate would
	h.build = func(task string, n int, dir string) error {
		content := strings.ToUpper(task) + "\n"
		if task == "b" && n == 1 {
			waitBuilt(t, ev, id, "a") // a builds first, so the Foreman's scripted replies line up with the facts
		}
		if n > 1 {
			content = "B on A\n" // second build of b sees a's file
			if b, err := os.ReadFile(filepath.Join(dir, "a/shared.txt")); err != nil || string(b) != "A\n" {
				return fmt.Errorf("b was not rebuilt on top of a: %q %v", b, err)
			}
		}
		return writeIn(task+"/shared.txt", content)(task, n, dir)
	}
	foremanCalls := 0
	h.onRole = func(s harness.SessionSpec) { // lands after the Foreman's last turn, so its next resumed prompt lists the conflict
		if s.Role != harness.RoleForeman {
			return
		}
		if foremanCalls++; foremanCalls == 5 {
			if _, err := ev.e.Facts.Append(context.Background(), id, workflow.KindIntegrationConflict, workflow.IntegrationConflict{A: "a", B: "b", Files: []string{"shared.txt"}}); err != nil {
				t.Error(err)
			}
		}
	}
	h.say(harness.RoleArchitect, "spec", planJSON(taskA+","+taskB))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`),
		call("dispatch_build", `{"task_ids":["a","b"]}`), call("request_inspection", `{"task_id":"a"}`), call("request_inspection", `{"task_id":"b"}`),
		call("serialize", `{"task_a":"b","task_b":"a"}`),
		call("serialize", `{"task_a":"a","task_b":"b"}`), call("dispatch_build", `{"task_ids":["b"]}`), call("request_inspection", `{"task_id":"b"}`))
	ev = lineEnv(t, h)
	id = ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if k := ev.kinds(t, ev.e, id); count(k, workflow.KindIntegrationConflict) != 1 {
		t.Fatalf("kinds = %v", k)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	if got := sh(t, ev.p.RepoURL, "git", "show", "lf/"+id+":b/shared.txt"); got != "B on A" {
		t.Errorf("b/shared.txt = %q", got)
	}
	if log := sh(t, ev.p.RepoURL, "git", "log", "--format=%s", "lf/"+id); strings.Count(log, "lf: merge a") != 1 || strings.Count(log, "lf: merge b") != 1 {
		t.Errorf("tasks not merged exactly once:\n%s", log)
	}
	var conflictPrompt string
	for _, p := range h.promptsFor(harness.RoleForeman) {
		if strings.Contains(p, "integration.conflict") && strings.Contains(p, "shared.txt") {
			conflictPrompt = p
		}
	}
	if conflictPrompt == "" {
		t.Error("Foreman was not shown the conflict")
	}
	if acc, rej := decisions(t, ev, id); len(rej) != 1 || rej[0] != "serialize" || !contains(acc, "serialize") {
		t.Errorf("reversed serialize not rejected once: accepted %v rejected %v", acc, rej)
	}
}

func TestExtractJSON(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"fenced json":            {"text\n```json\n{\"a\":1}\n```\nbye", `{"a":1}`},
		"last fence wins":        {"```json\n{\"a\":1}\n```\n```json\n{\"a\":2}\n```", `{"a":2}`},
		"bare object":            {`I think {"tool":"x","args":{"n":{"m":1}}}`, `{"tool":"x","args":{"n":{"m":1}}}`},
		"last object wins":       {`{"a":1} then {"b":"}{"}`, `{"b":"}{"}`},
		"braces in prose":        {`use {braces} like {"ok":true}`, `{"ok":true}`},
		"unlabelled fence":       {"```\n{\"a\":3}\n```", `{"a":3}`},
		"non-json fence skipped": {"```go\nfunc main() {}\n```\n{\"a\":4}", `{"a":4}`},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := extractJSON(tc.in)
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	if _, err := extractJSON("no objects { here"); err == nil {
		t.Error("expected an error")
	}
}

func TestParsePlanRejectsUnsafeTaskIDs(t *testing.T) {
	for _, id := range []string{"main", "../x", "a/b", "", "a b"} {
		if _, err := parsePlan(planJSON(fmt.Sprintf(`{"id":%q,"description":"d"}`, id))); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
	p, err := parsePlan(planJSON(`{"id":"t-1.x","description":"d","owned_paths":["a"],"depends_on":["z"]}`))
	if err != nil || p.Tasks[0].Paths[0] != "a" || p.Tasks[0].DependsOn[0] != "z" {
		t.Errorf("plan = %+v, %v", p, err)
	}
}
