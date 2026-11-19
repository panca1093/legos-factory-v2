package integration

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
)

// StartRunner starts a long-running command and returns its merged
// stdout+stderr and a wait function that reports the exit status.
type StartRunner func(ctx context.Context, name string, args ...string) (output io.ReadCloser, wait func() error, err error)

// ExecStart runs a real process with no TTY and a closed stdin.
func ExecStart(ctx context.Context, name string, args ...string) (io.ReadCloser, func() error, error) {
	pr, pw := io.Pipe()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = pw, pw
	cmd.Stdin = strings.NewReader("\n")
	if err := cmd.Start(); err != nil {
		pw.Close()
		return nil, nil, err
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pw.Close()
		done <- err
	}()
	return pr, func() error { return <-done }, nil
}

// GHAuth borrows tokens from the gh CLI's own login store and drives
// `gh auth login`. Tokens are returned per call and never kept.
type GHAuth struct {
	run           Runner
	start         StartRunner
	fallbackAfter time.Duration
}

const (
	ghLoginFallbackAfter = 10 * time.Second
	ghLoginTimeout       = 15 * time.Minute // a GitHub device code expires after 15 minutes
)

// NewGHAuth returns a CLIAuth for gh; nil runners execute the real binary.
func NewGHAuth(run Runner, start StartRunner) *GHAuth {
	if run == nil {
		run = ExecRunner
	}
	if start == nil {
		start = ExecStart
	}
	return &GHAuth{run: run, start: start, fallbackAfter: ghLoginFallbackAfter}
}

var (
	hostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*(:[0-9]{1,5})?$`)
	userPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	loggedIn    = regexp.MustCompile(`Logged in to \S+ (?:account|as) (\S+)`)
	oneTimeCode = regexp.MustCompile(`\b([A-Z0-9]{4}-[A-Z0-9]{4})\b`)
	urlPattern  = regexp.MustCompile(`https?://[^\s"'<>]+`)
	loginDone   = regexp.MustCompile(`Logged in as (\S+)`)
)

func (a *GHAuth) gh(ctx context.Context, args ...string) (string, string, error) {
	out, errOut, err := a.run(ctx, "gh", args...)
	if errors.Is(err, exec.ErrNotFound) {
		return "", "", model.ErrGHMissing
	}
	return out, errOut, err
}

// Status lists the accounts gh is logged in to on host; none is an empty list, not an error.
func (a *GHAuth) Status(ctx context.Context, host string) ([]string, error) {
	if !hostPattern.MatchString(host) {
		return nil, fmt.Errorf("%w: invalid host %q", model.ErrPermanent, host)
	}
	out, errOut, err := a.gh(ctx, "auth", "status", "--hostname", host)
	if errors.Is(err, model.ErrGHMissing) || ctx.Err() != nil {
		if err == nil {
			err = ctx.Err()
		}
		return nil, err
	}
	users := []string{}
	for _, m := range loggedIn.FindAllStringSubmatch(out+"\n"+errOut, -1) {
		users = append(users, strings.Trim(m[1], "()"))
	}
	return users, nil
}

// Token returns the token for one logged-in account. It never appears in errors.
func (a *GHAuth) Token(ctx context.Context, host, user string) (string, error) {
	if !hostPattern.MatchString(host) || !userPattern.MatchString(user) {
		return "", fmt.Errorf("%w: invalid host or user", model.ErrPermanent)
	}
	out, _, err := a.gh(ctx, "auth", "token", "--hostname", host, "--user", user)
	if errors.Is(err, model.ErrGHMissing) {
		return "", err
	}
	tok := strings.TrimSpace(out)
	if err != nil || tok == "" {
		return "", fmt.Errorf("%w: no gh token for %s on %s", model.ErrGHUnauthenticated, user, host)
	}
	return tok, nil
}

// StartLogin runs `gh auth login --web` without a TTY and reports its
// one-time code and URL as steps, then done or failed. When neither is seen
// within the fallback window it emits a fallback step carrying the command
// the user can run in their own terminal, and stops the process.
func (a *GHAuth) StartLogin(ctx context.Context, host string) (<-chan model.CLILoginStep, error) {
	if !hostPattern.MatchString(host) {
		return nil, fmt.Errorf("%w: invalid host %q", model.ErrPermanent, host)
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, ghLoginTimeout)
	out, wait, err := a.start(ctx, "gh", "auth", "login", "--hostname", host, "--web", "--git-protocol", "https")
	if err != nil {
		cancel()
		if errors.Is(err, exec.ErrNotFound) {
			return nil, model.ErrGHMissing
		}
		return nil, err
	}
	steps := make(chan model.CLILoginStep, 8)
	send := func(s model.CLILoginStep) {
		select {
		case steps <- s:
		case <-parent.Done():
		}
	}
	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				io.Copy(io.Discard, out)
				return
			}
		}
	}()
	go func() {
		defer close(steps)
		defer cancel()
		defer out.Close()
		timer := time.NewTimer(a.fallbackAfter)
		defer timer.Stop()
		var gotCode, gotURL bool
		var login, last string
		for lines != nil {
			select {
			case l, ok := <-lines:
				if !ok {
					lines = nil
					continue
				}
				if t := strings.TrimSpace(l); t != "" {
					last = t
				}
				if m := loginDone.FindStringSubmatch(l); m != nil {
					login = strings.Trim(m[1], ".()")
				}
				if !gotCode && strings.Contains(strings.ToLower(l), "code") {
					if m := oneTimeCode.FindStringSubmatch(l); m != nil {
						gotCode = true
						send(model.CLILoginStep{Kind: "code", Code: m[1]})
					}
				}
				if !gotURL {
					if u := urlPattern.FindString(l); u != "" {
						gotURL = true
						send(model.CLILoginStep{Kind: "url", URL: strings.TrimRight(u, ".,)")})
					}
				}
			case <-timer.C:
				if !gotCode && !gotURL {
					cancel()
					wait()
					send(model.CLILoginStep{Kind: "fallback", Message: fmt.Sprintf("gh auth login --hostname %s --web --git-protocol https", host)})
					return
				}
			case <-ctx.Done():
				wait()
				send(model.CLILoginStep{Kind: "failed", Message: "login cancelled or timed out"})
				return
			}
		}
		if err := wait(); err != nil {
			msg := last
			if msg == "" {
				msg = err.Error()
			}
			send(model.CLILoginStep{Kind: "failed", Message: msg})
			return
		}
		send(model.CLILoginStep{Kind: "done", Login: login})
	}()
	return steps, nil
}
