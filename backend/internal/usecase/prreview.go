package usecase

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

var (
	ErrNoReviewIdentity  = errors.New("no review account: connect a personal account for this git host in Settings")
	ErrIdentityAmbiguous = errors.New("more than one account can review here: pick one in Settings")
	ErrIdentityChanged   = errors.New("the account behind the pinned review identity changed")
	ErrReviewRunning     = errors.New("an AI review is already running for this pull request")
	ErrAIUnavailable     = errors.New("AI review is not available")
	ErrReviewNotFound    = errors.New("review not found")
	ErrCLIUnavailable    = errors.New("CLI login is available only in local development mode")
	ErrReadOnlyToken     = errors.New("this account's token cannot post reviews")
)

// PostConflict is returned when the client's view of the review no longer matches the server's.
type PostConflict struct {
	Reason  string
	Current model.PostExpect
}

func (e *PostConflict) Error() string { return e.Reason }

// NewReviewerFunc builds a PRReviewer for a credential; username is only used by Bitbucket.
type NewReviewerFunc func(provider, baseURL, username, token string) PRReviewer

// ReviewWorkspace gives the AI reviewer a detached checkout of a pull request's head.
type ReviewWorkspace interface {
	PrepareReview(ctx context.Context, projectID string, number int, repoDir, ref string, auth *model.GitAuth) (dir, sha string, err error)
	RemoveReview(ctx context.Context, projectID string, number int, repoDir string) error
}

// ReviewAgent is the harness, model and account an AI review runs on, resolved for one user.
type ReviewAgent struct {
	Harness  Harness
	Agent    string
	Provider string
	Model    string
	Env      []string
	Args     []string
}

// Reviews owns pull-request review: identity resolution, listing, drafts, posting and AI runs.
// Business rules live here; the store and the provider adapters only move data.
type Reviews struct {
	Store        ReviewStore
	Projects     *Projects
	Integrations *Integrations
	WO           WorkOrderStore
	CLI          CLIAuth // nil: no CLI borrowing
	NewReviewer  NewReviewerFunc
	Bus          Publisher
	Usage        Budget
	KB           *KB
	Workspace    ReviewWorkspace
	Agent        func(ctx context.Context, userID string) (ReviewAgent, error)
	Log          *slog.Logger
	Now          func() time.Time
	TTL          time.Duration // list cache lifetime, default 60 s

	mu         sync.Mutex
	lists      map[string]*listEntry
	refreshing map[string]bool
	cache      sync.Map // string → cacheEntry
	running    sync.Map // review id → struct{}
	bg         sync.WaitGroup
}

type cacheEntry struct {
	v  any
	at time.Time
}

const (
	defaultListTTL = 60 * time.Second
	meTTL          = 5 * time.Minute
	infoTTL        = 5 * time.Minute
	railResolve    = 3 * time.Second
)

func (r *Reviews) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reviews) ttl() time.Duration {
	if r.TTL > 0 {
		return r.TTL
	}
	return defaultListTTL
}

func (r *Reviews) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// memo caches fn's result for ttl; failures are never cached.
func (r *Reviews) memo(key string, ttl time.Duration, fresh bool, fn func() (any, error)) (any, error) {
	if v, ok := r.cache.Load(key); ok && !fresh && r.now().Sub(v.(cacheEntry).at) < ttl {
		return v.(cacheEntry).v, nil
	}
	v, err := fn()
	if err != nil {
		return nil, err
	}
	r.cache.Store(key, cacheEntry{v: v, at: r.now()})
	return v, nil
}

// missing reports a store lookup that found no row; the store contract does not pin how it says so.
func missing(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrReviewNotFound) ||
		(err != nil && strings.Contains(strings.ToLower(err.Error()), "not found"))
}

// reviewCtx is one user's resolved access to one project's repository on its host.
type reviewCtx struct {
	project      *model.Project
	userID       string
	provider     string
	host         string
	repo         string
	rv           PRReviewer
	me           model.HostUser
	source       string
	connectionID string
	cliUser      string
	token        string // in memory only; never logged, stored or sent to an agent
	username     string
	key          string
}

func (rc *reviewCtx) gitAuth() *model.GitAuth {
	if rc.token == "" {
		return nil
	}
	switch rc.provider {
	case model.ProviderGitHub:
		return &model.GitAuth{Username: "x-access-token", Password: rc.token}
	case model.ProviderGitLab:
		return &model.GitAuth{Username: "oauth2", Password: rc.token}
	}
	return &model.GitAuth{Username: rc.username, Password: rc.token}
}

