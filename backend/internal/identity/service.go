// Package identity implements authentication (dev mode + OIDC) and
// workspace-level RBAC. Production uses OIDC; dev mode is explicit config.
package identity

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleOwner    Role = "OWNER"
	RoleAdmin    Role = "ADMIN"
	RoleEngineer Role = "ENGINEER"
	RoleViewer   Role = "VIEWER"
)

// CanWrite reports whether the role may mutate work.
func (r Role) CanWrite() bool { return r != RoleViewer }

// IsAdmin reports whether the role may change governance/settings.
func (r Role) IsAdmin() bool { return r == RoleOwner || r == RoleAdmin }

type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  Role   `json:"role"`
}

type Service struct {
	DB      *sql.DB
	Mode    string // dev|oidc
	DevUser User
	OIDC    *OIDCClient
	Log     *slog.Logger
}

func (s *Service) EnsureDevUser(ctx context.Context) (*User, error) {
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO users (id, email, name, role, created_at) VALUES (?,?,?,?,?)
		ON CONFLICT(id) DO NOTHING`, s.DevUser.ID, s.DevUser.Email, s.DevUser.Name, string(RoleOwner), time.Now().Unix()); err != nil {
		return nil, err
	}
	return &s.DevUser, nil
}

// Login issues a session. In dev mode this signs in DevUser directly; OIDC
// code exchange produces a UserInfo that upserts a user first.
func (s *Service) Login(ctx context.Context, u *User) (sessionID string, expires bool, err error) {
	sid := "sess_" + uuid.NewString()
	ttl := 7 * 24 * time.Hour
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO sessions (id, user_id, expires_at, created_at) VALUES (?,?,?,?)`,
		sid, u.ID, time.Now().Add(ttl).Unix(), time.Now().Unix()); err != nil {
		return "", false, err
	}
	return sid, true, nil
}

// UserForSession resolves the session cookie value to a user.
func (s *Service) UserForSession(ctx context.Context, sid string) (*User, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT u.id, u.email, u.name, u.role FROM sessions s
		JOIN users u ON u.id = s.user_id WHERE s.id = ? AND s.expires_at > ?`, sid, time.Now().Unix())
	var u User
	var role string
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Role = Role(role)
	return &u, nil
}

// UpsertOIDCUser creates or updates a local user from verified OIDC claims.
func (s *Service) UpsertUser(ctx context.Context, email, name string) (*User, error) {
	id := "usr_" + uuid.NewString()[:12]
	var existing string
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO users (id, email, name, role, created_at) VALUES (?,?,?,?,?)`,
			id, email, name, string(RoleEngineer), time.Now().Unix()); err != nil {
			return nil, err
		}
		return &User{ID: id, Email: email, Name: name, Role: RoleEngineer}, nil
	}
	if err != nil {
		return nil, err
	}
	var name0, role string
	if err := s.DB.QueryRowContext(ctx, `SELECT name, role FROM users WHERE id = ?`, existing).Scan(&name0, &role); err != nil {
		return nil, err
	}
	return &User{ID: existing, Email: email, Name: name0, Role: Role(role)}, nil
}

var ErrNotFound = errors.New("session not found")

// ---- OIDC client (authorization code flow, discovery-based) ----

type OIDCClient struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	HTTP         *http.Client

	discovered bool
	authEndpoint  string
	tokenEndpoint string
	userEndpoint  string
}

type providerMeta struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

func (o *OIDCClient) discover(ctx context.Context) error {
	if o.discovered {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.Issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return err
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("oidc discovery: %w", err)
	}
	defer resp.Body.Close()
	var meta providerMeta
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta); err != nil {
		return fmt.Errorf("oidc discovery: %w", err)
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return fmt.Errorf("oidc discovery: incomplete document")
	}
	o.authEndpoint, o.tokenEndpoint, o.userEndpoint = meta.AuthorizationEndpoint, meta.TokenEndpoint, meta.UserinfoEndpoint
	o.discovered = true
	return nil
}

// AuthURL builds the provider authorization URL for one login attempt.
func (o *OIDCClient) AuthURL(ctx context.Context, state string) (string, error) {
	if err := o.discover(ctx); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s?response_type=code&client_id=%s&redirect_uri=%s&scope=openid%%20profile%%20email&state=%s",
		o.authEndpoint, o.ClientID, o.RedirectURL, state), nil
}

// Exchange swaps a code for user claims (authorization code grant).
func (o *OIDCClient) Exchange(ctx context.Context, code string) (email, name string, err error) {
	if err := o.discover(ctx); err != nil {
		return "", "", err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", o.RedirectURL)
	form.Set("client_id", o.ClientID)
	form.Set("client_secret", o.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("oidc token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", "", fmt.Errorf("oidc token: status %d: %s", resp.StatusCode, b)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", "", err
	}
	if tok.AccessToken == "" {
		return "", "", fmt.Errorf("oidc token: empty access_token")
	}
	if o.userEndpoint == "" { // fall back to id_token payload claims
		return decodeIDTokenClaims(tok.IDToken)
	}
	ureq, err := http.NewRequestWithContext(ctx, http.MethodGet, o.userEndpoint, nil)
	if err != nil {
		return "", "", err
	}
	ureq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uresp, err := o.HTTP.Do(ureq)
	if err != nil {
		return "", "", fmt.Errorf("oidc userinfo: %w", err)
	}
	defer uresp.Body.Close()
	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(uresp.Body).Decode(&claims); err != nil {
		return "", "", err
	}
	if claims.Email == "" {
		return "", "", fmt.Errorf("oidc userinfo: no email claim")
	}
	return claims.Email, claims.Name, nil
}

// decodeIDTokenClaims parses the id_token payload without signature
// verification. Safe only because the token was obtained directly from the
// token endpoint over TLS (no audience confusion possible in this flow).
func decodeIDTokenClaims(idToken string) (email, name string, err error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", "", fmt.Errorf("malformed id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", fmt.Errorf("id_token payload: %w", err)
	}
	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", err
	}
	return claims.Email, claims.Name, nil
}
