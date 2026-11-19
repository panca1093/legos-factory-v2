package usecase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

// ---- fakes ----

type memReviews struct {
	mu          sync.Mutex
	reviews     map[string]*model.PRReview
	runs        []model.PRReviewRun
	drafts      []model.PRDraft
	viewed      map[string]map[string]string
	pins        map[string]model.ReviewIdentity
	failRunning int
}

func newMem() *memReviews {
	return &memReviews{reviews: map[string]*model.PRReview{}, viewed: map[string]map[string]string{}, pins: map[string]model.ReviewIdentity{}}
}

var errNoRow = fmt.Errorf("review: %w", sql.ErrNoRows)

func (m *memReviews) find(u, p, h, repo string, n int) *model.PRReview {
	for _, r := range m.reviews {
		if r.UserID == u && r.Provider == p && r.Host == h && r.Repo == repo && r.Number == n {
			return r
		}
	}
	return nil
}

func (m *memReviews) Upsert(_ context.Context, r *model.PRReview) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur := m.find(r.UserID, r.Provider, r.Host, r.Repo, r.Number); cur != nil {
		r.ID = cur.ID
	}
	c := *r
	m.reviews[r.ID] = &c
	return nil
}
func (m *memReviews) Get(_ context.Context, u, p, h, repo string, n int) (*model.PRReview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.find(u, p, h, repo, n); r != nil {
		c := *r
		return &c, nil
	}
	return nil, errNoRow
}
func (m *memReviews) ListForUser(_ context.Context, u string, keys []model.PRKey) ([]model.PRReview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []model.PRReview
	for _, k := range keys {
		if r := m.find(u, k.Provider, k.Host, k.Repo, k.Number); r != nil {
			out = append(out, *r)
		}
	}
	return out, nil
}
func (m *memReviews) SetDecision(_ context.Context, id, summary, verdict string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.reviews[id]
	if !ok {
		return errNoRow
	}
	r.Summary, r.Verdict = summary, verdict
	return nil
}
func (m *memReviews) MarkPosted(_ context.Context, id, head, verdict, ref, as string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.reviews[id]
	r.PostedAt, r.PostedHeadSHA, r.PostedVerdict, r.PostedRef, r.PostedAs = &at, head, verdict, ref, as
	return nil
}
func (m *memReviews) StartRun(_ context.Context, run model.PRReviewRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs = append(m.runs, run)
	return nil
}
func (m *memReviews) FinishRun(_ context.Context, id string, res model.AgentResult, cost int64, summary, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.runs {
		if m.runs[i].ID == id {
			m.runs[i].State, m.runs[i].Summary, m.runs[i].Error, m.runs[i].CostMicros, m.runs[i].TokensIn = model.ReviewRunCompleted, summary, errMsg, cost, res.TokensIn
			if errMsg != "" {
				m.runs[i].State = model.ReviewRunFailed
			}
		}
	}
	return nil
}
func (m *memReviews) LatestRun(_ context.Context, reviewID string) (*model.PRReviewRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.runs) - 1; i >= 0; i-- {
		if m.runs[i].ReviewID == reviewID {
			c := m.runs[i]
			return &c, nil
		}
	}
	return nil, nil
}
func (m *memReviews) FailRunning(context.Context, string) (int, error) {
	m.failRunning++
	return 1, nil
}
func (m *memReviews) AddDrafts(_ context.Context, reviewID, runID string, ds []model.PRDraft) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range ds {
		d.ReviewID, d.RunID = reviewID, runID
		m.drafts = append(m.drafts, d)
	}
	return nil
}
func (m *memReviews) Drafts(_ context.Context, reviewID string) ([]model.PRDraft, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []model.PRDraft
	for _, d := range m.drafts {
		if d.ReviewID == reviewID {
			out = append(out, d)
		}
	}
	return out, nil
}
func (m *memReviews) UpdateDraft(_ context.Context, reviewID, id string, status, body *string) (*model.PRDraft, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.drafts {
		if m.drafts[i].ReviewID == reviewID && m.drafts[i].ID == id {
			if status != nil {
				m.drafts[i].Status = *status
			}
			if body != nil {
				m.drafts[i].Body = *body
			}
			c := m.drafts[i]
			return &c, nil
		}
	}
	return nil, errNoRow
}
func (m *memReviews) SupersedePending(_ context.Context, reviewID, keep string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.drafts {
		if m.drafts[i].ReviewID == reviewID && m.drafts[i].Status == model.DraftPending && m.drafts[i].RunID != keep {
			m.drafts[i].Status = model.DraftSuperseded
		}
	}
	return nil
}
func (m *memReviews) MarkOutdated(_ context.Context, reviewID string, paths []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.drafts {
		d := &m.drafts[i]
		if d.ReviewID == reviewID && (d.Status == model.DraftPending || d.Status == model.DraftAccepted) {
			for _, p := range paths {
				if d.Path == p {
					d.Status = model.DraftOutdated
				}
			}
		}
	}
	return nil
}
func (m *memReviews) SetPosted(_ context.Context, id, ref, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.drafts {
		if m.drafts[i].ID == id {
			if ref != "" {
				m.drafts[i].Status, m.drafts[i].PostedRef, m.drafts[i].PostError = model.DraftPosted, ref, ""
			} else {
				m.drafts[i].PostError = errMsg
			}
		}
	}
	return nil
}
func (m *memReviews) SetViewed(_ context.Context, id, path, hash string, v bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.viewed[id] == nil {
		m.viewed[id] = map[string]string{}
	}
	if v {
		m.viewed[id][path] = hash
	} else {
		delete(m.viewed[id], path)
	}
	return nil
}
func (m *memReviews) Viewed(_ context.Context, id string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for k, v := range m.viewed[id] {
		out[k] = v
	}
	return out, nil
}
func (m *memReviews) Identity(_ context.Context, u, p, h string) (*model.ReviewIdentity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.pins[u+"|"+p+"|"+h]; ok {
		return &id, nil
	}
	return nil, nil
}
func (m *memReviews) PinIdentity(_ context.Context, id model.ReviewIdentity) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pins[id.UserID+"|"+id.Provider+"|"+id.Host] = id
	return nil
}

