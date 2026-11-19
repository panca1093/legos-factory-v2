package usecase

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
)

var (
	hostName  = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	loginName = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)
)

type hostKey struct{ provider, host string }

// reviewHosts are the (provider, host) pairs the user can review on: their own git connections, the hosts
// of the projects they see, and github.com when the gh login may be borrowed.
func (r *Reviews) reviewHosts(ctx context.Context, u *model.User, conns []model.Connection) []hostKey {
	seen := map[hostKey]bool{}
	var out []hostKey
	add := func(k hostKey) {
		if k.provider != "" && k.host != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, c := range conns {
		if c.OwnerID == u.ID && c.IsGit() {
			add(hostKey{c.Provider, connectionHost(c.BaseURL)})
		}
	}
	if projects, err := r.Projects.List(ctx, u); err == nil {
		for i := range projects {
			host, _ := parseRemote(r.Integrations.origin(ctx, &projects[i]))
			add(hostKey{providerForHost(host), host})
		}
	}
	if r.cliAllowed() {
		add(hostKey{model.ProviderGitHub, "github.com"})
	}
	slices.SortFunc(out, func(a, b hostKey) int { return strings.Compare(a.provider+a.host, b.provider+b.host) })
	return out
}

// Identities lists, per git host, the accounts the user could review with and the one pinned.
func (r *Reviews) Identities(ctx context.Context, u *model.User) ([]model.IdentityOptions, error) {
	conns, err := r.Integrations.Store.Visible(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	out := []model.IdentityOptions{}
	for _, k := range r.reviewHosts(ctx, u, conns) {
		opt := model.IdentityOptions{Provider: k.provider, Host: k.host, Connections: []model.ConnectionChoice{}}
		if pin, err := r.Store.Identity(ctx, u.ID, k.provider, k.host); err != nil && !missing(err) {
			return nil, err
		} else {
			opt.Pinned = pin
		}
		for i := range conns {
			c := &conns[i]
			if c.OwnerID != u.ID || c.Provider != k.provider || connectionHost(c.BaseURL) != k.host || c.Status != model.ConnectionActive {
				continue
			}
			ch := model.ConnectionChoice{ID: c.ID, Name: c.Name, Login: c.Account, Readiness: model.ReadinessUnknown}
			if token, err := r.Integrations.token(ctx, c); err == nil {
				if rv, err := r.reviewer(c.Provider, c.BaseURL, c.Username, token); err == nil {
					rc := &reviewCtx{userID: u.ID, provider: c.Provider, host: k.host, source: model.IdentitySourceConnection, connectionID: c.ID, rv: rv, key: "conn|" + c.ID}
					info, ierr := r.tokenInfo(ctx, rc)
					ch.Readiness, ch.ExpiresAt = r.readiness(info, ierr)
				}
			}
			opt.Connections = append(opt.Connections, ch)
		}
		if r.cliAllowed() && k.provider == model.ProviderGitHub {
			cli := &model.CLIStatus{Tool: "gh", Installed: true, Users: []string{}}
			users, err := r.CLI.Status(ctx, k.host)
			switch {
			case errors.Is(err, model.ErrGHMissing):
				cli.Installed = false
			case err == nil:
				cli.Users = users
			}
			opt.CLI = cli
		}
		out = append(out, opt)
	}
	return out, nil
}

// Pin chooses the account that reviews for the user on a host; the credential must belong to the login.
func (r *Reviews) Pin(ctx context.Context, u *model.User, req model.PinRequest) (*model.ReviewIdentity, error) {
	if !hostName.MatchString(req.Host) {
		return nil, fmt.Errorf("%w: host is not a valid host name", ErrInvalid)
	}
	if req.Provider != model.ProviderGitHub && req.Provider != model.ProviderGitLab && req.Provider != model.ProviderBitbucket {
		return nil, fmt.Errorf("%w: unknown provider %q", ErrInvalid, req.Provider)
	}
	rc := &reviewCtx{userID: u.ID, provider: req.Provider, host: strings.ToLower(req.Host), source: req.Source}
	switch req.Source {
	case model.IdentitySourceConnection:
		conns, err := r.Integrations.Store.Visible(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		var c *model.Connection
		for i := range conns {
			if conns[i].ID == req.ConnectionID && conns[i].OwnerID == u.ID && conns[i].Provider == req.Provider && connectionHost(conns[i].BaseURL) == rc.host {
				c = &conns[i]
			}
		}
		if c == nil {
			return nil, model.ErrConnectionNotFound
		}
		if c.Status != model.ConnectionActive {
			return nil, model.ErrConnectionDisabled
		}
		if err := r.bind(ctx, rc, req.Source, c.ID, "", conns); err != nil {
			return nil, err
		}
	case model.IdentitySourceGH:
		if !r.cliAllowed() {
			return nil, ErrCLIUnavailable
		}
		if req.Provider != model.ProviderGitHub || !loginName.MatchString(req.Login) {
			return nil, fmt.Errorf("%w: gh serves GitHub accounts only and needs a login", ErrInvalid)
		}
		users, err := r.CLI.Status(ctx, rc.host)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(users, req.Login) {
			return nil, fmt.Errorf("%w: gh has no login %q on %s", ErrInvalid, req.Login, rc.host)
		}
		if err := r.bind(ctx, rc, req.Source, "", req.Login, nil); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: source must be connection or gh", ErrInvalid)
	}
	me, err := r.me(ctx, rc, true)
	if err != nil {
		return nil, err
	}
	if req.Source == model.IdentitySourceGH && !strings.EqualFold(me.Login, req.Login) {
		return nil, fmt.Errorf("%w: gh's token for %s belongs to %s", ErrIdentityChanged, req.Login, me.Login)
	}
	id := model.ReviewIdentity{UserID: u.ID, Provider: rc.provider, Host: rc.host, Source: rc.source, ConnectionID: rc.connectionID,
		Login: me.Login, HostUserID: me.ID, PinnedAt: r.now()}
	if err := r.Store.PinIdentity(ctx, id); err != nil {
		return nil, err
	}
	return &id, nil
}

// StartCLILogin begins `gh auth login` for the host and forwards its steps to the user's live stream.
func (r *Reviews) StartCLILogin(ctx context.Context, u *model.User, tool, host string) (string, error) {
	if !r.cliAllowed() {
		return "", ErrCLIUnavailable
	}
	if tool != "gh" {
		return "", fmt.Errorf("%w: only gh login is supported", ErrInvalid)
	}
	if !hostName.MatchString(host) {
		return "", fmt.Errorf("%w: host is not a valid host name", ErrInvalid)
	}
	lctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	steps, err := r.CLI.StartLogin(lctx, host)
	if err != nil {
		cancel()
		return "", err
	}
	id := fmt.Sprintf("login_%d", r.now().UnixNano())
	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		defer cancel()
		for s := range steps {
			r.emit(u.ID, "cli_login."+s.Kind, map[string]any{"login_id": id, "kind": s.Kind, "code": s.Code, "url": s.URL, "login": s.Login, "message": s.Message})
		}
	}()
	return id, nil
}
