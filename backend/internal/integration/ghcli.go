package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

// Runner executes a command and returns its stdout and stderr.
type Runner func(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)

// ExecRunner runs real processes.
func ExecRunner(ctx context.Context, name string, args ...string) (string, string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// GHCLI publishes through the user's own gh login for one GitHub repository,
// so no connection or stored token is involved.
type GHCLI struct {
	Repo   string // owner/name
	Remote string // the folder's own origin URL (https or ssh), used for fetch and push
	run    Runner
}

// NewGHCLI returns a provider for repo; a nil run executes the real gh binary.
func NewGHCLI(repo, remote string, run Runner) *GHCLI {
	if run == nil {
		run = ExecRunner
	}
	return &GHCLI{Repo: repo, Remote: remote, run: run}
}

const branchTTL = 10 * time.Minute

type branchEntry struct {
	name string
	exp  time.Time
}

// branches caches default branches per repo so polling never shells out for them.
var branches sync.Map

// Warm resolves and caches the repo's default branch for GitAuth.
func (g *GHCLI) Warm(ctx context.Context) error {
	if e, ok := branches.Load(g.Repo); ok && time.Now().Before(e.(branchEntry).exp) {
		return nil
	}
	b, err := g.DefaultBranch(ctx)
	if err != nil {
		return err
	}
	if b == "" {
		return fmt.Errorf("gh could not report the default branch of %s", g.Repo)
	}
	branches.Store(g.Repo, branchEntry{b, time.Now().Add(branchTTL)})
	return nil
}

func (g *GHCLI) gh(ctx context.Context, args ...string) (string, error) {
	out, errOut, err := g.run(ctx, "gh", args...)
	if err == nil {
		return out, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "", model.ErrGHMissing
	}
	msg := strings.TrimSpace(errOut)
	if msg == "" {
		msg = err.Error()
	}
	status := 0
	if strings.Contains(msg, "HTTP 404") || strings.Contains(msg, "Could not resolve") {
		status = 404
	}
	return "", &model.ProviderError{Provider: "gh", Status: status, Message: msg}
}

func (g *GHCLI) CheckAuth(ctx context.Context) (string, error) {
	out, err := g.gh(ctx, "api", "user", "--jq", ".login")
	var pe *model.ProviderError
	if errors.As(err, &pe) {
		if strings.Contains(pe.Message, "gh auth login") || strings.Contains(pe.Message, "HTTP 401") || strings.Contains(pe.Message, "not logged") {
			return "", fmt.Errorf("%w: %s", model.ErrGHUnauthenticated, pe.Message)
		}
		return "", fmt.Errorf("%w: %s", model.ErrGHUnreachable, pe.Message)
	}
	return strings.TrimSpace(out), err
}

// GitAuth has no credential: git uses the user's configured helper
// (`gh auth setup-git`). It never shells out; the default branch comes from
// the cache Warm fills, and is empty when that has not run.
func (g *GHCLI) GitAuth() model.GitAuth {
	a := model.GitAuth{RemoteURL: g.Remote}
	if e, ok := branches.Load(g.Repo); ok {
		a.DefaultBranch = e.(branchEntry).name
	}
	return a
}

func (g *GHCLI) DefaultBranch(ctx context.Context) (string, error) {
	out, err := g.gh(ctx, "repo", "view", g.Repo, "--json", "defaultBranchRef")
	if err != nil {
		return "", err
	}
	var r struct {
		DefaultBranchRef struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return "", err
	}
	return r.DefaultBranchRef.Name, nil
}

// OpenPullRequest returns the open PR for Head→Base, creating it only when
// none exists, so a retried job never opens a duplicate.
func (g *GHCLI) OpenPullRequest(ctx context.Context, pr model.PRRequest) (model.PullRequest, error) {
	out, err := g.gh(ctx, "pr", "list", "--repo", g.Repo, "--state", "open", "--head", pr.Head, "--base", pr.Base,
		"--json", "number,url", "--limit", "1")
	if err != nil {
		return model.PullRequest{}, err
	}
	var open []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal([]byte(out), &open); err != nil {
		return model.PullRequest{}, fmt.Errorf("gh pr list: %w", err)
	}
	if len(open) > 0 {
		return g.pullRequest(open[0].Number, open[0].URL), nil
	}
	out, err = g.gh(ctx, "pr", "create", "--repo", g.Repo, "--head", pr.Head, "--base", pr.Base, "--title", pr.Title, "--body", pr.Body)
	if err != nil {
		return model.PullRequest{}, err
	}
	url := strings.TrimSpace(out)
	if i := strings.LastIndex(url, "\n"); i >= 0 {
		url = url[i+1:]
	}
	n, err := strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	if err != nil {
		return model.PullRequest{}, fmt.Errorf("gh pr create: unexpected output %q", out)
	}
	return g.pullRequest(n, url), nil
}

func (g *GHCLI) pullRequest(n int, url string) model.PullRequest {
	return model.PullRequest{Number: n, URL: url, Ref: fmt.Sprintf("github:%s#%d", g.Repo, n)}
}

func (g *GHCLI) PullRequestStatus(ctx context.Context, ref string) (model.PRStatus, error) {
	repo, n, err := splitPRRef(ref, "github", "#")
	if err != nil {
		return model.PRStatus{}, err
	}
	out, err := g.gh(ctx, "pr", "view", strconv.Itoa(n), "--repo", repo, "--json", "state,mergedAt,closedAt")
	if err != nil {
		return model.PRStatus{}, err
	}
	var p struct {
		State    string `json:"state"`
		MergedAt string `json:"mergedAt"`
		ClosedAt string `json:"closedAt"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		return model.PRStatus{}, fmt.Errorf("gh pr view: %w", err)
	}
	switch p.State {
	case "MERGED":
		return model.PRStatus{State: model.PRStateMerged, At: parseTime(p.MergedAt)}, nil
	case "CLOSED":
		return model.PRStatus{State: model.PRStateClosed, At: parseTime(p.ClosedAt)}, nil
	}
	return model.PRStatus{State: model.PRStateOpen}, nil
}
