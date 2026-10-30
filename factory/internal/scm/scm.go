// Package scm opens pull requests and observes CI checks on GitHub and
// Bitbucket Cloud. Clients are stateless and never log or echo tokens.
package scm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type PRRequest struct{ Repo, Head, Base, Title, Body string }

type PR struct {
	Number                    int
	URL, Head, HeadSHA, State string
}

// Check.State is one of pending|success|failure.
type Check struct{ Name, State, URL string }

type GitAuth struct{ Username, Token string }

type Provider interface {
	OpenPR(ctx context.Context, req PRRequest) (PR, error)
	FindPR(ctx context.Context, repo, head string) (PR, bool, error)
	Checks(ctx context.Context, repo, sha string) ([]Check, error)
	// PRState returns open|merged|closed.
	PRState(ctx context.Context, repo string, number int) (string, error)
	// Merge merges the PR with method squash|merge|rebase (empty means squash); a refusal wraps ErrMergeRefused.
	Merge(ctx context.Context, repo string, number int, method string) error
	// Mergeable returns clean|conflict|unknown for an open PR.
	Mergeable(ctx context.Context, repo string, number int) (string, error)
	GitAuth() GitAuth
}

// ErrMergeRefused means the SCM declined the merge (branch protection, conflicts, stale head); a human must act.
var ErrMergeRefused = errors.New("merge refused")

func mergeErr(err error, refusals ...int) error {
	var pe *ProviderError
	if errors.As(err, &pe) && slices.Contains(refusals, pe.Status) {
		return fmt.Errorf("%w: %w", ErrMergeRefused, err)
	}
	return err
}

// ProviderError is a non-2xx API response; Status 0 means the API was unreachable.
type ProviderError struct {
	Provider string
	Status   int
	Message  string
}

func (e *ProviderError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("%s API unreachable: %s", e.Provider, e.Message)
	}
	return fmt.Sprintf("%s API returned %d: %s", e.Provider, e.Status, e.Message)
}

func (e *ProviderError) Transient() bool {
	return e.Status == 0 || e.Status == 429 || e.Status >= 500
}

// IsTransient reports whether retrying err may succeed. Non-provider errors are permanent.
func IsTransient(err error) bool {
	var pe *ProviderError
	return errors.As(err, &pe) && pe.Transient()
}

func New(kind, baseURL, username, token string, hc *http.Client) (Provider, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	base := strings.TrimRight(baseURL, "/")
	switch kind {
	case "github":
		if base == "" {
			base = "https://api.github.com"
		}
		return &github{api{"github", base, hc, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		}}, token}, nil
	case "bitbucket":
		if base == "" {
			base = "https://api.bitbucket.org/2.0"
		}
		return &bitbucket{api{"bitbucket", base, hc, func(r *http.Request) { r.SetBasicAuth(username, token) }}, username, token}, nil
	}
	return nil, fmt.Errorf("unsupported scm provider %q", kind)
}

type api struct {
	provider string
	base     string
	hc       *http.Client
	auth     func(*http.Request)
}

func (a api) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	a.auth(req)
	resp, err := a.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return &ProviderError{Provider: a.provider, Message: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &ProviderError{Provider: a.provider, Status: resp.StatusCode, Message: errorMessage(raw)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s API returned unexpected JSON: %w", a.provider, err)
	}
	return nil
}

