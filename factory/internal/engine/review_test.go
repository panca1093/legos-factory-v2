package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/sandbox"
	"legos-factory/factory/internal/workflow"
)

// seedOrch prepares a cloned workspace and an orch over it, without running any role.
func seedOrch(t *testing.T, ev *env) *orch {
	t.Helper()
	ctx := context.Background()
	id := ev.create(t)
	ws, err := ev.e.Runtime.CreateWorkspace(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := ev.e.Runtime.Start(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Stop(ctx) })
	if err := sandbox.Clone(ctx, sb, ev.p.RepoURL, nil, "main"); err != nil {
		t.Fatal(err)
	}
	dir, err := sandbox.AddWorktree(ctx, sb, "main", "lf/"+id, "main")
	if err != nil {
		t.Fatal(err)
	}
	return &orch{e: ev.e, p: ev.p, woID: id, sb: sb, dir: dir, objective: "x", lim: workflow.DefaultLimits()}
}

// commitOn writes files on the task branch (creating it from main) and commits them.
func (o *orch) commitOn(t *testing.T, task string, files map[string]string) {
	t.Helper()
	ctx := context.Background()
	dir, err := sandbox.AddWorktree(ctx, o.sb, task, taskBranch(o.woID, task), "main")
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := writeIn(name, content)(task, 0, dir); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := sandbox.CommitAll(ctx, o.sb, dir, "lf: "+task); err != nil {
		t.Fatal(err)
	}
}

