package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"legos-factory/factory/internal/autonomy"
	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/sandbox"
	"legos-factory/factory/internal/scm"
	"legos-factory/factory/internal/workflow"
)

const (
	maxRebases      = 3
	maxRebaseRounds = 3
)

// computeLevel applies SPEC §9 to the diff of dir against origin/<base>. Without a recorded inspection (single-builder
// path) the inspector level is 2: nothing vouched that the logic is unchanged. A diff that cannot be read counts as level 0.
func computeLevel(ctx context.Context, p Project, sb sandbox.Sandbox, dir, sha string, facts []fact.Fact) fact.AutonomyLevel {
	st := workflow.Fold(facts)
	run := func(ctx context.Context, dir string, args ...string) (string, error) {
		stdout, stderr, code, err := sb.Exec(ctx, dir, nil, args...)
		if err != nil {
			return "", err
		}
		if code != 0 {
			return "", fmt.Errorf("%s: exit %d: %s", args[0], code, strings.TrimSpace(stderr))
		}
		return stdout, nil
	}
	l := fact.AutonomyLevel{HeadSHA: sha, Cap: st.LevelCap}
	if p.MaxLevel != nil && *p.MaxLevel < l.Cap {
		l.Cap = *p.MaxLevel
		l.Reasons = append(l.Reasons, fmt.Sprintf("project max_level %d", l.Cap))
	}
	changes, err := autonomy.Collect(ctx, dir, "origin/"+p.DefaultBranch, run)
	if err != nil {
		l.DiffLevel, l.Reasons = 0, []string{"diff unreadable, treated as protected: " + firstLine(err.Error(), 200)}
	} else {
		l.DiffLevel, l.Reasons = autonomy.DiffLevel(changes, p.ProtectedPaths)
	}
	l.InspectorLevel = 2
	if len(st.Tasks) > 0 {
		l.InspectorLevel = autonomy.InspectorLevel(st.LogicChanged())
		if st.LogicChanged() {
			l.Reasons = append(l.Reasons, "inspector reported a logic change")
		}
		if uninspectedCommits(facts) {
			l.InspectorLevel = 2
			l.Reasons = append(l.Reasons, "commits after the last inspection (CI fix or rebase) were not inspected")
		}
	} else {
		l.Reasons = append(l.Reasons, "no inspection on the single-builder path")
	}
	l.Effective = autonomy.Effective(l.DiffLevel, l.InspectorLevel, l.Cap)
	if l.Effective == l.Cap && l.Cap < min(l.DiffLevel, l.InspectorLevel) {
		l.Reasons = append(l.Reasons, fmt.Sprintf("work order capped at level %d", l.Cap))
	}
	return l
}

// uninspectedCommits is true when a re-queue (CI fix, rebase) came after the last inspection: its commits were never inspected.
func uninspectedCommits(facts []fact.Fact) bool {
	last := -1
	for i, f := range facts {
		if f.Kind == workflow.KindInspectionDone {
			last = i
		}
	}
	for _, f := range facts[last+1:] {
		if f.Kind == fact.KindWORequeued {
			return true
		}
	}
	return false
}

func prBody(objective string, l fact.AutonomyLevel) string {
	var b strings.Builder
	b.WriteString(objective)
	fmt.Fprintf(&b, "\n\n---\nAutonomy level %d (diff %d, inspector %d, cap %d)\n", l.Effective, l.DiffLevel, l.InspectorLevel, l.Cap)
	for _, r := range l.Reasons {
		b.WriteString("- " + r + "\n")
	}
	return b.String()
}

// lastLevel is the latest autonomy.level recorded for head; ok is false when none matches, which callers treat as level 0.
func lastLevel(facts []fact.Fact, head string) (fact.AutonomyLevel, bool) {
	for i := len(facts) - 1; i >= 0; i-- {
		if facts[i].Kind != fact.KindAutonomyLevel {
			continue
		}
		if l, err := fact.Decode[fact.AutonomyLevel](facts[i]); err == nil && l.HeadSHA == head {
			return l, true
		}
	}
	return fact.AutonomyLevel{}, false
}