func providerForHost(host string) string {
	switch host {
	case "github.com":
		return model.ProviderGitHub
	case "gitlab.com":
		return model.ProviderGitLab
	case "bitbucket.org":
		return model.ProviderBitbucket
	}
	return ""
}

func apiBase(provider, host string) string {
	switch provider {
	case model.ProviderGitHub:
		if host == "github.com" {
			return "https://api.github.com"
		}
		return "https://" + host + "/api/v3"
	case model.ProviderGitLab:
		return "https://" + host + "/api/v4"
	}
	return "https://api.bitbucket.org/2.0"
}

func (r *Reviews) cliAllowed() bool {
	return r.CLI != nil && r.Integrations != nil && r.Integrations.GH != nil
}

func (r *Reviews) reviewer(provider, baseURL, username, token string) (PRReviewer, error) {
	if r.NewReviewer == nil {
		return nil, fmt.Errorf("%w: pull request reviews are not configured", ErrNoReviewIdentity)
	}
	rv := r.NewReviewer(provider, baseURL, username, token)
	if rv == nil {
		return nil, fmt.Errorf("%w: %s reviews are not supported", ErrNoReviewIdentity, provider)
	}
	return rv, nil
}

// resolve picks the account u reviews with for p's repository: the pinned identity, else the user's own
// verified connection for the host, else (local mode only) the gh login. The first pick is pinned.
// fresh skips the cached Me(), as posting must.
func (r *Reviews) resolve(ctx context.Context, userID string, p *model.Project, fresh bool) (*reviewCtx, error) {
	host, repo := parseRemote(r.Integrations.origin(ctx, p))
	if host == "" || repo == "" {
		return nil, fmt.Errorf("%w: this project has no remote origin on a git host", ErrNoReviewIdentity)
	}
	conns, err := r.Integrations.Store.Visible(ctx, userID)
	if err != nil {
		return nil, err
	}
	provider := providerForHost(host)
	var mine []model.Connection
	for _, c := range conns {
		if c.OwnerID == userID && c.IsGit() && c.Status == model.ConnectionActive && c.VerifiedAt != nil && connectionHost(c.BaseURL) == host {
			if provider == "" {
				provider = c.Provider
			}
			if c.Provider == provider {
				mine = append(mine, c)
			}
		}
	}
	if provider == "" {
		return nil, fmt.Errorf("%w: no account for %s", ErrNoReviewIdentity, host)
	}
	rc := &reviewCtx{project: p, userID: userID, provider: provider, host: host, repo: repo}
	pin, err := r.Store.Identity(ctx, userID, provider, host)
	if err != nil && !missing(err) {
		return nil, err
	}
	switch {
	case pin != nil:
		err = r.bind(ctx, rc, pin.Source, pin.ConnectionID, pin.Login, conns)
	case len(mine) > 1:
		return nil, ErrIdentityAmbiguous
	case len(mine) == 1:
		err = r.bind(ctx, rc, model.IdentitySourceConnection, mine[0].ID, "", conns)
	case r.cliAllowed() && provider == model.ProviderGitHub:
		users, serr := r.CLI.Status(ctx, host)
		if serr != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoReviewIdentity, serr)
		}
		switch len(users) {
		case 0:
			return nil, ErrNoReviewIdentity
		case 1:
			err = r.bind(ctx, rc, model.IdentitySourceGH, "", users[0], conns)
		default:
			return nil, ErrIdentityAmbiguous
		}
	default:
		return nil, ErrNoReviewIdentity
	}
	if err != nil {
		return nil, err
	}
	me, err := r.me(ctx, rc, fresh)
	if err != nil {
		return nil, err
	}
	rc.me = me
	rc.key = strings.Join([]string{userID, provider, host, strings.ToLower(me.Login)}, "|")
	if pin != nil {
		if !strings.EqualFold(pin.Login, me.Login) {
			return nil, fmt.Errorf("%w: pinned %s, the credential now belongs to %s", ErrIdentityChanged, pin.Login, me.Login)
		}
		return rc, nil
	}
	err = r.Store.PinIdentity(ctx, model.ReviewIdentity{UserID: userID, Provider: provider, Host: host, Source: rc.source,
		ConnectionID: rc.connectionID, Login: me.Login, HostUserID: me.ID, PinnedAt: r.now()})
	return rc, err
}