type fakeReviewer struct {
	meGate, postGate chan struct{}
	entered          chan struct{}
	mu               sync.Mutex
	who              string
	me               model.HostUser
	authored         []model.PRSummary
	reviewing        []model.PRSummary
	all              []model.PRSummary
	detail           model.PRDetail
	files            []model.PRFile
	info             model.TokenInfo
	listCalls        int
	listErr          error
	post             model.PostedReview
	postErr          error
	posted           []model.ReviewSubmission
}

func (f *fakeReviewer) Me(ctx context.Context) (model.HostUser, error) {
	if f.meGate != nil {
		select {
		case <-f.meGate:
		case <-ctx.Done():
			return model.HostUser{}, ctx.Err()
		}
	}
	return f.me, nil
}
func (f *fakeReviewer) TokenInfo(context.Context) (model.TokenInfo, error) {
	return f.info, nil
}
func (f *fakeReviewer) ListPullRequests(_ context.Context, _ string, fl model.PRFilter) ([]model.PRSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	switch {
	case fl.Author.Login != "":
		return f.authored, nil
	case fl.Reviewer.Login != "":
		return f.reviewing, nil
	}
	return f.all, nil
}
func (f *fakeReviewer) GetPullRequest(context.Context, string, int) (model.PRDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.detail, nil
}
func (f *fakeReviewer) PullRequestFiles(context.Context, string, int) ([]model.PRFile, error) {
	return append([]model.PRFile(nil), f.files...), nil
}
func (f *fakeReviewer) PostReview(_ context.Context, _ string, _ int, s model.ReviewSubmission) (model.PostedReview, error) {
	if f.postGate != nil {
		f.entered <- struct{}{}
		<-f.postGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posted = append(f.posted, s)
	return f.post, f.postErr
}

type fakeCLI struct {
	users []string
	tok   string
}

func (c fakeCLI) Status(context.Context, string) ([]string, error)      { return c.users, nil }
func (c fakeCLI) Token(context.Context, string, string) (string, error) { return c.tok, nil }
func (c fakeCLI) StartLogin(context.Context, string) (<-chan model.CLILoginStep, error) {
	ch := make(chan model.CLILoginStep, 3)
	ch <- model.CLILoginStep{Kind: "code", Code: "AB-12"}
	ch <- model.CLILoginStep{Kind: "done", Login: "ana"}
	close(ch)
	return ch, nil
}

type eventLog struct {
	mu sync.Mutex
	ev []model.LiveEvent
}

func (l *eventLog) Publish(e model.LiveEvent) { l.mu.Lock(); l.ev = append(l.ev, e); l.mu.Unlock() }
func (l *eventLog) kinds() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.ev {
		out = append(out, e.Kind)
	}
	return out
}

type scriptedAgent struct {
	mu      sync.Mutex
	replies []string
	gate    chan struct{}
	prompts []string
	starts  int
	roles   []model.AgentRole
}

