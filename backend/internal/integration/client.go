// Package integration holds REST clients for external providers: GitHub,
// GitLab and Bitbucket Cloud (pull requests) and Jira Cloud (issue import).
// Clients are stateless and built per connection; they never log tokens.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
)

var defaultHTTP = &http.Client{Timeout: 30 * time.Second}

type api struct {
	provider string
	base     string
	hc       *http.Client
	auth     func(*http.Request)
}

func newAPI(provider, base string, hc *http.Client, auth func(*http.Request)) api {
	if hc == nil {
		hc = defaultHTTP
	}
	return api{provider: provider, base: strings.TrimRight(base, "/"), hc: hc, auth: auth}
}

// do sends a JSON request and decodes a JSON response into out. Non-2xx
// responses become *model.ProviderError with the provider's message.
func (a api) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	a.auth(req)
	resp, err := a.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s API unreachable: %w", a.provider, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &model.ProviderError{Provider: a.provider, Status: resp.StatusCode, Message: errorMessage(raw)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s API returned unexpected JSON: %w", a.provider, err)
	}
	return nil
}

// errorMessage extracts the provider's error text without echoing whole
// bodies (which can be HTML error pages).
func errorMessage(raw []byte) string {
	var e struct {
		Message       string   `json:"message"`
		Error         any      `json:"error"`
		ErrorMessages []string `json:"errorMessages"`
	}
	if json.Unmarshal(raw, &e) == nil {
		switch {
		case e.Message != "":
			return e.Message
		case len(e.ErrorMessages) > 0:
			return strings.Join(e.ErrorMessages, "; ")
		case e.Error != nil:
			if m, ok := e.Error.(map[string]any); ok && m["message"] != nil {
				return fmt.Sprint(m["message"])
			}
			return fmt.Sprint(e.Error)
		}
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func bearer(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func basic(user, token string) func(*http.Request) {
	return func(r *http.Request) { r.SetBasicAuth(user, token) }
}
