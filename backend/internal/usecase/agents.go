package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"

	"legos-factory/backend/internal/model"
)

// AgentSpec is one agent the factory can run, known in code.
type AgentSpec struct {
	ID, Name    string
	Command     []string // ACP command; Command[0] is what installed means
	InstallHint string
	Roles       []model.AgentRole
	statusCmd   []string
	loginCmd    []string
}

var allRoles = []model.AgentRole{model.AgentForeman, model.AgentArchitect, model.AgentBuilder, model.AgentInspector, model.AgentChat}

// AgentSpecs lists the agents in display order.
var AgentSpecs = []AgentSpec{
	{ID: "claude", Name: "Claude", Command: []string{"claude-agent-acp"},
		InstallHint: "npm install -g @agentclientprotocol/claude-agent-acp", Roles: allRoles,
		statusCmd: []string{"claude", "auth", "status"}, loginCmd: []string{"claude", "auth", "login"}},
	{ID: "codex", Name: "Codex", Command: []string{"codex-acp"},
		InstallHint: "npm install -g @agentclientprotocol/codex-acp",
		Roles:       allRoles,
		statusCmd:   []string{"codex", "login", "status"}, loginCmd: []string{"codex", "login", "--device-auth"}},
	{ID: "opencode", Name: "OpenCode", Command: []string{"opencode", "acp"},
		InstallHint: "curl -fsSL https://opencode.ai/install | bash", Roles: allRoles,
		statusCmd: []string{"opencode", "auth", "list", "--standalone"}, loginCmd: []string{"opencode", "auth", "login", "--standalone"}},
}

func AgentByID(id string) (AgentSpec, bool) {
	for _, s := range AgentSpecs {
		if s.ID == id {
			return s, true
		}
	}
	return AgentSpec{}, false
}

