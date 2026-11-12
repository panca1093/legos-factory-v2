package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

type MCPStore interface {
	Create(ctx context.Context, s *model.MCPServerRec, sealed []byte) error
	Update(ctx context.Context, s *model.MCPServerRec, sealed []byte) error
	Get(ctx context.Context, id string) (*model.MCPServerRec, error)
	List(ctx context.Context) ([]model.MCPServerRec, error)
	Secret(ctx context.Context, id string) ([]byte, error)
	Delete(ctx context.Context, id string) error
}

// MCPSelector hands a run the MCP servers its role may use.
type MCPSelector interface {
	Selection(ctx context.Context, role model.AgentRole) (model.MCPSelection, error)
}

// MCPServers manages the MCP servers offered to agent roles. Rules:
//   - admins only (the HTTP layer enforces it); credentials are sealed and never returned;
//   - a tool is available to a run only when enabled, and a tool that is not read-only
//     may be enabled only on a server offered to the Builder alone.
type MCPServers struct {
	Store   MCPStore
	Secrets Sealer // nil: only servers without credentials can be stored
	// Lister lists a server's tools by connecting to it.
	Lister func(ctx context.Context, s model.MCPServer) ([]model.MCPTool, error)

	// CallbackURL is the OAuth redirect URI the browser returns to.
	CallbackURL string
	// HTTP and Now are for OAuth calls and expiry; nil means a client without redirects and the wall clock.
	HTTP *http.Client
	Now  func() time.Time

	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	pending map[string]oauthPending
}

var (
	mcpEnvName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	mcpHeaderName = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

func mcpInvalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

func mcpToolName(server, tool string) string { return "mcp__" + server + "__" + tool }

func builderOnly(roles []string) bool {
	return len(roles) == 1 && roles[0] == string(model.AgentBuilder)
}

func (m *MCPServers) List(ctx context.Context) ([]model.MCPServerView, error) {
	recs, err := m.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.MCPServerView, 0, len(recs))
	for i := range recs {
		out = append(out, m.view(ctx, &recs[i]))
	}
	return out, nil
}

func (m *MCPServers) Get(ctx context.Context, id string) (model.MCPServerView, error) {
	rec, err := m.Store.Get(ctx, id)
	if err != nil {
		return model.MCPServerView{}, err
	}
	return m.view(ctx, rec), nil
}

func (m *MCPServers) view(ctx context.Context, rec *model.MCPServerRec) model.MCPServerView {
	keys := []string{}
	if rec.Auth != model.MCPAuthOAuth {
		for k := range m.secretMap(ctx, rec.ID) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	size := 0
	for _, t := range rec.Tools {
		if t.Enabled {
			size += t.Size
		}
	}
	v := model.MCPServerView{MCPServerRec: *rec, SecretKeys: keys, ApproxTokens: size / 4}
	if rec.Auth == model.MCPAuthOAuth {
		m.oauthView(ctx, rec, &v)
	}
	return v
}

func (m *MCPServers) secretMap(ctx context.Context, id string) map[string]string {
	if m.Secrets == nil {
		return nil
	}
	sealed, err := m.Store.Secret(ctx, id)
	if err != nil || len(sealed) == 0 {
		return nil
	}
	plain, err := m.Secrets.Open(sealed)
	if err != nil {
		return nil
	}
	var out map[string]string
	_ = json.Unmarshal(plain, &out)
	return out
}

func (m *MCPServers) seal(transport string, secret map[string]string) ([]byte, error) {
	if len(secret) == 0 {
		return []byte{}, nil
	}
	if m.Secrets == nil {
		return nil, model.ErrSecretsUnavailable
	}
	for k, v := range secret {
		valid := mcpEnvName.MatchString(k)
		if transport == model.MCPHTTP {
			valid = mcpHeaderName.MatchString(k)
		}
		if !valid || v == "" {
			return nil, mcpInvalid("secret %q needs a valid name and a value", k)
		}
	}
	b, _ := json.Marshal(secret)
	return m.Secrets.Seal(b)
}

func validateMCP(r *model.MCPServerRec) error {
	if len(r.Name) > 40 || !model.MCPNamePattern.MatchString(r.Name) {
		return mcpInvalid("name must be 1-40 characters of a-z and 0-9, with single _ between words")
	}
	switch r.Transport {
	case model.MCPStdio:
		if !filepath.IsAbs(r.Command) {
			return mcpInvalid("command must be an absolute path")
		}
		if r.URL != "" {
			return mcpInvalid("a stdio server has no url")
		}
	case model.MCPHTTP:
		u, err := url.Parse(r.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return mcpInvalid("url must be an http(s) address")
		}
		if r.Auth == model.MCPAuthOAuth && !isHTTPURL(r.URL) {
			return mcpInvalid("an OAuth server's url must be https, or http on localhost")
		}
		if r.Command != "" || len(r.Args) > 0 {
			return mcpInvalid("an http server has no command")
		}
	default:
		return mcpInvalid("transport must be stdio or http")
	}
	return nil
}

func mcpStoreErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return mcpInvalid("that name is already in use")
	}
	return err
}

