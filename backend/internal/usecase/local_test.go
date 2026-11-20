package usecase

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"legos-factory/backend/internal/model"
)

type fakeHost struct {
	repos  map[string]string // dir -> origin url
	cloned []string
}

func (h *fakeHost) IsGitRepo(dir string) bool { _, ok := h.repos[dir]; return ok }
func (h *fakeHost) OriginURL(_ context.Context, dir string) string {
	return h.repos[dir]
}
func (h *fakeHost) DefaultBranch(context.Context, string) string { return "main" }
func (h *fakeHost) Clone(_ context.Context, url, target string) error {
	h.cloned = append(h.cloned, url+" -> "+target)
	return os.Mkdir(target, 0o755)
}
func (h *fakeHost) Init(_ context.Context, dir string) error { return os.Mkdir(dir, 0o755) }
func (h *fakeHost) PickFolder(context.Context) (string, error) {
	return "", model.ErrNotSupported
}

type noConns struct{ ConnectionStore }

func (noConns) Get(context.Context, string) (*model.Connection, error) {
	return nil, model.ErrConnectionNotFound
}

type fakeGH struct {
	GitProvider
	err error
}

func (g fakeGH) CheckAuth(context.Context) (string, error) { return "octo", g.err }

func TestParseGitHubRepo(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/greeter.git":       "acme/greeter",
		"https://github.com/acme/greeter":           "acme/greeter",
		"https://github.com/acme/greeter/":          "acme/greeter",
		"http://github.com/acme/my.repo-1.git":      "acme/my.repo-1",
		"https://tok@github.com/acme/greeter.git":   "acme/greeter",
		"git@github.com:acme/greeter.git":           "acme/greeter",
		"git@github.com:acme/greeter":               "acme/greeter",
		"ssh://git@github.com/acme/greeter.git":     "acme/greeter",
		"HTTPS://GitHub.com/Acme/Greeter.git":       "Acme/Greeter",
		"https://gitlab.com/acme/greeter.git":       "",
		"https://github.com.evil.com/acme/greeter":  "",
		"https://github.com/acme":                   "",
		"https://github.com/acme/greeter/tree/main": "",
		"/Users/me/code/greeter":                    "",
		"":                                          "",
	} {
		if got := ParseGitHubRepo(in); got != want {
			t.Errorf("ParseGitHubRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGitForSelection(t *testing.T) {
	ctx := context.Background()
	host := &fakeHost{repos: map[string]string{"/w/gh": "git@github.com:acme/greeter.git", "/w/gl": "https://gitlab.com/a/b.git", "/w/ee": "ssh://git@git.corp.example:2222/team/app.git"}}
	var asked string
	conns := personalConns{conns: []model.Connection{
		{ID: "c1", Provider: model.ProviderGitLab, BaseURL: "https://gitlab.com/api/v4", OwnerID: "u1", Scope: model.ScopePersonal, Status: model.ConnectionActive},
		{ID: "c2", Provider: model.ProviderGitLab, BaseURL: "https://git.corp.example/api/v4", OwnerID: "u1", Scope: model.ScopePersonal, Status: model.ConnectionActive},
	}}
	var picked string
	in := &Integrations{Host: host, GH: func(repo, _ string) GitProvider { asked = repo; return fakeGH{} }, Store: conns, Secrets: plainSealer{},
		Git: func(c model.Connection, _ string) (GitProvider, error) { picked = c.ID; return fakeGH{}, nil }}

	g, repo, bound, err := in.GitFor(ctx, &model.Project{RepoURL: "/w/gh"}, "u1")
	if !bound || err != nil || g == nil || asked != "acme/greeter" || repo != "acme/greeter" {
		t.Fatalf("gh path: bound=%v err=%v asked=%q repo=%q", bound, err, asked, repo)
	}
	for path, want := range map[string][2]string{"/w/gl": {"c1", "a/b"}, "/w/ee": {"c2", "team/app"}} {
		picked = ""
		_, repo, bound, err := in.GitFor(ctx, &model.Project{RepoURL: path}, "u1")
		if !bound || err != nil || picked != want[0] || repo != want[1] {
			t.Fatalf("%s: bound=%v err=%v picked=%q repo=%q", path, bound, err, picked, repo)
		}
	}
	if _, _, bound, err := in.GitFor(ctx, &model.Project{RepoURL: "/w/gl"}, "u2"); !bound || err == nil {
		t.Fatalf("another user's connection must not serve the host: bound=%v err=%v", bound, err)
	}
	for _, p := range []*model.Project{{RepoURL: "/w/none"}, {RepoURL: "https://github.com/a/b"}, {}} {
		if _, _, bound, _ := in.GitFor(ctx, p, "u1"); bound {
			t.Fatalf("%q has no origin and must stay unbound", p.RepoURL)
		}
	}
	var off *Integrations
	if _, _, bound, _ := off.GitFor(ctx, &model.Project{RepoURL: "/w/gh"}, "u1"); bound {
		t.Fatal("nil integrations must be unbound")
	}
	asked = ""
	if _, _, bound, err := (&Integrations{Host: host, Store: conns, Secrets: plainSealer{}}).GitFor(ctx, &model.Project{RepoURL: "/w/gh"}, "u1"); !bound || err == nil || asked != "" {
		t.Fatalf("without a gh constructor a github.com origin needs a personal connection: bound=%v err=%v", bound, err)
	}
}

func TestGitForReportsMissingGH(t *testing.T) {
	host := &fakeHost{repos: map[string]string{"/w/gh": "https://github.com/acme/greeter.git"}}
	in := &Integrations{Host: host, GH: func(string, string) GitProvider { return fakeGH{err: model.ErrGHMissing} }}
	g, _, bound, err := in.GitFor(context.Background(), &model.Project{RepoURL: "/w/gh"}, "u1")
	if !bound || g != nil || !errors.Is(err, model.ErrGHMissing) {
		t.Fatalf("bound=%v g=%v err=%v", bound, g, err)
	}
}

func TestParseRemote(t *testing.T) {
	for in, want := range map[string][2]string{
		"https://github.com/acme/greeter.git":          {"github.com", "acme/greeter"},
		"https://tok@gitlab.example.com/g/sub/app":     {"gitlab.example.com", "g/sub/app"},
		"git@bitbucket.org:team/repo.git":              {"bitbucket.org", "team/repo"},
		"ssh://git@git.corp.example:2222/team/app.git": {"git.corp.example", "team/app"},
		"git@ssh.bitbucket.org:team/repo.git":          {"bitbucket.org", "team/repo"},
		"ssh://git@altssh.bitbucket.org:443/team/repo": {"bitbucket.org", "team/repo"},
		"ssh://git@ssh.github.com:443/acme/greeter":    {"github.com", "acme/greeter"},
		"/Users/me/code/x":                             {"", ""},
	} {
		if h, r := parseRemote(in); h != want[0] || r != want[1] {
			t.Errorf("parseRemote(%q) = %q, %q; want %q", in, h, r, want)
		}
	}
}

func TestAnnotate(t *testing.T) {
	host := &fakeHost{repos: map[string]string{"/w/gh": "https://github.com/acme/greeter.git", "/w/gl": "https://gitlab.com/a/b.git"}}
	in := &Integrations{Host: host, GH: func(string, string) GitProvider { return fakeGH{} }}
	for _, tc := range []struct {
		p    model.Project
		via  string
		repo string
	}{
		{model.Project{RepoURL: "/w/gh"}, "gh", "acme/greeter"},
		{model.Project{RepoURL: "/w/gl"}, "remote", ""},
		{model.Project{RepoURL: "/w/other"}, "none", ""},
	} {
		in.Annotate(context.Background(), &tc.p)
		if tc.p.PublishVia != tc.via || tc.p.GitHubRepo != tc.repo {
			t.Errorf("%+v: via=%q repo=%q", tc.p, tc.p.PublishVia, tc.p.GitHubRepo)
		}
	}
	var off *Integrations
	p := model.Project{}
	off.Annotate(context.Background(), &p)
	if p.PublishVia != "none" {
		t.Fatalf("nil integrations: %q", p.PublishVia)
	}
}

type realGit struct{ fakeHost }

func (realGit) IsGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%s", out)
	}
}