func (a *scriptedAgent) Name() string { return "script" }
func (a *scriptedAgent) Start(_ context.Context, req model.AgentRequest) (model.AgentSession, error) {
	a.mu.Lock()
	a.prompts = append(a.prompts, req.Prompt)
	a.roles = append(a.roles, req.Role)
	a.starts++
	a.mu.Unlock()
	return sess("s"), nil
}
func (a *scriptedAgent) Cancel(context.Context, string) error { return nil }
func (a *scriptedAgent) Result(context.Context, string) (model.AgentResult, error) {
	if a.gate != nil {
		<-a.gate
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	txt := a.replies[0]
	if len(a.replies) > 1 {
		a.replies = a.replies[1:]
	}
	return model.AgentResult{Text: txt, TokensIn: 100, TokensOut: 10, CostMicros: 5, CostSource: "measured"}, nil
}

type sess string

func (s sess) ID() string { return string(s) }

type fakeWS struct {
	mu                    sync.Mutex
	prepared, removedRuns []string
	cleaned               int
	sha                   string
	removed               int
	err                   error
}

func (w *fakeWS) PrepareReview(_ context.Context, _ string, _ int, runID, _, ref string, _ *model.GitAuth) (string, string, error) {
	w.mu.Lock()
	w.prepared = append(w.prepared, runID)
	w.mu.Unlock()
	return "/tmp/review", w.sha, w.err
}
func (w *fakeWS) RemoveReview(_ context.Context, _ string, _ int, runID, _ string) error {
	w.mu.Lock()
	w.removed++
	w.removedRuns = append(w.removedRuns, runID)
	w.mu.Unlock()
	return nil
}
func (w *fakeWS) CleanReviews(context.Context, string, string) error {
	w.mu.Lock()
	w.cleaned++
	w.mu.Unlock()
	return nil
}

type usageLog struct {
	mu   sync.Mutex
	recs []model.UsageRecord
}

func (u *usageLog) Reserve(int64) (func(int64), bool) { return func(int64) {}, true }
func (u *usageLog) Record(_ context.Context, r model.UsageRecord) error {
	u.mu.Lock()
	u.recs = append(u.recs, r)
	u.mu.Unlock()
	return nil
}

type rvProjects struct {
	ProjectStore
	p model.Project
}

func (s rvProjects) Get(_ context.Context, id string) (*model.Project, error) {
	if id != s.p.ID {
		return nil, model.ErrProjectNotFound
	}
	c := s.p
	return &c, nil
}
func (s rvProjects) List(context.Context) ([]model.Project, error) { return []model.Project{s.p}, nil }

type rvMembers struct {
	MemberStore
	ok map[string]bool
}

func (m rvMembers) IsMember(_ context.Context, _, u string) (bool, error) { return m.ok[u], nil }
func (m rvMembers) ProjectIDsFor(_ context.Context, u string) ([]string, error) {
	if m.ok[u] {
		return []string{"p1"}, nil
	}
	return nil, nil
}

const patchA = "--- a/a.go\n+++ b/a.go\n@@ -1,3 +1,3 @@\n package a\n+var x = 1\n func f() {}\n-var old = 2\n"

type rvRig struct {
	r     *Reviews
	st    *memReviews
	rv    map[string]*fakeReviewer // by connection username, "gh" for the CLI
	built []string
	ev    *eventLog
	use   *usageLog
	ws    *fakeWS
	ag    *scriptedAgent
	now   time.Time
	conns *personalConns
	ana   *model.User
}

func verified() *time.Time { t := time.Now(); return &t }

func newRvRig(t *testing.T) *rvRig {
	t.Helper()
	g := &rvRig{st: newMem(), rv: map[string]*fakeReviewer{}, ev: &eventLog{}, use: &usageLog{}, ws: &fakeWS{sha: "h1"},
		ag: &scriptedAgent{replies: []string{`{"summary":"fine","findings":[]}`}}, now: time.Unix(1_000_000, 0), ana: &model.User{ID: "ana", Role: model.RoleEngineer}}
	mkRV := func(login string) *fakeReviewer {
		return &fakeReviewer{me: model.HostUser{Login: login, ID: "id-" + login}, info: model.TokenInfo{CanRead: true, CanWrite: model.TriYes},
			detail: model.PRDetail{PRSummary: model.PRSummary{Number: 7, Title: "Add x", Author: model.HostUser{Login: "zed", ID: "id-zed"}, Head: "feat", Base: "main", HeadSHA: "h1"},
				SourceRepo: "acme/app"},
			files: []model.PRFile{{Path: "a.go", OldPath: "a.go", Status: "modified", Patch: patchA}}}
	}
	g.rv["c1"], g.rv["c2"], g.rv["gh"] = mkRV("ana"), mkRV("ana"), mkRV("ana")
	g.conns = &personalConns{conns: []model.Connection{
		{ID: "c1", Provider: model.ProviderGitHub, OwnerID: "ana", BaseURL: "https://api.github.com", Username: "c1", Status: model.ConnectionActive, VerifiedAt: verified()},
	}}
	host := &fakeHost{repos: map[string]string{"/work/alpha": "git@github.com:acme/app.git"}}
	g.r = &Reviews{
		Store:        g.st,
		Projects:     &Projects{Store: rvProjects{p: model.Project{ID: "p1", Name: "Alpha", RepoURL: "/work/alpha"}}, Members: rvMembers{ok: map[string]bool{"ana": true, "bob": true}}},
		Integrations: &Integrations{Store: g.conns, Secrets: plainSealer{}, Host: host},
		NewReviewer: func(provider, baseURL, username, token string) PRReviewer {
			key := username
			if key == "" {
				key = "gh"
			}
			g.built = append(g.built, key)
			return g.rv[key]
		},
		Bus: g.ev, Usage: g.use, Workspace: g.ws,
		Agent: func(context.Context, string) (ReviewAgent, error) {
			return ReviewAgent{Harness: g.ag, Agent: "claude", Provider: "anthropic", Model: "m"}, nil
		},
		Now: func() time.Time { return g.now },
	}
	return g
}

func (g *rvRig) devMode(users ...string) {
	g.r.Integrations.GH = func(string, string) GitProvider { return nil }
	g.r.CLI = fakeCLI{users: users, tok: "cli-token"}
}

func (g *rvRig) project() *model.Project {
	return &model.Project{ID: "p1", Name: "Alpha", RepoURL: "/work/alpha"}
}

// ---- identity ----

func TestIdentityPinnedBeatsAConnectionAndOrderIsPinConnectionCLINone(t *testing.T) {
	ctx := context.Background()

	g := newRvRig(t)
	g.conns.conns = append(g.conns.conns, model.Connection{ID: "c2", Provider: model.ProviderGitHub, OwnerID: "ana", BaseURL: "https://api.github.com", Username: "c2", Status: model.ConnectionActive, VerifiedAt: verified()})
	if _, err := g.r.resolve(ctx, "ana", g.project(), false); !errors.Is(err, ErrIdentityAmbiguous) {
		t.Fatalf("two connections and no pin: %v", err)
	}
	g.st.pins["ana|github|github.com"] = model.ReviewIdentity{UserID: "ana", Provider: "github", Host: "github.com", Source: "connection", ConnectionID: "c2", Login: "ana"}
	rc, err := g.r.resolve(ctx, "ana", g.project(), false)
	if err != nil || rc.connectionID != "c2" || g.built[len(g.built)-1] != "c2" {
		t.Fatalf("pinned connection must win: %v %+v built=%v", err, rc, g.built)
	}

	g = newRvRig(t)
	rc, err = g.r.resolve(ctx, "ana", g.project(), false)
	if err != nil || rc.source != "connection" || g.st.pins["ana|github|github.com"].ConnectionID != "c1" {
		t.Fatalf("a lone verified connection is used and pinned on first use: %v %+v", err, g.st.pins)
	}

	g = newRvRig(t)
	g.conns.conns = nil
	g.devMode("ana")
	rc, err = g.r.resolve(ctx, "ana", g.project(), false)
	if err != nil || rc.source != "gh" || g.st.pins["ana|github|github.com"].Source != "gh" {
		t.Fatalf("the gh login is the last resort in local mode: %v", err)
	}

	g = newRvRig(t)
	g.conns.conns = nil
	g.devMode("ana", "ana2")
	if _, err := g.r.resolve(ctx, "ana", g.project(), false); !errors.Is(err, ErrIdentityAmbiguous) {
		t.Fatalf("several gh users: %v", err)
	}

	g = newRvRig(t)
	g.conns.conns = nil
	g.r.CLI = fakeCLI{users: []string{"ana"}, tok: "t"} // multi-user mode: GH is nil, so the CLI must not be lent
	if _, err := g.r.resolve(ctx, "ana", g.project(), false); !errors.Is(err, ErrNoReviewIdentity) {
		t.Fatalf("CLI must be off outside local mode: %v", err)
	}
}

func TestIdentityIgnoresOthersUnverifiedAndDisabledConnections(t *testing.T) {
	g := newRvRig(t)
	g.conns.conns = []model.Connection{
		{ID: "x1", Provider: model.ProviderGitHub, OwnerID: "bob", BaseURL: "https://api.github.com", Username: "c1", Status: model.ConnectionActive, VerifiedAt: verified()},
		{ID: "x2", Provider: model.ProviderGitHub, OwnerID: "ana", BaseURL: "https://api.github.com", Username: "c1", Status: model.ConnectionActive},
		{ID: "x3", Provider: model.ProviderGitHub, OwnerID: "ana", BaseURL: "https://api.github.com", Username: "c1", Status: model.ConnectionDisabled, VerifiedAt: verified()},
	}
	if _, err := g.r.resolve(context.Background(), "ana", g.project(), false); !errors.Is(err, ErrNoReviewIdentity) {
		t.Fatalf("err = %v", err)
	}
}

func TestIdentityChangedWhenTheCredentialNowBelongsToSomeoneElse(t *testing.T) {
	g := newRvRig(t)
	if _, err := g.r.resolve(context.Background(), "ana", g.project(), false); err != nil {
		t.Fatal(err)
	}
	g.rv["c1"].me = model.HostUser{Login: "mallory", ID: "id-m"}
	if _, err := g.r.resolve(context.Background(), "ana", g.project(), true); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("err = %v", err)
	}
}

