// Package quality executes deterministic verification checks.
package quality

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
)

// RunCheck executes a single check command inside dir with a timeout.
// The command is run via the shell, matching how developers run it locally.
func RunCheck(ctx context.Context, dir string, check model.Check, timeout time.Duration) model.CheckResult {
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
	r := model.CheckResult{Name: check.Name, Command: check.Command, Output: string(outBytes)}
	if ctx.Err() != nil {
		r.Outcome = model.Unverified
		r.Output = fmt.Sprintf("check timed out after %s\n%s", timeout, r.Output)
		return r
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			r.ExitCode = &code
			r.Outcome = model.Fail
		} else {
			r.Outcome = model.Unverified
			r.Output = fmt.Sprintf("could not execute: %v\n%s", err, r.Output)
		}
		return r
	}
	code := 0
	r.ExitCode = &code
	r.Outcome = model.Pass
	return r
}

// gorootBroken reports whether a GOROOT path lacks an actual Go toolchain.
func gorootBroken(goroot string) bool {
	_, err := os.Stat(filepath.Join(goroot, "bin", "go"))
	return err != nil
}
