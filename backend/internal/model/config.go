package model

// Config is the resolved application configuration. Values come from
// environment variables with sensible local-development defaults.
type Config struct {
	Addr        string // HTTP listen address
	DataDir     string // SQLite database, workspaces, artifacts
	DatabaseURL string // resolved SQLite file path (empty => derived from DataDir)

	AuthMode         string // "dev" or "oidc"
	DevUserID        string // local development identity (auth.mode=dev only)
	DevUserName      string
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	SessionTTL       int // seconds

	OpenCodeURL  string // base URL of a running `opencode serve`
	OpenCodeUser string
	OpenCodePass string

	MaxConcurrentRuns int    // global worker pool size
	RunTimeoutSec     int    // per job execution timeout
	SandboxMode       string // "local" (trusted) or "docker" (requires daemon)

	FrontendOrigin string // allowed CORS origin for the Vite dev server
}