// bind builds the REST client for a source; login names the CLI user and is empty for connections.
func (r *Reviews) bind(ctx context.Context, rc *reviewCtx, source, connectionID, login string, conns []model.Connection) error {
	rc.source = source
	switch source {
	case model.IdentitySourceConnection:
		var c *model.Connection
		for i := range conns {
			if conns[i].ID == connectionID && conns[i].OwnerID == rc.userID && conns[i].Status == model.ConnectionActive {
				c = &conns[i]
			}
		}
		if c == nil {
			return fmt.Errorf("%w: the connection pinned for %s is gone or disabled", ErrNoReviewIdentity, rc.host)
		}
		token, err := r.Integrations.token(ctx, c)
		if err != nil {
			return err
		}
		rv, err := r.reviewer(c.Provider, c.BaseURL, c.Username, token)
		if err != nil {
			return err
		}
		rc.rv, rc.connectionID, rc.token, rc.username = rv, c.ID, token, c.Username
	case model.IdentitySourceGH:
		if !r.cliAllowed() {
			return fmt.Errorf("%w: %v", ErrNoReviewIdentity, ErrCLIUnavailable)
		}
		token, err := r.CLI.Token(ctx, rc.host, login)
		if err != nil {
			return fmt.Errorf("%w: gh has no login for %s on %s", ErrNoReviewIdentity, login, rc.host)
		}
		rv, err := r.reviewer(rc.provider, apiBase(rc.provider, rc.host), "", token)
		if err != nil {
			return err
		}
		rc.rv, rc.cliUser, rc.token = rv, login, token
	default:
		return fmt.Errorf("%w: unsupported identity source %q", ErrNoReviewIdentity, source)
	}
	return nil
}

func (r *Reviews) me(ctx context.Context, rc *reviewCtx, fresh bool) (model.HostUser, error) {
	sum := sha256.Sum256([]byte(rc.token))
	key := strings.Join([]string{"me", rc.userID, rc.provider, rc.host, rc.source, rc.connectionID, rc.cliUser, hex.EncodeToString(sum[:6])}, "|")
	v, err := r.memo(key, meTTL, fresh, func() (any, error) { return rc.rv.Me(ctx) })
	if err != nil {
		return model.HostUser{}, err
	}
	return v.(model.HostUser), nil
}

// tokenInfo is the credential's scopes and expiry, cached.
func (r *Reviews) tokenInfo(ctx context.Context, rc *reviewCtx) (model.TokenInfo, error) {
	key := "info|" + rc.key + "|" + rc.source + "|" + rc.connectionID
	v, err := r.memo(key, infoTTL, false, func() (any, error) { return rc.rv.TokenInfo(ctx) })
	if err != nil {
		return model.TokenInfo{}, err
	}
	return v.(model.TokenInfo), nil
}

func (r *Reviews) readiness(info model.TokenInfo, err error) (string, *time.Time) {
	var pe *model.ProviderError
	switch {
	case errors.As(err, &pe) && pe.Status == 401:
		return model.ReadinessInvalid, nil
	case err != nil:
		return model.ReadinessUnknown, nil
	case !info.CanRead:
		return model.ReadinessInvalid, info.ExpiresAt
	case info.ExpiresAt != nil && !info.ExpiresAt.After(r.now()):
		return model.ReadinessExpired, info.ExpiresAt
	}
	switch info.CanWrite {
	case model.TriNo:
		return model.ReadinessReadOnly, info.ExpiresAt
	case model.TriYes:
		return model.ReadinessReady, info.ExpiresAt
	}
	return model.ReadinessUnknown, info.ExpiresAt
}

func (r *Reviews) identityView(ctx context.Context, rc *reviewCtx) *model.IdentityView {
	if rc == nil {
		return nil
	}
	info, err := r.tokenInfo(ctx, rc)
	ready, exp := r.readiness(info, err)
	return &model.IdentityView{Provider: rc.provider, Host: rc.host, Source: rc.source, Login: rc.me.Login, Readiness: ready, ExpiresAt: exp}
}