// Allows reports whether the agent may serve the role.
func (s AgentSpec) Allows(role model.AgentRole) bool {
	if role == model.AgentCataloger {
		role = model.AgentArchitect
	}
	for _, r := range s.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// accountEnv is the environment that points the agent at one account's home.
func (s AgentSpec) accountEnv(home string) []string {
	switch s.ID {
	case "claude":
		return []string{"CLAUDE_CONFIG_DIR=" + home}
	case "codex":
		return []string{"CODEX_HOME=" + home}
	case "opencode":
		return []string{"XDG_DATA_HOME=" + filepath.Join(home, "data"), "XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
			"XDG_STATE_HOME=" + filepath.Join(home, "state"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache")}
	}
	return nil
}

// accountArgs are extra ACP command arguments an account needs. None today: `opencode acp` rejects --standalone
// (it only prints help), and the XDG variables alone isolate its auth, config and state (checked with a scratch dir).
func (s AgentSpec) accountArgs() []string { return nil }

const SystemAccountID = "system"

type AgentAccountStore interface {
	Create(ctx context.Context, a *model.AgentAccount) error
	Get(ctx context.Context, userID, agent, id string) (*model.AgentAccount, error)
	List(ctx context.Context, userID, agent string) ([]model.AgentAccount, error)
	Active(ctx context.Context, userID, agent string) (*model.AgentAccount, error)
	Activate(ctx context.Context, userID, agent, id string) error
	Delete(ctx context.Context, userID, agent, id string) error
	RoleAccounts(ctx context.Context, userID, agent string) (map[string]string, error)
	SetRoleAccounts(ctx context.Context, userID, agent, accountID string, roles []string) error
	Enabled(ctx context.Context, agent string) (bool, error)
	SetEnabled(ctx context.Context, agent string, enabled bool) error
}

// Agents is the registry of known agents and each user's accounts for them.
// Every external effect (PATH lookup, status commands, the Keychain, HTTP,
// codex's RPC) is a field so tests run without the real tools.
type Agents struct {
	Store   AgentAccountStore
	DataDir string // account homes live under DataDir/accounts/<user>/<agent>/<id>
	// Commands overrides an agent's ACP command, e.g. from FACTORY_ACP_COMMAND.
	Commands map[string][]string

	LookPath func(string) (string, error)
	// Run executes a command with extra env and returns its combined output.
	Run func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
	// Keychain returns the secret stored under a service name, or an error.
	Keychain func(ctx context.Context, service string) (string, error)
	HTTP     *http.Client
	UsageURL string
	// CodexLimits reads the rate limits of the codex account whose home is home ("" is the default login).
	CodexLimits func(ctx context.Context, home string) (CodexLimits, error)
	Now         func() time.Time

	StatusTTL, UsageTTL time.Duration
	StatusTimeout       time.Duration // 0 means 12s

	mu     sync.Mutex
	status map[string]cachedStatus
	usage  map[string]cachedUsage
}

type cachedStatus struct {
	at time.Time
	v  accountStatus
}

type cachedUsage struct {
	at  time.Time
	v   *AccountUsage
	err string
}

type accountStatus struct {
	Status    string
	Who, Plan string
	ExpiresAt *time.Time
}

type UsageWindow struct {
	UsedPct  float64   `json:"used_pct"`
	ResetsAt time.Time `json:"resets_at"`
}

type AccountUsage struct {
	Session *UsageWindow `json:"session,omitempty"`
	Week    *UsageWindow `json:"week,omitempty"`
}

// CodexLimits is the 5-hour and weekly window of a codex account.
type CodexLimits struct {
	Primary, Secondary *UsageWindow
	Plan               string
}

type AccountView struct {
	ID           string        `json:"id"`
	Label        string        `json:"label"`
	System       bool          `json:"system"`
	Active       bool          `json:"active"`
	Status       string        `json:"status"`
	Who          string        `json:"who,omitempty"`
	Plan         string        `json:"plan,omitempty"`
	ExpiresAt    *time.Time    `json:"expires_at,omitempty"`
	Usage        *AccountUsage `json:"usage,omitempty"`
	UsageError   string        `json:"usage_error,omitempty"`
	LoginCommand string        `json:"login_command"`
	Roles        []string      `json:"roles"`
}

type AgentView struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Command      string        `json:"command"`
	Installed    bool          `json:"installed"`
	Enabled      bool          `json:"enabled"`
	InstallHint  string        `json:"install_hint"`
	RolesAllowed []string      `json:"roles_allowed"`
	Accounts     []AccountView `json:"accounts"`
}

type AgentList struct {
	Agents []AgentView `json:"agents"`
}

// NewAgents wires the real PATH, command runner, Keychain and HTTP client.
func NewAgents(store AgentAccountStore, dataDir string) *Agents {
	a := &Agents{Store: store, DataDir: dataDir, LookPath: exec.LookPath, HTTP: &http.Client{Timeout: 8 * time.Second},
		UsageURL: "https://api.anthropic.com/api/oauth/usage"}
	a.Run = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = append(os.Environ(), env...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		cmd.WaitDelay = time.Second
		err := cmd.Run()
		return out.Bytes(), err
	}
	a.Keychain = func(ctx context.Context, service string) (string, error) {
		// Claude Code files its login under the OS user's account; other items with the same service (e.g. account "unknown") hold only MCP tokens.
		if u, err := user.Current(); err == nil && u.Username != "" {
			out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-a", u.Username, "-w").Output()
			if err == nil {
				return strings.TrimSpace(string(out)), nil
			}
		}
		out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-w").Output()
		return strings.TrimSpace(string(out)), err
	}
	a.CodexLimits = a.readCodexLimits
	return a
}

func (a *Agents) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Agents) command(spec AgentSpec) []string {
	if c := a.Commands[spec.ID]; len(c) > 0 {
		return c
	}
	return spec.Command
}

// Command is the ACP command to run for the agent.
func (a *Agents) Command(id string) ([]string, error) {
	spec, ok := AgentByID(id)
	if !ok {
		return nil, fmt.Errorf("%w: %q", model.ErrAgentUnknown, id)
	}
	return a.command(spec), nil
}

func (a *Agents) installed(spec AgentSpec) bool {
	_, err := a.LookPath(a.command(spec)[0])
	return err == nil
}

// Ready returns a permanent error naming the agent when it cannot run a job: switched off or not installed.
func (a *Agents) Ready(ctx context.Context, id string) error {
	spec, ok := AgentByID(id)
	if !ok {
		return fmt.Errorf("%w: %w: %q", model.ErrPermanent, model.ErrAgentUnknown, id)
	}
	if on, err := a.Store.Enabled(ctx, id); err == nil && !on {
		return fmt.Errorf("%w: the %s agent is switched off in Settings", model.ErrPermanent, spec.Name)
	}
	if !a.installed(spec) {
		return fmt.Errorf("%w: the %s agent is not installed (%s)", model.ErrPermanent, spec.Name, spec.InstallHint)
	}
	return nil
}

// RunEnv is the environment and extra ACP arguments for a run on behalf of userID: the account assigned to role, else the user's active account for the agent, or nothing for the system default. An empty role means the active account.
func (a *Agents) RunEnv(ctx context.Context, id, userID string, role model.AgentRole) (env, args []string, err error) {
	spec, ok := AgentByID(id)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %q", model.ErrAgentUnknown, id)
	}
	if userID == "" {
		return nil, nil, nil
	}
	acc, err := a.roleAccount(ctx, userID, id, role)
	if err != nil || acc == nil {
		return nil, nil, err
	}
	env = spec.accountEnv(acc.HomeDir)
	if spec.ID == "claude" {
		lift, _, err := claudeAPIEnv(acc.HomeDir)
		if err != nil {
			return nil, nil, err
		}
		env = append(env, lift...)
	}
	return env, spec.accountArgs(), nil
}

