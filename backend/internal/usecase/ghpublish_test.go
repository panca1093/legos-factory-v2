package usecase

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/sandbox"
	"legos-factory/backend/internal/store"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

type recordingGH struct {
	GitProvider
	auth model.GitAuth
	req  model.PRRequest
	fail error
}

func (g *recordingGH) CheckAuth(context.Context) (string, error) { return "octo", nil }
func (g *recordingGH) GitAuth() model.GitAuth                    { return g.auth }
func (g *recordingGH) OpenPullRequest(_ context.Context, r model.PRRequest) (model.PullRequest, error) {
	g.req = r
	if g.fail != nil {
		return model.PullRequest{}, g.fail
	}
	return model.PullRequest{Number: 3, URL: "https://github.com/acme/greeter/pull/3", Ref: "github:acme/greeter#3"}, nil
}

type openPRRig struct {
	f      *Factory
	wos    *store.WorkOrderRepo
	bare   string
	folder string
	dir    string
}

type personalConns struct {
	ConnectionStore
	conns []model.Connection
}

func (c personalConns) Visible(_ context.Context, userID string) ([]model.Connection, error) {
	var out []model.Connection
	for _, x := range c.conns {
		if x.OwnerID == userID || x.Scope == "shared" {
			out = append(out, x)
		}
	}
	return out, nil
}
func (personalConns) Secret(context.Context, string) ([]byte, error) { return []byte("sealed"), nil }

type plainSealer struct{}

func (plainSealer) Seal(b []byte) ([]byte, error) { return b, nil }
func (plainSealer) Open(b []byte) ([]byte, error) { return []byte("tok"), nil }

