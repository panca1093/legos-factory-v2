package usecase

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

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
}

var remoteRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)

func (in *Integrations) List(ctx context.Context, u *model.User) ([]model.Connection, error) {
	return in.Store.Visible(ctx, u.ID)
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

// GitFor returns the provider and auth for a project's bound connection;
// ok is false when the project is not bound.
func (in *Integrations) GitFor(ctx context.Context, p *model.Project) (g GitProvider, ok bool, err error) {
	if in == nil || p.GitConnection == "" {
		return nil, false, nil
	}
	c, err := in.Store.Get(ctx, p.GitConnection)
	if err != nil {
		return nil, true, err
	}
	if c.Status != model.ConnectionActive {
		return nil, true, model.ErrConnectionDisabled
	}
	token, err := in.token(ctx, c)
	if err != nil {
		return nil, true, err
	}
	g, err = in.Git(*c, token)
	return g, true, err
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
