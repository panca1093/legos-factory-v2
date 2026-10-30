package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/scm"
)

func startRunner(t *testing.T, ev *env, slots int) *Runner {
	t.Helper()
	r := &Runner{Engine: ev.e, Projects: func(string) (Project, error) { return ev.p, nil }, Slots: slots,
		Poll: 10 * time.Millisecond, ObserveEvery: 10 * time.Millisecond, Backoff: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Start(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunnerSlotsQueueFIFO(t *testing.T) {
	ev := newEnv(t)
	release := make(chan struct{})
	var inflight, peak atomic.Int32
	var mu sync.Mutex
	var order []string
	ev.h.edit = func(_ int, dir string) error {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		mu.Lock()
		order = append(order, dir)
		mu.Unlock()
		<-release
		inflight.Add(-1)
		return writeFile("out.txt", "x")(0, dir)
	}
	ids := make([]string, 4)
	for i := range ids {
		ids[i] = ev.create(t)
		time.Sleep(2 * time.Millisecond)
	}
	startRunner(t, ev, 3)

	waitFor(t, "three runs in flight", func() bool { return inflight.Load() == 3 })
	time.Sleep(200 * time.Millisecond)
	if inflight.Load() != 3 {
		t.Fatalf("inflight = %d, want the 4th work order to wait", inflight.Load())
	}
	if st := status(t, ev.e, ids[3]); st != fact.StatusQueued {
		t.Fatalf("4th status = %s, want queued", st)
	}
	mu.Lock()
	for i := 0; i < 3; i++ {
		if !strings.Contains(order[i], ids[i]) && !strings.Contains(strings.Join(order, " "), ids[i]) {
			t.Errorf("work order %s did not start among the first three (FIFO)", ids[i])
		}
	}
	if strings.Contains(strings.Join(order, " "), ids[3]) {
		t.Errorf("4th work order started before a slot freed")
	}
	mu.Unlock()

	close(release)
	for _, id := range ids {
		id := id
		waitFor(t, id+" pr_open", func() bool { return status(t, ev.e, id) == fact.StatusPROpen })
	}
	if peak.Load() > 3 {
		t.Fatalf("peak concurrency = %d", peak.Load())
	}
}

func TestRunnerObserveTakesNoSlot(t *testing.T) {
	ev := newEnv(t)
	ev.scm.checks = []scm.Check{{Name: "ci", State: "pending"}}
	opened := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, opened); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, opened); st != fact.StatusPROpen {
		t.Fatalf("setup status = %s", st)
	}
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	ev.h.edit = func(_ int, dir string) error {
		started <- struct{}{}
		<-block
		return writeFile("out.txt", "x")(0, dir)
	}
	busy := ev.create(t)
	startRunner(t, ev, 1)
	<-started

	waitFor(t, "check observed while the only slot is held", func() bool {
		return count(ev.kinds(t, ev.e, opened), fact.KindCheckObserved) > 0
	})
	close(block)
	waitFor(t, "busy pr_open", func() bool { return status(t, ev.e, busy) == fact.StatusPROpen })
}

func TestRunnerResumesFromFacts(t *testing.T) {
	ev := newEnv(t)
	id := ev.create(t)
	if _, err := ev.e.Facts.Append(context.Background(), id, fact.KindWORequeued, fact.WORequeued{Reason: "ci_failed", Failing: []string{"ci"}}); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusBuilding {
		t.Fatalf("status = %s", st)
	}
	startRunner(t, ev, 3)
	waitFor(t, "resumed to pr_open", func() bool { return status(t, ev.e, id) == fact.StatusPROpen })
}

func TestRunnerBacksOffAfterError(t *testing.T) {
	ev := newEnv(t)
	id := ev.create(t)
	var calls atomic.Int32
	r := &Runner{Engine: ev.e, Slots: 1, Poll: 5 * time.Millisecond, Backoff: time.Hour,
		Projects: func(string) (Project, error) { calls.Add(1); return Project{}, context.DeadlineExceeded }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Start(ctx); close(done) }()
	waitFor(t, "first attempt", func() bool { return calls.Load() == 1 })
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("attempts = %d, want backoff to hold the work order", calls.Load())
	}
	r.Nudge(id)
	waitFor(t, "retry after nudge", func() bool { return calls.Load() == 2 })
	cancel()
	<-done
}

func TestRetryResumesFailedWorkOrder(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	ev.h.edit = nil
	id := ev.create(t)
	if err := ev.e.Retry(ctx, id, ""); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("retry on queued = %v, want ErrNotAllowed", err)
	}
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusFailed {
		t.Fatalf("status = %s", st)
	}
	ev.h.edit = writeFile("out.txt", "built\n")
	if err := ev.e.Retry(ctx, id, "try again"); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusBuilding {
		t.Fatalf("status after retry = %s", st)
	}
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status after rerun = %s", st)
	}
	if err := ev.e.Retry(ctx, id, ""); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("retry on pr_open = %v, want ErrNotAllowed", err)
	}
}

func TestRetryResetsRequeueBudget(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	ev.p.MaxRequeues = 1
	ev.scm.checks = []scm.Check{{Name: "ci", State: "failure"}}
	id := ev.create(t)
	for i := 0; i < 2; i++ {
		if err := ev.e.Run(ctx, ev.p, id); err != nil {
			t.Fatal(err)
		}
		if err := ev.e.Observe(ctx, ev.p, id); err != nil {
			t.Fatal(err)
		}
	}
	if st := status(t, ev.e, id); st != fact.StatusFailed {
		t.Fatalf("status = %s, want failed (ci_retry_exhausted)", st)
	}
	if err := ev.e.Retry(ctx, id, ""); err != nil {
		t.Fatal(err)
	}
	if err := ev.e.Observe(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusBuilding {
		t.Fatalf("status = %s, want requeued after retry", st)
	}
}

func TestFollowUpStartsFromParentBranch(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	parent := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, parent); err != nil {
		t.Fatal(err)
	}
	ev.h.edit = writeFile("followup.txt", "more\n")
	ev.scm.pr = nil
	child, err := ev.e.FollowUp(ctx, ev.p, parent, "add followup.txt")
	if err != nil {
		t.Fatal(err)
	}
	facts, _ := ev.e.Facts.List(ctx, child)
	c, _ := fact.Decode[fact.WOCreated](facts[0])
	if c.ParentWO != parent || c.BaseBranch != "lf/"+parent {
		t.Fatalf("created = %+v", c)
	}
	if err := ev.e.Run(ctx, ev.p, child); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, child); st != fact.StatusPROpen {
		t.Fatalf("child status = %s", st)
	}
	if ev.scm.pr == nil || ev.scm.pr.Head != "lf/"+child {
		t.Fatalf("pr = %+v", ev.scm.pr)
	}
	out := sh(t, ev.p.RepoURL, "git", "ls-tree", "-r", "--name-only", "lf/"+child)
	if !strings.Contains(out, "out.txt") || !strings.Contains(out, "followup.txt") {
		t.Fatalf("child branch should carry the parent's out.txt and its own followup.txt, tree = %q", out)
	}
	if _, err := ev.e.FollowUp(ctx, ev.p, "wo-missing", "x"); err == nil {
		t.Fatal("follow-up of unknown work order should fail")
	}
	queued := ev.create(t)
	if _, err := ev.e.FollowUp(ctx, ev.p, queued, "x"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("follow-up of queued = %v, want ErrNotAllowed", err)
	}
}
