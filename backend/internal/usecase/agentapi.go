package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"legos-factory/backend/internal/model"
)

var apiModelVars = []string{"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"}

// legacyAPIKeys are the settings.json env keys the startup migration moves into the database.
var legacyAPIKeys = append([]string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"}, apiModelVars...)

// APIModels maps Claude's three model families to the provider's model names.
type APIModels struct {
	Opus   string `json:"opus"`
	Sonnet string `json:"sonnet"`
	Haiku  string `json:"haiku"`
}

func (m APIModels) get(family string) string {
	switch family {
	case "opus":
		return m.Opus
	case "sonnet":
		return m.Sonnet
	case "haiku":
		return m.Haiku
	}
	return ""
}

// filled lends an empty family the model of a set one (haiku from sonnet, sonnet and opus from each other), so all
// three ANTHROPIC_DEFAULT_*_MODEL vars are set whenever any is. The stored map stays as entered.
func (m APIModels) filled() APIModels {
	first := func(vs ...string) string {
		for _, v := range vs {
			if v != "" {
				return v
			}
		}
		return ""
	}
	return APIModels{Opus: first(m.Opus, m.Sonnet, m.Haiku), Sonnet: first(m.Sonnet, m.Opus, m.Haiku), Haiku: first(m.Haiku, m.Sonnet, m.Opus)}
}

// modelFamily folds a Claude model id into opus, sonnet or haiku; "" when it belongs to none.
func modelFamily(id string) string {
	l := strings.ToLower(id)
	switch {
	case l == "default" || l == "" || strings.Contains(l, "opus"):
		return "opus"
	case strings.Contains(l, "sonnet"):
		return "sonnet"
	case strings.Contains(l, "haiku"):
		return "haiku"
	}
	return ""
}

// APIInput is what a client sends to make an account an API-key account.
type APIInput struct {
	Provider string    `json:"provider"`
	BaseURL  string    `json:"base_url"`
	APIKey   string    `json:"api_key"`
	Models   APIModels `json:"models"`
}

// APIView is an API-key account as the client sees it; the key is never part of it.
type APIView struct {
	Provider string    `json:"provider"`
	BaseURL  string    `json:"base_url"`
	Host     string    `json:"host"`
	Models   APIModels `json:"models"`
	HasKey   bool      `json:"has_key"`
}

func parseAPIModels(raw string) APIModels {
	var m APIModels
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}

func apiView(acc model.AgentAccount) *APIView {
	if acc.APIBaseURL == "" {
		return nil
	}
	return &APIView{Provider: acc.APIProvider, BaseURL: acc.APIBaseURL, Host: hostOf(acc.APIBaseURL),
		Models: parseAPIModels(acc.APIModels), HasKey: len(acc.APIKeySealed) > 0}
}

var (
	modelNameRE = regexp.MustCompile(`^\S{1,100}$`)
	errNoSecret = fmt.Errorf("%w: the factory has no secret key, so API keys cannot be stored (set FACTORY_SECRET_KEY)", ErrInvalid)
)

func validateAPI(in *APIInput, needKey bool) error {
	switch in.Provider {
	case "":
		in.Provider = "custom"
	case "deepseek", "glm", "custom":
	default:
		return fmt.Errorf("%w: provider must be deepseek, glm or custom", ErrInvalid)
	}
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	u, err := url.Parse(in.BaseURL)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: base_url must be an absolute https URL", ErrInvalid)
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return fmt.Errorf("%w: base_url must be an absolute https URL (http is only allowed for localhost)", ErrInvalid)
	}
	in.APIKey = strings.TrimSpace(in.APIKey)
	if needKey && in.APIKey == "" {
		return fmt.Errorf("%w: api_key is required", ErrInvalid)
	}
	if strings.ContainsFunc(in.APIKey, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return fmt.Errorf("%w: api_key must not contain whitespace", ErrInvalid)
	}
	for _, v := range []string{in.Models.Opus, in.Models.Sonnet, in.Models.Haiku} {
		if v != "" && !modelNameRE.MatchString(v) {
			return fmt.Errorf("%w: each model name is at most 100 characters with no whitespace", ErrInvalid)
		}
	}
	if in.Models.Opus == "" && in.Models.Sonnet == "" {
		return fmt.Errorf("%w: set at least the opus or sonnet model", ErrInvalid)
	}
	return nil
}

// applyAPI validates in, seals its key and sets the API fields on acc. An empty key keeps acc's stored one.
func (a *Agents) applyAPI(acc *model.AgentAccount, spec AgentSpec, in APIInput) error {
	if spec.ID != "claude" {
		return fmt.Errorf("%w: only Claude accounts can use an API key", ErrInvalid)
	}
	if a.Secrets == nil {
		return errNoSecret
	}
	if err := validateAPI(&in, len(acc.APIKeySealed) == 0); err != nil {
		return err
	}
	if in.APIKey != "" {
		sealed, err := a.Secrets.Seal([]byte(in.APIKey))
		if err != nil {
			return errors.New("could not seal the API key")
		}
		acc.APIKeySealed = sealed
	}
	models, _ := json.Marshal(in.Models)
	acc.APIProvider, acc.APIBaseURL, acc.APIModels = in.Provider, in.BaseURL, string(models)
	return nil
}