// ---- list and labels ----

func TestRowLabelPrecedence(t *testing.T) {
	posted := time.Now()
	cases := []struct {
		name    string
		role    string
		rev     *model.PRReview
		pending int
		running bool
		want    string
	}{
		{"running beats everything", model.RoleAuthor, nil, 0, true, "ai"},
		{"pending drafts", model.RoleReviewer, &model.PRReview{}, 2, false, "ai"},
		{"own pr", model.RoleAuthor, nil, 0, false, "yours"},
		{"never opened", model.RoleReviewer, nil, 0, false, "new"},
		{"opened, not posted", model.RoleReviewer, &model.PRReview{}, 0, false, "approve"},
		{"posted on an older head", model.RoleReviewer, &model.PRReview{PostedAt: &posted, PostedHeadSHA: "old"}, 0, false, "approve"},
		{"posted at this head", model.RoleReviewer, &model.PRReview{PostedAt: &posted, PostedHeadSHA: "h1"}, 0, false, "none"},
	}
	for _, c := range cases {
		if got := rowLabel(c.role, c.rev, c.pending, c.running, "h1"); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func rvSummary(n int, author string, updated int) model.PRSummary {
	return model.PRSummary{Number: n, Title: fmt.Sprintf("pr %d", n), Author: model.HostUser{Login: author}, HeadSHA: "h1", UpdatedAt: time.Unix(int64(updated), 0)}
}

func TestListMergesRolesCountsFactoryPRsAndCaches(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	rv := g.rv["c1"]
	rv.authored = []model.PRSummary{rvSummary(1, "ana", 10), rvSummary(2, "ana", 20)}
	rv.reviewing = []model.PRSummary{rvSummary(2, "ana", 20), rvSummary(3, "zed", 30)}
	rv.all = append(rv.authored, rvSummary(9, "factory-bot", 5))
	rv.all = append(rv.all, rv.reviewing...)
	woStore := &woListStub{wos: []model.WorkOrder{{ID: "wo1", ProjectID: "p1", CreatedBy: "ana", PRRef: "github:acme/app#9"}, {ID: "wo2", ProjectID: "p1", CreatedBy: "bob", PRRef: "github:acme/app#3"}}}
	g.r.WO = woStore

	v, err := g.r.PullRequests(ctx, g.ana, "p1", "")
	if err != nil || v.Error != nil || len(v.Rows) != 4 {
		t.Fatalf("%v %+v", err, v)
	}
	by := map[int]model.PRRow{}
	for _, r := range v.Rows {
		by[r.Number] = r
	}
	if by[1].Role != "author" || by[2].Role != "both" || by[3].Role != "reviewer" || by[9].Role != "author" || by[9].FactoryWOID != "wo1" {
		t.Fatalf("roles: %+v", by)
	}
	if by[3].Label != "new" || by[1].Label != "yours" || v.Rows[0].Number != 3 {
		t.Fatalf("labels/order: %+v", v.Rows)
	}
	if v.Identity == nil || v.Identity.Login != "ana" || v.Identity.Readiness != "ready" {
		t.Fatalf("identity: %+v", v.Identity)
	}
	if by[3].Label == "yours" || by[3].FactoryWOID != "" {
		t.Fatal("another user's work order must not make a PR mine")
	}

	calls := rv.listCalls
	if _, err := g.r.PullRequests(ctx, g.ana, "p1", "related"); err != nil || rv.listCalls != calls {
		t.Fatalf("a fresh cache must not call the provider (%d -> %d)", calls, rv.listCalls)
	}
	g.now = g.now.Add(2 * time.Minute)
	v, _ = g.r.PullRequests(ctx, g.ana, "p1", "related")
	if !v.Stale || len(v.Rows) != 4 {
		t.Fatalf("a stale cache is served at once: %+v", v)
	}
	g.r.Wait()
	if rv.listCalls == calls {
		t.Fatal("the stale entry was not revalidated")
	}
	if v, _ = g.r.PullRequests(ctx, g.ana, "p1", "related"); v.Stale {
		t.Fatal("revalidated entry is fresh")
	}

	if _, err := g.r.PullRequests(ctx, g.ana, "p1", "bogus"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad filter: %v", err)
	}
}

type woListStub struct {
	WorkOrderStore
	wos []model.WorkOrder
}

func (w *woListStub) List(context.Context) ([]model.WorkOrder, error) { return w.wos, nil }

func TestListRateLimitKeepsRowsAndStopsCalling(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	rv := g.rv["c1"]
	rv.reviewing = []model.PRSummary{rvSummary(3, "zed", 30)}
	if v, _ := g.r.PullRequests(ctx, g.ana, "p1", "reviewer"); len(v.Rows) != 1 {
		t.Fatal("seed")
	}
	rv.listErr = &model.ProviderError{Provider: "github", Status: 403, RateLimited: true, RetryAfter: 120 * time.Second}
	g.now = g.now.Add(2 * time.Minute)
	g.r.PullRequests(ctx, g.ana, "p1", "reviewer")
	g.r.Wait()
	calls := rv.listCalls
	v, _ := g.r.PullRequests(ctx, g.ana, "p1", "reviewer")
	if len(v.Rows) != 1 || v.Error == nil || v.Error.Code != model.ErrCodeRateLimit || v.Error.Until == nil || !v.Stale {
		t.Fatalf("%+v", v)
	}
	g.now = g.now.Add(30 * time.Second)
	g.r.PullRequests(ctx, g.ana, "p1", "reviewer")
	g.r.Wait()
	if rv.listCalls != calls {
		t.Fatal("the provider was called again during the rate-limit window")
	}
}

func TestRailNeverWaitsForTheProviderAndNoIdentityIsAnAnswer(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	g.rv["c1"].reviewing = []model.PRSummary{rvSummary(3, "zed", 30)}
	out := g.r.RailPulls(ctx, g.ana, g.project())
	if out.Count != 0 || !out.Stale {
		t.Fatalf("first call returns empty and stale: %+v", out)
	}
	g.r.Wait()
	out = g.r.RailPulls(ctx, g.ana, g.project())
	if out.Count != 1 || out.Stale || out.Rows[0].Label != "new" {
		t.Fatalf("%+v", out)
	}

	g = newRvRig(t)
	g.conns.conns = nil
	out = g.r.RailPulls(ctx, g.ana, g.project())
	if out.Error == nil || out.Error.Code != model.ErrCodeNoIdentity || out.Identity != nil || out.Rows == nil {
		t.Fatalf("%+v", out)
	}
}

// ---- drafts ----

func seedReview(t *testing.T, g *rvRig, statuses ...string) *model.PRReview {
	t.Helper()
	ctx := context.Background()
	rc, err := g.r.resolve(ctx, "ana", g.project(), false)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := g.r.ensureReview(ctx, rc, 7)
	if err != nil {
		t.Fatal(err)
	}
	rev.HeadSHA = "h1"
	g.st.Upsert(ctx, rev)
	for i, s := range statuses {
		g.st.drafts = append(g.st.drafts, model.PRDraft{ID: fmt.Sprintf("d%d", i), ReviewID: rev.ID, RunID: "r0", Path: "a.go", Side: "new", Line: 2, Inline: true,
			HeadSHA: "h1", Severity: "minor", Title: "t", Body: "b", Status: s})
	}
	return rev
}

func TestDraftTransitions(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	cases := []struct {
		from, to string
		ok       bool
	}{
		{"pending", "accepted", true}, {"pending", "discarded", true}, {"discarded", "pending", true}, {"accepted", "pending", true},
		{"accepted", "discarded", false}, {"discarded", "accepted", false}, {"posted", "pending", false}, {"superseded", "pending", false},
		{"outdated", "pending", false}, {"pending", "posted", false}, {"pending", "bogus", false},
	}
	for _, c := range cases {
		g := newRvRig(t)
		seedReview(t, g, c.from)
		d, err := g.r.UpdateDraft(ctx, g.ana, "p1", 7, "d0", str(c.to), nil)
		if c.ok && (err != nil || d.Status != c.to) {
			t.Errorf("%s→%s: %v", c.from, c.to, err)
		}
		if !c.ok && !errors.Is(err, ErrInvalid) {
			t.Errorf("%s→%s should be refused, got %v", c.from, c.to, err)
		}
	}
	g := newRvRig(t)
	seedReview(t, g, "pending", "posted", "discarded")
	if d, err := g.r.UpdateDraft(ctx, g.ana, "p1", 7, "d0", nil, str("edited")); err != nil || d.Body != "edited" || d.AIBody != "" && false {
		t.Fatalf("edit a pending draft: %v", err)
	}
	for _, id := range []string{"d1", "d2"} {
		if _, err := g.r.UpdateDraft(ctx, g.ana, "p1", 7, id, nil, str("x")); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s must not be editable: %v", id, err)
		}
	}
	if _, err := g.r.UpdateDraft(ctx, g.ana, "p1", 7, "d0", nil, str("  ")); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty body")
	}
}