func errorMessage(raw []byte) string {
	var e struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil {
		if e.Message != "" {
			return e.Message
		}
		if e.Error.Message != "" {
			return e.Error.Message
		}
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

type github struct {
	api   api
	token string
}

func (g *github) GitAuth() GitAuth { return GitAuth{Username: "x-access-token", Token: g.token} }

type ghPull struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	// Mergeable is null while GitHub is still computing it.
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

func (p ghPull) pr() PR {
	return PR{Number: p.Number, URL: p.HTMLURL, Head: p.Head.Ref, HeadSHA: p.Head.SHA, State: p.state()}
}

func (p ghPull) state() string {
	switch {
	case p.Merged:
		return "merged"
	case p.State == "open":
		return "open"
	}
	return "closed"
}

func (g *github) FindPR(ctx context.Context, repo, head string) (PR, bool, error) {
	owner, _, ok := strings.Cut(repo, "/")
	if !ok {
		return PR{}, false, fmt.Errorf("github repo must be owner/name, got %q", repo)
	}
	q := url.Values{"state": {"open"}, "head": {owner + ":" + head}}
	var open []ghPull
	if err := g.api.do(ctx, http.MethodGet, "/repos/"+repo+"/pulls?"+q.Encode(), nil, &open); err != nil {
		return PR{}, false, err
	}
	if len(open) == 0 {
		return PR{}, false, nil
	}
	return open[0].pr(), true, nil
}

func (g *github) OpenPR(ctx context.Context, req PRRequest) (PR, error) {
	var p ghPull
	err := g.api.do(ctx, http.MethodPost, "/repos/"+req.Repo+"/pulls", map[string]string{
		"title": req.Title, "head": req.Head, "base": req.Base, "body": req.Body,
	}, &p)
	if err == nil {
		return p.pr(), nil
	}
	var pe *ProviderError
	if errors.As(err, &pe) && pe.Status == http.StatusUnprocessableEntity {
		if found, ok, ferr := g.FindPR(ctx, req.Repo, req.Head); ferr == nil && ok {
			return found, nil
		}
	}
	return PR{}, err
}

func (g *github) PRState(ctx context.Context, repo string, number int) (string, error) {
	var p ghPull
	if err := g.api.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), nil, &p); err != nil {
		return "", err
	}
	return p.state(), nil
}

func (g *github) Merge(ctx context.Context, repo string, number int, method string) error {
	if method == "" {
		method = "squash"
	}
	err := g.api.do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/pulls/%d/merge", repo, number), map[string]string{"merge_method": method}, nil)
	return mergeErr(err, http.StatusForbidden, http.StatusMethodNotAllowed, http.StatusConflict)
}

func (g *github) Mergeable(ctx context.Context, repo string, number int) (string, error) {
	var p ghPull
	if err := g.api.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), nil, &p); err != nil {
		return "", err
	}
	switch {
	case p.MergeableState == "dirty" || (p.Mergeable != nil && !*p.Mergeable):
		return "conflict", nil
	case p.Mergeable != nil && *p.Mergeable:
		return "clean", nil
	}
	return "unknown", nil
}

func (g *github) Checks(ctx context.Context, repo, sha string) ([]Check, error) {
	var runs struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"check_runs"`
	}
	if err := g.api.do(ctx, http.MethodGet, "/repos/"+repo+"/commits/"+sha+"/check-runs?per_page=100", nil, &runs); err != nil {
		return nil, err
	}
	var st struct {
		Statuses []struct {
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := g.api.do(ctx, http.MethodGet, "/repos/"+repo+"/commits/"+sha+"/status", nil, &st); err != nil {
		return nil, err
	}
	var out []Check
	for _, r := range runs.CheckRuns {
		out = append(out, Check{Name: r.Name, State: ghRunState(r.Status, r.Conclusion), URL: r.HTMLURL})
	}
	for _, s := range st.Statuses {
		out = append(out, Check{Name: s.Context, State: ghStatusState(s.State), URL: s.TargetURL})
	}
	return out, nil
}

func ghRunState(status, conclusion string) string {
	if status != "completed" {
		return "pending"
	}
	switch conclusion {
	case "success", "neutral", "skipped":
		return "success"
	}
	return "failure"
}

func ghStatusState(s string) string {
	switch s {
	case "success":
		return "success"
	case "failure", "error":
		return "failure"
	}
	return "pending"
}

type bitbucket struct {
	api             api
	username, token string
}

// TODO: uncertain — Atlassian API tokens may require a different git username than the REST username, verify against a real workspace before prod
func (b *bitbucket) GitAuth() GitAuth { return GitAuth{Username: b.username, Token: b.token} }

type bbPR struct {
	ID    int    `json:"id"`
	State string `json:"state"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
	Source struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
		Commit struct {
			Hash string `json:"hash"`
		} `json:"commit"`
	} `json:"source"`
}

func (p bbPR) pr() PR {
	return PR{Number: p.ID, URL: p.Links.HTML.Href, Head: p.Source.Branch.Name, HeadSHA: p.Source.Commit.Hash, State: bbPRState(p.State)}
}

