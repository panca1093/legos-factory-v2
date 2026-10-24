// Package quality implements the contract-driven quality system: language
// profiles, deterministic check execution and merge-readiness evaluation.
package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// Result is the recorded result of one executed check.
type Result struct {
	Name     string  `json:"name"`
	Command  string  `json:"command"`
	Outcome  Outcome `json:"outcome"`
	ExitCode *int    `json:"exit_code"`
	Output   string  `json:"output"`
	Duration time.Duration
}

// RunCheck executes a single check command inside dir with a timeout.
// The command is run via the shell, matching how developers run it locally.
func RunCheck(ctx context.Context, dir string, check Check, timeout time.Duration) Result {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	env := os.Environ()
	if goroot := os.Getenv("GOROOT"); goroot != "" && gorootBroken(goroot) {
		// tolerate a stale GOROOT in the environment (common on misconfigured
		// local machines); child toolchains fall back to the realpath of go.
		filtered := env[:0]
		for _, kv := range env {
			if !strings.HasPrefix(kv, "GOROOT=") {
				filtered = append(filtered, kv)
			}
		}
		env = filtered
	}

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", check.Command)
	cmd.Dir = dir
	cmd.Env = env
	outBytes, err := cmd.CombinedOutput()
	r := Result{Name: check.Name, Command: check.Command, Output: string(outBytes)}
	if ctx.Err() != nil {
		r.Outcome = Unverified
		r.Output = fmt.Sprintf("check timed out after %s\n%s", timeout, r.Output)
		return r
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			r.ExitCode = &code
			r.Outcome = Fail
		} else {
			r.Outcome = Unverified
			r.Output = fmt.Sprintf("could not execute: %v\n%s", err, r.Output)
		}
		return r
	}
	code := 0
	r.ExitCode = &code
	r.Outcome = Pass
	return r
}

// gorootBroken reports whether a GOROOT path lacks an actual Go toolchain.
func gorootBroken(goroot string) bool {
	_, err := os.Stat(filepath.Join(goroot, "bin", "go"))
	return err != nil
}