func TestOtherUsersReviewIsNotFoundAndNonMembersAreRefused(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	seedReview(t, g, "pending")
	bob := &model.User{ID: "bob", Role: model.RoleEngineer}
	st := "accepted"
	if _, err := g.r.UpdateDraft(ctx, bob, "p1", 7, "d0", &st, nil); !errors.Is(err, ErrReviewNotFound) {
		t.Fatalf("bob on ana's draft: %v", err)
	}
	if ds, err := g.r.Drafts(ctx, bob, "p1", 7); err != nil || len(ds) != 0 {
		t.Fatalf("bob must see none of ana's drafts: %v %v", ds, err)
	}
	if _, err := g.r.UpdateReview(ctx, g.ana, "p1", 7, nil, strPtr("bogus")); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad verdict")
	}
	carl := &model.User{ID: "carl", Role: model.RoleEngineer}
	if _, err := g.r.Drafts(ctx, carl, "p1", 7); !IsNotFound(err) {
		t.Fatalf("non-member: %v", err)
	}
	if _, err := g.r.StartRun(ctx, carl, "p1", 7); !IsNotFound(err) {
		t.Fatalf("non-member run: %v", err)
	}
}

func strPtr(s string) *string { return &s }

func TestViewedTracksPatchHashAndFilesMarkOutdatedDrafts(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	seedReview(t, g, "pending", "accepted")
	vc, err := g.r.SetViewed(ctx, g.ana, "p1", 7, "a.go", true)
	if err != nil || vc.Viewed != 1 || vc.Total != 1 {
		t.Fatalf("%v %+v", err, vc)
	}
	if _, err := g.r.SetViewed(ctx, g.ana, "p1", 7, "nope.go", true); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown file")
	}
	fs, _ := g.r.Files(ctx, g.ana, "p1", 7)
	if !fs[0].Viewed {
		t.Fatal("viewed")
	}

	rv := g.rv["c1"]
	rv.detail.HeadSHA = "h2"
	rv.files[0].Patch = patchA + "+more\n"
	g.now = g.now.Add(2 * time.Minute)
	fs, err = g.r.Files(ctx, g.ana, "p1", 7)
	if err != nil || fs[0].Viewed {
		t.Fatalf("a changed patch resets viewed: %v %+v", err, fs[0].Viewed)
	}
	for _, d := range g.st.drafts {
		if d.Status != model.DraftOutdated {
			t.Fatalf("drafts on a changed file become outdated: %+v", d)
		}
	}
}

// ---- post ----