// SetAccountAPI makes the account an API-key account, or edits it; an empty api_key keeps the stored key.
func (a *Agents) SetAccountAPI(ctx context.Context, u *model.User, agent, id string, in APIInput) (*AccountView, error) {
	spec, err := a.spec(agent)
	if err != nil {
		return nil, err
	}
	if spec.ID != "claude" {
		return nil, fmt.Errorf("%w: only Claude accounts can use an API key", ErrInvalid)
	}
	if id == SystemAccountID {
		return nil, fmt.Errorf("%w: the system default account cannot use an API key", ErrInvalid)
	}
	acc, err := a.Store.Get(ctx, u.ID, agent, id)
	if err != nil {
		return nil, err
	}
	if err := a.applyAPI(acc, spec, in); err != nil {
		return nil, err
	}
	if err := a.Store.SetAPI(ctx, u.ID, agent, id, acc.APIProvider, acc.APIBaseURL, acc.APIKeySealed, acc.APIModels, ""); err != nil {
		return nil, err
	}
	a.forget(spec, *acc)
	return a.Refresh(ctx, u, agent, id)
}

// claudeAccountEnv is the provider environment of a Claude account: its database settings, else the legacy settings.json lift.
func (a *Agents) claudeAccountEnv(acc model.AgentAccount) ([]string, error) {
	if acc.APIBaseURL == "" {
		env, _, err := claudeAPIEnv(acc.HomeDir)
		return env, err
	}
	if a.Secrets == nil {
		return nil, errors.New("the factory has no secret key to open the API key")
	}
	if len(acc.APIKeySealed) == 0 {
		return nil, errors.New("no API key is stored")
	}
	key, err := a.Secrets.Open(acc.APIKeySealed)
	if err != nil {
		return nil, errors.New("the stored API key cannot be opened")
	}
	// the other key var is set blank: it overrides the backend's own key, which os/exec would otherwise hand to this provider
	env := []string{"ANTHROPIC_BASE_URL=" + acc.APIBaseURL, "ANTHROPIC_AUTH_TOKEN=" + string(key), "ANTHROPIC_API_KEY="}
	if acc.APIKeyHeader == "api_key" {
		env[1], env[2] = "ANTHROPIC_AUTH_TOKEN=", "ANTHROPIC_API_KEY="+string(key)
	}
	m := parseAPIModels(acc.APIModels).filled()
	for i, v := range []string{m.Opus, m.Sonnet, m.Haiku} {
		if v != "" {
			env = append(env, apiModelVars[i]+"="+v)
		}
	}
	return env, nil
}

// RoleAPI is the provider side of the account serving a role: Models is nil for a login account.
type RoleAPI struct {
	// AccountID is the account serving the role, SystemAccountID for the system default; empty when no user is known.
	AccountID string
	// Sig changes whenever the account, its endpoint or its model map change, so it keys caches of model lists.
	Sig    string
	Host   string
	Models *APIModels
}

func (a *Agents) RoleAPI(ctx context.Context, agent, userID string, role model.AgentRole) (RoleAPI, error) {
	if userID == "" {
		return RoleAPI{}, nil
	}
	acc, err := a.roleAccount(ctx, userID, agent, role)
	if err != nil {
		return RoleAPI{}, err
	}
	return roleAPIOf(agent, acc), nil
}

func roleAPIOf(agent string, acc *model.AgentAccount) RoleAPI {
	if acc == nil {
		return RoleAPI{AccountID: SystemAccountID}
	}
	r := RoleAPI{Sig: acc.ID, AccountID: acc.ID}
	switch {
	case acc.APIBaseURL != "":
		m := parseAPIModels(acc.APIModels).filled()
		r.Models, r.Host = &m, hostOf(acc.APIBaseURL)
		r.Sig += "\x00" + acc.APIBaseURL + "\x00" + acc.APIModels
	case agent == "claude":
		env, host, err := claudeAPIEnv(acc.HomeDir)
		if err != nil || len(env) == 0 {
			break
		}
		var m APIModels
		for _, e := range env {
			for i, k := range apiModelVars {
				if v, ok := strings.CutPrefix(e, k+"="); ok {
					*[]*string{&m.Opus, &m.Sonnet, &m.Haiku}[i] = v
				}
			}
		}
		r.Models, r.Host = &m, host
		mj, _ := json.Marshal(m)
		r.Sig += "\x00" + host + "\x00" + string(mj)
	}
	return r
}

