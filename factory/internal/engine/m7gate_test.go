package engine

import (
	"context"
	"strings"
	"testing"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/scm"
	"legos-factory/factory/internal/workflow"
)

func escalations(t *testing.T, ev *env, id string) []workflow.Escalated {
	t.Helper()
	var out []workflow.Escalated
	for _, f := range mustList(t, ev, id) {
		if f.Kind == workflow.KindEscalated {
			e, _ := fact.Decode[workflow.Escalated](f)
			out = append(out, e)
		}
	}
	return out
}

func failureReason(t *testing.T, ev *env, id string) string {
	t.Helper()
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindWOFailed {
			w, _ := fact.Decode[fact.WOFailed](f)
			return w.Reason
		}
	}
	return ""
}

func rebaseNeeded(t *testing.T, ev *env, id string) {
	t.Helper()
	ev.scm.mergeable = "conflict"
	observeN(t, ev, id, 1)
	ev.scm.mergeable = "clean"
}

func TestRebaseThatEmptiesTheBranchFailsWithoutPushing(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	ev.h.edit = writeFile("out.txt", "same\n")
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	before := sh(t, ev.p.RepoURL, "git", "rev-parse", "lf/"+id)
	pushToMain(t, ev.p.RepoURL, "out.txt", "same\n") // main already carries the change
	rebaseNeeded(t, ev, id)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st, reason := status(t, ev.e, id), failureReason(t, ev, id); st != fact.StatusFailed || reason != "rebase_empty" {
		t.Fatalf("status = %s reason = %q", st, reason)
	}
	if sh(t, ev.p.RepoURL, "git", "rev-parse", "lf/"+id) != before {
		t.Error("the emptied branch was pushed over the PR")
	}
}

func TestRebaseBinaryConflictFailsWithoutBuilder(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	ev.h.edit = writeFile("x.bin", "a\x00mine\n")
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	pushToMain(t, ev.p.RepoURL, "x.bin", "a\x00theirs\n")
	pushToMain(t, ev.p.RepoURL, "y.bin", "a\x00other\n")
	rebaseNeeded(t, ev, id)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if reason := failureReason(t, ev, id); reason != "rebase_conflict" {
		t.Fatalf("reason = %q", reason)
	}
	if len(ev.h.prompts) != 1 {
		t.Fatalf("the builder was woken for a binary conflict: %d prompts", len(ev.h.prompts))
	}
}

func TestLeaseRejectedPushEscalatesInsteadOfRetrying(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	pushToMain(t, ev.p.RepoURL, "other.go", "package x\n")
	rebaseNeeded(t, ev, id)
	sh(t, ev.p.RepoURL, "git", "update-ref", "refs/heads/lf/"+id, sh(t, ev.p.RepoURL, "git", "rev-parse", "main")) // someone else pushed
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	es := escalations(t, ev, id)
	if len(es) != 1 || es[0].Class != "merge" || !strings.Contains(es[0].Reason, "branch changed outside the factory") {
		t.Fatalf("escalations = %+v", es)
	}
	if st := status(t, ev.e, id); st == fact.StatusFailed || count(ev.kinds(t, ev.e, id), fact.KindPRPushed) != 0 {
		t.Fatalf("status = %s", st)
	}
	if ph, g := gate(t, ev, id); ph != workflow.PhaseAwaitingHuman || g != workflow.GateEscalation {
		t.Fatalf("phase = %s gate = %q", ph, g)
	}
}

func TestMergePassesTheEvaluatedHead(t *testing.T) {
	_, ev, id := greenLine(t, "docs/guide.md", "docs/", cleanInspection)
	observeN(t, ev, id, 2)
	if len(ev.scm.mergeHeads) != 1 || ev.scm.mergeHeads[0] != levelFact(t, ev, id).HeadSHA || ev.scm.mergeHeads[0] == "" {
		t.Fatalf("merge heads = %v", ev.scm.mergeHeads)
	}
}

func TestMergeEscalatesOnceWhenHeadMovedOutsideFactory(t *testing.T) {
	_, ev, id := greenLine(t, "docs/guide.md", "docs/", cleanInspection)
	ev.scm.prHead = "someone-elses-commit" // e.g. a follow-up work order merged into this branch
	observeN(t, ev, id, 6)
	es := escalations(t, ev, id)
	if len(es) != 1 || es[0].Class != "merge" || !strings.Contains(es[0].Reason, "pr head moved outside the factory") {
		t.Fatalf("escalations = %+v", es)
	}
	if k := ev.kinds(t, ev.e, id); count(k, fact.KindPRMerged) != 0 || len(ev.scm.merges) != 1 {
		t.Fatalf("merges = %v facts = %v", ev.scm.merges, k)
	}
	if ph, g := gate(t, ev, id); ph != workflow.PhaseAwaitingHuman || g != workflow.GateEscalation {
		t.Fatalf("phase = %s gate = %q", ph, g)
	}
}

func TestEscalationAtThePRGateSwitchesGateAndDoesNotRepeat(t *testing.T) {
	_, ev, id := greenLine(t, "pkg/out.go", "pkg/", cleanInspection) // level 2 parks at the PR gate
	observeN(t, ev, id, 3)
	if _, g := gate(t, ev, id); g != workflow.GatePR {
		t.Fatalf("gate = %q", g)
	}
	if _, err := ev.e.Facts.Append(context.Background(), id, workflow.KindEscalated, workflow.Escalated{Class: "merge", Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	if ph, g := gate(t, ev, id); ph != workflow.PhaseAwaitingHuman || g != workflow.GateEscalation {
		t.Fatalf("phase = %s gate = %q", ph, g)
	}
	observeN(t, ev, id, 3)
	if n := len(escalations(t, ev, id)); n != 1 {
		t.Fatalf("escalated %d times", n)
	}
}

func TestTransientMergeErrorRetriesWithoutFailing(t *testing.T) {
	_, ev, id := greenLine(t, "docs/guide.md", "docs/", cleanInspection)
	observeN(t, ev, id, 1)
	ev.scm.mergeErr = &scm.ProviderError{Provider: "github", Status: 502, Message: "bad gateway"}
	if err := ev.e.Observe(context.Background(), ev.p, id); err == nil || !IsTransient(err) {
		t.Fatalf("err = %v", err)
	}
	if st := status(t, ev.e, id); st == fact.StatusFailed {
		t.Fatal("work order failed on a transient merge error")
	}
	ev.scm.mergeErr = nil
	observeN(t, ev, id, 1)
	if st := status(t, ev.e, id); st != fact.StatusMerged {
		t.Fatalf("status = %s", st)
	}
}

func TestMergeErrorButMergedOnSCMRecordsMerge(t *testing.T) {
	_, ev, id := greenLine(t, "docs/guide.md", "docs/", cleanInspection)
	observeN(t, ev, id, 1)
	ev.scm.mergeErr = &scm.ProviderError{Provider: "github", Status: 502, Message: "bad gateway"}
	ev.scm.mergedOnErr = true
	observeN(t, ev, id, 1)
	if st := status(t, ev.e, id); st != fact.StatusMerged {
		t.Fatalf("status = %s", st)
	}
}

func TestMergeThatIsNotYetMergedDoesNotRecordMerge(t *testing.T) {
	_, ev, id := greenLine(t, "docs/guide.md", "docs/", cleanInspection)
	ev.scm.acceptButStayOpen = true // Bitbucket 202: accepted, merge still running
	observeN(t, ev, id, 2)
	if k := ev.kinds(t, ev.e, id); count(k, fact.KindPRMerged) != 0 {
		t.Fatalf("recorded a merge that was not confirmed: %v", k)
	}
}