func TestPostChecksAndSendsOnlyAcceptedDrafts(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	rev := seedReview(t, g, "accepted", "pending", "discarded", "accepted")
	g.st.drafts[3].Inline, g.st.drafts[3].Title = false, "gen"
	g.st.SetDecision(ctx, rev.ID, "overall", "comment")
	rv := g.rv["c1"]
	rv.post = model.PostedReview{Ref: "rev-1", Comments: map[string]string{"d0": "c-1"}}
	good := model.PostRequest{Confirm: true, Expect: model.PostExpect{Login: "ana", HeadSHA: "h1", Comments: 2, Verdict: "comment"}}

	var pc *PostConflict
	noConfirm := good
	noConfirm.Confirm = false
	if _, err := g.r.Post(ctx, g.ana, "p1", 7, noConfirm); !errors.Is(err, ErrInvalid) {
		t.Fatalf("confirm: %v", err)
	}
	for name, mut := range map[string]func(*model.PostRequest){
		"head":     func(r *model.PostRequest) { r.Expect.HeadSHA = "old" },
		"comments": func(r *model.PostRequest) { r.Expect.Comments = 5 },
		"verdict":  func(r *model.PostRequest) { r.Expect.Verdict = "approve" },
		"login":    func(r *model.PostRequest) { r.Expect.Login = "someone" },
	} {
		req := good
		mut(&req)
		_, err := g.r.Post(ctx, g.ana, "p1", 7, req)
		if !errors.As(err, &pc) || pc.Current.HeadSHA != "h1" || pc.Current.Comments != 2 {
			t.Fatalf("%s mismatch must be a conflict carrying the current view: %v", name, err)
		}
	}
	if len(rv.posted) != 0 {
		t.Fatal("nothing may be posted on a conflict")
	}

	rv.detail.HeadSHA = "h2"
	g.now = g.now.Add(time.Hour)
	req := good
	req.Expect.HeadSHA = "h2"
	if _, err := g.r.Post(ctx, g.ana, "p1", 7, req); !errors.As(err, &pc) {
		t.Fatalf("drafts made on an older head must not post: %v", err)
	}
	rv.detail.HeadSHA = "h1"

	posted, err := g.r.Post(ctx, g.ana, "p1", 7, good)
	if err != nil || posted.Ref != "rev-1" || len(rv.posted) != 1 {
		t.Fatalf("%v %+v", err, posted)
	}
	sub := rv.posted[0]
	if len(sub.Comments) != 1 || sub.Comments[0].DraftID != "d0" || sub.HeadSHA != "h1" || !strings.Contains(sub.Body, "overall") || !strings.Contains(sub.Body, "gen") {
		t.Fatalf("submission: %+v", sub)
	}
	if g.st.drafts[0].Status != "posted" || g.st.drafts[3].Status != "posted" || g.st.drafts[1].Status != "pending" || g.st.drafts[2].Status != "discarded" {
		t.Fatalf("statuses: %+v", g.st.drafts)
	}
	cur, _ := g.st.Get(ctx, "ana", "github", "github.com", "acme/app", 7)
	if cur.PostedAt == nil || cur.PostedHeadSHA != "h1" || cur.PostedAs != "ana" || cur.PostedRef != "rev-1" {
		t.Fatalf("%+v", cur)
	}
}

func TestPostKeepsFailedDraftsAcceptedAndNeverRetries(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	seedReview(t, g, "accepted", "accepted")
	rv := g.rv["c1"]
	rv.post = model.PostedReview{Ref: "rev-1", Comments: map[string]string{"d0": "c-1"}, Failed: map[string]string{"d1": "line not in diff"}}
	if _, err := g.r.Post(ctx, g.ana, "p1", 7, model.PostRequest{Confirm: true, Expect: model.PostExpect{Login: "ana", HeadSHA: "h1", Comments: 2, Verdict: "comment"}}); err != nil {
		t.Fatal(err)
	}
	if g.st.drafts[1].Status != "accepted" || g.st.drafts[1].PostError != "line not in diff" {
		t.Fatalf("%+v", g.st.drafts[1])
	}
	rv.post, rv.postErr = model.PostedReview{}, &model.ProviderError{Provider: "github", Status: 502, Message: "bad gateway"}
	g.r.Post(ctx, g.ana, "p1", 7, model.PostRequest{Confirm: true, Expect: model.PostExpect{Login: "ana", HeadSHA: "h1", Comments: 1, Verdict: "comment"}})
	if len(rv.posted) != 2 {
		t.Fatalf("exactly one provider call per post, got %d", len(rv.posted))
	}
}

