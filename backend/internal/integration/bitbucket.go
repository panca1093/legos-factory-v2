package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"legos-factory/backend/internal/model"
)

// Bitbucket talks to Bitbucket Cloud (api.bitbucket.org/2.0) with basic auth:
// the connection username plus an API token or app password.
type Bitbucket struct {
	api      api
	username string
	token    string
}

func NewBitbucket(baseURL, username, token string, hc *http.Client) *Bitbucket {
	return &Bitbucket{api: newAPI("bitbucket", baseURL, hc, basic(username, token)), username: username, token: token}
}

func (b *Bitbucket) CheckAuth(ctx context.Context) (string, error) {
	var u struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
	}
	if err := b.api.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return "", err
	}
	if u.Username != "" {
		return u.Username, nil
	}
	return u.DisplayName, nil
}

// GitAuth reuses the REST credential for HTTPS push.
// TODO: uncertain — Atlassian API tokens may require a different git username than the REST username, verify against a real workspace before prod
func (b *Bitbucket) GitAuth() model.GitAuth {
	return model.GitAuth{Username: b.username, Password: b.token}
}

type bbPR struct {
	ID    int `json:"id"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

// OpenPullRequest finds or creates the pull request for Head→Base.
func (b *Bitbucket) OpenPullRequest(ctx context.Context, pr model.PRRequest) (model.PullRequest, error) {
	path := "/repositories/" + pr.Repo + "/pullrequests"
	q := url.Values{"state": {"OPEN"}, "q": {fmt.Sprintf(`source.branch.name="%s" AND destination.branch.name="%s"`, pr.Head, pr.Base)}}
	var open struct {
		Values []bbPR `json:"values"`
	}
	if err := b.api.do(ctx, http.MethodGet, path+"?"+q.Encode(), nil, &open); err != nil {
		return model.PullRequest{}, err
	}
	p := bbPR{}
	if len(open.Values) > 0 {
		p = open.Values[0]
	} else if err := b.api.do(ctx, http.MethodPost, path, map[string]any{
		"title": pr.Title, "description": pr.Body,
		"source":      map[string]any{"branch": map[string]string{"name": pr.Head}},
		"destination": map[string]any{"branch": map[string]string{"name": pr.Base}},
	}, &p); err != nil {
		return model.PullRequest{}, err
	}
	return model.PullRequest{Number: p.ID, URL: p.Links.HTML.Href, Ref: fmt.Sprintf("bitbucket:%s#%d", pr.Repo, p.ID)}, nil
}
