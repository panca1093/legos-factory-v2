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
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

var defaultHTTP = &http.Client{Timeout: 30 * time.Second}

type api struct {
	provider string
	base     string
	hc       *http.Client
	auth     func(*http.Request)
	cache    *etagCache // GitHub only: conditional GETs, per client
}

func newAPI(provider, base string, hc *http.Client, auth func(*http.Request)) api {
	if hc == nil {
		hc = defaultHTTP
	}
	return api{provider: provider, base: strings.TrimRight(base, "/"), hc: hc, auth: auth}
}

type reqOpts struct {
	limit  int64  // response bytes kept; 0 = 1 MiB
	cache  bool   // use ETag / If-None-Match when the client has a cache
	accept string // overrides the JSON Accept header (raw diffs)
}

type response struct {
	status int
	header http.Header
	body   []byte
	capped bool // body was cut at the limit
}

type etagEntry struct {
	etag   string
	body   []byte
	header http.Header
}

type etagCache struct {
	mu sync.Mutex
	m  map[string]etagEntry
}

const etagCacheMax = 256

func newETagCache() *etagCache { return &etagCache{m: map[string]etagEntry{}} }

func (c *etagCache) get(k string) (etagEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	return e, ok
}

func (c *etagCache) put(k string, e etagEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= etagCacheMax {
		c.m = map[string]etagEntry{}
	}
	c.m[k] = e
}

// resolve accepts a path under the base URL or an absolute URL on the base's
// host (pagination links); any other host would receive the credential.
func (a api) resolve(target string) (string, error) {
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		return a.base + target, nil
	}
	t, err := url.Parse(target)
	b, berr := url.Parse(a.base)
	if err != nil || berr != nil || t.Host != b.Host {
		return "", fmt.Errorf("%s API returned a link to another host", a.provider)
	}
	return target, nil
}

// send performs one request. Non-2xx responses become *model.ProviderError;
// a 304 for a cached GET is returned as the cached 200.
func (a api) send(ctx context.Context, method, target string, body any, o reqOpts) (response, error) {
	full, err := a.resolve(target)
	if err != nil {
		return response{}, err
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return response{}, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, full, rd)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Accept", "application/json")
	if o.accept != "" {
		req.Header.Set("Accept", o.accept)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	useCache := o.cache && a.cache != nil && method == http.MethodGet
	var cached etagEntry
	var hit bool
	if useCache {
		if cached, hit = a.cache.get(full); hit {
			req.Header.Set("If-None-Match", cached.etag)
		}
	}
	a.auth(req)
	resp, err := a.hc.Do(req)
	if err != nil {
		return response{}, fmt.Errorf("%s API unreachable: %w", a.provider, err)
	}
	defer resp.Body.Close()
	limit := o.limit
	if limit == 0 {
		limit = 1 << 20
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	capped := int64(len(raw)) > limit
	if capped {
		raw = raw[:limit]
	}
	if resp.StatusCode == http.StatusNotModified && hit {
		return response{status: 200, header: cached.header, body: cached.body}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return response{}, providerError(a.provider, resp.StatusCode, resp.Header, raw)
	}
	if etag := resp.Header.Get("ETag"); useCache && etag != "" && !capped {
		a.cache.put(full, etagEntry{etag: etag, body: raw, header: resp.Header.Clone()})
	}
	return response{status: resp.StatusCode, header: resp.Header, body: raw, capped: capped}, nil
}

// do sends a JSON request and decodes a JSON response into out. Non-2xx
// responses become *model.ProviderError with the provider's message.
func (a api) do(ctx context.Context, method, path string, body, out any) error {
	_, err := a.call(ctx, method, path, body, out, reqOpts{})
	return err
}

// call is do that also returns the response headers.
func (a api) call(ctx context.Context, method, path string, body, out any, o reqOpts) (http.Header, error) {
	r, err := a.send(ctx, method, path, body, o)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return r.header, nil
	}
	if err := json.Unmarshal(r.body, out); err != nil {
		return nil, fmt.Errorf("%s API returned unexpected JSON: %w", a.provider, err)
	}
	return r.header, nil
}

func providerError(provider string, status int, h http.Header, raw []byte) *model.ProviderError {
	pe := &model.ProviderError{Provider: provider, Status: status, Message: errorMessage(raw)}
	limited := status == http.StatusTooManyRequests ||
		(provider == model.ProviderGitHub && status == http.StatusForbidden &&
			(h.Get("X-RateLimit-Remaining") == "0" || strings.Contains(strings.ToLower(pe.Message), "rate limit")))
	if limited {
		pe.RateLimited = true
		pe.RetryAfter = retryAfter(h, time.Now())
	}
	return pe
}

// retryAfter reads Retry-After (seconds or HTTP date), then the epoch reset
// headers GitHub (X-RateLimit-Reset) and GitLab (RateLimit-Reset) send.
func retryAfter(h http.Header, now time.Time) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if s, err := strconv.Atoi(v); err == nil && s >= 0 {
			return time.Duration(s) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			return max(0, t.Sub(now))
		}
	}
	for _, k := range []string{"X-RateLimit-Reset", "RateLimit-Reset"} {
		if s, err := strconv.ParseInt(strings.TrimSpace(h.Get(k)), 10, 64); err == nil && s > 0 {
			return max(0, time.Unix(s, 0).Sub(now))
		}
	}
	return 0
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

// splitPRRef parses the "<provider>:<repo><sep><number>" ref OpenPullRequest stores.
func splitPRRef(ref, provider, sep string) (repo string, number int, err error) {
	rest, ok := strings.CutPrefix(ref, provider+":")
	i := strings.LastIndex(rest, sep)
	if ok && i > 0 {
		number, err = strconv.Atoi(rest[i+len(sep):])
	}
	if !ok || i <= 0 || err != nil || number < 1 {
		return "", 0, fmt.Errorf("%w: %q is not a %s pull request ref", model.ErrPermanent, ref, provider)
	}
	return rest[:i], number, nil
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