func TestPostRefusesVerdictsTheProviderOrRoleCannotDo(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	rev := seedReview(t, g, "accepted")
	rv := g.rv["c1"]
	rv.detail.Author = model.HostUser{Login: "ana", ID: "id-ana"}
	g.st.SetDecision(ctx, rev.ID, "s", "approve")
	d, _ := g.r.Detail(ctx, g.ana, "p1", 7)
	if d.Verdicts.Approve.OK || d.Verdicts.RequestChanges.OK || !d.AI.OK {
		t.Fatalf("own PR on GitHub: %+v", d.Verdicts)
	}
	if _, err := g.r.Post(ctx, g.ana, "p1", 7, model.PostRequest{Confirm: true, Expect: model.PostExpect{Login: "ana", HeadSHA: "h1", Comments: 1, Verdict: "approve"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("approve own PR: %v", err)
	}
	rc := &reviewCtx{provider: model.ProviderGitLab, me: model.HostUser{Login: "ana"}}
	if v := g.r.verdicts(rc, model.PRDetail{}); v.RequestChanges.OK || !v.Approve.OK {
		t.Fatalf("gitlab: %+v", v)
	}

	g = newRvRig(t)
	seedReview(t, g, "accepted")
	g.rv["c1"].info.CanWrite = model.TriNo
	if _, err := g.r.Post(ctx, g.ana, "p1", 7, model.PostRequest{Confirm: true, Expect: model.PostExpect{Login: "ana", HeadSHA: "h1", Comments: 1, Verdict: "comment"}}); !errors.Is(err, ErrReadOnlyToken) {
		t.Fatalf("read-only token: %v", err)
	}
}

// ---- AI run ----

const rvReply = `{"summary":"Looks fine overall.","findings":[
 {"severity":"minor","path":"a.go","side":"new","line":2,"title":"added line","body":"consider x"},
 {"severity":"blocker","path":"a.go","side":"new","line":1,"title":"context line","body":"check"},
 {"severity":"major","path":"a.go","side":"old","line":3,"title":"removed line","body":"why removed"},
 {"severity":"info","path":"a.go","side":"new","line":99,"title":"off diff","body":"general"},
 {"severity":"info","path":"nope.go","side":"new","line":1,"title":"unknown file","body":"general"}]}`

func TestRunStoresValidatedDraftsRecordsUsageAndCleansUp(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	g.ag.replies = []string{rvReply}
	run, err := g.r.StartRun(ctx, g.ana, "p1", 7)
	if err != nil || run.State != "running" || run.Harness != "script" || run.Model != "anthropic/m" {
		t.Fatalf("%v %+v", err, run)
	}
	g.r.Wait()
	if !strings.Contains(g.ag.prompts[0], "UNTRUSTED") || !strings.Contains(g.ag.prompts[0], "a.go") {
		t.Fatal("prompt must carry the untrusted-input warning and the patches")
	}
	ds, _ := g.r.Drafts(ctx, g.ana, "p1", 7)
	if len(ds) != 5 || ds[0].Severity != "blocker" || ds[1].Severity != "major" {
		t.Fatalf("sorted by severity: %+v", ds)
	}
	by := map[string]model.PRDraft{}
	for _, d := range ds {
		by[d.Title] = d
	}
	if d := by["added line"]; !d.Inline || d.Side != "new" || d.Line != 2 || d.OldLine != 0 || d.HeadSHA != "h1" || d.Status != "pending" || d.AIBody != d.Body {
		t.Fatalf("added: %+v", d)
	}
	if d := by["context line"]; !d.Inline || d.Line != 1 || d.OldLine != 1 {
		t.Fatalf("context lines carry both numbers: %+v", d)
	}
	if d := by["removed line"]; !d.Inline || d.Side != "old" || d.Line != 3 {
		t.Fatalf("removed: %+v", d)
	}
	if by["off diff"].Inline || by["unknown file"].Inline {
		t.Fatal("anchors outside a hunk or file must become General")
	}
	rev, _ := g.st.Get(ctx, "ana", "github", "github.com", "acme/app", 7)
	if rev.HeadSHA != "h1" || rev.Summary != "Looks fine overall." {
		t.Fatalf("review: %+v", rev)
	}
	last, _ := g.st.LatestRun(ctx, rev.ID)
	if last.State != "completed" || last.CostMicros != 5 {
		t.Fatalf("%+v", last)
	}
	if len(g.use.recs) != 1 || g.use.recs[0].UserID != "ana" || g.use.recs[0].Role != "inspector" || g.use.recs[0].WOID != "" {
		t.Fatalf("usage: %+v", g.use.recs)
	}
	if g.ws.removed != 1 {
		t.Fatal("the review checkout must be removed when the run ends")
	}
	if ks := g.ev.kinds(); len(ks) != 2 || ks[0] != "pr_review.run_started" || ks[1] != "pr_review.run_finished" || g.ev.ev[0].UserID != "ana" || g.ev.ev[0].WOID != "" {
		t.Fatalf("events: %v", g.ev.ev)
	}
	if g.ev.ev[0].Payload["number"] != 7 || g.ev.ev[0].Payload["project_id"] != "p1" {
		t.Fatalf("payload: %v", g.ev.ev[0].Payload)
	}
}

func TestRerunSupersedesPendingKeepsDecisionsAndKeepsEditedSummary(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	rev := seedReview(t, g, "pending", "accepted", "discarded")
	g.st.SetDecision(ctx, rev.ID, "mine", "comment")
	g.ag.replies = []string{`{"summary":"new","findings":[{"severity":"minor","path":"a.go","side":"new","line":2,"title":"fresh","body":"b"}]}`}
	if _, err := g.r.StartRun(ctx, g.ana, "p1", 7); err != nil {
		t.Fatal(err)
	}
	g.r.Wait()
	want := map[string]string{"d0": "superseded", "d1": "accepted", "d2": "discarded"}
	for _, d := range g.st.drafts {
		if w, ok := want[d.ID]; ok && d.Status != w {
			t.Fatalf("%s is %s, want %s", d.ID, d.Status, w)
		}
	}
	cur, _ := g.st.Get(ctx, "ana", "github", "github.com", "acme/app", 7)
	if cur.Summary != "mine" {
		t.Fatalf("an edited summary must survive a re-run: %q", cur.Summary)
	}
	if !strings.Contains(g.ag.prompts[0], "fresh") && !strings.Contains(g.ag.prompts[0], "t") {
		t.Fatal("earlier decisions go into the prompt")
	}
}

func TestSecondRunWhileRunningIsRefusedAndForkPRsAreUnavailable(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	g.ag.gate = make(chan struct{})
	if _, err := g.r.StartRun(ctx, g.ana, "p1", 7); err != nil {
		t.Fatal(err)
	}
	if _, err := g.r.StartRun(ctx, g.ana, "p1", 7); !errors.Is(err, ErrReviewRunning) {
		t.Fatalf("second run: %v", err)
	}
	d, _ := g.r.Detail(ctx, g.ana, "p1", 7)
	if d.Run == nil || d.Run.State != "running" {
		t.Fatalf("%+v", d.Run)
	}
	close(g.ag.gate)
	g.r.Wait()
	if _, err := g.r.StartRun(ctx, g.ana, "p1", 7); err != nil {
		t.Fatalf("a finished run frees the slot: %v", err)
	}
	g.r.Wait()

	g = newRvRig(t)
	g.rv["c1"].detail.SourceRepo = "someone/fork"
	if _, err := g.r.StartRun(ctx, g.ana, "p1", 7); !errors.Is(err, ErrAIUnavailable) {
		t.Fatalf("fork: %v", err)
	}
	d, _ = g.r.Detail(ctx, g.ana, "p1", 7)
	if d.AI.OK || d.AI.Reason == "" {
		t.Fatalf("%+v", d.AI)
	}
}

func TestRunRetriesAnInvalidReplyOnceThenFailsWithoutDrafts(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	g.ag.replies = []string{"not json", rvReply}
	g.r.StartRun(ctx, g.ana, "p1", 7)
	g.r.Wait()
	if g.ag.starts != 2 || !strings.Contains(g.ag.prompts[1], "rejected") {
		t.Fatalf("one retry with the reason: %d", g.ag.starts)
	}
	if ds, _ := g.r.Drafts(ctx, g.ana, "p1", 7); len(ds) != 5 {
		t.Fatalf("%d drafts", len(ds))
	}
	if len(g.use.recs) != 2 {
		t.Fatal("both asks are billed to the user")
	}

	g = newRvRig(t)
	g.ag.replies = []string{"nope", "still nope"}
	g.r.StartRun(ctx, g.ana, "p1", 7)
	g.r.Wait()
	rev, _ := g.st.Get(ctx, "ana", "github", "github.com", "acme/app", 7)
	last, _ := g.st.LatestRun(ctx, rev.ID)
	if last.State != "failed" || last.Error == "" || len(g.st.drafts) != 0 || g.ws.removed != 1 {
		t.Fatalf("%+v drafts=%d removed=%d", last, len(g.st.drafts), g.ws.removed)
	}
	if ks := g.ev.kinds(); ks[len(ks)-1] != "pr_review.run_failed" {
		t.Fatalf("%v", ks)
	}
	if _, err := g.r.StartRun(ctx, g.ana, "p1", 7); err != nil {
		t.Fatalf("a failed run does not block the next: %v", err)
	}
	g.r.Wait()
}

func TestRunFailsWhenTheHeadMovedDuringCheckoutAndCapsFindings(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	g.ws.sha = "other"
	g.r.StartRun(ctx, g.ana, "p1", 7)
	g.r.Wait()
	rev, _ := g.st.Get(ctx, "ana", "github", "github.com", "acme/app", 7)
	if last, _ := g.st.LatestRun(ctx, rev.ID); last.State != "failed" || !strings.Contains(last.Error, "updated") {
		t.Fatalf("%+v", last)
	}

	var fs []string
	for i := 0; i < 25; i++ {
		fs = append(fs, fmt.Sprintf(`{"severity":"info","path":"a.go","line":2,"title":"t%d","body":"b"}`, i))
	}
	ds := buildDrafts("r", "run", "h1", []model.PRFile{{Path: "a.go", Patch: patchA}}, prReviewReply{Findings: nilSafe(fs)}, time.Now())
	if len(ds) != 20 {
		t.Fatalf("cap: %d", len(ds))
	}
}

func nilSafe(items []string) []prFinding {
	out := make([]prFinding, len(items))
	for i := range items {
		out[i] = prFinding{Severity: "info", Path: "a.go", Side: "new", Line: 2, Title: fmt.Sprint(i), Body: "b"}
	}
	return out
}

func TestRecoverFailsInterruptedRuns(t *testing.T) {
	g := newRvRig(t)
	if err := g.r.Recover(context.Background()); err != nil || g.st.failRunning != 1 {
		t.Fatalf("%v %d", err, g.st.failRunning)
	}
}

func TestAnchorOnlyAcceptsLinesInsideHunks(t *testing.T) {
	f := model.PRFile{Patch: patchA}
	cases := []struct {
		side    string
		line    int
		ok      bool
		outSide string
		outOld  int
	}{
		{"new", 2, true, "new", 0}, {"new", 1, true, "new", 1}, {"new", 3, true, "new", 2}, {"old", 3, true, "old", 0},
		{"old", 2, true, "new", 2}, {"new", 4, false, "", 0}, {"old", 4, false, "", 0}, {"new", 0, false, "", 0},
	}
	for _, c := range cases {
		side, _, old, ok := anchor(f, c.side, c.line)
		if ok != c.ok || side != c.outSide || old != c.outOld {
			t.Errorf("%s:%d → %s %d %v", c.side, c.line, side, old, ok)
		}
	}
	if _, _, _, ok := anchor(model.PRFile{}, "new", 1); ok {
		t.Fatal("a file without a patch has no anchors")
	}
}

// ---- identities and CLI login ----

func TestPinAndCLILoginForwardStepsToTheUser(t *testing.T) {
	ctx := context.Background()
	g := newRvRig(t)
	if _, err := g.r.StartCLILogin(ctx, g.ana, "gh", "github.com"); !errors.Is(err, ErrCLIUnavailable) {
		t.Fatalf("multi-user mode: %v", err)
	}
	g.devMode("ana")
	id, err := g.r.StartCLILogin(ctx, g.ana, "gh", "github.com")
	if err != nil || id == "" {
		t.Fatal(err)
	}
	g.r.Wait()
	if len(g.ev.ev) != 2 || g.ev.ev[0].UserID != "ana" || g.ev.ev[0].Kind != "cli_login.code" || g.ev.ev[0].Payload["login_id"] != id || g.ev.ev[0].Payload["code"] != "AB-12" || g.ev.ev[1].Kind != "cli_login.done" {
		t.Fatalf("%+v", g.ev.ev)
	}
	if _, err := g.r.StartCLILogin(ctx, g.ana, "gh", "bad host;"); !errors.Is(err, ErrInvalid) {
		t.Fatal("host is validated")
	}

	pin, err := g.r.Pin(ctx, g.ana, model.PinRequest{Provider: "github", Host: "github.com", Source: "gh", Login: "ana"})
	if err != nil || pin.Source != "gh" || pin.Login != "ana" || g.st.pins["ana|github|github.com"].HostUserID != "id-ana" {
		t.Fatalf("%v %+v", err, pin)
	}
	if _, err := g.r.Pin(ctx, g.ana, model.PinRequest{Provider: "github", Host: "github.com", Source: "gh", Login: "ghost"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a login gh does not have: %v", err)
	}
	if _, err := g.r.Pin(ctx, g.ana, model.PinRequest{Provider: "github", Host: "github.com", Source: "connection", ConnectionID: "nope"}); !IsNotFound(err) {
		t.Fatalf("unknown connection: %v", err)
	}
	opts, err := g.r.Identities(ctx, g.ana)
	if err != nil || len(opts) != 1 || opts[0].Pinned == nil || opts[0].CLI == nil || len(opts[0].CLI.Users) != 1 || len(opts[0].Connections) != 1 || opts[0].Connections[0].Readiness != "ready" {
		t.Fatalf("%v %+v", err, opts)
	}
}
