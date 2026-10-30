package profile

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"strings"

	"legos-factory/factory/internal/harness"
)

type Model struct {
	ID string `json:"id"`
}

// TODO: confirm the default model lists in plans.json against each plan's current offering before relying on them.
//
//go:embed plans.json
var defaultPlans []byte

var knownProviders = map[string]bool{"anthropic": true, "zai": true, "moonshot": true, "minimax": true,
	"openai": true, "opencode-go": true, "chatgpt": true, "claude": true}

func isCustom(c Credential) bool { return !knownProviders[c.Provider] }

// TODO: confirm these Anthropic-compatible and OpenAI-compatible endpoints against each provider's docs.
var anthropicBase = map[string]string{
	"zai":      "https://api.z.ai/api/anthropic",
	"moonshot": "https://api.moonshot.ai/anthropic",
	"minimax":  "https://api.minimax.io/anthropic",
}

var openaiBase = map[string]string{
	"zai":      "https://api.z.ai/api/paas/v4",
	"moonshot": "https://api.moonshot.ai/v1",
	"minimax":  "https://api.minimax.io/v1",
	"openai":   "https://api.openai.com/v1",
}

// Agents is the SPEC §8 compatibility matrix.
func Agents(c Credential) []string {
	if c.Kind == "subscription" {
		switch c.Provider {
		case "claude":
			return []string{"claude-agent-acp"}
		case "chatgpt":
			return []string{"codex-acp", "opencode"}
		case "opencode-go":
			return []string{"opencode"}
		}
		return nil
	}
	switch c.Provider {
	case "anthropic", "zai", "moonshot", "minimax":
		return []string{"claude-agent-acp", "opencode"}
	case "openai":
		return []string{"codex-acp", "opencode"}
	case "opencode-go":
		return []string{"opencode"}
	}
	return []string{"opencode", "claude-agent-acp", "codex-acp"}
}

// AgentCommand is the default ACP launch command for an agent.
func AgentCommand(agent string) []string {
	if agent == "opencode" {
		return []string{"opencode", "acp"}
	}
	return []string{agent}
}

func hasModel(ms []Model, id string) bool {
	for _, m := range ms {
		if m.ID == id {
			return true
		}
	}
	return false
}

func (s *Store) Models(ctx context.Context, c Credential) ([]Model, error) {
	if c.Kind == "subscription" {
		return s.planModels(c.Provider)
	}
	secret, err := s.Secret(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	var url string
	hdr := http.Header{}
	anthropic := c.Provider == "anthropic"
	if anthropic {
		url = strings.TrimSuffix(orDefault(c.BaseURL, "https://api.anthropic.com"), "/") + "/v1/models?limit=1000"
		hdr.Set("x-api-key", secret)
		hdr.Set("anthropic-version", "2023-06-01")
	} else {
		base := orDefault(c.BaseURL, openaiBase[c.Provider])
		if base == "" {
			return nil, fmt.Errorf("provider %q needs a base_url", c.Provider)
		}
		url = strings.TrimSuffix(base, "/") + "/models"
		hdr.Set("Authorization", "Bearer "+secret)
	}
	var all []Model
	pageURL := url
	for page := 0; page < 20; page++ {
		var out struct {
			Data    []Model `json:"data"`
			HasMore bool    `json:"has_more"`
			LastID  string  `json:"last_id"`
		}
		if err := s.getJSON(ctx, pageURL, hdr, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Data...)
		if !anthropic || !out.HasMore || out.LastID == "" {
			break
		}
		pageURL = url + "&after_id=" + neturl.QueryEscape(out.LastID)
	}
	return all, nil
}

func (s *Store) getJSON(ctx context.Context, url string, hdr http.Header, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header = hdr.Clone()
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("parse model list: %w", err)
	}
	return nil
}

func (s *Store) planModels(plan string) ([]Model, error) {
	raw := defaultPlans
	if s.plansFile != "" {
		if b, err := os.ReadFile(s.plansFile); err == nil {
			raw = b
		}
	}
	var plans map[string][]Model
	if err := json.Unmarshal(raw, &plans); err != nil {
		return nil, fmt.Errorf("parse plans file: %w", err)
	}
	ms, ok := plans[plan]
	if !ok {
		return nil, fmt.Errorf("no plan %q in plans file", plan)
	}
	return ms, nil
}

func orDefault(s, d string) string {
	if s != "" {
		return s
	}
	return d
}

