package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Outcome values. An unexecuted check is never treated as success.
type Outcome string

const (
	Pass       Outcome = "PASS"
	Fail       Outcome = "FAIL"
	Unverified Outcome = "UNVERIFIED"
)

// Check is one configured verification command. Dir is relative to the workspace root; Source names the repo line it was copied from.
type Check struct {
	Name     string `json:"name"`
	Command  string `json:"command"`
	Dir      string `json:"dir,omitempty"`
	Optional bool   `json:"optional,omitempty"` // recorded, never fails verification, never counts as evidence
	Source   string `json:"source,omitempty"`
	PerRun   bool   `json:"per_run,omitempty"` // setup only: runs after the run's own database exists
}

// SetupSteps reads a v1 setup string or a v2 list of steps.
type SetupSteps []Check

func (s *SetupSteps) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*s = nil
		if strings.TrimSpace(one) != "" {
			*s = SetupSteps{{Name: "setup", Command: one}}
		}
		return nil
	}
	var many []Check
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

// ProfileSource is a repo file the profile was derived from, hashed when the profile was approved.
type ProfileSource struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// ProfileServices are the compose services checks need; Ports maps each to its container port.
type ProfileServices struct {
	Compose string         `json:"compose"`
	Names   []string       `json:"names"`
	Ports   map[string]int `json:"ports"`
}

// ProfileDatabase says how to create a run's own database: AdminURL connects to an existing database on Service.
type ProfileDatabase struct {
	Service  string `json:"service"`
	AdminURL string `json:"admin_url"`
}

// SmokeProcess is an app process the Playwright smoke run starts on a free port.
// Env is added to the profile env; besides the run's placeholders it may use {{port.<name>}} for another process's port.
type SmokeProcess struct {
	Name    string            `json:"name"`
	Dir     string            `json:"dir,omitempty"`
	Command string            `json:"command"`
	PortEnv string            `json:"port_env"`
	Ready   string            `json:"ready"`
	Env     map[string]string `json:"env,omitempty"`
}

// SmokeLogin signs the smoke browser in with credentials read from the resolved env.
type SmokeLogin struct {
	Path        string `json:"path"`
	UserEnv     string `json:"user_env"`
	PasswordEnv string `json:"password_env"`
}

// Smoke configures the factory-owned Playwright smoke run.
type Smoke struct {
	Start      []SmokeProcess `json:"start"`
	Base       string         `json:"base"`
	Login      *SmokeLogin    `json:"login,omitempty"`
	RoutesFrom string         `json:"routes_from"`
	Blocking   bool           `json:"blocking,omitempty"`
	// Params fills dynamic route segments ([id]): a literal, or "sql:<query>" run against the run's database (first column of the first row).
	Params map[string]string `json:"params,omitempty"`
	// ReadyTimeoutSec bounds how long each process may take to answer 200 on its ready path; 0 means 90.
	ReadyTimeoutSec int `json:"ready_timeout_sec,omitempty"`
}

// Profile defines the deterministic verification commands for a project.
// Placeholders in Env and Database.AdminURL: {{<service>}} → 127.0.0.1:<host port>, {{db}} → the run's database and bucket name, {{redis_db}} → the run's Redis index.
type Profile struct {
	Version  int               `json:"version,omitempty"`
	Language string            `json:"language"`
	Sources  []ProfileSource   `json:"sources,omitempty"`
	Services *ProfileServices  `json:"services,omitempty"`
	Database *ProfileDatabase  `json:"database,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Setup    SetupSteps        `json:"setup,omitempty"` // installs dependencies in a fresh checkout before checks or a builder run
	Checks   []Check           `json:"checks"`
	Smoke    *Smoke            `json:"smoke,omitempty"`
}

// NeedsStack reports whether checks need the factory's service stack.
func (p Profile) NeedsStack() bool { return p.Services != nil && len(p.Services.Names) > 0 }

func ParseProfile(profileJSON, language string) (Profile, error) {
	var p Profile
	if err := json.Unmarshal([]byte(profileJSON), &p); err != nil {
		return Profile{}, fmt.Errorf("invalid quality profile: %w", err)
	}
	if p.Language == "" {
		p.Language = language
	}
	return p, nil
}

// CheckResult is the in-memory result of one executed check.
type CheckResult struct {
	Name     string  `json:"name"`
	Command  string  `json:"command"`
	Outcome  Outcome `json:"outcome"`
	ExitCode *int    `json:"exit_code"`
	Output   string  `json:"output"`
	Duration time.Duration
}

// QualityCheck is a persisted check result, tied to a work-order revision.
type QualityCheck struct {
	ID        string    `json:"id"`
	WOID      string    `json:"-"`
	Name      string    `json:"name"`
	Command   string    `json:"command"`
	Outcome   string    `json:"outcome"`
	ExitCode  int       `json:"exit_code"`
	Output    string    `json:"output"`
	Optional  bool      `json:"optional,omitempty"`
	Revision  int64     `json:"revision"`
	CommitSHA string    `json:"commit_sha,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// FileDiff is one changed file against the work order's base commit; Hunks
// is the unified-diff text from the first @@ on, empty for binary files.
type FileDiff struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Hunks   string `json:"hunks"`
}
