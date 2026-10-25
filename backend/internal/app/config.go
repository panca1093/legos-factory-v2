package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"legos-factory/backend/internal/model"
)

func LoadConfig() (*model.Config, error) {
	c := &model.Config{
		Addr:              env("FACTORY_ADDR", "127.0.0.1:8080"),
		DataDir:           env("FACTORY_DATA_DIR", "./data"),
		AuthMode:          strings.ToLower(env("FACTORY_AUTH_MODE", "dev")),
		DevUserID:         env("FACTORY_DEV_USER_ID", "usr-dev"),
		DevUserName:       env("FACTORY_DEV_USER_NAME", "Local Developer"),
		OIDCIssuer:        env("FACTORY_OIDC_ISSUER", ""),
		OIDCClientID:      env("FACTORY_OIDC_CLIENT_ID", ""),
		OIDCClientSecret:  env("FACTORY_OIDC_CLIENT_SECRET", ""),
		SessionTTL:        envInt("FACTORY_SESSION_TTL", 60*60*24*7),
		OpenCodeURL:       env("FACTORY_OPENCODE_URL", ""),
		OpenCodeUser:      env("FACTORY_OPENCODE_USER", "opencode"),
		OpenCodePass:      env("FACTORY_OPENCODE_PASSWORD", ""),
		Harness:           strings.ToLower(env("FACTORY_HARNESS", "")),
		ACPCommand:        strings.Fields(env("FACTORY_ACP_COMMAND", "opencode acp")),
		MaxConcurrentRuns: envInt("FACTORY_MAX_CONCURRENT_RUNS", 2),
		RunTimeoutSec:     envInt("FACTORY_RUN_TIMEOUT", 600),
		SandboxMode:       env("FACTORY_SANDBOX_MODE", "local"),
		SecretKey:         env("FACTORY_SECRET_KEY", ""),
		FrontendOrigin:    env("FACTORY_FRONTEND_ORIGIN", "http://localhost:5173"),
	}
	if c.AuthMode != "dev" && c.AuthMode != "oidc" {
		return nil, fmt.Errorf("FACTORY_AUTH_MODE must be dev or oidc, got %q", c.AuthMode)
	}
	if c.AuthMode == "oidc" && (c.OIDCIssuer == "" || c.OIDCClientID == "") {
		return nil, fmt.Errorf("oidc auth requires FACTORY_OIDC_ISSUER and FACTORY_OIDC_CLIENT_ID")
	}
	if c.Harness != "" && c.Harness != "acp" {
		return nil, fmt.Errorf("FACTORY_HARNESS must be acp or empty, got %q", c.Harness)
	}
	if c.SandboxMode != "local" && c.SandboxMode != "docker" {
		return nil, fmt.Errorf("FACTORY_SANDBOX_MODE must be local or docker, got %q", c.SandboxMode)
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
