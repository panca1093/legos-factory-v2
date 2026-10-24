package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"legos-factory/backend/internal/model"
)

// GitHub talks to api.github.com or a GitHub Enterprise /api/v3 base URL
// with a personal access token (fine-grained tokens need Pull requests:
// write and Contents: write on the target repository).
type GitHub struct {
	api   api
	token string
}

func NewGitHub(baseURL, token string, hc *http.Client) *GitHub {
	return &GitHub{api: newAPI("github", baseURL, hc, func(r *http.Request) {
		bearer(token)(r)
		r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}), token: token}
}

func (g *GitHub) CheckAuth(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	err := g.api.do(ctx, http.MethodGet, "/user", nil, &u)
	return u.Login, err
}

func (g *GitHub) GitAuth() model.GitAuth {
	return model.GitAuth{Username: "x-access-token", Password: g.token}
}

type ghPull struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// OpenPullRequest returns the open PR for Head→Base, creating it only when
// none exists, so a retried job never opens a duplicate.
func (g *GitHub) OpenPullRequest(ctx context.Context, pr model.PRRequest) (model.PullRequest, error) {
	owner, _, ok := strings.Cut(pr.Repo, "/")
	if !ok {
		return model.PullRequest{}, fmt.Errorf("github repo must be owner/name, got %q", pr.Repo)
	}
	q := url.Values{"state": {"open"}, "head": {owner + ":" + pr.Head}, "base": {pr.Base}}
	var open []ghPull
	if err := g.api.do(ctx, http.MethodGet, "/repos/"+pr.Repo+"/pulls?"+q.Encode(), nil, &open); err != nil {
		return model.PullRequest{}, err
	}
	p := ghPull{}
	if len(open) > 0 {
		p = open[0]
	} else if err := g.api.do(ctx, http.MethodPost, "/repos/"+pr.Repo+"/pulls", map[string]string{
		"title": pr.Title, "head": pr.Head, "base": pr.Base, "body": pr.Body,
	}, &p); err != nil {
		return model.PullRequest{}, err
	}
	return model.PullRequest{Number: p.Number, URL: p.HTMLURL, Ref: fmt.Sprintf("github:%s#%d", pr.Repo, p.Number)}, nil
}
