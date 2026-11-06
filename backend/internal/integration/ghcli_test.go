package integration

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

type fakeGH struct {
	replies map[string]string // command prefix -> stdout
	fail    map[string]error
	stderr  string
	calls   []string
}

func (f *fakeGH) run(_ context.Context, name string, args ...string) (string, string, error) {
	cmd := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	for prefix, err := range f.fail {
		if strings.HasPrefix(cmd, prefix) {
			return "", f.stderr, err
		}
	}
	for prefix, out := range f.replies {
		if strings.HasPrefix(cmd, prefix) {
			return out, "", nil
		}
	}
	return "", "", nil
}

func (f *fakeGH) called(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestGHOpenPullRequestFindsExisting(t *testing.T) {
	f := &fakeGH{replies: map[string]string{"gh pr list": `[{"number":9,"url":"https://github.com/acme/greeter/pull/9"}]`}}
	pr, err := NewGHCLI("acme/greeter", "", f.run).OpenPullRequest(context.Background(), model.PRRequest{Head: "b", Base: "main", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 9 || pr.Ref != "github:acme/greeter#9" || f.called("gh pr create") != 0 {
		t.Fatalf("pr=%+v calls=%v", pr, f.calls)
	}
}

func TestGHOpenPullRequestCreatesWhenNone(t *testing.T) {
	f := &fakeGH{replies: map[string]string{
		"gh pr list":   `[]`,
		"gh pr create": "Creating pull request\nhttps://github.com/acme/greeter/pull/12\n",
	}}
	pr, err := NewGHCLI("acme/greeter", "", f.run).OpenPullRequest(context.Background(), model.PRRequest{Head: "b", Base: "main", Title: "t", Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 12 || pr.URL != "https://github.com/acme/greeter/pull/12" || pr.Ref != "github:acme/greeter#12" {
		t.Fatalf("pr=%+v", pr)
	}
	if f.called("gh pr create --repo acme/greeter --head b --base main") != 1 {
		t.Fatalf("calls=%v", f.calls)
	}
}

func TestGHPullRequestStatus(t *testing.T) {
	at := time.Date(2026, 11, 6, 3, 4, 5, 0, time.UTC)
	ts := at.Format(time.RFC3339)
	for _, tc := range []struct {
		body  string
		state string
		at    time.Time
	}{
		{`{"state":"OPEN","mergedAt":null,"closedAt":null}`, model.PRStateOpen, time.Time{}},
		{`{"state":"MERGED","mergedAt":"` + ts + `","closedAt":"` + ts + `"}`, model.PRStateMerged, at},
		{`{"state":"CLOSED","mergedAt":null,"closedAt":"` + ts + `"}`, model.PRStateClosed, at},
	} {
		f := &fakeGH{replies: map[string]string{"gh pr view 12 --repo acme/greeter": tc.body}}
		st, err := NewGHCLI("acme/greeter", "", f.run).PullRequestStatus(context.Background(), "github:acme/greeter#12")
		if err != nil || st.State != tc.state || !st.At.Equal(tc.at) {
			t.Fatalf("%s: st=%+v err=%v", tc.state, st, err)
		}
	}
}

func TestGHStatusRejectsForeignRef(t *testing.T) {
	_, err := NewGHCLI("a/b", "", (&fakeGH{}).run).PullRequestStatus(context.Background(), "gitlab:a/b!1")
	if !errors.Is(err, model.ErrPermanent) {
		t.Fatalf("err=%v", err)
	}
}

func TestGHCheckAuth(t *testing.T) {
	ctx := context.Background()
	acct, err := NewGHCLI("", "", (&fakeGH{replies: map[string]string{"gh api user": "octo\n"}}).run).CheckAuth(ctx)
	if err != nil || acct != "octo" {
		t.Fatalf("acct=%q err=%v", acct, err)
	}
	_, err = NewGHCLI("", "", (&fakeGH{fail: map[string]error{"gh api user": errors.New("exit 1")}, stderr: "To get started with GitHub CLI, please run:  gh auth login"}).run).CheckAuth(ctx)
	if !errors.Is(err, model.ErrGHUnauthenticated) {
		t.Fatalf("err=%v", err)
	}
	_, err = NewGHCLI("", "", (&fakeGH{fail: map[string]error{"gh api user": errors.New("exit 1")}, stderr: "error connecting to api.github.com"}).run).CheckAuth(ctx)
	if !errors.Is(err, model.ErrGHUnreachable) || errors.Is(err, model.ErrGHUnauthenticated) {
		t.Fatalf("network failure must not read as logged out: %v", err)
	}
	_, err = NewGHCLI("", "", (&fakeGH{fail: map[string]error{"gh": exec.ErrNotFound}}).run).CheckAuth(ctx)
	if !errors.Is(err, model.ErrGHMissing) {
		t.Fatalf("err=%v", err)
	}
}

func TestGHGitAuthCarriesRemoteAndDefaultBranch(t *testing.T) {
	f := &fakeGH{replies: map[string]string{"gh repo view acme/trunky": `{"defaultBranchRef":{"name":"trunk"}}`}}
	g := NewGHCLI("acme/trunky", "git@github.com:acme/trunky.git", f.run)
	if a := g.GitAuth(); a.DefaultBranch != "" || len(f.calls) != 0 {
		t.Fatalf("GitAuth must not shell out before Warm: %+v %v", a, f.calls)
	}
	if err := g.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = g.Warm(context.Background())
	a := g.GitAuth()
	if a.RemoteURL != "git@github.com:acme/trunky.git" || a.DefaultBranch != "trunk" || a.Password != "" || f.called("gh repo view") != 1 || len(f.calls) != 1 {
		t.Fatalf("auth=%+v calls=%v", a, f.calls)
	}
}
