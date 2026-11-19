package usecase

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

const (
	FilterRelated  = "related"
	FilterReviewer = "reviewer"
	FilterAuthor   = "author"
)

func checkFilter(f string) (string, error) {
	switch f {
	case "":
		return FilterRelated, nil
	case FilterRelated, FilterReviewer, FilterAuthor:
		return f, nil
	}
	return "", fmt.Errorf("%w: filter must be related, reviewer or author", ErrInvalid)
}

// listEntry is one cached provider listing: the open PRs the user is involved in and why.
type listEntry struct {
	at      time.Time
	rows    []model.PRSummary
	roles   map[int]string
	factory map[int]string // PR number → work order id
	err     *model.ProviderErr
	until   time.Time // rate limit: no provider call before this
}

func (e *listEntry) clone() *listEntry {
	if e == nil {
		return &listEntry{}
	}
	c := *e
	return &c
}

func sameUser(a, b model.HostUser) bool {
	if a.ID != "" && b.ID != "" {
		return a.ID == b.ID
	}
	return a.Login != "" && strings.EqualFold(a.Login, b.Login)
}

var woPRRef = regexp.MustCompile(`^[a-z]+:(.+)[#!](\d+)$`)

// factoryPRs maps the open PR numbers of this user's work orders in the project's repository to the order.
func (r *Reviews) factoryPRs(ctx context.Context, rc *reviewCtx) map[int]string {
	out := map[int]string{}
	if r.WO == nil {
		return out
	}
	all, err := r.WO.List(ctx)
	if err != nil {
		return out
	}
	for _, wo := range all {
		if wo.ProjectID != rc.project.ID || wo.CreatedBy != rc.userID || wo.PRMergedAt != nil || wo.PRClosedAt != nil {
			continue
		}
		if m := woPRRef.FindStringSubmatch(wo.PRRef); m != nil && strings.EqualFold(m[1], rc.repo) {
			n, _ := strconv.Atoi(m[2])
			out[n] = wo.ID
		}
	}
	return out
}

func (r *Reviews) loadPulls(ctx context.Context, rc *reviewCtx, filter string) (*listEntry, error) {
	e := &listEntry{at: r.now(), roles: map[int]string{}, factory: map[int]string{}}
	byNum := map[int]model.PRSummary{}
	add := func(list []model.PRSummary, role string) {
		for _, s := range list {
			byNum[s.Number] = s
			switch {
			case e.roles[s.Number] == "" || e.roles[s.Number] == role:
				e.roles[s.Number] = role
			default:
				e.roles[s.Number] = model.RoleBoth
			}
		}
	}
	if filter != FilterReviewer {
		l, err := retryGet(ctx, func() ([]model.PRSummary, error) {
			return rc.rv.ListPullRequests(ctx, rc.repo, model.PRFilter{Author: rc.me})
		})
		if err != nil {
			return nil, err
		}
		add(l, model.RoleAuthor)
		if fac := r.factoryPRs(ctx, rc); len(fac) > 0 {
			all, err := retryGet(ctx, func() ([]model.PRSummary, error) {
				return rc.rv.ListPullRequests(ctx, rc.repo, model.PRFilter{})
			})
			if err != nil {
				return nil, err
			}
			for _, s := range all {
				if id, ok := fac[s.Number]; ok {
					e.factory[s.Number] = id
					add([]model.PRSummary{s}, model.RoleAuthor)
				}
			}
		}
	}
	if filter != FilterAuthor {
		l, err := retryGet(ctx, func() ([]model.PRSummary, error) {
			return rc.rv.ListPullRequests(ctx, rc.repo, model.PRFilter{Reviewer: rc.me})
		})
		if err != nil {
			return nil, err
		}
		add(l, model.RoleReviewer)
	}
	for _, s := range byNum {
		e.rows = append(e.rows, s)
	}
	sort.Slice(e.rows, func(i, j int) bool {
		if !e.rows[i].UpdatedAt.Equal(e.rows[j].UpdatedAt) {
			return e.rows[i].UpdatedAt.After(e.rows[j].UpdatedAt)
		}
		return e.rows[i].Number > e.rows[j].Number
	})
	return e, nil
}

// fetchPulls refreshes one cache slot; a failure keeps the previous rows and records the reason.
func (r *Reviews) fetchPulls(ctx context.Context, rc *reviewCtx, filter, key string) *listEntry {
	e, err := r.loadPulls(ctx, rc, filter)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lists == nil {
		r.lists = map[string]*listEntry{}
	}
	if err != nil {
		prev := r.lists[key]
		e = prev.clone()
		e.at, e.err = r.now(), r.providerErr(err)
		if e.err.Code == model.ErrCodeRateLimit && e.err.Until != nil {
			e.until = *e.err.Until
		}
	}
	r.lists[key] = e
	return e.clone()
}

