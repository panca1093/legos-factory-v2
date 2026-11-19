package usecase

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

// Integrations manages provider connections and their use. Rules:
//   - shared connections are created and managed by admins and are the only
//     ones a project can bind for PRs;
//   - personal connections are visible to and usable by their owner only;
//   - credentials are sealed at rest and never returned.
type Integrations struct {
	Store    ConnectionStore
	Secrets  Sealer // nil disables credential storage
	Projects *Projects
	Git      func(c model.Connection, token string) (GitProvider, error)
	Issues   func(c model.Connection, token string) (IssueTracker, error)
	// Host and GH enable publishing local projects through the user's own gh
	// login; both are set in dev auth mode only.
	Host LocalHost
	GH   func(repo, remoteURL string) GitProvider

	origins sync.Map // project dir -> originInfo, keyed to .git/config's mtime
	authOK  sync.Map // repo -> time of the last successful gh auth check
}

type originInfo struct {
	mod    time.Time
	origin string
}

const ghAuthTTL = 5 * time.Minute

// warmer is implemented by providers that cache per-repo facts (default branch).
type warmer interface {
	Warm(ctx context.Context) error
}

var remoteRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)

func (in *Integrations) List(ctx context.Context, u *model.User) ([]model.Connection, error) {
	return in.Store.Visible(ctx, u.ID)
}

// ProjectRef names a project that uses a connection.
type ProjectRef struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
}

// ConnectionUse is a connection with the projects bound to it that the caller may see.
type ConnectionUse struct {
	model.Connection
	UsedBy []ProjectRef `json:"used_by"`
}

// ProviderGroup is one provider's accounts; Group is "code" for git hosts and "work" for issue trackers.
type ProviderGroup struct {
	ID       string          `json:"id"`
	Group    string          `json:"group"`
	Accounts []ConnectionUse `json:"accounts"`
}

var providerOrder = []struct{ id, group string }{
	{model.ProviderGitHub, "code"}, {model.ProviderGitLab, "code"}, {model.ProviderBitbucket, "code"}, {model.ProviderJira, "work"},
}

// Grouped lists the caller's connections per provider, every provider present even with no accounts.
func (in *Integrations) Grouped(ctx context.Context, u *model.User) ([]ProviderGroup, error) {
	conns, err := in.List(ctx, u)
	if err != nil {
		return nil, err
	}
	used := map[string][]ProjectRef{}
	if in.Projects != nil {
		projects, err := in.Projects.List(ctx, u)
		if err != nil {
			return nil, err
		}
		for _, p := range projects {
			if p.GitConnection != "" {
				used[p.GitConnection] = append(used[p.GitConnection], ProjectRef{ProjectID: p.ID, Name: p.Name})
			}
		}
	}
	out := make([]ProviderGroup, len(providerOrder))
	idx := map[string]int{}
	for i, p := range providerOrder {
		out[i] = ProviderGroup{ID: p.id, Group: p.group, Accounts: []ConnectionUse{}}
		idx[p.id] = i
	}
	for _, c := range conns {
		i, ok := idx[c.Provider]
		if !ok {
			continue
		}
		refs := used[c.ID]
		if refs == nil {
			refs = []ProjectRef{}
		}
		out[i].Accounts = append(out[i].Accounts, ConnectionUse{Connection: c, UsedBy: refs})
	}
	return out, nil
}

func (in *Integrations) Create(ctx context.Context, u *model.User, req model.CreateConnectionRequest) (*model.Connection, error) {
	if in.Secrets == nil {
		return nil, model.ErrSecretsUnavailable
	}
	if req.Scope == model.ScopeShared && !u.Role.IsAdmin() {
		return nil, ErrForbidden
	}
	sealed, err := in.Secrets.Seal([]byte(req.Token))
	if err != nil {
		return nil, err
	}
	c := &model.Connection{Provider: req.Provider, Name: req.Name, Scope: req.Scope, OwnerID: u.ID,
		BaseURL: req.BaseURL, Username: req.Username}
	if err := in.Store.Create(ctx, c, sealed); err != nil {
		return nil, err
	}
	return c, nil
}

