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
}

func (g *recordingGH) CheckAuth(context.Context) (string, error) { return "octo", nil }
func (g *recordingGH) GitAuth() model.GitAuth                    { return g.auth }
func (g *recordingGH) OpenPullRequest(_ context.Context, r model.PRRequest) (model.PullRequest, error) {
	g.req = r
	return model.PullRequest{Number: 3, URL: "https://github.com/acme/greeter/pull/3", Ref: "github:acme/greeter#3"}, nil
}

// A workspace prepared while gh was unavailable still has the user's folder as
// origin; once gh works, the push must go to the remote and never into the folder.
func TestOpenPRInGHModeNeverPushesIntoTheUsersFolder(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	bare := filepath.Join(root, "github.git")
	runGit(t, root, "init", "-q", "--bare", bare)
	folder := filepath.Join(root, "folder")
	runGit(t, root, "init", "-q", "-b", "main", folder)
	runGit(t, folder, "commit", "-q", "--allow-empty", "-m", "base")
	runGit(t, folder, "remote", "add", "origin", "https://github.com/acme/greeter.git")
	runGit(t, folder, "push", "-q", "file://"+bare, "main")

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
	if err := wos.Create(ctx, &model.WorkOrder{ID: "wo_1", ProjectID: "p1", Title: "t", Objective: "o", State: model.StateReady, CreatedBy: "u1"}); err != nil {
		t.Fatal(err)
	}

	box := sandbox.Local{DataDir: root}
	dir, err := box.Prepare(ctx, "wo_1", folder, nil) // gh was missing at creation: origin stays the folder
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := box.Commit(ctx, dir, "work"); err != nil {
		t.Fatal(err)
	}

	gh := &recordingGH{auth: model.GitAuth{RemoteURL: "file://" + bare, DefaultBranch: "main"}}
	f := &Factory{WO: wos, Proj: projs, Sandbox: box, DataDir: root, Bus: nopBus{}, Log: slog.Default(),
		Integrations: &Integrations{Host: sandbox.Host{}, GH: func(string, string) GitProvider { return gh }}}
	if err := f.handleOpenPR(ctx, model.Job{WOID: "wo_1"}); err != nil {
		t.Fatal(err)
	}

	if got := runGit(t, bare, "log", "-1", "--format=%s", "legos-factory/wo_1"); got != "work" {
		t.Fatalf("remote branch tip = %q", got)
	}
	if out := runGit(t, folder, "branch", "--list", "legos-factory/*"); out != "" {
		t.Fatalf("the user's folder received a branch: %q", out)
	}
	if gh.req.Base != "main" || gh.req.Head != "legos-factory/wo_1" {
		t.Fatalf("PR request = %+v", gh.req)
	}
	wo, _ := wos.Get(ctx, "wo_1")
	if wo.PRRef != "github:acme/greeter#3" {
		t.Fatalf("pr_ref = %q", wo.PRRef)
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
		if _, bound, err := in.GitFor(ctx, p); !bound || err != nil {
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