func bbPRState(s string) string {
	switch s {
	case "OPEN":
		return "open"
	case "MERGED":
		return "merged"
	}
	return "closed"
}

func (b *bitbucket) FindPR(ctx context.Context, repo, head string) (PR, bool, error) {
	q := url.Values{"state": {"OPEN"}, "q": {fmt.Sprintf(`source.branch.name="%s"`, head)}}
	var open struct {
		Values []bbPR `json:"values"`
	}
	if err := b.api.do(ctx, http.MethodGet, "/repositories/"+repo+"/pullrequests?"+q.Encode(), nil, &open); err != nil {
		return PR{}, false, err
	}
	if len(open.Values) == 0 {
		return PR{}, false, nil
	}
	return open.Values[0].pr(), true, nil
}

func (b *bitbucket) OpenPR(ctx context.Context, req PRRequest) (PR, error) {
	if p, ok, err := b.FindPR(ctx, req.Repo, req.Head); err != nil || ok {
		return p, err
	}
	var p bbPR
	err := b.api.do(ctx, http.MethodPost, "/repositories/"+req.Repo+"/pullrequests", map[string]any{
		"title": req.Title, "description": req.Body,
		"source":      map[string]any{"branch": map[string]string{"name": req.Head}},
		"destination": map[string]any{"branch": map[string]string{"name": req.Base}},
	}, &p)
	if err != nil {
		return PR{}, err
	}
	return p.pr(), nil
}

func (b *bitbucket) PRState(ctx context.Context, repo string, number int) (string, error) {
	var p bbPR
	if err := b.api.do(ctx, http.MethodGet, fmt.Sprintf("/repositories/%s/pullrequests/%d", repo, number), nil, &p); err != nil {
		return "", err
	}
	return bbPRState(p.State), nil
}

var bbStrategies = map[string]string{"squash": "squash", "merge": "merge_commit", "rebase": "fast_forward"}

func (b *bitbucket) Merge(ctx context.Context, repo string, number int, method string) error {
	if method == "" {
		method = "squash"
	}
	strategy, ok := bbStrategies[method]
	if !ok {
		return fmt.Errorf("unsupported merge method %q", method)
	}
	err := b.api.do(ctx, http.MethodPost, fmt.Sprintf("/repositories/%s/pullrequests/%d/merge", repo, number), map[string]string{"merge_strategy": strategy}, nil)
	return mergeErr(err, http.StatusBadRequest, http.StatusForbidden, http.StatusMethodNotAllowed, http.StatusConflict)
}

// TODO: uncertain — Bitbucket has no mergeability flag; this reads the "merge conflict" diffstat status, verify against a real workspace before prod
func (b *bitbucket) Mergeable(ctx context.Context, repo string, number int) (string, error) {
	var res struct {
		Values []struct {
			Status string `json:"status"`
		} `json:"values"`
	}
	if err := b.api.do(ctx, http.MethodGet, fmt.Sprintf("/repositories/%s/pullrequests/%d/diffstat?pagelen=100", repo, number), nil, &res); err != nil {
		return "", err
	}
	if len(res.Values) == 0 {
		return "unknown", nil
	}
	for _, v := range res.Values {
		if v.Status == "merge conflict" {
			return "conflict", nil
		}
	}
	return "clean", nil
}

func (b *bitbucket) Checks(ctx context.Context, repo, sha string) ([]Check, error) {
	var res struct {
		Values []struct {
			Name  string `json:"name"`
			Key   string `json:"key"`
			State string `json:"state"`
			URL   string `json:"url"`
		} `json:"values"`
	}
	if err := b.api.do(ctx, http.MethodGet, "/repositories/"+repo+"/commit/"+sha+"/statuses?pagelen=100", nil, &res); err != nil {
		return nil, err
	}
	var out []Check
	for _, v := range res.Values {
		name := v.Name
		if name == "" {
			name = v.Key
		}
		out = append(out, Check{Name: name, State: bbCheckState(v.State), URL: v.URL})
	}
	return out, nil
}

func bbCheckState(s string) string {
	switch s {
	case "SUCCESSFUL":
		return "success"
	case "FAILED", "STOPPED":
		return "failure"
	}
	return "pending"
}
