package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"legos-factory/backend/internal/model"
)

// GitLab talks to gitlab.com/api/v4 or a self-managed /api/v4 base URL with
// a personal/project access token (scope: api).
type GitLab struct {
	api   api
	token string
}

func NewGitLab(baseURL, token string, hc *http.Client) *GitLab {
	return &GitLab{api: newAPI("gitlab", baseURL, hc, func(r *http.Request) {
		r.Header.Set("PRIVATE-TOKEN", token)
	}), token: token}
}

func (g *GitLab) CheckAuth(ctx context.Context) (string, error) {
	var u struct {
		Username string `json:"username"`
	}
	err := g.api.do(ctx, http.MethodGet, "/user", nil, &u)
	return u.Username, err
}

func (g *GitLab) GitAuth() model.GitAuth {
	return model.GitAuth{Username: "oauth2", Password: g.token}
}

type glMR struct {
	IID    int    `json:"iid"`
	WebURL string `json:"web_url"`
}

// OpenPullRequest finds or creates the merge request for Head→Base.
func (g *GitLab) OpenPullRequest(ctx context.Context, pr model.PRRequest) (model.PullRequest, error) {
	project := "/projects/" + url.PathEscape(pr.Repo) + "/merge_requests"
	q := url.Values{"state": {"opened"}, "source_branch": {pr.Head}, "target_branch": {pr.Base}}
	var open []glMR
	if err := g.api.do(ctx, http.MethodGet, project+"?"+q.Encode(), nil, &open); err != nil {
		return model.PullRequest{}, err
	}
	mr := glMR{}
	if len(open) > 0 {
		mr = open[0]
	} else if err := g.api.do(ctx, http.MethodPost, project, map[string]string{
		"source_branch": pr.Head, "target_branch": pr.Base, "title": pr.Title, "description": pr.Body,
	}, &mr); err != nil {
		return model.PullRequest{}, err
	}
	return model.PullRequest{Number: mr.IID, URL: mr.WebURL, Ref: fmt.Sprintf("gitlab:%s!%d", pr.Repo, mr.IID)}, nil
}