func (r *Reviews) refreshAsync(rc *reviewCtx, filter, key string) {
	r.mu.Lock()
	if r.refreshing == nil {
		r.refreshing = map[string]bool{}
	}
	if r.refreshing[key] {
		r.mu.Unlock()
		return
	}
	r.refreshing[key] = true
	r.mu.Unlock()
	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		defer func() {
			if v := recover(); v != nil {
				r.log().Error("pull request refresh panicked", "panic", v)
			}
			r.mu.Lock()
			delete(r.refreshing, key)
			r.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		r.fetchPulls(ctx, rc, filter, key)
	}()
}

// pulls serves the cached listing, refreshing in the background once it is older than the TTL.
// With block set, a first load waits for the provider; without it, it returns empty and stale.
func (r *Reviews) pulls(ctx context.Context, rc *reviewCtx, filter string, block bool) (e *listEntry, stale bool) {
	key := rc.key + "|" + rc.repo + "|" + filter
	r.mu.Lock()
	cur := r.lists[key]
	r.mu.Unlock()
	now := r.now()
	switch {
	case cur != nil && now.Before(cur.until):
		return cur.clone(), true
	case cur != nil && now.Sub(cur.at) < r.ttl():
		return cur.clone(), cur.err != nil
	case cur != nil:
		r.refreshAsync(rc, filter, key)
		return cur.clone(), true
	case !block:
		r.refreshAsync(rc, filter, key)
		return &listEntry{}, true
	}
	e = r.fetchPulls(ctx, rc, filter, key)
	return e, e.err != nil
}

// rowLabel is the first matching state of a PR for the user: ai, yours, new, approve, none.
func rowLabel(role string, rev *model.PRReview, pending int, running bool, headSHA string) string {
	switch {
	case running || pending > 0:
		return model.LabelAI
	case role == model.RoleAuthor || role == model.RoleBoth:
		return model.LabelYours
	case rev == nil:
		return model.LabelNew
	case rev.PostedAt == nil || !sameHead(rev.PostedHeadSHA, headSHA):
		return model.LabelApprove
	}
	return model.LabelNone
}

func (r *Reviews) rows(ctx context.Context, rc *reviewCtx, e *listEntry) ([]model.PRRow, error) {
	out := []model.PRRow{}
	if len(e.rows) == 0 {
		return out, nil
	}
	keys := make([]model.PRKey, len(e.rows))
	for i, s := range e.rows {
		keys[i] = model.PRKey{Provider: rc.provider, Host: rc.host, Repo: rc.repo, Number: s.Number}
	}
	revs, err := r.Store.ListForUser(ctx, rc.userID, keys)
	if err != nil {
		return nil, err
	}
	byNum := map[int]*model.PRReview{}
	for i := range revs {
		if revs[i].Provider == rc.provider && revs[i].Host == rc.host && revs[i].Repo == rc.repo {
			byNum[revs[i].Number] = &revs[i]
		}
	}
	for _, s := range e.rows {
		role := e.roles[s.Number]
		if role == "" {
			role = model.RoleReviewer
		}
		rev := byNum[s.Number]
		pending, running := 0, false
		if rev != nil {
			ds, err := r.Store.Drafts(ctx, rev.ID)
			if err != nil {
				return nil, err
			}
			for _, d := range ds {
				if d.Status == model.DraftPending {
					pending++
				}
			}
			_, running = r.running.Load(rev.ID)
		}
		out = append(out, model.PRRow{Number: s.Number, Title: s.Title, URL: s.URL, Author: s.Author, Head: s.Head, Base: s.Base,
			Draft: s.Draft, UpdatedAt: s.UpdatedAt, Role: role, Label: rowLabel(role, rev, pending, running, s.HeadSHA),
			PendingDrafts: pending, Running: running, FactoryWOID: e.factory[s.Number]})
	}
	return out, nil
}

// PullRequests lists the open PRs of a project the user is involved in. Provider failures are part of
// the answer (view.Error), not errors, so the page can still show the last rows and the reason.
func (r *Reviews) PullRequests(ctx context.Context, u *model.User, projectID, filter string) (*model.PRListView, error) {
	filter, err := checkFilter(filter)
	if err != nil {
		return nil, err
	}
	p, err := r.Projects.Get(ctx, u, projectID)
	if err != nil {
		return nil, err
	}
	return r.projectList(ctx, u.ID, p, filter, true, 0)
}

