package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

var ErrWorkbenchUnavailable = errors.New("Bitbucket via workbench is available only in local development mode")

const workbenchHost = "bitbucket.org"

var wbCloneURL = regexp.MustCompile(`https://[^\s"\\]+@bitbucket\.org/[^\s"\\]*`)

// Workbench reaches Bitbucket through the user's workbench MCP sign-in: a short-lived curl_session token
// for the REST proxy, and bitbucket_get_clone_url for git. The workspace has one sign-in per server, so
// this is wired in local development mode only. Tokens live in memory; they are never stored, logged or sent to an agent.
type Workbench struct {
	MCP *MCPServers
	// Call runs one MCP tool call (agent.CallMCPTool).
	Call func(ctx context.Context, s model.MCPServer, name string, args any) (string, error)
	// NewReviewer builds the Bitbucket reviewer for the proxy base URL.
	NewReviewer func(baseURL string, token func(ctx context.Context, force bool) (string, error)) PRReviewer
	Now         func() time.Time

	mu     sync.Mutex
	tokens map[string]wbToken
}

type wbToken struct {
	token, base string
	until       time.Time
}

func (w *Workbench) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Workbench) ready() bool {
	return w != nil && w.MCP != nil && w.Call != nil && w.NewReviewer != nil
}

func (w *Workbench) choices(ctx context.Context) []model.WorkbenchChoice {
	if !w.ready() {
		return nil
	}
	recs, err := w.MCP.WorkbenchServers(ctx)
	if err != nil {
		return nil
	}
	var out []model.WorkbenchChoice
	for _, rec := range recs {
		out = append(out, model.WorkbenchChoice{ID: rec.ID, Name: rec.Name})
	}
	return out
}

func (w *Workbench) candidate(ctx context.Context, id string) error {
	for _, c := range w.choices(ctx) {
		if c.ID == id {
			return nil
		}
	}
	return fmt.Errorf("%q is not a signed-in workbench MCP server", id)
}

func (w *Workbench) server(ctx context.Context, id string) (model.MCPServer, error) {
	if !w.ready() {
		return model.MCPServer{}, ErrWorkbenchUnavailable
	}
	return w.MCP.Wire(ctx, id)
}

// session returns the cached curl_session token for a server, minting one when it is missing, about to expire or force is set.
func (w *Workbench) session(ctx context.Context, id string, force bool) (wbToken, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.tokens[id]; ok && !force && w.now().Before(t.until) {
		return t, nil
	}
	srv, err := w.server(ctx, id)
	if err != nil {
		return wbToken{}, err
	}
	out, err := w.Call(ctx, srv, "curl_session", map[string]any{"integrations": []string{"atlassian-bitbucket"}, "expiresInSeconds": 900})
	if err != nil {
		return wbToken{}, fmt.Errorf("workbench: could not start a Bitbucket session: %w", err)
	}
	var m struct {
		Token        string `json:"token"`
		ExpiresIn    int    `json:"expiresIn"`
		ProxyBaseURL string `json:"proxyBaseUrl"`
	}
	if json.Unmarshal([]byte(out), &m) != nil || m.Token == "" || m.ExpiresIn <= 0 {
		return wbToken{}, errors.New("workbench: curl_session answered with an unexpected shape")
	}
	if base, perr := url.Parse(m.ProxyBaseURL); perr != nil || (base.Scheme != "https" && !(base.Scheme == "http" && isLoopback(base.Hostname()))) {
		return wbToken{}, errors.New("workbench: curl_session returned an unusable proxy URL")
	}
	ttl := time.Duration(m.ExpiresIn)*time.Second - time.Minute
	if ttl <= 0 {
		ttl = time.Duration(m.ExpiresIn) * time.Second / 2
	}
	t := wbToken{token: m.Token, base: strings.TrimRight(m.ProxyBaseURL, "/") + "/atlassian-bitbucket", until: w.now().Add(ttl)}
	if w.tokens == nil {
		w.tokens = map[string]wbToken{}
	}
	w.tokens[id] = t
	return t, nil
}

func isLoopback(host string) bool { return host == "localhost" || host == "127.0.0.1" || host == "::1" }

func (w *Workbench) reviewer(ctx context.Context, id string) (PRReviewer, error) {
	if err := w.candidate(ctx, id); err != nil {
		return nil, err
	}
	t, err := w.session(ctx, id, false)
	if err != nil {
		return nil, err
	}
	return w.NewReviewer(t.base, func(ctx context.Context, force bool) (string, error) {
		t, err := w.session(ctx, id, force)
		return t.token, err
	}), nil
}

// CloneAuth asks bitbucket_get_clone_url for repo ("workspace/slug") and returns the credentials inside the URL, for one git fetch.
func (w *Workbench) CloneAuth(ctx context.Context, id, repo string) (*model.GitAuth, error) {
	ws, slug, ok := strings.Cut(repo, "/")
	if !ok || ws == "" || slug == "" {
		return nil, fmt.Errorf("workbench: %q is not a workspace/repository", repo)
	}
	srv, err := w.server(ctx, id)
	if err != nil {
		return nil, err
	}
	out, err := w.Call(ctx, srv, "execute_tools", map[string]any{"executions": []any{
		map[string]any{"tool": "bitbucket_get_clone_url", "args": map[string]any{"workspace": ws, "repoSlug": slug}},
	}})
	if err != nil {
		return nil, fmt.Errorf("workbench: could not get a clone URL: %w", err)
	}
	var res struct {
		Results []struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		} `json:"results"`
	}
	if json.Unmarshal([]byte(out), &res) != nil || len(res.Results) == 0 {
		return nil, errors.New("workbench: execute_tools answered with an unexpected shape")
	}
	r := res.Results[0]
	if e := strings.TrimSpace(string(r.Error)); e != "" && e != "null" {
		var msg string
		if json.Unmarshal(r.Error, &msg) != nil {
			msg = e
		}
		return nil, fmt.Errorf("workbench: bitbucket_get_clone_url failed: %s", truncateRunes(msg, 200))
	}
	u, err := url.Parse(wbCloneURL.FindString(string(r.Result)))
	if err != nil || u.User == nil {
		return nil, errors.New("workbench: no clone URL with credentials in the answer")
	}
	pw, _ := u.User.Password()
	if pw == "" {
		return nil, errors.New("workbench: no clone URL with credentials in the answer")
	}
	return &model.GitAuth{Username: u.User.Username(), Password: pw}, nil
}