func (m *MCPServers) Create(ctx context.Context, in model.CreateMCPServer) (model.MCPServerView, error) {
	rec := &model.MCPServerRec{Name: in.Name, Transport: in.Transport, Command: in.Command, Args: in.Args, URL: in.URL,
		Auth: model.MCPAuthNone, Status: model.MCPActive}
	if in.Auth == model.MCPAuthOAuth {
		rec.Auth = model.MCPAuthOAuth
	}
	if err := validateMCP(rec); err != nil {
		return model.MCPServerView{}, err
	}
	var sealed []byte
	var err error
	switch in.Auth {
	case model.MCPAuthOAuth:
		rec.Auth, rec.Status = model.MCPAuthOAuth, model.MCPNeedsSignIn
		sealed, err = m.createOAuth(in)
	case "", model.MCPAuthNone, model.MCPAuthToken:
		sealed, err = m.seal(rec.Transport, in.Secret)
		if len(in.Secret) > 0 {
			rec.Auth = model.MCPAuthToken
		}
	default:
		err = mcpInvalid("auth must be none, token or oauth")
	}
	if err != nil {
		return model.MCPServerView{}, err
	}
	if err := m.Store.Create(ctx, rec, sealed); err != nil {
		return model.MCPServerView{}, mcpStoreErr(err)
	}
	return m.view(ctx, rec), nil
}

// Update changes a server. Changing what it connects to, or its name (the tool prefix),
// drops the discovered tools: they belong to the old server and must be listed and enabled again.
func (m *MCPServers) Update(ctx context.Context, id string, in model.UpdateMCPServer) (model.MCPServerView, error) {
	defer m.lockServer(id)()
	rec, err := m.Store.Get(ctx, id)
	if err != nil {
		return model.MCPServerView{}, err
	}
	before := mcpDefinition(rec)
	if in.Name != nil {
		rec.Name = *in.Name
	}
	if in.Command != nil {
		rec.Command = *in.Command
	}
	if in.Args != nil {
		rec.Args = *in.Args
	}
	if in.URL != nil {
		rec.URL = *in.URL
	}
	if in.Status != nil {
		if *in.Status != model.MCPActive && *in.Status != model.MCPDisabled {
			return model.MCPServerView{}, mcpInvalid("status must be active or disabled")
		}
		rec.Status = *in.Status
	}
	if err := validateMCP(rec); err != nil {
		return model.MCPServerView{}, err
	}
	var sealed []byte
	if in.Secret != nil {
		if sealed, err = m.seal(rec.Transport, *in.Secret); err != nil {
			return model.MCPServerView{}, err
		}
		rec.Auth = model.MCPAuthNone
		if len(*in.Secret) > 0 {
			rec.Auth = model.MCPAuthToken
		}
	}
	if mcpDefinition(rec) != before {
		rec.Tools, rec.DiscoveredAt, rec.LastError = nil, nil, ""
		if rec.Auth == model.MCPAuthOAuth && in.Secret == nil && in.URL != nil {
			if sealed, err = m.resetOAuthTarget(ctx, rec); err != nil {
				return model.MCPServerView{}, err
			}
		}
	}
	if err := m.Store.Update(ctx, rec, sealed); err != nil {
		return model.MCPServerView{}, mcpStoreErr(err)
	}
	return m.view(ctx, rec), nil
}