// roleAccount is the account serving role; nil means the system default.
func (a *Agents) roleAccount(ctx context.Context, userID, agent string, role model.AgentRole) (*model.AgentAccount, error) {
	if role == model.AgentCataloger {
		role = model.AgentArchitect
	}
	if role != "" {
		assigned, err := a.Store.RoleAccounts(ctx, userID, agent)
		if err != nil {
			return nil, err
		}
		if id, ok := assigned[string(role)]; ok {
			if id == SystemAccountID {
				return nil, nil
			}
			acc, err := a.Store.Get(ctx, userID, agent, id)
			if err == nil {
				return acc, nil
			}
			if !errors.Is(err, model.ErrAgentAccountNotFound) {
				return nil, err
			}
		}
	}
	return a.Store.Active(ctx, userID, agent)
}

// claudeAPIEnv reads the Anthropic-compatible endpoint from <home>/settings.json. Claude Code ignores these three keys
// there, so the factory lifts them into the process env. Errors never carry file content: it holds the key.
func claudeAPIEnv(home string) (env []string, host string, err error) {
	raw, err := os.ReadFile(filepath.Join(home, "settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", errors.New("cannot read the account's settings.json")
	}
	var cfg struct {
		Env map[string]any `json:"env"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return nil, "", errors.New("the account's settings.json is not valid JSON")
	}
	val := func(k string) string { v, _ := cfg.Env[k].(string); return v }
	base := val("ANTHROPIC_BASE_URL")
	if base == "" {
		return nil, "", nil
	}
	env = []string{"ANTHROPIC_BASE_URL=" + base}
	for _, k := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if v := val(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	if u, err := url.Parse(base); err == nil {
		host = u.Host
	}
	return env, host, nil
}

// apiKeyStatus is the status of a Claude account that talks to an Anthropic-compatible provider; ok is false for a regular login.
func apiKeyStatus(spec AgentSpec, acc model.AgentAccount) (accountStatus, bool) {
	if spec.ID != "claude" || acc.ID == SystemAccountID {
		return accountStatus{}, false
	}
	env, host, err := claudeAPIEnv(acc.HomeDir)
	if err != nil || len(env) < 2 {
		return accountStatus{}, false
	}
	return accountStatus{Status: "signed_in", Plan: "API key · " + host}, true
}

// List describes every known agent with the user's accounts and their status.
func (a *Agents) List(ctx context.Context, u *model.User) (*AgentList, error) {
	out := &AgentList{Agents: make([]AgentView, len(AgentSpecs))}
	errs := make([]error, len(AgentSpecs))
	var wg sync.WaitGroup
	for i, spec := range AgentSpecs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := a.view(ctx, u.ID, spec, false)
			if err != nil {
				errs[i] = err
				return
			}
			out.Agents[i] = *v
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *Agents) view(ctx context.Context, userID string, spec AgentSpec, fresh bool) (*AgentView, error) {
	on, err := a.Store.Enabled(ctx, spec.ID)
	if err != nil {
		return nil, err
	}
	roles := make([]string, len(spec.Roles))
	for i, r := range spec.Roles {
		roles[i] = string(r)
	}
	v := &AgentView{ID: spec.ID, Name: spec.Name, Command: strings.Join(a.command(spec), " "), Installed: a.installed(spec),
		Enabled: on, InstallHint: spec.InstallHint, RolesAllowed: roles}
	stored, err := a.Store.List(ctx, userID, spec.ID)
	if err != nil {
		return nil, err
	}
	active := false
	for _, s := range stored {
		active = active || s.Active
	}
	assigned, err := a.Store.RoleAccounts(ctx, userID, spec.ID)
	if err != nil {
		return nil, err
	}
	accs := []model.AgentAccount{{ID: SystemAccountID, UserID: userID, Agent: spec.ID, Label: "System default", Active: !active}}
	accs = append(accs, stored...)
	v.Accounts = make([]AccountView, len(accs))
	var wg sync.WaitGroup
	for i := range accs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v.Accounts[i] = a.accountView(ctx, spec, accs[i], v.Installed, fresh)
			v.Accounts[i].Roles = []string{}
			for _, r := range allRoles {
				if assigned[string(r)] == accs[i].ID {
					v.Accounts[i].Roles = append(v.Accounts[i].Roles, string(r))
				}
			}
		}()
	}
	wg.Wait()
	return v, nil
}

func (a *Agents) accountView(ctx context.Context, spec AgentSpec, acc model.AgentAccount, installed, fresh bool) AccountView {
	av := AccountView{ID: acc.ID, Label: acc.Label, System: acc.ID == SystemAccountID, Active: acc.Active, Status: "unknown"}
	av.LoginCommand, _ = spec.loginLine(a.home(acc))
	if !installed {
		return av
	}
	st := a.accountStatus(ctx, spec, acc, fresh)
	av.Status, av.Who, av.Plan, av.ExpiresAt = st.Status, st.Who, st.Plan, st.ExpiresAt
	if st.Status != "signed_in" {
		return av
	}
	switch spec.ID {
	case "codex":
		cl, err := a.codexUsage(ctx, acc, fresh)
		if cl != nil {
			av.Usage = cl
		} else if err != "" {
			av.UsageError = err
		}
	case "claude":
		if _, api := apiKeyStatus(spec, acc); !api {
			av.Usage, av.UsageError = a.claudeUsage(ctx, acc, fresh)
		}
	}
	return av
}

func (a *Agents) home(acc model.AgentAccount) string {
	if acc.ID == SystemAccountID {
		return ""
	}
	return acc.HomeDir
}

func (a *Agents) statusKey(spec AgentSpec, acc model.AgentAccount) string {
	if acc.ID == SystemAccountID {
		return spec.ID + ":system"
	}
	return spec.ID + ":" + acc.ID
}

func (a *Agents) accountStatus(ctx context.Context, spec AgentSpec, acc model.AgentAccount, fresh bool) accountStatus {
	if st, ok := apiKeyStatus(spec, acc); ok {
		return st // read from settings.json each time, so an edit shows without waiting for the status cache
	}
	key := a.statusKey(spec, acc)
	ttl := a.StatusTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	a.mu.Lock()
	c, ok := a.status[key]
	a.mu.Unlock()
	if ok && !fresh && a.now().Sub(c.at) < ttl {
		return c.v
	}
	limit := a.StatusTimeout
	if limit <= 0 {
		limit = 12 * time.Second // `opencode auth list --standalone` takes ~3.5s warm, so 5s timed out on a cold start
	}
	rctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var env []string
	if h := a.home(acc); h != "" {
		env = spec.accountEnv(h)
	}
	out, err := a.Run(rctx, env, spec.statusCmd[0], spec.statusCmd[1:]...)
	st := parseStatus(spec.ID, out, err)
	if rctx.Err() != nil {
		return st // a timeout is not an answer; don't cache it so the next load asks again
	}
	a.mu.Lock()
	if a.status == nil {
		a.status = map[string]cachedStatus{}
	}
	a.status[key] = cachedStatus{at: a.now(), v: st}
	a.mu.Unlock()
	return st
}

func (a *Agents) forget(spec AgentSpec, acc model.AgentAccount) {
	a.mu.Lock()
	delete(a.status, a.statusKey(spec, acc))
	delete(a.usage, a.statusKey(spec, acc))
	a.mu.Unlock()
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// parseStatus reads a status command's output. Anything it cannot tell is "unknown", never a guess.
func parseStatus(agent string, out []byte, runErr error) accountStatus {
	unknown := accountStatus{Status: "unknown"}
	text := ansiRE.ReplaceAllString(string(out), "")
	switch agent {
	case "claude":
		i := strings.Index(text, "{")
		if i < 0 {
			return unknown
		}
		var j struct {
			LoggedIn         *bool  `json:"loggedIn"`
			SubscriptionType string `json:"subscriptionType"`
			PlanTier         string `json:"planTier"`
			LoginExpiresAt   string `json:"loginExpiresAt"`
		}
		if json.NewDecoder(strings.NewReader(text[i:])).Decode(&j) != nil || j.LoggedIn == nil {
			return unknown
		}
		if !*j.LoggedIn {
			return accountStatus{Status: "signed_out"}
		}
		st := accountStatus{Status: "signed_in", Plan: j.PlanTier}
		if st.Plan == "" {
			st.Plan = j.SubscriptionType
		}
		if t, err := time.Parse(time.RFC3339, j.LoginExpiresAt); err == nil {
			st.ExpiresAt = &t
		}
		return st
	case "codex":
		low := strings.ToLower(text)
		switch {
		case strings.Contains(low, "not logged in"):
			return accountStatus{Status: "signed_out"}
		case strings.Contains(low, "logged in using chatgpt"):
			return accountStatus{Status: "signed_in", Plan: "ChatGPT"}
		case strings.Contains(low, "logged in using"):
			return accountStatus{Status: "signed_in", Plan: "API key"}
		case strings.Contains(low, "logged in") && runErr == nil:
			return accountStatus{Status: "signed_in"}
		}
		return unknown
	case "opencode":
		var names []string
		for _, line := range strings.Split(text, "\n") {
			line = strings.Trim(strings.TrimSpace(line), "│┌└●◇◆ ")
			cols := regexp.MustCompile(`\s{2,}`).Split(line, -1)
			if len(cols) < 2 || strings.HasPrefix(line, "Credentials") || strings.Contains(line, "credentials") {
				continue
			}
			names = append(names, cols[0])
		}
		switch {
		case len(names) > 0:
			return accountStatus{Status: "signed_in", Who: strings.Join(names, ", ")}
		case runErr == nil:
			return accountStatus{Status: "signed_out"}
		}
		return unknown
	}
	return unknown
}

// ---- usage ----

func (a *Agents) usageCached(key string, ttl time.Duration, fresh bool) (cachedUsage, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.usage[key]
	return c, ok && !fresh && a.now().Sub(c.at) < ttl
}

func (a *Agents) putUsage(key string, c cachedUsage) {
	c.at = a.now()
	a.mu.Lock()
	if a.usage == nil {
		a.usage = map[string]cachedUsage{}
	}
	a.usage[key] = c
	a.mu.Unlock()
}

func (a *Agents) codexUsage(ctx context.Context, acc model.AgentAccount, fresh bool) (*AccountUsage, string) {
	key := "codex:" + acc.ID
	ttl := a.UsageTTL
	if ttl <= 0 {
		ttl = time.Minute
	}
	if c, ok := a.usageCached(key, ttl, fresh); ok {
		return c.v, c.err
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lim, err := a.CodexLimits(rctx, a.home(acc))
	c := cachedUsage{}
	if err != nil || (lim.Primary == nil && lim.Secondary == nil) {
		c.err = "unavailable"
	} else {
		c.v = &AccountUsage{Session: lim.Primary, Week: lim.Secondary}
	}
	a.putUsage(key, c)
	return c.v, c.err
}

// claudeServiceName is the Keychain service Claude Code stores an account's login under: the system default has none, a config dir is
// scoped by the first 8 hex digits of the SHA-256 of its NFC-normalized path.
func claudeServiceName(home string) string {
	if home == "" {
		return "Claude Code-credentials"
	}
	sum := sha256.Sum256([]byte(norm.NFC.String(home)))
	return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
}

// claudeUsage reads the subscription windows from Anthropic's undocumented OAuth usage endpoint. It never
// refreshes the token, and reports any failure as "unavailable".
func (a *Agents) claudeUsage(ctx context.Context, acc model.AgentAccount, fresh bool) (*AccountUsage, string) {
	key := "claude:" + acc.ID
	ttl := a.UsageTTL
	if ttl <= 0 {
		ttl = time.Minute
	}
	if c, ok := a.usageCached(key, ttl, fresh); ok {
		return c.v, c.err
	}
	rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	u, err := a.fetchClaudeUsage(rctx, a.home(acc))
	c := cachedUsage{v: u}
	if err != nil {
		c.err = "unavailable"
	}
	a.putUsage(key, c)
	return c.v, c.err
}

func (a *Agents) fetchClaudeUsage(ctx context.Context, home string) (*AccountUsage, error) {
	raw, err := a.Keychain(ctx, claudeServiceName(home))
	if err != nil {
		return nil, errors.New("no stored login")
	}
	var cred struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal([]byte(raw), &cred) != nil || cred.OAuth.AccessToken == "" {
		return nil, errors.New("unreadable login")
	}
	if cred.OAuth.ExpiresAt > 0 && cred.OAuth.ExpiresAt <= a.now().UnixMilli() {
		return nil, errors.New("login token expired")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.UsageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.OAuth.AccessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("usage request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage endpoint answered %d", resp.StatusCode)
	}
	return parseClaudeUsage(resp.Body)
}

type claudeWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

func (w *claudeWindow) window() *UsageWindow {
	if w == nil || w.Utilization == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339, w.ResetsAt)
	if err != nil {
		return nil
	}
	return &UsageWindow{UsedPct: *w.Utilization, ResetsAt: t}
}

func parseClaudeUsage(r io.Reader) (*AccountUsage, error) {
	var j struct {
		FiveHour *claudeWindow `json:"five_hour"`
		SevenDay *claudeWindow `json:"seven_day"`
	}
	if err := json.NewDecoder(r).Decode(&j); err != nil {
		return nil, err
	}
	u := &AccountUsage{Session: j.FiveHour.window(), Week: j.SevenDay.window()}
	if u.Session == nil && u.Week == nil {
		return nil, errors.New("unexpected usage shape")
	}
	return u, nil
}

// readCodexLimits asks `codex app-server` over stdio for account/rateLimits/read, under the account's CODEX_HOME.
// TODO: uncertain — codex app-server's protocol is experimental; the parsing is pinned to codex-cli 0.166, verify after codex upgrades
func (a *Agents) readCodexLimits(ctx context.Context, home string) (CodexLimits, error) {
	// like a run, it gets a private CODEX_HOME with a copy of the login and an empty config: the real
	// config.toml would start the user's MCP servers
	tmp, err := os.MkdirTemp("", "legos-codex-limits-")
	if err != nil {
		return CodexLimits{}, err
	}
	defer os.RemoveAll(tmp)
	src := home
	if src == "" {
		if src = os.Getenv("CODEX_HOME"); src == "" {
			h, herr := os.UserHomeDir()
			if herr != nil {
				return CodexLimits{}, herr
			}
			src = filepath.Join(h, ".codex")
		}
	}
	if auth, rerr := os.ReadFile(filepath.Join(src, "auth.json")); rerr == nil {
		if err := os.WriteFile(filepath.Join(tmp, "auth.json"), auth, 0o600); err != nil {
			return CodexLimits{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "config.toml"), nil, 0o600); err != nil {
		return CodexLimits{}, err
	}
	cmd := exec.CommandContext(ctx, "codex", "app-server")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+tmp)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return CodexLimits{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return CodexLimits{}, err
	}
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return CodexLimits{}, err
	}
	defer func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	for _, m := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"legos-factory","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"account/rateLimits/read"}`,
	} {
		if _, err := fmt.Fprintln(stdin, m); err != nil {
			return CodexLimits{}, err
		}
	}
	res := make(chan CodexLimits, 1)
	go func() {
		defer close(res)
		dec := json.NewDecoder(stdout)
		for {
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
			}
			if dec.Decode(&msg) != nil {
				return
			}
			if string(msg.ID) == "2" {
				if l, err := parseCodexLimits(msg.Result); err == nil {
					res <- l
				}
				return
			}
		}
	}()
	select {
	case l, ok := <-res:
		if !ok {
			return CodexLimits{}, errors.New("codex gave no rate limits")
		}
		return l, nil
	case <-ctx.Done():
		return CodexLimits{}, ctx.Err()
	}
}

