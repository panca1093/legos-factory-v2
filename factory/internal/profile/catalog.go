package profile

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	if c.Provider == "anthropic" {
		url = strings.TrimSuffix(orDefault(c.BaseURL, "https://api.anthropic.com"), "/") + "/v1/models"
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header = hdr
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	var out struct {
		Data []Model `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse model list: %w", err)
	}
	return out.Data, nil
}

func (s *Store) planModels(plan string) ([]Model, error) {
	raw := defaultPlans
	if s.plansFile != "" {
		if b, err := os.ReadFile(s.plansFile); err == nil {
			raw = b
		} else if os.IsNotExist(err) {
			_ = os.WriteFile(s.plansFile, defaultPlans, 0o644)
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

// RunEnv builds the agent env for a profile; credentials reach the agent only through this map.
func (s *Store) RunEnv(ctx context.Context, p Profile) (harness.Profile, map[string]string, error) {
	c, err := s.GetCredential(ctx, p.CredentialID)
	if err != nil {
		return harness.Profile{}, nil, err
	}
	secret, err := s.Secret(ctx, c.ID)
	if err != nil {
		return harness.Profile{}, nil, err
	}
	hp := harness.Profile{Agent: p.Agent, Provider: c.Provider, Model: p.Model, BillingKind: c.Kind}
	env := map[string]string{}
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
				return hp, nil, fmt.Errorf("provider %q needs a base_url for claude-agent-acp", c.Provider)
			}
			env["ANTHROPIC_BASE_URL"] = base
			env["ANTHROPIC_AUTH_TOKEN"] = secret
		}
		for _, k := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL"} {
			env[k] = p.Model
		}
	case "codex-acp":
		if c.Kind == "subscription" {
			// TODO: ChatGPT login needs codex's auth.json written into CODEX_HOME inside the sandbox.
			return hp, nil, ErrAuthFileRequired
		}
		env["OPENAI_API_KEY"] = secret
		if c.BaseURL != "" {
			env["OPENAI_BASE_URL"] = c.BaseURL
		}
	case "opencode":
		name, ok := opencodeKeyEnv[c.Provider]
		if c.Kind == "subscription" || !ok {
			// TODO: subscriptions and custom providers need opencode's auth.json; OPENCODE_CONFIG_CONTENT is taken by the harness for permissions.
			return hp, nil, ErrAuthFileRequired
		}
		env[name] = secret
	default:
		return hp, nil, fmt.Errorf("unknown agent %q", p.Agent)
	}
	return hp, env, nil
}
