package profile

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/sandbox"
)

const smokeFile = "SMOKE.md"

const smokeRepo = `mkdir -p "$1" && cd "$1" && git init -q -b main &&
printf 'module smoke\n\ngo 1.21\n' > go.mod &&
printf 'package smoke\n\nimport "testing"\n\nfunc TestOK(t *testing.T) {}\n' > smoke_test.go &&
git add -A && git -c user.name=smoke -c user.email=smoke@localhost commit -q -m init`

// Smoke runs a builder session on a throwaway Go repo; a created file, passing `go test` and a commit set VerifiedAt.
func (s *Store) Smoke(ctx context.Context, h harness.Harness, rt sandbox.Runtime, p Profile) error {
	rs, err := s.RunEnv(ctx, p)
	if err != nil {
		return err
	}
	release, err := s.Acquire(ctx, p.CredentialID)
	if err != nil {
		return err
	}
	defer release()

	id := newID("smoke-")
	ws, err := rt.CreateWorkspace(ctx, id)
	if err != nil {
		return err
	}
	defer rt.DeleteWorkspace(context.WithoutCancel(ctx), id)
	sb, err := rt.Start(ctx, ws, nil)
	if err != nil {
		return err
	}
	defer sb.Stop(context.WithoutCancel(ctx))

	dir := ws.RepoDir()
	if out, errOut, code, err := sb.Exec(ctx, "", nil, "sh", "-c", smokeRepo, "sh", dir); err != nil || code != 0 {
		return fmt.Errorf("smoke repo setup: %v %s%s", err, out, errOut)
	}

	rs.Env["CLAUDE_CONFIG_DIR"] = path.Join(ws.HomeDir(), "smoke")
	sess, err := h.Start(ctx, harness.SessionSpec{Role: harness.RoleBuilder, Profile: rs.Profile, AgentCommand: AgentCommand(p.Agent),
		Dir: dir, Env: rs.Env, Files: rs.Files, Sandbox: sb})
	if err != nil {
		return fmt.Errorf("smoke start: %w", err)
	}
	var tokens int
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for ev := range sess.Events() {
			if ev.Kind == harness.EventUsage {
				u := ev.Usage
				tokens += u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheCreateTokens + len(u.Models)
			}
		}
	}()
	stop, perr := sess.Prompt(ctx, "Create the file "+smokeFile+" at the repository root containing the single line `smoke ok`. Change nothing else, then stop.")
	_ = sess.Close()
	<-drained
	if perr != nil {
		return fmt.Errorf("smoke prompt: %w", perr)
	}
	if stop != "end_turn" {
		return fmt.Errorf("smoke: stop reason %q, want end_turn", stop)
	}
	if tokens == 0 {
		return errors.New("smoke: agent reported no token usage")
	}

	content, _, code, err := sb.Exec(ctx, dir, nil, "cat", smokeFile)
	if err != nil || code != 0 {
		return fmt.Errorf("smoke: agent did not create %s", smokeFile)
	}
	if strings.TrimSpace(content) != "smoke ok" {
		return fmt.Errorf("smoke: %s contains %q, want %q", smokeFile, strings.TrimSpace(content), "smoke ok")
	}
	status, _, code, err := sb.Exec(ctx, dir, nil, "git", "status", "--porcelain")
	if err != nil || code != 0 {
		return fmt.Errorf("smoke: git status: %v", err)
	}
	if strings.TrimSpace(status) != "?? "+smokeFile {
		return fmt.Errorf("smoke: agent changed more than %s: %q", smokeFile, strings.TrimSpace(status))
	}
	if out, errOut, code, err := sb.Exec(ctx, dir, nil, "go", "test", "./..."); err != nil || code != 0 {
		return fmt.Errorf("smoke: go test failed: %v %s", err, strings.TrimSpace(out+errOut))
	}
	if _, changed, err := sandbox.CommitAll(ctx, sb, dir, "smoke"); err != nil {
		return err
	} else if !changed {
		return fmt.Errorf("smoke: no commit produced")
	}
	return s.markVerified(ctx, p.ID)
}