func (r *Reviews) projectList(ctx context.Context, userID string, p *model.Project, filter string, block bool, resolveTimeout time.Duration) (*model.PRListView, error) {
	rctx := ctx
	if resolveTimeout > 0 {
		var cancel context.CancelFunc
		rctx, cancel = context.WithTimeout(ctx, resolveTimeout)
		defer cancel()
	}
	rc, err := r.resolve(rctx, userID, p, false)
	if err != nil {
		if !block && errors.Is(err, context.DeadlineExceeded) {
			// who the user is is still being learned: finish it in the background so the next call has rows
			r.warm(userID, p, filter)
			return &model.PRListView{Rows: []model.PRRow{}, Stale: true}, nil
		}
		return &model.PRListView{Rows: []model.PRRow{}, Error: r.providerErr(err)}, nil
	}
	e, stale := r.pulls(ctx, rc, filter, block)
	rows, err := r.rows(ctx, rc, e)
	if err != nil {
		return nil, err
	}
	return &model.PRListView{Identity: r.identityView(rctx, rc), Rows: rows, Stale: stale, Error: e.err}, nil
}

// warm resolves the identity and loads the list in the background after a rail call gave up waiting.
func (r *Reviews) warm(userID string, p *model.Project, filter string) {
	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if rc, err := r.resolve(ctx, userID, p, false); err == nil {
			r.pulls(ctx, rc, filter, false)
		}
	}()
}

// AllReviews is the cross-project inbox over every project the user can see.
func (r *Reviews) AllReviews(ctx context.Context, u *model.User, filter string) ([]model.ProjectPRs, error) {
	filter, err := checkFilter(filter)
	if err != nil {
		return nil, err
	}
	projects, err := r.Projects.List(ctx, u)
	if err != nil {
		return nil, err
	}
	out := make([]model.ProjectPRs, len(projects))
	errs := make([]error, len(projects))
	var wg sync.WaitGroup
	for i := range projects {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := &projects[i]
			v, err := r.projectList(ctx, u.ID, p, filter, true, 0)
			errs[i] = err
			if v != nil {
				out[i] = model.ProjectPRs{Project: model.ReviewProject{ID: p.ID, Name: p.Name}, Identity: v.Identity, Rows: v.Rows, Error: v.Error}
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RailPulls is the rail's pull request part; it never waits on the provider for rows.
func (r *Reviews) RailPulls(ctx context.Context, u *model.User, p *model.Project) *model.RailPulls {
	out := &model.RailPulls{Rows: []model.RailPullRow{}}
	v, err := r.projectList(ctx, u.ID, p, FilterRelated, false, railResolve)
	if err != nil {
		out.Error = r.providerErr(err)
		return out
	}
	out.Identity, out.Stale, out.Error = v.Identity, v.Stale, v.Error
	for _, row := range v.Rows {
		out.Rows = append(out.Rows, model.RailPullRow{Number: row.Number, Title: row.Title, Label: row.Label, PendingDrafts: row.PendingDrafts, Running: row.Running})
	}
	out.Count = len(out.Rows)
	return out
}

var runningStates = map[model.State]bool{
	model.StateRequirement: true, model.StateDecomposition: true, model.StatePlanGeneration: true, model.StatePlanning: true,
	model.StateQueued: true, model.StateImplementing: true, model.StateVerifying: true, model.StateSpecialistReview: true,
	model.StatePRPreparation: true, model.StateMergeReadiness: true,
}

// Summary is one card per project for the Projects list. PRs waiting on the user come from the cached
// listing and are null until one exists; the call never waits on a provider's list.
func (r *Reviews) Summary(ctx context.Context, u *model.User) ([]model.SummaryRow, error) {
	projects, err := r.Projects.List(ctx, u)
	if err != nil {
		return nil, err
	}
	wos, err := r.WO.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.SummaryRow, len(projects))
	now := r.now()
	var wg sync.WaitGroup
	for i := range projects {
		p := &projects[i]
		row := model.SummaryRow{ProjectID: p.ID}
		for _, wo := range wos {
			if wo.ProjectID != p.ID {
				continue
			}
			if row.LastActivity == nil || wo.UpdatedAt.After(*row.LastActivity) {
				t := wo.UpdatedAt
				row.LastActivity = &t
			}
			if wo.PRMergedAt != nil && now.Sub(*wo.PRMergedAt) <= 30*24*time.Hour {
				row.Shipped30d++
			}
			if !railOpen(&wo) {
				continue
			}
			if waitingOnYou(&wo) {
				row.NeedsYou++
			} else if runningStates[wo.State] {
				row.WORunning++
			}
		}
		out[i] = row
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := r.projectList(ctx, u.ID, p, FilterRelated, false, railResolve)
			if err != nil || v.Identity == nil {
				return
			}
			out[i].Identity = v.Identity
			if v.Error == nil && !v.Stale || len(v.Rows) > 0 {
				n := len(v.Rows)
				out[i].PRsForMe = &n
			}
		}()
	}
	wg.Wait()
	return out, nil
}