func parseCodexLimits(raw json.RawMessage) (CodexLimits, error) {
	type win struct {
		UsedPercent *float64 `json:"usedPercent"`
		ResetsAt    int64    `json:"resetsAt"`
	}
	var j struct {
		RateLimits *struct {
			Primary   *win   `json:"primary"`
			Secondary *win   `json:"secondary"`
			PlanType  string `json:"planType"`
		} `json:"rateLimits"`
	}
	if err := json.Unmarshal(raw, &j); err != nil || j.RateLimits == nil {
		return CodexLimits{}, errors.New("unexpected rate limit shape")
	}
	conv := func(w *win) *UsageWindow {
		if w == nil || w.UsedPercent == nil {
			return nil
		}
		return &UsageWindow{UsedPct: *w.UsedPercent, ResetsAt: time.Unix(w.ResetsAt, 0).UTC()}
	}
	l := CodexLimits{Primary: conv(j.RateLimits.Primary), Secondary: conv(j.RateLimits.Secondary), Plan: j.RateLimits.PlanType}
	if l.Primary == nil && l.Secondary == nil {
		return l, errors.New("unexpected rate limit shape")
	}
	return l, nil
}

// ---- accounts ----

func (a *Agents) spec(id string) (AgentSpec, error) {
	s, ok := AgentByID(id)
	if !ok {
		return AgentSpec{}, fmt.Errorf("%w: %q", model.ErrAgentUnknown, id)
	}
	return s, nil
}

