package integration

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

// Nothing here may start a real gh: every runner is a fake.

func authRunner(out, errOut string, err error, calls *[]string) Runner {
	return func(_ context.Context, name string, args ...string) (string, string, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		return out, errOut, err
	}
}

func TestGHAuthStatusParsesAccounts(t *testing.T) {
	var calls []string
	multi := "github.com\n  ✓ Logged in to github.com account octocat (keyring)\n  - Active account: true\n  ✓ Logged in to github.com account hubot (keyring)\n  - Active account: false\n"
	users, err := NewGHAuth(authRunner(multi, "", nil, &calls), nil).Status(context.Background(), "github.com")
	if err != nil || len(users) != 2 || users[0] != "octocat" || users[1] != "hubot" {
		t.Fatalf("%v %v", users, err)
	}
	if calls[0] != "gh auth status --hostname github.com" {
		t.Fatalf("%v", calls)
	}
	old := "github.com\n  ✓ Logged in to github.com as octocat (oauth_token)\n"
	users, _ = NewGHAuth(authRunner("", old, nil, &calls), nil).Status(context.Background(), "github.com")
	if len(users) != 1 || users[0] != "octocat" {
		t.Fatalf("stderr + old wording: %v", users)
	}
}

func TestGHAuthStatusNotLoggedInAndMissing(t *testing.T) {
	var calls []string
	exit1 := errors.New("exit status 1")
	users, err := NewGHAuth(authRunner("", "You are not logged into any GitHub hosts.", exit1, &calls), nil).Status(context.Background(), "ghe.acme.io")
	if err != nil || users == nil || len(users) != 0 {
		t.Fatalf("not logged in is an empty list: %v %v", users, err)
	}
	_, err = NewGHAuth(authRunner("", "", exec.ErrNotFound, &calls), nil).Status(context.Background(), "github.com")
	if !errors.Is(err, model.ErrGHMissing) {
		t.Fatalf("%v", err)
	}
	if _, err = NewGHAuth(authRunner("", "", nil, &calls), nil).Status(context.Background(), "--evil"); err == nil {
		t.Fatal("flag-like host accepted")
	}
}

func TestGHAuthToken(t *testing.T) {
	var calls []string
	a := NewGHAuth(authRunner("gho_secrettoken\n", "", nil, &calls), nil)
	tok, err := a.Token(context.Background(), "github.com", "octocat")
	if err != nil || tok != "gho_secrettoken" || calls[0] != "gh auth token --hostname github.com --user octocat" {
		t.Fatalf("%q %v %v", tok, err, calls)
	}
	bad := NewGHAuth(authRunner("", "no oauth token found for github.com account nobody", errors.New("exit status 1"), &calls), nil)
	_, err = bad.Token(context.Background(), "github.com", "nobody")
	if !errors.Is(err, model.ErrGHUnauthenticated) {
		t.Fatalf("%v", err)
	}
	if _, err = a.Token(context.Background(), "github.com", "-x"); err == nil {
		t.Fatal("flag-like user accepted")
	}
	_, err = NewGHAuth(authRunner("tok-in-err", "", errors.New("boom tok-in-err"), &calls), nil).Token(context.Background(), "github.com", "u")
	if err == nil || strings.Contains(err.Error(), "tok-in-err") {
		t.Fatalf("errors must not echo output: %v", err)
	}
}

type fakeStart struct {
	output string
	block  bool // keep the stream open until the context ends
	exit   error
	args   []string
}

func (f *fakeStart) run(ctx context.Context, name string, args ...string) (io.ReadCloser, func() error, error) {
	f.args = append([]string{name}, args...)
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		io.WriteString(pw, f.output)
		if f.block {
			<-ctx.Done()
		}
		pw.Close()
		close(done)
	}()
	return pr, func() error { <-done; return f.exit }, nil
}

func collect(t *testing.T, ch <-chan model.CLILoginStep) []model.CLILoginStep {
	t.Helper()
	var out []model.CLILoginStep
	timeout := time.After(5 * time.Second)
	for {
		select {
		case s, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, s)
		case <-timeout:
			t.Fatalf("login stream did not finish: %+v", out)
		}
	}
}

func TestGHLoginEmitsCodeUrlDone(t *testing.T) {
	f := &fakeStart{output: "! First copy your one-time code: ABCD-1234\nPress Enter to open github.com in your browser... \n" +
		"! Failed opening a web browser at https://github.com/login/device\n  Please try entering the URL in your browser manually\n" +
		"✓ Authentication complete.\n- gh config set -h github.com git_protocol https\n✓ Configured git protocol\n✓ Logged in as octocat\n"}
	ch, err := NewGHAuth(nil, f.run).StartLogin(context.Background(), "github.com")
	if err != nil {
		t.Fatal(err)
	}
	steps := collect(t, ch)
	if len(steps) != 3 || steps[0].Kind != "code" || steps[0].Code != "ABCD-1234" || steps[1].Kind != "url" || steps[1].URL != "https://github.com/login/device" || steps[2].Kind != "done" || steps[2].Login != "octocat" {
		t.Fatalf("%+v", steps)
	}
	if strings.Join(f.args, " ") != "gh auth login --hostname github.com --web --git-protocol https" {
		t.Fatalf("%v", f.args)
	}
}

func TestGHLoginFailure(t *testing.T) {
	f := &fakeStart{output: "! First copy your one-time code: WXYZ-9876\nOpen this URL to continue: https://github.com/login/device\nfailed to authenticate via web browser: expired_token\n", exit: errors.New("exit status 1")}
	ch, _ := NewGHAuth(nil, f.run).StartLogin(context.Background(), "github.com")
	steps := collect(t, ch)
	last := steps[len(steps)-1]
	if last.Kind != "failed" || !strings.Contains(last.Message, "expired_token") {
		t.Fatalf("%+v", steps)
	}
}

func TestGHLoginFallsBackWhenNothingParses(t *testing.T) {
	f := &fakeStart{output: "something unexpected\n", block: true}
	a := NewGHAuth(nil, f.run)
	a.fallbackAfter = 30 * time.Millisecond
	ch, _ := a.StartLogin(context.Background(), "ghe.acme.io")
	steps := collect(t, ch)
	if len(steps) != 1 || steps[0].Kind != "fallback" || steps[0].Message != "gh auth login --hostname ghe.acme.io --web --git-protocol https" {
		t.Fatalf("%+v", steps)
	}
}

func TestGHLoginNoFallbackOnceCodeSeen(t *testing.T) {
	f := &fakeStart{output: "! First copy your one-time code: ABCD-1234\n", block: true}
	a := NewGHAuth(nil, f.run)
	a.fallbackAfter = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := a.StartLogin(ctx, "github.com")
	time.Sleep(80 * time.Millisecond)
	cancel()
	for _, s := range collect(t, ch) {
		if s.Kind == "fallback" {
			t.Fatal("fallback after the code was shown")
		}
	}
}

func TestGHLoginMissingBinary(t *testing.T) {
	start := func(context.Context, string, ...string) (io.ReadCloser, func() error, error) {
		return nil, nil, exec.ErrNotFound
	}
	if _, err := NewGHAuth(nil, start).StartLogin(context.Background(), "github.com"); !errors.Is(err, model.ErrGHMissing) {
		t.Fatalf("%v", err)
	}
	if _, err := NewGHAuth(nil, start).StartLogin(context.Background(), "-h"); err == nil {
		t.Fatal("flag-like host accepted")
	}
}
