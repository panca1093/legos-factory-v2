package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/workflow"
)

func verdict(logic bool, findings ...string) string {
	return "Review done.\n```json\n" + fmt.Sprintf(`{"findings":[%s],"logic_changed":%v,"summary":"s"}`, strings.Join(findings, ","), logic) + "\n```"
}

func finding(sev, msg string) string {
	return fmt.Sprintf(`{"severity":%q,"file":"pkg/out.txt","line":3,"message":%q}`, sev, msg)
}

func inspectorLine(t *testing.T, inspector []string, foreman ...string) (*scripted, *env) {
	t.Helper()
	h := newScripted()
	h.build = func(_ string, n int, dir string) error {
		return writeIn("pkg/out.txt", fmt.Sprintf("v%d\n", n))("", n, dir)
	}
	h.say(harness.RoleArchitect, "The spec.", planJSON(`{"id":"t1","description":"add out","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, append([]string{call("approve_spec", `{}`), call("approve_plan", `{}`)}, foreman...)...)
	h.say(harness.RoleInspector, inspector...)
	return h, lineEnv(t, h, "test -f pkg/out.txt")
}

func dispatch() string    { return call("dispatch_build", `{"task_ids":["t1"]}`) }
func inspectCall() string { return call("request_inspection", `{"task_id":"t1"}`) }

func runWO(t *testing.T, ev *env) (string, []fact.Fact) {
	t.Helper()
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	return id, mustList(t, ev, id)
}

func TestInspectorFindingsResumeBuilderSessionAndFixLands(t *testing.T) {
	h, ev := inspectorLine(t, []string{verdict(true, finding("major", "off by one in loop")), verdict(true)},
		dispatch(), inspectCall(), dispatch(), inspectCall())
	id, facts := runWO(t, ev)
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	var builders []harness.SessionSpec
	for _, s := range h.specs {
		if s.Role == harness.RoleBuilder {
			builders = append(builders, s)
		}
	}
	if len(builders) != 2 || builders[0].SessionID != "" || !strings.HasPrefix(builders[1].SessionID, "s-builder-") {
		t.Fatalf("builder sessions: %+v", builders)
	}
	var first string
	for _, f := range facts {
		if f.Kind == fact.KindRunStarted {
			if r, _ := fact.Decode[fact.RunStarted](f); r.Role == "builder" {
				if first == "" {
					first = r.SessionID
				} else if r.SessionID != first {
					t.Errorf("builder session %q after findings, want the same %q", r.SessionID, first)
				}
			}
		}
	}
	if builders[1].SessionID != first {
		t.Errorf("resumed %q, first was %q", builders[1].SessionID, first)
	}
	if p := h.promptsFor(harness.RoleBuilder); len(p) != 2 || !strings.Contains(p[1], "off by one in loop") || !strings.Contains(p[1], "pkg/out.txt:3") || strings.Contains(p[0], "off by one") {
		t.Errorf("builder prompts = %q", p)
	}
	if got := sh(t, ev.p.RepoURL, "git", "show", "lf/"+id+":pkg/out.txt"); got != "v2" {
		t.Errorf("pushed content = %q, want the fix commit", got)
	}
	if !workflow.Fold(facts).LogicChanged() {
		t.Error("logic_changed not aggregated")
	}
	ip := h.promptsFor(harness.RoleInspector)
	if len(ip) != 2 || !strings.Contains(ip[0], "<<<DIFF-") || !strings.Contains(ip[0], "<<<END-DIFF-") || !strings.Contains(ip[0], "+v1") || !strings.Contains(ip[1], "+v2") {
		t.Errorf("inspector prompts = %q", ip)
	}
	for _, s := range h.specs {
		if s.Role == harness.RoleInspector && (!contains(s.ToolPolicy.Deny, "Edit") || !contains(s.ToolPolicy.Deny, "Bash") || !strings.HasSuffix(s.Env["CLAUDE_CONFIG_DIR"], "/inspector-t1") || filepath.Base(s.Dir) != "t1") {
			t.Errorf("inspector spec = %+v", s)
		}
	}
}

func TestInspectorRejectedAfterKRoundsFailsTaskForForeman(t *testing.T) {
	bad := verdict(false, finding("blocker", "wrong result"))
	h, ev := inspectorLine(t, []string{bad, bad, bad},
		dispatch(), inspectCall(), dispatch(), inspectCall(), dispatch(), inspectCall())
	id, facts := runWO(t, ev)
	var failed *fact.TaskFailed
	for _, f := range facts {
		if f.Kind == fact.KindTaskFailed {
			v, _ := fact.Decode[fact.TaskFailed](f)
			failed = &v
		}
	}
	if failed == nil || failed.Class != fact.ClassTask || failed.Reason != "inspector_rejected" || !strings.Contains(failed.Message, "wrong result") {
		t.Fatalf("task.failed = %+v", failed)
	}
	if got := len(h.promptsFor(harness.RoleInspector)); got != 3 {
		t.Errorf("inspector ran %d times, want K=3", got)
	}
	if phase(t, ev, id) != workflow.PhaseAwaitingHuman {
		t.Errorf("phase = %s, want the Foreman's escalation", phase(t, ev, id))
	}
	if tk, _ := workflow.Fold(facts).Task("t1"); tk.Status != workflow.TaskFailed {
		t.Errorf("task = %+v", tk)
	}
	if fp := h.promptsFor(harness.RoleForeman); !strings.Contains(fp[len(fp)-1], "inspector_rejected") {
		t.Error("the Foreman is not told why the task failed")
	}
}

func TestInspectorMinorOnlyFindingsPass(t *testing.T) {
	h, ev := inspectorLine(t, []string{verdict(false, finding("minor", "rename x"))}, dispatch(), inspectCall())
	id, facts := runWO(t, ev)
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s", st)
	}
	var done workflow.InspectionDone
	for _, f := range facts {
		if f.Kind == workflow.KindInspectionDone {
			done, _ = fact.Decode[workflow.InspectionDone](f)
		}
	}
	if done.Findings != 1 || done.Blocking != 0 || done.SeverityMax != "minor" || len(done.List) != 1 || done.LogicChanged {
		t.Errorf("inspection.done = %+v", done)
	}
	if len(h.promptsFor(harness.RoleBuilder)) != 1 {
		t.Error("builder was re-run for minor findings")
	}
}

func TestInspectorWorktreeEditsAreDiscarded(t *testing.T) {
	h, ev := inspectorLine(t, []string{verdict(false, finding("major", "fix it")), verdict(false)},
		dispatch(), inspectCall(), dispatch(), inspectCall())
	h.onRole = func(s harness.SessionSpec) {
		if s.Role == harness.RoleInspector {
			_ = os.WriteFile(filepath.Join(s.Dir, "pkg", "out.txt"), []byte("tampered\n"), 0o644)
			_ = os.WriteFile(filepath.Join(s.Dir, "pkg", "evil.txt"), []byte("x\n"), 0o644)
		}
	}
	build := h.build
	h.build = func(task string, n int, dir string) error {
		if n == 2 {
			if _, err := os.Stat(filepath.Join(dir, "pkg", "evil.txt")); err == nil {
				return fmt.Errorf("inspector's new file survived")
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "pkg", "out.txt")); string(b) != "v1\n" {
				return fmt.Errorf("inspector's edit survived: %q", b)
			}
		}
		return build(task, n, dir)
	}
	id, facts := runWO(t, ev)
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	steps := 0
	for _, f := range facts {
		if f.Kind == fact.KindRunStep {
			if s, _ := fact.Decode[fact.RunStep](f); s.Step == "inspector_modified_worktree" {
				steps++
			}
		}
	}
	if steps != 2 {
		t.Errorf("inspector_modified_worktree steps = %d, want 2", steps)
	}
	if out := sh(t, ev.p.RepoURL, "git", "ls-tree", "-r", "--name-only", "lf/"+id); strings.Contains(out, "evil.txt") {
		t.Errorf("inspector file reached the branch: %s", out)
	}
}

func TestInspectorSameModelEscalatesBeforeInspecting(t *testing.T) {
	h, ev := inspectorLine(t, nil, dispatch(), inspectCall())
	ev.p.Builder = BuilderConfig{Profile: harness.Profile{Agent: "claude-agent-acp", Model: "sonnet"}, AgentCommand: []string{"claude-agent-acp"}}
	_, facts := runWO(t, ev)
	if len(h.promptsFor(harness.RoleInspector)) != 0 {
		t.Error("inspector ran on the builder's model")
	}
	st := workflow.Fold(facts)
	if st.Phase != workflow.PhaseAwaitingHuman {
		t.Errorf("phase %s", st.Phase)
	}
	if tk, _ := st.Task("t1"); tk.Status != workflow.TaskBuilt {
		t.Errorf("task = %+v, want built", tk)
	}
	var esc workflow.Escalated
	for _, f := range facts {
		if f.Kind == workflow.KindEscalated {
			esc, _ = fact.Decode[workflow.Escalated](f)
		}
	}
	if !strings.HasPrefix(esc.Reason, "inspector_same_model") || esc.Class != fact.ClassTask {
		t.Errorf("escalation = %+v", esc)
	}
}

func TestInspectorUnparseableReplyIsReAskedOnceThenBlocks(t *testing.T) {
	for _, reply := range []string{"no json here", `{}`, `{"summary":"ok"}`} {
		h, ev := inspectorLine(t, []string{reply, reply}, dispatch(), inspectCall(), dispatch())
		id, facts := runWO(t, ev)
		if st := status(t, ev.e, id); st == fact.StatusPROpen {
			t.Fatalf("%q: an unusable verdict passed the task", reply)
		}
		if got := len(h.promptsFor(harness.RoleInspector)); got != 2 {
			t.Errorf("%q: inspector prompts = %d, want one re-ask", reply, got)
		}
		n := 0
		var done workflow.InspectionDone
		for _, f := range facts {
			if f.Kind == fact.KindRunStep {
				if s, _ := fact.Decode[fact.RunStep](f); s.Step == "inspector_unparseable" {
					n++
				}
			}
			if f.Kind == workflow.KindInspectionDone {
				done, _ = fact.Decode[workflow.InspectionDone](f)
			}
		}
		if n != 1 || done.Blocking != 1 || done.SeverityMax != "blocker" || !strings.Contains(done.List[0].Message, "inspector_unparseable") {
			t.Errorf("%q: steps %d, inspection.done = %+v", reply, n, done)
		}
		if p := h.promptsFor(harness.RoleBuilder); len(p) != 2 || !strings.Contains(p[1], "inspector_unparseable") {
			t.Errorf("%q: the builder is not sent back with the blocker: %q", reply, p)
		}
	}
}

func TestInspectorReAskRecoversVerdict(t *testing.T) {
	h, ev := inspectorLine(t, []string{"{broken", verdict(false, finding("major", "real problem"))},
		dispatch(), inspectCall(), dispatch())
	_, facts := runWO(t, ev)
	if len(h.promptsFor(harness.RoleBuilder)) != 2 {
		t.Fatal("re-asked verdict was not applied")
	}
	for _, f := range facts {
		if f.Kind == fact.KindRunStep {
			if s, _ := fact.Decode[fact.RunStep](f); s.Step == "inspector_unparseable" {
				t.Error("recovered verdict still logged as unparseable")
			}
		}
	}
}

func TestInspectionDoneSeverities(t *testing.T) {
	v := inspection{Findings: []workflow.Finding{{Severity: "Minor"}, {Severity: "weird"}, {Severity: "blocker"}}}
	d := v.done("t1")
	if d.Findings != 3 || d.Blocking != 2 || d.SeverityMax != "blocker" || d.List[1].Severity != "major" {
		t.Errorf("done = %+v", d)
	}
}
