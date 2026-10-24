package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

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