func mcpDefinition(rec *model.MCPServerRec) string {
	b, _ := json.Marshal([]any{rec.Name, rec.Command, rec.Args, rec.URL})
	return string(b)
}

func redactSecrets(msg string, secret map[string]string) string {
	for _, v := range secret {
		if len(v) >= 4 {
			msg = strings.ReplaceAll(msg, v, "***")
		}
	}
	return msg
}

func (m *MCPServers) Delete(ctx context.Context, id string) error { return m.Store.Delete(ctx, id) }

func (m *MCPServers) wire(ctx context.Context, rec *model.MCPServerRec) (model.MCPServer, error) {
	s := model.MCPServer{Name: rec.Name, Transport: rec.Transport, Command: rec.Command, Args: rec.Args, URL: rec.URL}
	if rec.Auth != model.MCPAuthOAuth {
		s.Secret = m.secretMap(ctx, rec.ID)
		return s, nil
	}
	var err error
	s.Secret, err = m.oauthHeaders(ctx, rec)
	return s, err
}

// Discover lists the server's tools. New tools start disabled, tools the server dropped are removed,
// and the enabled flag of a tool that is still there is kept unless it no longer qualifies.
// A connection failure is recorded in last_error and returned in the view, not as an error.
func (m *MCPServers) Discover(ctx context.Context, id string) (model.MCPServerView, error) {
	rec, err := m.Store.Get(ctx, id)
	if err != nil {
		return model.MCPServerView{}, err
	}
	listed, werr := m.wire(ctx, rec)
	if werr != nil {
		if rec, err = m.Store.Get(ctx, id); err != nil {
			return model.MCPServerView{}, err
		}
		rec.LastError = truncateRunes(werr.Error(), 500)
		if err := m.Store.Update(ctx, rec, nil); err != nil {
			return model.MCPServerView{}, err
		}
		return m.view(ctx, rec), nil
	}
	if rec, err = m.Store.Get(ctx, id); err != nil {
		return model.MCPServerView{}, err
	}
	listedWire, _ := json.Marshal(listed)
	tools, derr := m.Lister(ctx, listed)
	if rec, err = m.Store.Get(ctx, id); err != nil {
		return model.MCPServerView{}, err
	}
	nowSrv, _ := m.wire(ctx, rec)
	if nowWire, _ := json.Marshal(nowSrv); !bytes.Equal(nowWire, listedWire) {
		return model.MCPServerView{}, stateConflict("the server changed while its tools were being listed; discover again")
	}
	if derr != nil {
		redact := listed.Secret
		if rec.Auth == model.MCPAuthOAuth {
			redact = map[string]string{"bearer": strings.TrimPrefix(listed.Secret["Authorization"], "Bearer ")}
		}
		rec.LastError = truncateRunes(redactSecrets(derr.Error(), redact), 500)
	} else {
		was := map[string]bool{}
		for _, t := range rec.Tools {
			was[t.Name] = t.Enabled
		}
		for i := range tools {
			if !model.MCPToolNamePattern.MatchString(tools[i].Name) {
				tools[i].Unusable, tools[i].UnusableReason = true, "the name has characters or a length Claude Code cannot address"
			}
			tools[i].Enabled = !tools[i].Unusable && was[tools[i].Name] && (tools[i].ReadOnly || builderOnly(rec.Roles))
		}
		now := time.Now()
		rec.Tools, rec.DiscoveredAt, rec.LastError = tools, &now, ""
	}
	if err := m.Store.Update(ctx, rec, nil); err != nil {
		return model.MCPServerView{}, err
	}
	return m.view(ctx, rec), nil
}

