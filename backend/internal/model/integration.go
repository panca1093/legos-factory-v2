package model

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// IssueKeyPattern matches Jira issue keys such as PAY-42.
var IssueKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]+-[0-9]+$`)

const (
	ProviderGitHub    = "github"
	ProviderGitLab    = "gitlab"
	ProviderBitbucket = "bitbucket"
	ProviderJira      = "jira"

	ScopeShared   = "shared"
	ScopePersonal = "personal"

	ConnectionActive   = "active"
	ConnectionDisabled = "disabled"
)

var (
	ErrConnectionNotFound = errors.New("connection not found")
	ErrConnectionDisabled = errors.New("connection is disabled")
	ErrSecretsUnavailable = errors.New("credential storage is not configured")
)

// ProviderError is a non-2xx response from a provider API. Status 401/403/404
// are not retried: they mean bad credentials, missing scope or wrong repo.
type ProviderError struct {
	Provider string
	Status   int
	Message  string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s API returned %d: %s", e.Provider, e.Status, e.Message)
}

func (e *ProviderError) Permanent() bool {
	return e.Status == 400 || e.Status == 401 || e.Status == 403 || e.Status == 404 || e.Status == 422
}

// Connection never carries the credential itself; HasCredential reports that
// one is stored.
type Connection struct {
	ID            string     `json:"id"`
	WorkspaceID   string     `json:"workspace_id"`
	Provider      string     `json:"provider"`
	Name          string     `json:"name"`
	Scope         string     `json:"scope"`
	OwnerID       string     `json:"owner_id"`
	BaseURL       string     `json:"base_url"`
	Username      string     `json:"username,omitempty"`
	Status        string     `json:"status"`
	Account       string     `json:"account,omitempty"`
	VerifiedAt    *time.Time `json:"verified_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	HasCredential bool       `json:"has_credential"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (c Connection) IsGit() bool {
	return c.Provider == ProviderGitHub || c.Provider == ProviderGitLab || c.Provider == ProviderBitbucket
}

// GitAuth is the HTTPS basic credential used for clone and push.
type GitAuth struct {
	Username string
	Password string
}

type PRRequest struct {
	Repo  string // provider slug, e.g. org/repo
	Head  string // branch to merge
	Base  string // target branch
	Title string
	Body  string
}

type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Ref    string `json:"ref"` // e.g. github:org/repo#12
}

type Issue struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

// IssueDraft prefills a new work order from an imported issue.
type IssueDraft struct {
	SourceRef  string `json:"source_ref"`
	Title      string `json:"title"`
	Objective  string `json:"objective"`
	Acceptance string `json:"acceptance"`
	URL        string `json:"url"`
}

var defaultBaseURL = map[string]string{
	ProviderGitHub:    "https://api.github.com",
	ProviderGitLab:    "https://gitlab.com/api/v4",
	ProviderBitbucket: "https://api.bitbucket.org/2.0",
}

type CreateConnectionRequest struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	BaseURL  string `json:"base_url"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

func (r *CreateConnectionRequest) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Username = strings.TrimSpace(r.Username)
	r.Token = strings.TrimSpace(r.Token)
	r.BaseURL = strings.TrimRight(strings.TrimSpace(r.BaseURL), "/")
	switch r.Provider {
	case ProviderGitHub, ProviderGitLab, ProviderBitbucket, ProviderJira:
	default:
		return fmt.Errorf("provider must be github, gitlab, bitbucket or jira")
	}
	if r.Scope != ScopeShared && r.Scope != ScopePersonal {
		return fmt.Errorf("scope must be shared or personal")
	}
	if r.Name == "" || len(r.Name) > 100 {
		return fmt.Errorf("name is required (max 100 characters)")
	}
	if r.Token == "" {
		return fmt.Errorf("token is required")
	}
	if r.BaseURL == "" {
		r.BaseURL = defaultBaseURL[r.Provider]
	}
	if r.BaseURL == "" {
		return fmt.Errorf("base_url is required for %s", r.Provider)
	}
	if err := ValidateBaseURL(r.BaseURL); err != nil {
		return err
	}
	if (r.Provider == ProviderBitbucket || r.Provider == ProviderJira) && r.Username == "" {
		return fmt.Errorf("username (account email) is required for %s", r.Provider)
	}
	return nil
}

// ValidateBaseURL allows https everywhere and plain http only on loopback,
// so a connection cannot downgrade credentials onto the network.
func ValidateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("base_url must be an absolute URL")
	}
	host := u.Hostname()
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return fmt.Errorf("base_url must use https")
	}
	if u.User != nil {
		return fmt.Errorf("base_url must not embed credentials")
	}
	return nil
}

type UpdateConnectionRequest struct {
	Name   *string `json:"name"`
	Status *string `json:"status"`
	Token  *string `json:"token"`
}

type ProjectBindingRequest struct {
	GitConnectionID string `json:"git_connection_id"`
	RemoteRepo      string `json:"remote_repo"`
}

type IssueLookupRequest struct {
	ConnectionID string `json:"connection_id"`
	Key          string `json:"key"`
}
