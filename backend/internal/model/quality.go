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
	Checks   []Check `json:"checks"`
}

// Preset returns the built-in profile for a language. Repositories refine
// these during onboarding; the preset is a safe default, not a framework.
func Preset(language string) Profile {
	switch language {
	case "go":
		return Profile{Language: language, Checks: []Check{
			{Name: "go-test", Command: "go test ./..."},
			{Name: "go-vet", Command: "go vet ./..."},
		}}
	case "typescript":
		return Profile{Language: language, Checks: []Check{
			{Name: "typecheck", Command: "npm run -s typecheck"},
			{Name: "test", Command: "npm test -- --run"},
		}}
	case "rust":
		return Profile{Language: language, Checks: []Check{
			{Name: "cargo-check", Command: "cargo check"},
			{Name: "cargo-test", Command: "cargo test"},
		}}
	case "node":
		return Profile{Language: language, Checks: []Check{
			{Name: "test", Command: "npm test -- --run"},
		}}
	default:
		return Profile{Language: language, Checks: nil}
	}
}

func ParseProfile(profileJSON, language string) (Profile, error) {
	if profileJSON == "" || profileJSON == "{}" || profileJSON == "[]" {
		return Preset(language), nil
	}
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
	CreatedAt time.Time `json:"created_at"`
}