func (a *Agents) SetEnabled(ctx context.Context, id string, enabled bool) error {
	if _, err := a.spec(id); err != nil {
		return err
	}
	return a.Store.SetEnabled(ctx, id, enabled)
}

// safeDirName keeps a user id usable as a directory name.
func safeDirName(s string) string {
	if regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`).MatchString(s) && s != "." && s != ".." {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return "u-" + hex.EncodeToString(sum[:8])
}

type NewAccount struct {
	Account      AccountView `json:"account"`
	LoginCommand string      `json:"login_command"`
	EnvHint      string      `json:"env_hint"`
}

// loginLine is the shell command that signs an account in, and its env prefix; the system default (home "") has none.
func (s AgentSpec) loginLine(home string) (login, envHint string) {
	var hint []string
	if home != "" {
		for _, e := range s.accountEnv(home) {
			k, v, _ := strings.Cut(e, "=")
			hint = append(hint, k+"="+shellQuote(v))
		}
	}
	envHint = strings.Join(hint, " ")
	login = strings.Join(s.loginCmd, " ")
	if envHint != "" {
		login = envHint + " " + login
	}
	return login, envHint
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// CreateAccount makes the account's private home and returns the command that signs it in.
func (a *Agents) CreateAccount(ctx context.Context, u *model.User, agent, label string) (*NewAccount, error) {
	spec, err := a.spec(agent)
	if err != nil {
		return nil, err
	}
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 60 {
		return nil, fmt.Errorf("%w: label is required and at most 60 characters", ErrInvalid)
	}
	acc := &model.AgentAccount{ID: "acc_" + uuid.NewString()[:12], UserID: u.ID, Agent: agent, Label: label}
	acc.HomeDir = filepath.Join(a.DataDir, "accounts", safeDirName(u.ID), agent, acc.ID)
	if err := mkPrivateDirs(a.DataDir, acc.HomeDir); err != nil {
		return nil, fmt.Errorf("could not create the account directory: %w", err)
	}
	if spec.ID == "opencode" {
		for _, d := range []string{"data", "config", "state", "cache"} {
			if err := os.MkdirAll(filepath.Join(acc.HomeDir, d), 0o700); err != nil {
				_ = os.RemoveAll(acc.HomeDir)
				return nil, err
			}
		}
	}
	if err := a.Store.Create(ctx, acc); err != nil {
		_ = os.RemoveAll(acc.HomeDir)
		return nil, err
	}
	login, envHint := spec.loginLine(acc.HomeDir)
	return &NewAccount{
		Account:      AccountView{ID: acc.ID, Label: acc.Label, Status: "signed_out", LoginCommand: login},
		LoginCommand: login,
		EnvHint:      envHint,
	}, nil
}

// mkPrivateDirs creates dir and every missing parent below root with mode 0700.
func mkPrivateDirs(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return err
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		if err := os.Mkdir(cur, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := os.Chmod(cur, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Activate selects the user's account for the agent; SystemAccountID selects the system default.
func (a *Agents) Activate(ctx context.Context, u *model.User, agent, id string) error {
	if _, err := a.spec(agent); err != nil {
		return err
	}
	if id == SystemAccountID {
		return a.Store.Activate(ctx, u.ID, agent, "")
	}
	return a.Store.Activate(ctx, u.ID, agent, id)
}

// SetAccountRoles makes the account serve exactly roles for the user; they leave the user's other accounts of the agent.
func (a *Agents) SetAccountRoles(ctx context.Context, u *model.User, agent, id string, roles []string) error {
	spec, err := a.spec(agent)
	if err != nil {
		return err
	}
	if id != SystemAccountID {
		if _, err := a.Store.Get(ctx, u.ID, agent, id); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	var uniq []string
	for _, r := range roles {
		role := model.AgentRole(r)
		if !slices.Contains(allRoles, role) {
			return fmt.Errorf("%w: unknown role %q", ErrInvalid, r)
		}
		if !spec.Allows(role) {
			return fmt.Errorf("%w: %s cannot serve the %s role", ErrInvalid, spec.Name, r)
		}
		if !seen[r] {
			seen[r] = true
			uniq = append(uniq, r)
		}
	}
	return a.Store.SetRoleAccounts(ctx, u.ID, agent, id, uniq)
}

// DeleteAccount removes the account and its home; deleting the active one falls back to the system default.
func (a *Agents) DeleteAccount(ctx context.Context, u *model.User, agent, id string) error {
	if _, err := a.spec(agent); err != nil {
		return err
	}
	if id == SystemAccountID {
		return fmt.Errorf("%w: the system default account cannot be deleted", ErrInvalid)
	}
	acc, err := a.Store.Get(ctx, u.ID, agent, id)
	if err != nil {
		return err
	}
	if err := a.Store.Delete(ctx, u.ID, agent, id); err != nil {
		return err
	}
	owned := filepath.Join(a.DataDir, "accounts", safeDirName(u.ID)) + string(filepath.Separator)
	if strings.HasPrefix(filepath.Clean(acc.HomeDir)+string(filepath.Separator), owned) && filepath.Clean(acc.HomeDir) != filepath.Clean(owned) {
		_ = os.RemoveAll(acc.HomeDir)
	}
	spec, _ := AgentByID(agent)
	a.forget(spec, *acc)
	return nil
}

// Refresh re-reads one account's status and usage, bypassing the caches.
func (a *Agents) Refresh(ctx context.Context, u *model.User, agent, id string) (*AccountView, error) {
	spec, err := a.spec(agent)
	if err != nil {
		return nil, err
	}
	v, err := a.view(ctx, u.ID, spec, true)
	if err != nil {
		return nil, err
	}
	for i := range v.Accounts {
		if v.Accounts[i].ID == id {
			return &v.Accounts[i], nil
		}
	}
	return nil, model.ErrAgentAccountNotFound
}
