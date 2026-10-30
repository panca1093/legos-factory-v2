package profile

import (
	"context"
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
	hp, env, err := s.RunEnv(ctx, p)
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

	env["CLAUDE_CONFIG_DIR"] = path.Join(ws.HomeDir(), "smoke")
	sess, err := h.Start(ctx, harness.SessionSpec{Role: harness.RoleBuilder, Profile: hp, AgentCommand: AgentCommand(p.Agent),
		Dir: dir, Env: env, Sandbox: sb})
	if err != nil {
		return fmt.Errorf("smoke start: %w", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	_, perr := sess.Prompt(ctx, "Create the file "+smokeFile+" at the repository root containing the single line `smoke ok`. Change nothing else, then stop.")
	_ = sess.Close()
	if perr != nil {
		return fmt.Errorf("smoke prompt: %w", perr)
	}

	if _, _, code, err := sb.Exec(ctx, dir, nil, "test", "-f", smokeFile); err != nil || code != 0 {
		return fmt.Errorf("smoke: agent did not create %s", smokeFile)
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
