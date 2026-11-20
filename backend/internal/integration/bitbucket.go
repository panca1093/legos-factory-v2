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
	proxy    bool
}

// BitbucketTokenFunc returns a bearer token. stale is the token the proxy just rejected ("" on a first try); the
// func must then return a different token, minting only when it has none newer.
type BitbucketTokenFunc func(ctx context.Context, stale string) (string, error)

// NewBitbucketProxy talks to Bitbucket through the workbench curl_session proxy (baseURL is <proxyBaseUrl>/atlassian-bitbucket).
func NewBitbucketProxy(baseURL string, token BitbucketTokenFunc, hc *http.Client) *Bitbucket {
	if hc == nil {
		hc = defaultHTTP
	}
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	u, _ := url.Parse(baseURL)
	c := *hc
	c.Transport = &proxyTransport{base: base, token: token, host: u.Host}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Bitbucket{api: newAPI("bitbucket", baseURL, &c, func(*http.Request) {}), proxy: true}
}

// proxyTransport signs requests to the proxy host only and retries once with a fresh token on a 401.
// Accept-Encoding identity: the proxy labels plain JSON as gzip, which Go's transparent decoding turns into an empty body.
type proxyTransport struct {
	base  http.RoundTripper
	token BitbucketTokenFunc
	host  string
}

func (t *proxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer req.Body.Close()
	}
	if req.URL.Host != t.host {
		return nil, fmt.Errorf("bitbucket proxy: refusing to send a request to %s", req.URL.Host)
	}
	tok, err := t.token(req.Context(), "")
	if err != nil {
		return nil, err
	}
	resp, err := t.send(req, tok)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || (req.Body != nil && req.GetBody == nil) {
		return resp, err
	}
	resp.Body.Close()
	if tok, err = t.token(req.Context(), tok); err != nil {
		return nil, err
	}
	return t.send(req, tok)
}

func (t *proxyTransport) send(req *http.Request, tok string) (*http.Response, error) {
	r := req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		r.Body = body
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	r.Header.Set("Accept-Encoding", "identity")
	return t.base.RoundTrip(r)
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
	if b.proxy {
		return model.GitAuth{}
	}
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

// Bitbucket Cloud has no merged_on field, so updated_on stands in for the merge/close time.
func (b *Bitbucket) PullRequestStatus(ctx context.Context, ref string) (model.PRStatus, error) {
	repo, id, err := splitPRRef(ref, "bitbucket", "#")
	if err != nil {
		return model.PRStatus{}, err
	}
	var p struct {
		State     string `json:"state"`
		UpdatedOn string `json:"updated_on"`
	}
	if err := b.api.do(ctx, http.MethodGet, fmt.Sprintf("/repositories/%s/pullrequests/%d", repo, id), nil, &p); err != nil {
		return model.PRStatus{}, err
	}
	switch p.State {
	case "MERGED":
		return model.PRStatus{State: model.PRStateMerged, At: parseTime(p.UpdatedOn)}, nil
	case "DECLINED", "SUPERSEDED":
		return model.PRStatus{State: model.PRStateClosed, At: parseTime(p.UpdatedOn)}, nil
	}
	return model.PRStatus{State: model.PRStateOpen}, nil
}