// FoldModels keeps, for each family the provider maps, one listed model of that family and names it after the mapped
// model: the first one in the allowed set when allowed is non-empty, else the first listed. The listed ids stay as they
// are so selecting one still matches what the agent offers.
func FoldModels(m APIModels, ms []model.ModelInfo, allowed map[string]bool) []model.ModelInfo {
	byFam := map[string]int{}
	for i, mi := range ms {
		fam := modelFamily(mi.ModelID)
		if fam == "" || m.get(fam) == "" {
			continue
		}
		j, ok := byFam[fam]
		if !ok || (len(allowed) > 0 && !allowed[ms[j].ProviderID+"/"+ms[j].ModelID] && allowed[mi.ProviderID+"/"+mi.ModelID]) {
			byFam[fam] = i
		}
	}
	out := make([]model.ModelInfo, 0, 3)
	for i, mi := range ms {
		if fam := modelFamily(mi.ModelID); fam != "" && byFam[fam] == i && m.get(fam) != "" {
			mi.Name = m.get(fam)
			out = append(out, mi)
		}
	}
	return out
}

// Served is the provider model and host behind a model ref of the role's account; empty for a login account.
func (a *Agents) Served(ctx context.Context, agent, userID string, role model.AgentRole, modelRef string) (served, host string) {
	r, err := a.RoleAPI(ctx, agent, userID, role)
	if err != nil || r.Models == nil {
		return "", ""
	}
	_, id := SplitModelRef(modelRef)
	return r.Models.get(modelFamily(id)), r.Host
}

// RunAccount is the account that serves one run, taken when the run starts: the account can change while it runs.
type RunAccount struct {
	Agent, AccountID, Host string
	Models                 *APIModels
}

func (a *Agents) RunAccount(ctx context.Context, agent, userID string, role model.AgentRole) RunAccount {
	r, err := a.RoleAPI(ctx, agent, userID, role)
	if err != nil || r.AccountID == "" {
		return RunAccount{}
	}
	return RunAccount{Agent: agent, AccountID: r.AccountID, Host: r.Host, Models: r.Models}
}

// Stamp records the account on u. A run on an API-key account has no provider pricing, so its cost is dropped
// rather than shown at the rate of the Claude model the agent believes it called.
func (ra RunAccount) Stamp(u *model.UsageRecord) {
	u.Agent, u.AccountID, u.ProviderHost = ra.Agent, ra.AccountID, ra.Host
	if ra.Models != nil {
		u.ServedModel = ra.Models.get(modelFamily(u.Model))
	}
	if ra.Host != "" {
		u.CostMicros, u.Source = 0, "unknown"
	}
}

// MigrateAPIAccounts moves the API settings of hand-made Claude accounts from their settings.json into the database and
// removes them from the file. It is safe to run on every start. Without a secret key it does nothing.
func (a *Agents) MigrateAPIAccounts(ctx context.Context) error {
	if a.Secrets == nil {
		return nil
	}
	accs, err := a.Store.ListAll(ctx, "claude")
	if err != nil {
		return err
	}
	var errs []error
	for _, acc := range accs {
		if acc.HomeDir == "" {
			continue
		}
		if err := a.migrateAPIAccount(ctx, acc); err != nil {
			errs = append(errs, fmt.Errorf("account %q: %w", acc.Label, err))
		}
	}
	return errors.Join(errs...)
}

func (a *Agents) migrateAPIAccount(ctx context.Context, acc model.AgentAccount) error {
	path := filepath.Join(acc.HomeDir, "settings.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot read settings.json")
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return errors.New("settings.json is not valid JSON")
	}
	var env map[string]json.RawMessage
	if json.Unmarshal(top["env"], &env) != nil || env == nil {
		return nil
	}
	val := func(k string) string {
		var s string
		_ = json.Unmarshal(env[k], &s)
		return s
	}
	if acc.APIBaseURL == "" {
		base, key, header := val("ANTHROPIC_BASE_URL"), val("ANTHROPIC_AUTH_TOKEN"), "auth_token"
		if key == "" {
			key, header = val("ANTHROPIC_API_KEY"), "api_key"
		}
		if base == "" || key == "" {
			return nil
		}
		sealed, err := a.Secrets.Seal([]byte(key))
		if err != nil {
			return errors.New("could not seal the API key")
		}
		models, _ := json.Marshal(APIModels{Opus: val(apiModelVars[0]), Sonnet: val(apiModelVars[1]), Haiku: val(apiModelVars[2])})
		if err := a.Store.SetAPI(ctx, acc.UserID, acc.Agent, acc.ID, guessProvider(base), base, sealed, string(models), header); err != nil {
			return err
		}
	}
	changed := false
	for _, k := range legacyAPIKeys {
		if _, ok := env[k]; ok {
			delete(env, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	top["env"], _ = json.Marshal(env)
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	if writeFileAtomic(path, append(out, '\n'), 0o600) != nil {
		return errors.New("cannot write settings.json")
	}
	return nil
}

func guessProvider(base string) string {
	h := strings.ToLower(hostOf(base))
	switch {
	case strings.Contains(h, "deepseek"):
		return "deepseek"
	case strings.Contains(h, "z.ai"), strings.Contains(h, "bigmodel"):
		return "glm"
	}
	return "custom"
}