func TestInspect(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "a-repo")
	gitInit(t, repo)
	gitInit(t, filepath.Join(root, "b-repo"))
	if err := os.MkdirAll(filepath.Join(root, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	host := &realGit{fakeHost{repos: map[string]string{}}}
	l := &Local{Host: host}
	ctx := context.Background()

	r, err := l.Inspect(ctx, repo)
	if err != nil || !r.IsGit || r.Name != "a-repo" || r.Language != "go" || r.Repos == nil || len(r.Repos) != 0 {
		t.Fatalf("repo: %+v err=%v", r, err)
	}

	r, err = l.Inspect(ctx, root)
	if err != nil || r.IsGit || len(r.Repos) != 2 || r.Repos[0].Name != "a-repo" || r.Repos[1].Name != "b-repo" || !r.Repos[0].IsGit {
		t.Fatalf("parent: %+v err=%v", r, err)
	}

	for _, bad := range []string{"relative/dir", "", filepath.Join(root, "missing"), filepath.Join(repo, "go.mod")} {
		if _, err := l.Inspect(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("Inspect(%q) err=%v, want ErrInvalid", bad, err)
		}
	}
}

func TestInspectNonGitHubRemoteAndGoWork(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "mono")
	gitInit(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "go.work"), []byte("go 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := "git@bitbucket.org:acme/mono.git"
	l := &Local{Host: &realGit{fakeHost{repos: map[string]string{repo: origin}}}}

	r, err := l.Inspect(context.Background(), repo)
	if err != nil || r.RemoteURL != origin || r.GitHubRepo != "" || r.Language != "go" {
		t.Fatalf("got %+v err=%v", r, err)
	}
}

func TestInspectExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := (&Local{Host: &fakeHost{}}).Inspect(context.Background(), "~/proj")
	if err != nil || r.Path != filepath.Join(home, "proj") {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestInitAndCloneValidation(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	host := &fakeHost{repos: map[string]string{}}
	l := &Local{Host: host}

	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "-x"} {
		if _, err := l.Init(ctx, name, parent); !errors.Is(err, ErrInvalid) {
			t.Errorf("Init(%q) err=%v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(parent, "taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Init(ctx, "taken", parent); !errors.Is(err, ErrInvalid) {
		t.Errorf("Init existing err=%v", err)
	}
	if _, err := l.Init(ctx, "fresh", "relative"); !errors.Is(err, ErrInvalid) {
		t.Errorf("Init relative parent err=%v", err)
	}
	if _, err := l.Init(ctx, "fresh", parent); err != nil {
		t.Errorf("Init ok err=%v", err)
	}

	for _, url := range []string{"", "--upload-pack=x", "/local/path", "ext::sh -c id", "file:///etc"} {
		if _, err := l.Clone(ctx, url, parent); !errors.Is(err, ErrInvalid) {
			t.Errorf("Clone(%q) err=%v", url, err)
		}
	}
	if _, err := l.Clone(ctx, "https://github.com/acme/taken.git", parent); !errors.Is(err, ErrInvalid) {
		t.Errorf("Clone existing target err=%v", err)
	}
	if _, err := l.Clone(ctx, "git@github.com:acme/greeter.git", parent); err != nil {
		t.Fatalf("Clone ok err=%v", err)
	}
	if want := "git@github.com:acme/greeter.git -> " + filepath.Join(parent, "greeter"); len(host.cloned) != 1 || host.cloned[0] != want {
		t.Fatalf("cloned=%v want %q", host.cloned, want)
	}
}

type blockingHost struct {
	fakeHost
	entered chan struct{}
}

func (h *blockingHost) PickFolder(ctx context.Context) (string, error) {
	h.entered <- struct{}{}
	<-ctx.Done()
	return "", ctx.Err()
}

func (h *blockingHost) Clone(ctx context.Context, _, target string) error {
	_ = os.Mkdir(target, 0o755)
	_ = os.WriteFile(filepath.Join(target, "partial"), nil, 0o644)
	<-ctx.Done()
	return ctx.Err()
}

func TestPickFolderOneAtATimeAndAbortable(t *testing.T) {
	h := &blockingHost{entered: make(chan struct{})}
	l := &Local{Host: h}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { _, err := l.PickFolder(ctx); done <- err }()
	<-h.entered
	if _, err := l.PickFolder(context.Background()); !errors.Is(err, model.ErrBusy) {
		t.Fatalf("second pick err=%v, want ErrBusy", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("aborted pick err=%v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() { <-h.entered; cancel2() }()
	if _, err := l.PickFolder(ctx2); errors.Is(err, model.ErrBusy) {
		t.Fatal("busy flag must be released after the first pick ends")
	}
}

func TestAbortedCloneRemovesPartialTarget(t *testing.T) {
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := &Local{Host: &blockingHost{}}
	if _, err := l.Clone(ctx, "https://github.com/acme/greeter.git", parent); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(filepath.Join(parent, "greeter")); !os.IsNotExist(err) {
		t.Fatalf("partial clone left behind: %v", err)
	}
}

func TestGHStatus(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		gh              func(string, string) GitProvider
		installed, auth bool
		account         string
	}{
		{nil, false, false, ""},
		{func(string, string) GitProvider { return fakeGH{} }, true, true, "octo"},
		{func(string, string) GitProvider { return fakeGH{err: model.ErrGHUnauthenticated} }, true, false, ""},
		{func(string, string) GitProvider { return fakeGH{err: model.ErrGHMissing} }, false, false, ""},
	} {
		i, a, acct, _ := (&Local{GH: tc.gh}).GHStatus(ctx)
		if i != tc.installed || a != tc.auth || (a && acct != tc.account) {
			t.Errorf("got %v %v %q, want %v %v %q", i, a, acct, tc.installed, tc.auth, tc.account)
		}
	}
}
