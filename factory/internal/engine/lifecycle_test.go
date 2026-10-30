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
	"legos-factory/factory/internal/scm"
	"legos-factory/factory/internal/workflow"
)

// greenLine runs a one-task line whose builder writes rel and whose inspector answers verdictJSON, ending with an open PR whose required check "ci" passes.
func greenLine(t *testing.T, rel, owned, verdictJSON string) (*scripted, *env, string) {
	t.Helper()
	h := newScripted()
	h.build = writeIn(rel, "built\n")
	h.say(harness.RoleArchitect, "spec", planJSON(fmt.Sprintf(`{"id":"t1","description":"d","owned_paths":[%q]}`, owned)))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), dispatch(), inspectCall())
	h.say(harness.RoleInspector, verdictJSON)
	ev := lineEnv(t, h)
	ev.p.RequiredChecks = []string{"ci"}
	ev.scm.checks = []scm.Check{{Name: "ci", State: "success"}}
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	return h, ev, id
}

func levelFact(t *testing.T, ev *env, id string) fact.AutonomyLevel {
	t.Helper()
	var out []fact.AutonomyLevel
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindAutonomyLevel {
			l, err := fact.Decode[fact.AutonomyLevel](f)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		t.Fatal("no autonomy.level fact")
	}
	return out[len(out)-1]
}

func gate(t *testing.T, ev *env, id string) (workflow.Phase, string) {
	t.Helper()
	st := workflow.Fold(mustList(t, ev, id))
	return st.Phase, st.Gate
}

func TestLevelThreeDocsOnlyAutoMerges(t *testing.T) {
	_, ev, id := greenLine(t, "docs/guide.md", "docs/", cleanInspection)
	l := levelFact(t, ev, id)
	if l.Effective != 3 || l.DiffLevel != 3 || l.InspectorLevel != 3 || l.Cap != 3 || len(l.Reasons) == 0 {
		t.Fatalf("level = %+v", l)
	}
	if !strings.Contains(ev.scm.body, "Autonomy level 3 (diff 3, inspector 3, cap 3)") || !strings.Contains(ev.scm.body, "only docs") {
		t.Errorf("PR body = %q", ev.scm.body)
	}
	observeN(t, ev, id, 1)
	if len(ev.scm.merges) != 0 {
		t.Fatal("merged before checks were stable")
	}
	observeN(t, ev, id, 1)
	k := ev.kinds(t, ev.e, id)
	if len(ev.scm.merges) != 1 || ev.scm.merges[0] != "squash" || count(k, fact.KindMergeRequested) != 1 || count(k, fact.KindPRMerged) != 1 {
		t.Fatalf("merges = %v facts = %v", ev.scm.merges, k)
	}
	if st := status(t, ev.e, id); st != fact.StatusMerged {
		t.Fatalf("status = %s", st)
	}
}

func TestMergeMethodComesFromProject(t *testing.T) {
	_, ev, id := greenLine(t, "docs/guide.md", "docs/", cleanInspection)
	ev.p.MergeMethod = "rebase"
	observeN(t, ev, id, 2)
	if len(ev.scm.merges) != 1 || ev.scm.merges[0] != "rebase" {
		t.Fatalf("merges = %v", ev.scm.merges)
	}
}

func TestLevelTwoWaitsForHumanThenSeesTheirMerge(t *testing.T) {
	_, ev, id := greenLine(t, "pkg/out.go", "pkg/", cleanInspection)
	if l := levelFact(t, ev, id); l.Effective != 2 || l.DiffLevel != 2 {
		t.Fatalf("level = %+v", l)
	}
	observeN(t, ev, id, 3)
	if ph, g := gate(t, ev, id); ph != workflow.PhaseAwaitingHuman || g != workflow.GatePR {
		t.Fatalf("phase = %s gate = %q", ph, g)
	}
	if st := status(t, ev.e, id); st != fact.StatusAwaitingHuman || len(ev.scm.merges) != 0 {
		t.Fatalf("status = %s merges = %v", st, ev.scm.merges)
	}
	if n := count(ev.kinds(t, ev.e, id), workflow.KindAwaitingHuman); n != 1 {
		t.Fatalf("awaiting.human recorded %d times", n)
	}
	ev.scm.state = "merged"
	observeN(t, ev, id, 1)
	if st := status(t, ev.e, id); st != fact.StatusMerged {
		t.Fatalf("status = %s", st)
	}
}

