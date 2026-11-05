package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// Outcome values. An unexecuted check is never treated as success.
type Outcome string

const (
	Pass       Outcome = "PASS"
	Fail       Outcome = "FAIL"
	Unverified Outcome = "UNVERIFIED"
)

// Check is one configured verification command.
type Check struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

// Profile defines the deterministic verification commands for a project.
type Profile struct {
	Language string  `json:"language"`
	Setup    string  `json:"setup,omitempty"` // installs dependencies in a fresh checkout before checks or a builder run
	Checks   []Check `json:"checks"`
}

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