func (m *MCPServers) SetRoles(ctx context.Context, id string, roles []string) (model.MCPServerView, error) {
	defer m.lockServer(id)()
	rec, err := m.Store.Get(ctx, id)
	if err != nil {
		return model.MCPServerView{}, err
	}
	var clean []string
	for _, r := range model.MCPRoles {
		if slices.Contains(roles, r) {
			clean = append(clean, r)
		}
	}
	if len(clean) != len(dedupe(roles)) {
		return model.MCPServerView{}, mcpInvalid("roles must be among %s", strings.Join(model.MCPRoles, ", "))
	}
	if !builderOnly(clean) {
		for _, t := range rec.Tools {
			if t.Enabled && !t.ReadOnly {
				return model.MCPServerView{}, mcpInvalid("tool %q writes, so this server can only be offered to the builder; disable it first", t.Name)
			}
		}
	}
	rec.Roles = clean
	if err := m.Store.Update(ctx, rec, nil); err != nil {
		return model.MCPServerView{}, err
	}
	return m.view(ctx, rec), nil
}

func dedupe(in []string) []string {
	out := []string{}
	for _, s := range in {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func (m *MCPServers) SetTools(ctx context.Context, id string, toggles []model.MCPToolToggle) (model.MCPServerView, error) {
	defer m.lockServer(id)()
	rec, err := m.Store.Get(ctx, id)
	if err != nil {
		return model.MCPServerView{}, err
	}
	if rec.DiscoveredAt == nil {
		return model.MCPServerView{}, mcpInvalid("discover the server's tools before enabling any")
	}
	for _, tg := range toggles {
		i := slices.IndexFunc(rec.Tools, func(t model.MCPTool) bool { return t.Name == tg.Name })
		if i < 0 {
			return model.MCPServerView{}, mcpInvalid("the server has no tool %q", tg.Name)
		}
		if tg.Enabled && rec.Tools[i].Unusable {
			return model.MCPServerView{}, mcpInvalid("tool %q has a name Claude Code cannot address, so it cannot be enabled", tg.Name)
		}
		if tg.Enabled && !rec.Tools[i].ReadOnly && !builderOnly(rec.Roles) {
			return model.MCPServerView{}, mcpInvalid("tool %q writes, so it can be enabled only on a server offered to the builder alone", tg.Name)
		}
		rec.Tools[i].Enabled = tg.Enabled
	}
	if err := m.Store.Update(ctx, rec, nil); err != nil {
		return model.MCPServerView{}, err
	}
	return m.view(ctx, rec), nil
}

// Selection is what a run in role gets: active servers offered to the role that have an enabled tool,
// the names of those tools, and the discovered tools that stay disabled so the agent can be told to drop them.
func (m *MCPServers) Selection(ctx context.Context, role model.AgentRole) (model.MCPSelection, error) {
	var sel model.MCPSelection
	recs, err := m.Store.List(ctx)
	if err != nil {
		return sel, err
	}
	for i := range recs {
		rec := &recs[i]
		if rec.Status != model.MCPActive || !slices.Contains(rec.Roles, string(role)) {
			continue
		}
		var allow, deny []string
		for _, t := range rec.Tools {
			// a writing tool outside a builder-only server is dropped even if its flag says enabled
			if t.Enabled && !t.Unusable && (t.ReadOnly || (role == model.AgentBuilder && builderOnly(rec.Roles))) {
				allow = append(allow, mcpToolName(rec.Name, t.Name))
			} else {
				deny = append(deny, mcpToolName(rec.Name, t.Name))
			}
		}
		if len(allow) == 0 {
			continue
		}
		srv, err := m.wire(ctx, rec)
		if err != nil {
			continue
		}
		sel.Servers = append(sel.Servers, srv)
		sel.Allow = append(sel.Allow, allow...)
		sel.Deny = append(sel.Deny, deny...)
	}
	return sel, nil
}