func TestInspectorLogicChangedLowersThreeToTwo(t *testing.T) {
	_, ev, id := greenLine(t, "docs/guide.md", "docs/", verdict(true))
	l := levelFact(t, ev, id)
	if l.DiffLevel != 3 || l.InspectorLevel != 2 || l.Effective != 2 {
		t.Fatalf("level = %+v", l)
	}
	observeN(t, ev, id, 3)
	if len(ev.scm.merges) != 0 {
		t.Fatal("merged despite logic_changed")
	}
	if ph, g := gate(t, ev, id); ph != workflow.PhaseAwaitingHuman || g != workflow.GatePR {
		t.Fatalf("phase = %s gate = %q", ph, g)
	}
}

func TestSingleBuilderPathIsNeverAboveLevelTwo(t *testing.T) {
	ev := newEnv(t)
	ev.h.edit = func(_ int, dir string) error {
		if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
			return err
		}
		return writeFile("docs/a.md", "x\n")(0, dir)
	}
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if l := levelFact(t, ev, id); l.DiffLevel != 3 || l.InspectorLevel != 2 || l.Effective != 2 {
		t.Fatalf("level = %+v", l)
	}
}

func TestLevelCapLowersEffectiveLevel(t *testing.T) {
	h := newScripted()
	h.build = writeIn("docs/guide.md", "built\n")
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["docs/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), dispatch(), inspectCall())
	ev := lineEnv(t, h)
	id, err := ev.e.CreateAt(context.Background(), ev.p, "doc", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	l := levelFact(t, ev, id)
	if l.Cap != 2 || l.Effective != 2 || !strings.Contains(strings.Join(l.Reasons, ";"), "capped at level 2") {
		t.Fatalf("level = %+v", l)
	}
}

func TestProtectedPlanPathForcesPlanGateEvenAtCapThree(t *testing.T) {
	ctx := context.Background()
	h := newScripted()
	h.build = writeIn("db/migrations/README.md", "notes\n")
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["db/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), dispatch(), inspectCall())
	ev := lineEnv(t, h)
	ev.p.ProtectedPaths = []string{"db/migrations/**"}
	ev.p.RequiredChecks = []string{"ci"}
	ev.scm.checks = []scm.Check{{Name: "ci", State: "success"}}
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if ph, g := gate(t, ev, id); ph != workflow.PhaseAwaitingHuman || g != workflow.GatePlan {
		t.Fatalf("phase = %s gate = %q", ph, g)
	}
	if len(h.promptsFor(harness.RoleBuilder)) != 0 {
		t.Fatal("built before the human approved the plan")
	}
	if _, err := ev.e.Facts.Append(ctx, id, workflow.KindHumanApproved, workflow.HumanApproved{Gate: workflow.GatePlan}); err != nil {
		t.Fatal(err)
	}
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	l := levelFact(t, ev, id)
	if l.Effective != 0 || l.DiffLevel != 0 || !strings.Contains(strings.Join(l.Reasons, ";"), "protected path: db/migrations/README.md") {
		t.Fatalf("level = %+v", l)
	}
	observeN(t, ev, id, 3)
	if len(ev.scm.merges) != 0 {
		t.Fatal("auto-merged a protected path")
	}
	if ph, g := gate(t, ev, id); ph != workflow.PhaseAwaitingHuman || g != workflow.GatePR {
		t.Fatalf("phase = %s gate = %q", ph, g)
	}
}

func TestUnprotectedPlanSkipsPlanGate(t *testing.T) {
	h := newScripted()
	h.build = writeIn("pkg/out.txt", "x\n")
	h.say(harness.RoleArchitect, "spec", planJSON(`{"id":"t1","description":"d","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman, call("approve_spec", `{}`), call("approve_plan", `{}`), dispatch(), inspectCall())
	ev := lineEnv(t, h)
	ev.p.ProtectedPaths = []string{"db/migrations/**"}
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s", st)
	}
}

func TestMergeRefusedEscalatesToHuman(t *testing.T) {
	_, ev, id := greenLine(t, "docs/guide.md", "docs/", cleanInspection)
	ev.scm.mergeErr = fmt.Errorf("%w: %w", scm.ErrMergeRefused, &scm.ProviderError{Provider: "github", Status: 405, Message: "Base branch policy prohibits the merge"})
	observeN(t, ev, id, 4)
	if len(ev.scm.merges) != 1 {
		t.Fatalf("merge attempted %d times", len(ev.scm.merges))
	}
	if ph, g := gate(t, ev, id); ph != workflow.PhaseAwaitingHuman || g != workflow.GateEscalation {
		t.Fatalf("phase = %s gate = %q", ph, g)
	}
	if st := status(t, ev.e, id); st != fact.StatusAwaitingHuman {
		t.Fatalf("status = %s", st)
	}
	for _, f := range mustList(t, ev, id) {
		if f.Kind == workflow.KindEscalated {
			e, _ := fact.Decode[workflow.Escalated](f)
			if e.Class != "merge" || !strings.Contains(e.Reason, "Base branch policy") {
				t.Errorf("escalated = %+v", e)
			}
		}
	}
	ev.scm.state = "merged" // the human merged it on the SCM
	observeN(t, ev, id, 1)
	if st := status(t, ev.e, id); st != fact.StatusMerged {
		t.Fatalf("status = %s", st)
	}
}

func TestMergeRefusedButAlreadyMergedRecordsMerge(t *testing.T) {
	_, ev, id := greenLine(t, "docs/guide.md", "docs/", cleanInspection)
	ev.scm.mergeErr = fmt.Errorf("%w: %w", scm.ErrMergeRefused, &scm.ProviderError{Provider: "github", Status: 405, Message: "already merged"})
	observeN(t, ev, id, 1)
	ev.scm.state = "merged"
	observeN(t, ev, id, 1)
	if st := status(t, ev.e, id); st != fact.StatusMerged {
		t.Fatalf("status = %s", st)
	}
}

func pushToMain(t *testing.T, remote, file, content string) {
	t.Helper()
	dir := t.TempDir()
	sh(t, dir, "git", "clone", "-q", remote, "w")
	w := filepath.Join(dir, "w")
	if err := os.WriteFile(filepath.Join(w, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, w, "git", "add", "-A")
	sh(t, w, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "main moves: "+file)
	sh(t, w, "git", "push", "-q", "origin", "main")
}

func TestRebaseConflictResumesBuilderThenForcePushesWithLease(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	ev.h.edit = func(call int, dir string) error {
		if call == 1 {
			return writeFile("notes.md", "mine\n")(call, dir)
		}
		return writeFile("notes.md", "resolved\n")(call, dir)
	}
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	pushToMain(t, ev.p.RepoURL, "notes.md", "theirs\n")
	pushToMain(t, ev.p.RepoURL, "other.go", "package x\n")

	observeN(t, ev, id, 1)
	if n := count(ev.kinds(t, ev.e, id), fact.KindWORequeued); n != 0 {
		t.Fatal("requeued although the SCM reports a clean merge")
	}
	ev.scm.mergeable = "conflict"
	observeN(t, ev, id, 2)
	var reqs []fact.WORequeued
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindWORequeued {
			r, _ := fact.Decode[fact.WORequeued](f)
			reqs = append(reqs, r)
		}
	}
	if len(reqs) != 1 || reqs[0].Reason != workflow.ReasonNeedsRebase {
		t.Fatalf("requeues = %+v", reqs)
	}
	if ph := workflow.Fold(mustList(t, ev, id)).Phase; ph != workflow.PhaseNeedsRebase {
		t.Fatalf("phase = %s", ph)
	}
	oldHead := sh(t, ev.p.RepoURL, "git", "rev-parse", "lf/"+id)

	ev.scm.mergeable = "clean"
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	if got := sh(t, ev.p.RepoURL, "git", "show", "lf/"+id+":notes.md"); got != "resolved" {
		t.Errorf("pushed content = %q", got)
	}
	sh(t, ev.p.RepoURL, "git", "merge-base", "--is-ancestor", "main", "lf/"+id)
	if sh(t, ev.p.RepoURL, "git", "rev-parse", "lf/"+id) == oldHead {
		t.Error("branch not force-pushed")
	}
	k := ev.kinds(t, ev.e, id)
	if count(k, fact.KindPRPushed) != 1 || count(k, fact.KindPROpened) != 1 || ev.scm.opened != 1 {
		t.Fatalf("facts = %v opened = %d", k, ev.scm.opened)
	}
	if len(ev.h.prompts) != 2 || !strings.Contains(ev.h.prompts[1], "notes.md") || !strings.Contains(ev.h.prompts[1], "Resolve every conflict marker") {
		t.Fatalf("builder prompts = %q", ev.h.prompts)
	}
	if ev.h.specs[1].SessionID == "" {
		t.Error("builder session was not resumed")
	}
	if l := levelFact(t, ev, id); l.DiffLevel != 3 {
		t.Errorf("level after rebase = %+v (diff must not include the commits pulled in from main)", l)
	}
}

func TestRebaseWithoutConflictDoesNotWakeTheBuilder(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	ev.h.edit = writeFile("notes.md", "mine\n")
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	pushToMain(t, ev.p.RepoURL, "other.go", "package x\n")
	ev.scm.mergeable = "conflict"
	observeN(t, ev, id, 1)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if len(ev.h.prompts) != 1 {
		t.Fatalf("builder prompted %d times", len(ev.h.prompts))
	}
	sh(t, ev.p.RepoURL, "git", "merge-base", "--is-ancestor", "main", "lf/"+id)
	if count(ev.kinds(t, ev.e, id), fact.KindPRPushed) != 1 {
		t.Fatal("rebased branch not pushed")
	}
	if l := levelFact(t, ev, id); l.DiffLevel != 3 {
		t.Fatalf("level = %+v: main's code change leaked into the diff", l)
	}
}

func TestRebaseGivesUpAfterBuilderLeavesConflictMarkers(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	ev.h.edit = func(call int, dir string) error {
		if call == 1 {
			return writeFile("out.txt", "mine\n")(call, dir)
		}
		return nil
	}
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	pushToMain(t, ev.p.RepoURL, "out.txt", "theirs\n")
	ev.scm.mergeable = "conflict"
	observeN(t, ev, id, 1)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusFailed {
		t.Fatalf("status = %s", st)
	}
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindWOFailed {
			if w, _ := fact.Decode[fact.WOFailed](f); w.Reason != "rebase_conflict" {
				t.Fatalf("failure = %+v", w)
			}
		}
	}
}

func TestRepeatedConflictsEscalateInsteadOfLooping(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	for range maxRebases {
		ev.scm.mergeable = "conflict"
		observeN(t, ev, id, 1)
		if _, err := ev.e.Facts.Append(ctx, id, fact.KindPRPushed, fact.PRPushed{HeadSHA: "h"}); err != nil {
			t.Fatal(err)
		}
	}
	observeN(t, ev, id, 1)
	if ph, g := gate(t, ev, id); ph != workflow.PhaseAwaitingHuman || g != workflow.GateEscalation {
		t.Fatalf("phase = %s gate = %q", ph, g)
	}
}

func TestFollowUpLevelIsComputedAgainstItsOwnBase(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	ev.h.edit = func(_ int, dir string) error {
		return writeFile("logic.go", "package x\n\nfunc F() int { return 1 }\n")(0, dir)
	}
	parent := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, parent); err != nil {
		t.Fatal(err)
	}
	if l := levelFact(t, ev, parent); l.DiffLevel != 2 {
		t.Fatalf("parent level = %+v", l)
	}
	ev.h.edit = func(_ int, dir string) error {
		if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
			return err
		}
		return writeFile("docs/note.md", "reviewed\n")(0, dir)
	}
	child, err := ev.e.FollowUp(ctx, ev.p, parent, "address review")
	if err != nil {
		t.Fatal(err)
	}
	if err := ev.e.Run(ctx, ev.p, child); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, child); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, child))
	}
	l := levelFact(t, ev, child)
	if l.DiffLevel != 3 || !strings.Contains(strings.Join(l.Reasons, ";"), "only docs") {
		t.Fatalf("follow-up level = %+v: the parent's code change must not count", l)
	}
}