func (o *orch) branchFile(t *testing.T, ref, file string) string {
	t.Helper()
	out, _, code, err := o.sb.Exec(context.Background(), o.dir, nil, "git", "show", ref+":"+file)
	if err != nil || code != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

func builtTask(id string, deps ...string) workflow.Task {
	return workflow.Task{PlanTask: workflow.PlanTask{ID: id, Paths: []string{id + "/"}, DependsOn: deps}, Status: workflow.TaskDone}
}

func TestForemanRepeatingRetryTaskGetsToolErrorAndEscalates(t *testing.T) {
	h := newScripted()
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`),
		call("retry_task", `{"task_id":"t1","change":"same"}`), call("retry_task", `{"task_id":"t1","change":"same"}`),
		call("retry_task", `{"task_id":"t1","change":"same"}`), call("retry_task", `{"task_id":"t1","change":"same"}`),
		call("retry_task", `{"task_id":"t1","change":"same"}`))
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	acc, rej := decisions(t, ev, id)
	if len(rej) != 3 || strings.Join(rej, ",") != "retry_task,retry_task,retry_task" {
		t.Fatalf("accepted %v rejected %v", acc, rej)
	}
	if got := phase(t, ev, id); got != workflow.PhaseAwaitingHuman {
		t.Fatalf("phase = %s", got)
	}
	if n := len(h.promptsFor(harness.RoleForeman)); n != 3+1+3 {
		t.Errorf("foreman asked %d times; the loop must stop after the rejections escalate", n)
	}
	if fp := h.promptsFor(harness.RoleForeman); !strings.HasPrefix(fp[len(fp)-1], "tool error: retry_task rejected") {
		t.Errorf("last prompt = %q", fp[len(fp)-1])
	}
}

func TestIntegrateStartsFromBaseSoRebuiltTasksMergeOnce(t *testing.T) {
	ctx := context.Background()
	o := seedOrch(t, newEnv(t))
	o.commitOn(t, "a", map[string]string{"a/f.txt": "A\n"})
	o.commitOn(t, "b", map[string]string{"b/f.txt": "B\n"})
	st := workflow.State{Tasks: []workflow.Task{builtTask("a"), builtTask("b")}}
	if ready, err := o.integrate(ctx, st, nil); err != nil || !ready {
		t.Fatalf("first integrate = %v, %v", ready, err)
	}
	o.commitOn(t, "b", map[string]string{"b/f.txt": "B2\n"})
	if _, err := o.git(ctx, o.dir, "commit", "--allow-empty", "-m", "stale partial integration"); err != nil {
		t.Fatal(err)
	}
	if ready, err := o.integrate(ctx, st, nil); err != nil || !ready {
		t.Fatalf("second integrate = %v, %v", ready, err)
	}
	if got := o.branchFile(t, "HEAD", "b/f.txt"); got != "B2" {
		t.Errorf("b/f.txt = %q", got)
	}
	log, _ := o.git(ctx, o.dir, "log", "--format=%s")
	if strings.Count(log, "lf: merge a") != 1 || strings.Count(log, "lf: merge b") != 1 || strings.Contains(log, "stale partial") {
		t.Errorf("integration history:\n%s", log)
	}
}

func TestIntegrateConflictEscalatesAfterTwoRoundsPerPair(t *testing.T) {
	ctx := context.Background()
	o := seedOrch(t, newEnv(t))
	o.commitOn(t, "a", map[string]string{"shared.txt": "A\n"})
	o.commitOn(t, "b", map[string]string{"shared.txt": "B\n"})
	st := workflow.State{Tasks: []workflow.Task{builtTask("a"), builtTask("b")}}

	if ready, err := o.integrate(ctx, st, nil); err != nil || ready {
		t.Fatalf("integrate = %v, %v", ready, err)
	}
	if st := workflow.Fold(mustListFacts(t, o)); len(st.Conflicts) != 1 || st.Conflicts[0].A != "a" || st.Conflicts[0].B != "b" || st.Phase == workflow.PhaseAwaitingHuman {
		t.Fatalf("first conflict not recorded: %+v", st.Conflicts)
	}

	prior := workflow.Conflict{IntegrationConflict: workflow.IntegrationConflict{A: "a", B: "b"}, Serialized: true}
	st.Conflicts = []workflow.Conflict{prior, prior}
	if ready, err := o.integrate(ctx, st, nil); err != nil || ready {
		t.Fatalf("integrate = %v, %v", ready, err)
	}
	facts := mustListFacts(t, o)
	if last := facts[len(facts)-1]; last.Kind != workflow.KindEscalated || !strings.Contains(string(last.Payload), "still conflict") {
		t.Errorf("last fact = %s %s", last.Kind, last.Payload)
	}
}

func mustListFacts(t *testing.T, o *orch) []fact.Fact {
	t.Helper()
	facts, err := o.e.Facts.List(context.Background(), o.woID)
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func TestIntegrateMergeFailureWithoutConflictIsInfrastructureError(t *testing.T) {
	ctx := context.Background()
	o := seedOrch(t, newEnv(t))
	st := workflow.State{Tasks: []workflow.Task{builtTask("ghost")}}
	if ready, err := o.integrate(ctx, st, nil); err == nil || ready {
		t.Fatalf("missing branch = %v, %v", ready, err)
	}
	for _, f := range mustListFacts(t, o) {
		if f.Kind == workflow.KindIntegrationConflict || f.Kind == fact.KindWOFailed {
			t.Fatalf("fabricated %s", f.Kind)
		}
	}
}

func TestTopoOrderReportsCycles(t *testing.T) {
	ok, err := topoOrder([]workflow.Task{builtTask("b", "a"), builtTask("a")})
	if err != nil || ok[0].ID != "a" {
		t.Fatalf("order = %v, %v", ok, err)
	}
	if _, err := topoOrder([]workflow.Task{builtTask("a", "b"), builtTask("b", "a")}); err == nil {
		t.Fatal("cycle ordered")
	}
	if _, err := topoOrder([]workflow.Task{builtTask("a", "missing")}); err == nil {
		t.Fatal("unknown dependency ordered")
	}
}

func TestDependencyMergeConflictFailsTaskUnlessSerialized(t *testing.T) {
	ctx := context.Background()
	h := newScripted()
	h.build = writeIn("b/g.txt", "rebuilt\n")
	ev := newEnv(t)
	ev.e.Harness = h
	o := seedOrch(t, ev)
	o.commitOn(t, "a", map[string]string{"shared.txt": "A\n"})
	o.commitOn(t, "b", map[string]string{"shared.txt": "B\n"})
	b := workflow.Task{PlanTask: workflow.PlanTask{ID: "b", Paths: []string{"b/", "shared.txt"}, DependsOn: []string{"a"}}, Status: workflow.TaskBuilding}
	cfg := BuilderConfig{Profile: harness.Profile{Agent: "claude-agent-acp"}, AgentCommand: []string{"claude-agent-acp"}}
	o.p.Builder = cfg

	if err := o.buildTask(ctx, b, mustListFacts(t, o), 30000); err != nil {
		t.Fatal(err)
	}
	facts := mustListFacts(t, o)
	last := facts[len(facts)-1]
	if f, _ := fact.Decode[fact.TaskFailed](last); last.Kind != fact.KindTaskFailed || f.Class != fact.ClassIntegration || f.Reason != "dependency_conflict" {
		t.Fatalf("last fact = %s %s", last.Kind, last.Payload)
	}
	if len(h.promptsFor(harness.RoleBuilder)) != 0 {
		t.Fatal("builder ran on a conflicting dependency")
	}
	if got := o.branchFile(t, taskBranch(o.woID, "b"), "shared.txt"); got != "B" {
		t.Errorf("b's work was discarded without a serialize: %q", got)
	}

	if _, err := o.e.Facts.Append(ctx, o.woID, workflow.KindTasksSerialized, workflow.TasksSerialized{A: "a", B: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := o.buildTask(ctx, b, mustListFacts(t, o), 30000); err != nil {
		t.Fatal(err)
	}
	if got := o.branchFile(t, taskBranch(o.woID, "b"), "shared.txt"); got != "A" {
		t.Errorf("serialized rebuild did not start from a: %q", got)
	}
	if len(h.promptsFor(harness.RoleBuilder)) != 1 {
		t.Fatal("builder did not run after serialize")
	}
}

func TestParsePlanCleansOwnedPaths(t *testing.T) {
	p, err := parsePlan(planJSON(`{"id":"a","description":"d","owned_paths":["./src//x/../y/","../out","/abs"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Tasks[0].Paths; got[0] != "src/y" || got[1] != "../out" || got[2] != "/abs" {
		t.Errorf("paths = %q", got)
	}
}

func TestBuildOutsideOwnedPathsFailsAsPathViolation(t *testing.T) {
	h := newScripted()
	h.build = func(task string, n int, dir string) error {
		if err := writeIn("pkg/out.txt", "ok\n")(task, n, dir); err != nil {
			return err
		}
		return writeIn("stray/leak.txt", "no\n")(task, n, dir)
	}
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`))
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	var failed fact.TaskFailed
	for _, f := range mustList(t, ev, id) {
		if f.Kind == workflow.KindTaskBuilt {
			t.Fatal("violating task was recorded as built")
		}
		if f.Kind == fact.KindTaskFailed {
			failed, _ = fact.Decode[fact.TaskFailed](f)
		}
	}
	if failed.Class != "task" || failed.Reason != "path_violation" || !strings.Contains(failed.Message, "stray/leak.txt") || strings.Contains(failed.Message, "pkg/out.txt") {
		t.Errorf("failure = %+v", failed)
	}
	if fp := h.promptsFor(harness.RoleForeman); !strings.Contains(fp[len(fp)-1], "path_violation") {
		t.Errorf("Foreman not told: %q", fp[len(fp)-1])
	}
}

func TestArchitectEditsDoNotReachThePR(t *testing.T) {
	h := newScripted()
	h.build = writeIn("pkg/out.txt", "built\n")
	var archDirs []string
	h.onRole = func(s harness.SessionSpec) {
		if s.Role == harness.RoleArchitect {
			archDirs = append(archDirs, s.Dir)
			_ = os.WriteFile(filepath.Join(s.Dir, "architect-scribble.txt"), []byte("x"), 0o644)
		}
	}
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`), call("request_inspection", `{"task_id":"t1"}`))
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	if len(archDirs) != 2 || archDirs[0] != archDirs[1] || !strings.HasSuffix(archDirs[0], "/wt/_architect") {
		t.Fatalf("architect dirs = %q", archDirs)
	}
	if files := sh(t, ev.p.RepoURL, "git", "ls-tree", "-r", "--name-only", "lf/"+id); strings.Contains(files, "architect-scribble") {
		t.Errorf("architect file reached the PR branch:\n%s", files)
	}
	for _, s := range h.specs {
		if s.Role == harness.RoleArchitect && !contains(s.ToolPolicy.Deny, "Write") {
			t.Errorf("architect policy = %+v", s.ToolPolicy)
		}
	}
}

func TestForemanRunsToollessInEmptyDirectory(t *testing.T) {
	h := newScripted()
	var seen []string
	h.onRole = func(s harness.SessionSpec) {
		if s.Role == harness.RoleForeman {
			entries, _ := os.ReadDir(s.Dir)
			seen = append(seen, s.Dir, string(rune('0'+len(entries))))
		}
	}
	h.say(harness.RoleArchitect, "spec")
	h.say(harness.RoleForeman, call("approve_spec", `{}`))
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	var fm *harness.SessionSpec
	for i := range h.specs {
		if h.specs[i].Role == harness.RoleForeman {
			fm = &h.specs[i]
		}
	}
	if fm == nil || !fm.ToolPolicy.AllowNone || !strings.HasSuffix(fm.Dir, "/home/foreman-cwd") {
		t.Fatalf("foreman spec = %+v", fm)
	}
	if len(seen) < 2 || seen[1] != "0" {
		t.Errorf("foreman cwd not empty: %q", seen)
	}
}

func TestLineModeIsFixedAtCreation(t *testing.T) {
	ctx := context.Background()
	h := newScripted()
	ev := newEnv(t)
	ev.e.Harness = h
	h.build = writeIn("out.txt", "built\n")
	id := ev.create(t)
	if created, _ := fact.Decode[fact.WOCreated](mustList(t, ev, id)[0]); created.Line {
		t.Fatal("line recorded for a project without foreman or architect")
	}
	cfg := BuilderConfig{Profile: harness.Profile{Agent: "claude-agent-acp", Model: "opus"}, AgentCommand: []string{"claude-agent-acp"}}
	ev.p.Roles = map[string]BuilderConfig{"foreman": cfg, "architect": cfg}
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	if len(h.promptsFor(harness.RoleForeman)) != 0 || len(h.promptsFor(harness.RoleArchitect)) != 0 || len(h.promptsFor(harness.RoleBuilder)) != 1 {
		t.Errorf("work order switched to the line mid-flight")
	}

	line := ev.create(t)
	if created, _ := fact.Decode[fact.WOCreated](mustList(t, ev, line)[0]); !created.Line {
		t.Error("line not recorded when roles are configured at creation")
	}
}

func TestDecideRefoldsWhenAnotherFactLandsFirst(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	id := ev.create(t)
	for _, f := range []struct {
		kind string
		v    any
	}{
		{fact.KindRunStarted, fact.RunStarted{Run: "r1", Role: workflow.RoleArchitect}},
		{workflow.KindSpecReady, workflow.SpecReady{Spec: "s"}},
	} {
		if _, err := ev.e.Facts.Append(ctx, id, f.kind, f.v); err != nil {
			t.Fatal(err)
		}
	}
	o := &orch{e: ev.e, woID: id, lim: workflow.DefaultLimits()}
	calls := 0
	o.beforeAppend = func() {
		if calls++; calls == 1 {
			if _, err := ev.e.Facts.Append(ctx, id, workflow.KindEscalated, workflow.Escalated{Reason: "human got there first"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	cerr, err := o.decide(ctx, call("approve_spec", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || cerr == nil || !strings.Contains(cerr.Error(), "needs spec_ready") {
		t.Fatalf("calls = %d, tool error = %v", calls, cerr)
	}
	if acc, rej := decisions(t, ev, id); len(acc) != 0 || len(rej) != 1 {
		t.Errorf("approve_spec was applied to a stale state: accepted %v rejected %v", acc, rej)
	}
}

func TestResumedForemanSessionGetsOnlyTheDelta(t *testing.T) {
	h := newScripted()
	h.build = writeIn("pkg/out.txt", "built\n")
	h.say(harness.RoleArchitect, "The spec text.", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), call("dispatch_build", `{"task_ids":["t1"]}`), call("request_inspection", `{"task_id":"t1"}`))
	ev := lineEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	fp := h.promptsFor(harness.RoleForeman)
	if len(fp) != 4 || !strings.Contains(fp[0], "Transition tools (JSON schema)") || !strings.Contains(fp[0], "The spec text.") {
		t.Fatalf("first prompt is not the full context: %q", fp[0])
	}
	if !strings.Contains(fp[1], "Phase: plan_ready") || !strings.Contains(fp[1], "plan.ready") || !strings.Contains(fp[1], `"id":"t1"`) {
		t.Errorf("delta lacks the plan: %q", fp[1])
	}
	for i, p := range fp[1:] {
		if strings.Contains(p, "Transition tools (JSON schema)") || strings.Contains(p, "Objective:") {
			t.Errorf("resumed prompt %d repeats the full context: %q", i+1, p)
		}
	}
	if last := fp[3]; !strings.Contains(last, "task.built") || !strings.Contains(last, "t1[built]") {
		t.Errorf("last delta = %q", last)
	}
}