// newOpenPRRig is a project folder whose origin is origin (routed to a local bare repo for pushes), with
// a ready work order u1 created whose worktree holds one commit on legos/wo_1.
func newOpenPRRig(t *testing.T, origin string, integ func(gh GitProvider) *Integrations, gh GitProvider) *openPRRig {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	bare := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "-q", "--bare", "-b", "main", bare)
	folder := filepath.Join(root, "folder")
	runGit(t, root, "init", "-q", "-b", "main", folder)
	runGit(t, folder, "config", "user.email", "t@t")
	runGit(t, folder, "config", "user.name", "t")
	runGit(t, folder, "commit", "-q", "--allow-empty", "-m", "base")

	db, err := store.Open(filepath.Join(root, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := (&store.UserRepo{DB: db}).Create(ctx, model.User{ID: "u1", Email: "u1@t", Name: "U", Role: model.RoleEngineer}); err != nil {
		t.Fatal(err)
	}
	projs := &store.ProjectRepo{DB: db}
	if err := projs.Create(ctx, &model.Project{ID: "p1", Name: "p1", RepoURL: folder}); err != nil {
		t.Fatal(err)
	}
	wos := &store.WorkOrderRepo{DB: db}
	if err := wos.Create(ctx, &model.WorkOrder{ID: "wo_1", ProjectID: "p1", Title: "t", Objective: "o", State: model.StateReady, CreatedBy: "u1", BaseBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	box := sandbox.Local{DataDir: root}
	dir, _, err := box.Prepare(ctx, "p1", "wo_1", folder, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := box.Commit(ctx, dir, "work"); err != nil {
		t.Fatal(err)
	}
	if origin != "" { // added after Prepare so no test touches the network; pushes are routed to the local bare repo
		runGit(t, folder, "remote", "add", "origin", origin)
		runGit(t, folder, "config", "url.file://"+bare+".pushInsteadOf", origin)
		runGit(t, folder, "push", "-q", "file://"+bare, "main")
	}
	f := &Factory{WO: wos, Proj: projs, Sandbox: box, DataDir: root, Bus: nopBus{}, Log: slog.Default(), Integrations: integ(gh)}
	return &openPRRig{f, wos, bare, folder, dir}
}

func ghIntegrations(gh GitProvider) *Integrations {
	return &Integrations{Host: sandbox.Host{}, GH: func(string, string) GitProvider { return gh }}
}

func TestOpenPRInGHModePushesTheBranchToOriginAndTargetsTheBase(t *testing.T) {
	ctx := context.Background()
	gh := &recordingGH{}
	r := newOpenPRRig(t, "https://github.com/acme/greeter.git", ghIntegrations, gh)
	gh.fail = errors.New("github: 502")
	if err := r.f.handleOpenPR(ctx, model.Job{WOID: "wo_1"}); err == nil {
		t.Fatal("a failed PR open must fail the job")
	}
	if wo, _ := r.wos.Get(ctx, "wo_1"); wo.PROpenFailedAt == nil || wo.PRURL != "" {
		t.Fatalf("a failed open must mark pr_open_failed_at: %+v", wo)
	}
	gh.fail = nil
	if err := r.f.handleOpenPR(ctx, model.Job{WOID: "wo_1"}); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, r.bare, "log", "-1", "--format=%s", "legos/wo_1"); got != "work" {
		t.Fatalf("remote branch tip = %q", got)
	}
	if gh.req.Base != "main" || gh.req.Head != "legos/wo_1" || gh.req.Repo != "acme/greeter" {
		t.Fatalf("PR request = %+v", gh.req)
	}
	wo, _ := r.wos.Get(ctx, "wo_1")
	if wo.PRRef != "github:acme/greeter#3" || wo.PROpenFailedAt != nil {
		t.Fatalf("pr_ref = %q, a later open must clear pr_open_failed_at: %v", wo.PRRef, wo.PROpenFailedAt)
	}
}

func TestOpenPROnAnotherHostUsesTheCreatorsPersonalConnection(t *testing.T) {
	ctx := context.Background()
	prov := &recordingGH{auth: model.GitAuth{Username: "oauth2", Password: "tok"}}
	var used model.Connection
	conn := func(owner, scope string) model.Connection {
		return model.Connection{ID: "c_" + owner, Provider: model.ProviderGitLab, BaseURL: "https://gitlab.example.com/api/v4", OwnerID: owner, Scope: scope, Status: model.ConnectionActive}
	}
	integ := func(GitProvider) *Integrations {
		return &Integrations{
			Host: sandbox.Host{}, Secrets: plainSealer{},
			Store: personalConns{conns: []model.Connection{conn("someone-else", model.ScopePersonal), conn("u1", model.ScopePersonal)}},
			Git:   func(c model.Connection, _ string) (GitProvider, error) { used = c; return prov, nil },
		}
	}
	r := newOpenPRRig(t, "git@gitlab.example.com:team/sub/app.git", integ, prov)
	if err := r.f.handleOpenPR(ctx, model.Job{WOID: "wo_1"}); err != nil {
		t.Fatal(err)
	}
	if used.OwnerID != "u1" {
		t.Fatalf("the connection used belongs to %q, want the work order's creator", used.OwnerID)
	}
	if prov.req.Repo != "team/sub/app" || prov.req.Base != "main" || prov.req.Head != "legos/wo_1" {
		t.Fatalf("PR request = %+v", prov.req)
	}
	if got := runGit(t, r.bare, "log", "-1", "--format=%s", "legos/wo_1"); got != "work" {
		t.Fatalf("remote branch tip = %q", got)
	}
}

func TestOpenPRFailsVisiblyWithoutAConnectionOrARemote(t *testing.T) {
	ctx := context.Background()
	prov := &recordingGH{}
	for name, tc := range map[string]struct {
		origin string
		integ  func(GitProvider) *Integrations
		want   string
	}{
		"no connection for the host": {"https://gitlab.example.com/a/b.git", func(GitProvider) *Integrations {
			return &Integrations{Host: sandbox.Host{}, Secrets: plainSealer{}, Store: personalConns{conns: []model.Connection{{ID: "c1", Provider: model.ProviderGitLab, BaseURL: "https://gitlab.com/api/v4", OwnerID: "u1", Scope: model.ScopePersonal, Status: model.ConnectionActive}}}}
		}, "gitlab.example.com"},
		"someone else's connection only": {"https://gitlab.com/a/b.git", func(GitProvider) *Integrations {
			return &Integrations{Host: sandbox.Host{}, Secrets: plainSealer{}, Store: personalConns{conns: []model.Connection{{ID: "c1", Provider: model.ProviderGitLab, BaseURL: "https://gitlab.com/api/v4", OwnerID: "u2", Scope: model.ScopePersonal, Status: model.ConnectionActive}}}}
		}, "gitlab.com"},
		"no origin remote": {"", ghIntegrations, "no origin remote"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newOpenPRRig(t, tc.origin, tc.integ, prov)
			err := r.f.handleOpenPR(ctx, model.Job{WOID: "wo_1"})
			if err == nil || !errors.Is(err, model.ErrPermanent) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a permanent failure naming %q", err, tc.want)
			}
			if wo, _ := r.wos.Get(ctx, "wo_1"); wo.PROpenFailedAt == nil {
				t.Fatal("the failure must be visible on the work order")
			}
			evs, _ := r.wos.Events(ctx, "wo_1", 0, 50)
			found := false
			for _, e := range evs {
				found = found || e.Kind == "pr_open_failed"
			}
			if !found {
				t.Fatal("a pr_open_failed event must explain why")
			}
			if _, statErr := os.Stat(r.dir); statErr != nil || runGit(t, r.folder, "branch", "--list", "legos/wo_1") == "" {
				t.Fatal("the worktree and local branch must be kept")
			}
		})
	}
}

type countingGH struct {
	GitProvider
	checks *atomic.Int32
}

func (g countingGH) CheckAuth(context.Context) (string, error) { g.checks.Add(1); return "octo", nil }

type countingHost struct {
	realGit
	origins *atomic.Int32
}

func (h *countingHost) OriginURL(context.Context, string) string {
	h.origins.Add(1)
	return "https://github.com/acme/greeter.git"
}

func TestGitForAndAnnotateAreCached(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "r")
	gitInit(t, repo)
	var checks, origins atomic.Int32
	in := &Integrations{
		Host: &countingHost{origins: &origins},
		GH:   func(string, string) GitProvider { return countingGH{checks: &checks} },
	}
	p := &model.Project{RepoURL: repo}
	for i := 0; i < 3; i++ {
		in.Annotate(ctx, p)
		if _, _, bound, err := in.GitFor(ctx, p, "u1"); !bound || err != nil {
			t.Fatalf("bound=%v err=%v", bound, err)
		}
	}
	if checks.Load() != 1 || origins.Load() != 1 {
		t.Fatalf("gh auth checks=%d origin lookups=%d, want 1 each", checks.Load(), origins.Load())
	}
}

func TestCancelPickClosesChooserAndAnswersCancelled(t *testing.T) {
	h := &blockingHost{entered: make(chan struct{})}
	l := &Local{Host: h}
	l.CancelPick() // nothing in flight: no-op
	done := make(chan error)
	go func() { _, err := l.PickFolder(context.Background()); done <- err }()
	<-h.entered
	l.CancelPick()
	l.CancelPick()
	if err := <-done; !errors.Is(err, model.ErrCancelled) {
		t.Fatalf("err=%v, want ErrCancelled", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-h.entered; cancel() }()
	if _, err := l.PickFolder(ctx); errors.Is(err, model.ErrBusy) || errors.Is(err, model.ErrCancelled) {
		t.Fatalf("a later pick must start clean, err=%v", err)
	}
}

func TestGHStatusSeparatesNetworkFromLoggedOut(t *testing.T) {
	l := &Local{GH: func(string, string) GitProvider { return fakeGH{err: errors.Join(model.ErrGHUnreachable)} }}
	installed, authed, _, problem := l.GHStatus(context.Background())
	if !installed || authed || problem == "" {
		t.Fatalf("installed=%v authed=%v problem=%q", installed, authed, problem)
	}
}
