package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// codexConfig is the config.toml of a run's private CODEX_HOME. It names no MCP servers on purpose: codex reads
// ~/.codex/config.toml, and any MCP server listed there would otherwise reach every run.
const codexConfig = "# written by legos-factory for one run; it lists no MCP servers on purpose\n"

// runEnv returns env for one run. A codex run gets its own CODEX_HOME holding a copy of the account's auth.json
// and a factory-written config.toml; cleanup removes it, saving a token codex refreshed back to the account.
// Other agents pass env through.
func (h *ACPHarness) runEnv(env []string) ([]string, func(), error) {
	noop := func() {}
	if h.Provider != "codex" {
		return env, noop, nil
	}
	src := os.Getenv("CODEX_HOME")
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "CODEX_HOME="); ok {
			src = v
		}
	}
	if src == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, noop, err
		}
		src = filepath.Join(home, ".codex")
	}
	tmp, err := os.MkdirTemp("", "legos-codex-")
	if err != nil {
		return nil, noop, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "config.toml"), []byte(codexConfig), 0o600); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, noop, err
	}
	srcAuth, tmpAuth := filepath.Join(src, "auth.json"), filepath.Join(tmp, "auth.json")
	copied, err := os.ReadFile(srcAuth)
	if err == nil {
		if err := os.WriteFile(tmpAuth, copied, 0o600); err != nil {
			_ = os.RemoveAll(tmp)
			return nil, noop, err
		}
	}
	cleanup := func() {
		if after, err := os.ReadFile(tmpAuth); err == nil && len(after) > 0 && !bytes.Equal(after, copied) {
			// codex rotated the login during the run; losing it would sign the account out
			if f, err := os.CreateTemp(src, ".auth-*"); err == nil {
				_ = f.Chmod(0o600)
				_, werr := f.Write(after)
				cerr := f.Close()
				if werr == nil && cerr == nil {
					_ = os.Rename(f.Name(), srcAuth)
				} else {
					_ = os.Remove(f.Name())
				}
			}
		}
		_ = os.RemoveAll(tmp)
	}
	return append(append([]string{}, env...), "CODEX_HOME="+tmp), cleanup, nil
}