func (in *Integrations) Update(ctx context.Context, u *model.User, id string, req model.UpdateConnectionRequest) (*model.Connection, error) {
	if _, err := in.manageable(ctx, u, id); err != nil {
		return nil, err
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len(name) > 100 {
			return nil, fmt.Errorf("%w: name is required (max 100 characters)", ErrInvalid)
		}
		if err := in.Store.Rename(ctx, id, name); err != nil {
			return nil, err
		}
	}
	if req.Status != nil {
		if *req.Status != model.ConnectionActive && *req.Status != model.ConnectionDisabled {
			return nil, fmt.Errorf("%w: status must be active or disabled", ErrInvalid)
		}
		if err := in.Store.SetStatus(ctx, id, *req.Status); err != nil {
			return nil, err
		}
	}
	if req.Token != nil {
		if in.Secrets == nil {
			return nil, model.ErrSecretsUnavailable
		}
		token := strings.TrimSpace(*req.Token)
		if token == "" {
			return nil, fmt.Errorf("%w: token must not be empty", ErrInvalid)
		}
		sealed, err := in.Secrets.Seal([]byte(token))
		if err != nil {
			return nil, err
		}
		if err := in.Store.RotateSecret(ctx, id, sealed); err != nil {
			return nil, err
		}
	}
	return in.Store.Get(ctx, id)
}

// Delete revokes the connection; projects bound to it become unbound.
func (in *Integrations) Delete(ctx context.Context, u *model.User, id string) error {
	if _, err := in.manageable(ctx, u, id); err != nil {
		return err
	}
	return in.Store.Delete(ctx, id)
}