// providerErr maps any failure of a review call to the wire error.
func (r *Reviews) providerErr(err error) *model.ProviderErr {
	var pe *model.ProviderError
	var pc *PostConflict
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNoReviewIdentity), errors.Is(err, ErrIdentityAmbiguous), errors.Is(err, ErrIdentityChanged):
		return &model.ProviderErr{Code: model.ErrCodeNoIdentity, Message: err.Error()}
	case errors.Is(err, ErrReadOnlyToken):
		return &model.ProviderErr{Code: model.ErrCodeForbidden, Message: err.Error()}
	case errors.As(err, &pc):
		return &model.ProviderErr{Code: model.ErrCodeProvider, Message: err.Error()}
	case errors.As(err, &pe):
		switch ra := pe.RetryAfter; {
		case pe.RateLimited || pe.Status == 429:
			e := &model.ProviderErr{Code: model.ErrCodeRateLimit, Message: "rate limited by " + pe.Provider}
			if ra > 0 {
				until := r.now().Add(ra)
				e.Until = &until
			}
			return e
		case pe.Status == 401:
			return &model.ProviderErr{Code: model.ErrCodeNoIdentity, Message: "token rejected by " + pe.Provider}
		case pe.Status == 403:
			return &model.ProviderErr{Code: model.ErrCodeForbidden, Message: pe.Message}
		case pe.Status == 404:
			return &model.ProviderErr{Code: model.ErrCodeNotFound, Message: "repository or pull request not found, or no access"}
		}
		return &model.ProviderErr{Code: model.ErrCodeProvider, Message: pe.Error()}
	}
	return &model.ProviderErr{Code: model.ErrCodeProvider, Message: err.Error()}
}

var retrySleep = func(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// retryGet repeats a read once after a short provider-asked wait; longer waits go back to the caller.
func retryGet[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	v, err := fn()
	var pe *model.ProviderError
	if errors.As(err, &pe) {
		if ra := pe.RetryAfter; pe.RateLimited && ra > 0 && ra <= 10*time.Second {
			retrySleep(ctx, ra)
			return fn()
		}
	}
	return v, err
}

// ProviderErrOf is the wire form of a review failure, for handlers that map it to a status.
func (r *Reviews) ProviderErrOf(err error) *model.ProviderErr { return r.providerErr(err) }

// open authorizes u on the project and resolves the review account.
func (r *Reviews) open(ctx context.Context, u *model.User, projectID string, fresh bool) (*reviewCtx, error) {
	p, err := r.Projects.Get(ctx, u, projectID)
	if err != nil {
		return nil, err
	}
	return r.resolve(ctx, u.ID, p, fresh)
}

// Recover fails the runs a restart cut off.
func (r *Reviews) Recover(ctx context.Context) error {
	n, err := r.Store.FailRunning(ctx, "interrupted by restart")
	if err == nil && n > 0 {
		r.log().Warn("failed AI reviews left running by a dead process", "count", n)
	}
	return err
}

// Wait blocks until background refreshes and runs finish; tests use it.
func (r *Reviews) Wait() { r.bg.Wait() }

func (r *Reviews) review(ctx context.Context, rc *reviewCtx, number int) (*model.PRReview, error) {
	rev, err := r.Store.Get(ctx, rc.userID, rc.provider, rc.host, rc.repo, number)
	if err != nil {
		if missing(err) {
			return nil, nil
		}
		return nil, err
	}
	return rev, nil
}

// ensureReview returns the user's review row for the PR, creating it on first use.
func (r *Reviews) ensureReview(ctx context.Context, rc *reviewCtx, number int) (*model.PRReview, error) {
	if rev, err := r.review(ctx, rc, number); err != nil || rev != nil {
		return rev, err
	}
	now := r.now()
	nr := &model.PRReview{ID: fmt.Sprintf("prr_%d", now.UnixNano()), UserID: rc.userID, ProjectID: rc.project.ID, Provider: rc.provider,
		Host: rc.host, Repo: rc.repo, Number: number, Verdict: model.VerdictComment, CreatedAt: now, UpdatedAt: now}
	if err := r.Store.Upsert(ctx, nr); err != nil {
		return nil, err
	}
	rev, err := r.review(ctx, rc, number)
	if err != nil || rev == nil {
		return nr, err
	}
	return rev, nil
}

// requireReview is the caller's own review row; someone else's, or none, is not found.
func (r *Reviews) requireReview(ctx context.Context, rc *reviewCtx, number int) (*model.PRReview, error) {
	rev, err := r.review(ctx, rc, number)
	if err != nil {
		return nil, err
	}
	if rev == nil {
		return nil, ErrReviewNotFound
	}
	return rev, nil
}