// TODO: confirm opencode's env var names for zai/moonshot/minimax (taken from models.dev conventions).
var opencodeKeyEnv = map[string]string{
	"anthropic": "ANTHROPIC_API_KEY", "openai": "OPENAI_API_KEY",
	"zai": "ZHIPU_API_KEY", "moonshot": "MOONSHOT_API_KEY", "minimax": "MINIMAX_API_KEY",
}

// RunSpec is what a session needs from a profile; Files are relative to the sandbox home.
type RunSpec struct {
	Profile harness.Profile
	Env     map[string]string
	Files   map[string][]byte
}

const (
	opencodeAuthFile = ".local/share/opencode/auth.json"
	codexAuthFile    = ".codex/auth.json"
)

// authFile treats the stored secret as the login file itself (as written by `opencode auth login` / `codex login`).
func authFile(rel, secret string) (map[string][]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(secret), &obj); err != nil || len(obj) == 0 {
		return nil, fmt.Errorf("subscription secret must be the contents of %s (a JSON object)", rel)
	}
	return map[string][]byte{rel: []byte(secret)}, nil
}

// RunEnv builds the agent env and login files for a profile; credentials reach the agent only through this spec.
func (s *Store) RunEnv(ctx context.Context, p Profile) (RunSpec, error) {
	c, err := s.GetCredential(ctx, p.CredentialID)
	if err != nil {
		return RunSpec{}, err
	}
	secret, err := s.Secret(ctx, c.ID)
	if err != nil {
		return RunSpec{}, err
	}
	rs := RunSpec{Profile: harness.Profile{Agent: p.Agent, Provider: c.Provider, Model: p.Model, BillingKind: c.Kind}, Env: map[string]string{}}
	env := rs.Env
	switch p.Agent {
	case "claude-agent-acp":
		switch {
		case c.Kind == "subscription":
			env["CLAUDE_CODE_OAUTH_TOKEN"] = secret
		case c.Provider == "anthropic":
			env["ANTHROPIC_API_KEY"] = secret
			if c.BaseURL != "" {
				env["ANTHROPIC_BASE_URL"] = c.BaseURL
			}
		default:
			base := orDefault(c.BaseURL, anthropicBase[c.Provider])
			if base == "" {
				return rs, fmt.Errorf("provider %q needs a base_url for claude-agent-acp", c.Provider)
			}
			env["ANTHROPIC_BASE_URL"] = base
			env["ANTHROPIC_AUTH_TOKEN"] = secret
		}
		for _, k := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL"} {
			env[k] = p.Model
		}
	case "codex-acp":
		if c.Kind == "subscription" {
			if rs.Files, err = authFile(codexAuthFile, secret); err != nil {
				return rs, err
			}
			break
		}
		env["OPENAI_API_KEY"] = secret
		if c.BaseURL != "" {
			env["OPENAI_BASE_URL"] = c.BaseURL
		}
	case "opencode":
		name, known := opencodeKeyEnv[c.Provider]
		switch {
		case c.Kind == "subscription":
			if rs.Files, err = authFile(opencodeAuthFile, secret); err != nil {
				return rs, err
			}
		case known:
			env[name] = secret
			if c.BaseURL != "" {
				cfg, _ := json.Marshal(map[string]any{"provider": map[string]any{c.Provider: map[string]any{
					"options": map[string]string{"baseURL": c.BaseURL},
				}}})
				env["OPENCODE_CONFIG_CONTENT"] = string(cfg)
			}
		default:
			if c.BaseURL == "" {
				return rs, fmt.Errorf("custom provider %q needs a base_url for opencode", c.Provider)
			}
			auth, _ := json.Marshal(map[string]any{c.Provider: map[string]string{"type": "api", "key": secret}})
			cfg, _ := json.Marshal(map[string]any{"provider": map[string]any{c.Provider: map[string]any{
				"npm": "@ai-sdk/openai-compatible", "name": c.Provider,
				"options": map[string]string{"baseURL": c.BaseURL},
				"models":  map[string]any{p.Model: map[string]any{}},
			}}})
			rs.Files = map[string][]byte{opencodeAuthFile: auth}
			env["OPENCODE_CONFIG_CONTENT"] = string(cfg)
		}
	default:
		return rs, fmt.Errorf("unknown agent %q", p.Agent)
	}
	return rs, nil
}