// Test calls the provider with the stored credential and records the result;
// a connection is only "verified" after this succeeds.
func (in *Integrations) Test(ctx context.Context, u *model.User, id string) (*model.Connection, error) {
	c, err := in.manageable(ctx, u, id)
	if err != nil {
		return nil, err
	}
	token, err := in.token(ctx, c)
	if err != nil {
		return nil, err
	}
	var account string
	if c.IsGit() {
		var g GitProvider
		if g, err = in.Git(*c, token); err == nil {
			account, err = g.CheckAuth(ctx)
		}
	} else {
		var it IssueTracker
		if it, err = in.Issues(*c, token); err == nil {
			account, err = it.CheckAuth(ctx)
		}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if rerr := in.Store.RecordCheck(ctx, id, account, msg); rerr != nil {
		return nil, rerr
	}
	return in.Store.Get(ctx, id)
}

// Bind points a project at a shared git connection and provider repo slug;
// an empty connection id unbinds it.
func (in *Integrations) Bind(ctx context.Context, u *model.User, projectID string, req model.ProjectBindingRequest) (*model.Project, error) {
	if err := in.Projects.Authorize(ctx, u, projectID); err != nil {
		return nil, err
	}
	req.RemoteRepo = strings.Trim(strings.TrimSpace(req.RemoteRepo), "/")
	if req.GitConnectionID != "" {
		c, err := in.Store.Get(ctx, req.GitConnectionID)
		if err != nil {
			return nil, err
		}
		switch {
		case c.Scope != model.ScopeShared:
			return nil, fmt.Errorf("%w: only shared connections can be bound to a project", ErrInvalid)
		case !c.IsGit():
			return nil, fmt.Errorf("%w: %s is not a git provider", ErrInvalid, c.Provider)
		case !remoteRepoPattern.MatchString(req.RemoteRepo):
			return nil, fmt.Errorf("%w: remote_repo must look like owner/repo", ErrInvalid)
		}
	} else {
		req.RemoteRepo = ""
	}
	if err := in.Projects.Store.SetBinding(ctx, projectID, req.GitConnectionID, req.RemoteRepo); err != nil {
		return nil, err
	}
	return in.Projects.Store.Get(ctx, projectID)
}

// LookupIssue reads an issue through a visible Jira connection and returns a
// work-order draft. Jira is never modified.
func (in *Integrations) LookupIssue(ctx context.Context, u *model.User, req model.IssueLookupRequest) (*model.IssueDraft, error) {
	c, err := in.usable(ctx, u, req.ConnectionID)
	if err != nil {
		return nil, err
	}
	if c.Provider != model.ProviderJira {
		return nil, fmt.Errorf("%w: connection is not a Jira connection", ErrInvalid)
	}
	req.Key = strings.ToUpper(strings.TrimSpace(req.Key))
	if !model.IssueKeyPattern.MatchString(req.Key) {
		return nil, fmt.Errorf("%w: issue key must look like ABC-123", ErrInvalid)
	}
	token, err := in.token(ctx, c)
	if err != nil {
		return nil, err
	}
	it, err := in.Issues(*c, token)
	if err != nil {
		return nil, err
	}
	issue, err := it.GetIssue(ctx, req.Key)
	if err != nil {
		return nil, err
	}
	objective, acceptance := splitAcceptance(issue.Description)
	if objective == "" {
		objective = issue.Title
	}
	return &model.IssueDraft{SourceRef: "jira:" + issue.Key, Title: issue.Title, Objective: objective,
		Acceptance: acceptance, URL: issue.URL}, nil
}

// origin returns the project folder's origin URL, "" when it is not a git repo or has no origin.
func (in *Integrations) origin(ctx context.Context, p *model.Project) string {
	if in == nil || in.Host == nil {
		return ""
	}
	dir := expandPath(p.RepoURL)
	if !filepath.IsAbs(dir) {
		return ""
	}
	var mod time.Time
	if fi, err := os.Stat(filepath.Join(dir, ".git", "config")); err == nil {
		mod = fi.ModTime()
		if v, ok := in.origins.Load(dir); ok && v.(originInfo).mod.Equal(mod) {
			return v.(originInfo).origin
		}
	}
	if !in.Host.IsGitRepo(dir) {
		return ""
	}
	origin := in.Host.OriginURL(ctx, dir)
	if !mod.IsZero() {
		in.origins.Store(dir, originInfo{mod: mod, origin: origin})
	}
	return origin
}

var (
	urlRemote = regexp.MustCompile(`^(?:https?|ssh|git)://(?:[^@/]+@)?([^/:]+)(?::\d+)?/(.+)$`)
	scpRemote = regexp.MustCompile(`^(?:[^@/\s]+@)?([^:/\s]+):(.+)$`)
)

// parseRemote splits a git remote URL (https, ssh or scp-style) into its host and "owner/repo" path.
func parseRemote(origin string) (host, repo string) {
	origin = strings.TrimSpace(origin)
	m := urlRemote.FindStringSubmatch(origin)
	if m == nil {
		m = scpRemote.FindStringSubmatch(origin)
	}
	if m == nil {
		return "", ""
	}
	repo = strings.Trim(strings.TrimSuffix(strings.Trim(m[2], "/"), ".git"), "/")
	return strings.ToLower(m[1]), repo
}

// connectionHost is the git host a connection's API base URL serves (api.github.com serves github.com).
func connectionHost(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "api.")
}

// Annotate fills the computed publish_via and github_repo fields: gh for a GitHub origin, remote
// for any other origin (a personal connection serves it), none without an origin.
func (in *Integrations) Annotate(ctx context.Context, p *model.Project) {
	p.PublishVia, p.GitHubRepo = "none", ""
	origin := in.origin(ctx, p)
	if origin == "" {
		return
	}
	p.PublishVia = "remote"
	if repo := ParseGitHubRepo(origin); repo != "" && in.GH != nil {
		p.PublishVia, p.GitHubRepo = "gh", repo
	}
}

