package engine

import (
	"context"
	"sort"
	"sync"
	"time"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/workflow"
)

// Runner schedules every work order found in the fact store: Run for those needing agent work within Slots concurrent sandboxes (FIFO), Observe for open PRs without a slot (ADR-F07).
type Runner struct {
	Engine       *Engine
	Projects     func(id string) (Project, error)
	Slots        int           // default 3
	ObserveEvery time.Duration // default 30s
	Poll         time.Duration // default 2s
	Backoff      time.Duration // first retry delay after an error, doubling up to 5m; default 5s

	mu      sync.Mutex
	busy    map[string]bool
	used    int
	next    map[string]time.Time
	fails   map[string]int
	lastObs map[string]time.Time
	wake    chan struct{}
	wg      sync.WaitGroup
}

const maxBackoff = 5 * time.Minute

func (r *Runner) init() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy == nil {
		r.busy, r.next, r.fails, r.lastObs = map[string]bool{}, map[string]time.Time{}, map[string]int{}, map[string]time.Time{}
		r.wake = make(chan struct{}, 1)
	}
}

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (r *Runner) slots() int {
	if r.Slots > 0 {
		return r.Slots
	}
	return 3
}

// Nudge clears the work order's backoff and triggers an immediate scan.
func (r *Runner) Nudge(id string) {
	r.init()
	r.mu.Lock()
	delete(r.next, id)
	delete(r.fails, id)
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Running reports whether a Run or Observe for the work order is in flight.
func (r *Runner) Running(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busy[id]
}

// Start blocks until ctx is done and every in-flight Run or Observe has returned.
func (r *Runner) Start(ctx context.Context) {
	r.init()
	defer r.wg.Wait()
	for {
		r.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-time.After(or(r.Poll, 2*time.Second)):
		}
	}
}

type candidate struct {
	id      string
	project string
	created time.Time
	observe bool
	lastID  int64
}

func (r *Runner) scan(ctx context.Context) ([]candidate, error) {
	subjects, err := r.Engine.Facts.Subjects(ctx)
	if err != nil {
		return nil, err
	}
	var out []candidate
	for _, id := range subjects {
		facts, err := r.Engine.Facts.List(ctx, id)
		if err != nil || len(facts) == 0 || facts[0].Kind != fact.KindWOCreated {
			continue
		}
		created, err := fact.Decode[fact.WOCreated](facts[0])
		if err != nil || created.Project == "" {
			continue
		}
		st := fact.Derive(facts)
		phase := workflow.Fold(facts).Phase
		if st.Terminal() || phase.Terminal() || phase == workflow.PhaseAwaitingHuman {
			continue
		}
		c := candidate{id: id, project: created.Project, created: facts[0].At, lastID: facts[len(facts)-1].ID}
		switch st {
		case fact.StatusPROpen, fact.StatusCIFailed, fact.StatusCIGreen:
			c.observe = true
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].created.Before(out[j].created) })
	return out, nil
}

func (r *Runner) tick(ctx context.Context) {
	list, err := r.scan(ctx)
	if err != nil {
		r.Engine.log().Warn("runner scan", "err", err)
		return
	}
	now := time.Now()
	for _, c := range list {
		r.mu.Lock()
		skip := r.busy[c.id] || now.Before(r.next[c.id])
		if !skip {
			if c.observe {
				skip = now.Sub(r.lastObs[c.id]) < or(r.ObserveEvery, 30*time.Second)
			} else {
				skip = r.used >= r.slots()
			}
		}
		if skip {
			r.mu.Unlock()
			continue
		}
		r.busy[c.id] = true
		if c.observe {
			r.lastObs[c.id] = now
		} else {
			r.used++
		}
		r.mu.Unlock()
		r.wg.Add(1)
		go r.work(ctx, c)
	}
}

func (r *Runner) work(ctx context.Context, c candidate) {
	defer r.wg.Done()
	err := r.exec(ctx, c)
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.busy, c.id)
	if !c.observe {
		r.used--
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		r.fails[c.id]++
		d := or(r.Backoff, 5*time.Second) << min(r.fails[c.id]-1, 10)
		if d > maxBackoff || d <= 0 {
			d = maxBackoff
		}
		r.next[c.id] = time.Now().Add(d)
		r.Engine.log().Warn("runner step failed, backing off", "wo", c.id, "observe", c.observe, "delay", d, "transient", IsTransient(err), "err", err)
		return
	}
	delete(r.fails, c.id)
	if c.observe {
		return
	}
	// A Run that returns without appending a fact would otherwise be re-run every poll.
	if facts, err := r.Engine.Facts.List(ctx, c.id); err == nil && len(facts) > 0 && facts[len(facts)-1].ID == c.lastID {
		r.next[c.id] = time.Now().Add(10 * or(r.Poll, 2*time.Second))
	}
}

func (r *Runner) exec(ctx context.Context, c candidate) error {
	p, err := r.Projects(c.project)
	if err != nil {
		return err
	}
	if c.observe {
		return r.Engine.Observe(ctx, p, c.id)
	}
	return r.Engine.Run(ctx, p, c.id)
}