// rebaseStep puts lf/<wo> on top of the moved base; done is true when the work order was failed. Conflicts go to the
// builder session as a list of files; it edits them and the engine continues the rebase.
func (e *Engine) rebaseStep(ctx context.Context, p Project, woID string, sb sandbox.Sandbox, dir string, auth *sandbox.GitAuth, v *view) (done bool, err error) {
	in, err := sandbox.RebaseInProgress(ctx, sb, dir)
	if err != nil {
		return false, err
	}
	if !in {
		if err := sandbox.Fetch(ctx, sb, dir, p.DefaultBranch, auth); err != nil {
			return false, err
		}
		if _, err := sandbox.Rebase(ctx, sb, dir, "origin/"+p.DefaultBranch); err != nil {
			return false, err
		}
	}
	rv := *v
	for range maxRebaseRounds {
		files, err := sandbox.Unmerged(ctx, sb, dir)
		if err != nil {
			return false, err
		}
		marked, err := sandbox.HasConflictMarkers(ctx, sb, dir, files)
		if err != nil {
			return false, err
		}
		if marked {
			rv.requeue = &fact.WORequeued{Reason: workflow.ReasonNeedsRebase, Failing: files}
			if done, err := e.build(ctx, p, woID, sb, dir, &rv, nil); err != nil || done {
				return done, err
			}
			rv.openRun = ""
			if files, err = sandbox.Unmerged(ctx, sb, dir); err != nil {
				return false, err
			}
			if marked, err = sandbox.HasConflictMarkers(ctx, sb, dir, files); err != nil {
				return false, err
			} else if marked {
				continue
			}
		}
		left, err := sandbox.RebaseContinue(ctx, sb, dir)
		if err != nil {
			return false, err
		}
		if len(left) == 0 {
			return false, nil
		}
	}
	_ = sandbox.RebaseAbort(context.WithoutCancel(ctx), sb, dir)
	return true, e.fail(ctx, woID, fact.WOFailed{Class: fact.ClassTask, Reason: "rebase_conflict", Message: "builder could not resolve the conflicts with " + p.DefaultBranch})
}

func rebasing(v *view) bool {
	return v.requeue != nil && v.requeue.Reason == workflow.ReasonNeedsRebase
}

func rebaseRequeuePrompt(r fact.WORequeued) string {
	return "The base branch moved and your branch no longer merges cleanly. A rebase onto it is in progress in your worktree and stopped on conflicts in: " +
		strings.Join(r.Failing, ", ") + ".\nResolve every conflict marker by editing those files, keeping the intent of both sides. Do not commit or run git rebase; then stop."
}

// observeGreen acts on a PR whose checks are stable: merge at level 3, otherwise park for a human.
func (e *Engine) observeGreen(ctx context.Context, p Project, woID string, prov scm.Provider, facts []fact.Fact, number int, head string) error {
	if wf := workflow.Fold(facts); wf.Phase == workflow.PhaseAwaitingHuman {
		return nil
	}
	l, ok := lastLevel(facts, head)
	if !ok {
		l.Reasons = []string{"no autonomy level recorded for this head"}
	}
	if !autonomy.Decision(l.Effective).AutoMerge {
		_, err := e.Facts.Append(ctx, woID, workflow.KindAwaitingHuman, workflow.AwaitingHuman{Gate: workflow.GatePR,
			Reason: fmt.Sprintf("autonomy level %d: a human merges the pull request", l.Effective)})
		return err
	}
	method := p.MergeMethod
	if method == "" {
		method = "squash"
	}
	if _, err := e.Facts.Append(ctx, woID, fact.KindMergeRequested, fact.MergeRequested{Number: number, Method: method, Level: l.Effective}); err != nil {
		return err
	}
	err := prov.Merge(ctx, p.Repo, number, method)
	if errors.Is(err, scm.ErrMergeRefused) {
		if st, serr := prov.PRState(ctx, p.Repo, number); serr == nil && st == "merged" {
			_, err = e.Facts.Append(ctx, woID, fact.KindPRMerged, struct{}{})
			return err
		}
		_, ferr := e.Facts.Append(ctx, woID, workflow.KindEscalated, workflow.Escalated{Class: "merge", Reason: firstLine(err.Error(), 300)})
		return ferr
	}
	if err != nil {
		return e.failProvider(ctx, woID, err, harness.Profile{})
	}
	if st, err := prov.PRState(ctx, p.Repo, number); err == nil && st == "merged" {
		_, err = e.Facts.Append(ctx, woID, fact.KindPRMerged, struct{}{})
		return err
	}
	return nil
}

// conflictsWithBase asks the SCM whether the PR still merges; an unavailable answer counts as unknown.
func (e *Engine) conflictsWithBase(ctx context.Context, p Project, prov scm.Provider, number int) bool {
	state, err := prov.Mergeable(ctx, p.Repo, number)
	if err != nil {
		e.log().Warn("mergeable check failed", "repo", p.Repo, "pr", number, "err", err)
		return false
	}
	return state == "conflict"
}