// GitFor returns the provider and repo slug for the project's origin. A GitHub origin goes
// through the user's gh login; any other host through userID's own active connection whose base
// URL names that host. ok is false when the folder has no origin; a missing connection or login is err.
func (in *Integrations) GitFor(ctx context.Context, p *model.Project, userID string) (g GitProvider, repo string, ok bool, err error) {
	origin := in.origin(ctx, p)
	if origin == "" {
		return nil, "", false, nil
	}
	host, repo := parseRemote(origin)
	if host == "" || repo == "" {
		return nil, "", true, fmt.Errorf("origin %q is not a remote URL legos-factory can open pull requests on", origin)
	}
	if gh := ParseGitHubRepo(origin); gh != "" && in.GH != nil {
		g = in.GH(gh, origin)
		if at, ok := in.authOK.Load(gh); !ok || time.Since(at.(time.Time)) > ghAuthTTL {
			if _, err := g.CheckAuth(ctx); err != nil {
				return nil, gh, true, err
			}
			in.authOK.Store(gh, time.Now())
		}
		if w, ok := g.(warmer); ok {
			if err := w.Warm(ctx); err != nil {
				return nil, gh, true, fmt.Errorf("%w: %v", model.ErrGHUnreachable, err)
			}
		}
		return g, gh, true, nil
	}
	conns, err := in.Store.Visible(ctx, userID)
	if err != nil {
		return nil, repo, true, err
	}
	for i := range conns {
		c := &conns[i]
		if c.OwnerID != userID || !c.IsGit() || c.Status != model.ConnectionActive || connectionHost(c.BaseURL) != host {
			continue
		}
		token, err := in.token(ctx, c)
		if err != nil {
			return nil, repo, true, err
		}
		g, err = in.Git(*c, token)
		return g, repo, true, err
	}
	return nil, repo, true, fmt.Errorf("no active personal connection for %s: add a GitHub, GitLab or Bitbucket connection for that host in Settings, then retry", host)
}

// GitAuth is the auth the read-only checkouts use: nil unless the user's own gh login serves the
// folder, since a local folder clones without one.
func (in *Integrations) GitAuth(ctx context.Context, p *model.Project) (*model.GitAuth, error) {
	if in == nil {
		return nil, nil
	}
	g, _, ok, err := in.GitFor(ctx, p, "")
	if !ok || err != nil || g == nil {
		return nil, nil
	}
	a := g.GitAuth()
	return &a, nil
}

func (in *Integrations) token(ctx context.Context, c *model.Connection) (string, error) {
	if in.Secrets == nil {
		return "", model.ErrSecretsUnavailable
	}
	sealed, err := in.Store.Secret(ctx, c.ID)
	if err != nil {
		return "", err
	}
	plain, err := in.Secrets.Open(sealed)
	return string(plain), err
}

// usable returns c when u may use it: any shared connection, or u's own.
func (in *Integrations) usable(ctx context.Context, u *model.User, id string) (*model.Connection, error) {
	c, err := in.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Scope == model.ScopePersonal && c.OwnerID != u.ID {
		return nil, model.ErrConnectionNotFound
	}
	if c.Status != model.ConnectionActive {
		return nil, model.ErrConnectionDisabled
	}
	return c, nil
}

// manageable returns c when u may change it: admins for shared connections,
// the owner for personal ones. Others' personal connections stay invisible.
func (in *Integrations) manageable(ctx context.Context, u *model.User, id string) (*model.Connection, error) {
	c, err := in.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	switch {
	case c.Scope == model.ScopePersonal && c.OwnerID != u.ID:
		return nil, model.ErrConnectionNotFound
	case c.Scope == model.ScopeShared && !u.Role.IsAdmin():
		return nil, ErrForbidden
	}
	return c, nil
}

var acceptanceHeading = regexp.MustCompile(`(?im)^\s*(acceptance criteria|acceptance)\s*:?\s*$`)

// splitAcceptance separates an "Acceptance criteria" section, when present,
// from the rest of an issue description.
func splitAcceptance(desc string) (objective, acceptance string) {
	loc := acceptanceHeading.FindStringIndex(desc)
	if loc == nil {
		return strings.TrimSpace(desc), ""
	}
	return strings.TrimSpace(desc[:loc[0]]), strings.TrimSpace(desc[loc[1]:])
}

var (
	ErrForbidden = errors.New("not allowed for this role")
	ErrInvalid   = errors.New("invalid request")
)
