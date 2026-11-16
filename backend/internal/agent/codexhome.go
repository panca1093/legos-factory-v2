package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
			saveRotatedLogin(src, srcAuth, copied, after)
		}
		_ = os.RemoveAll(tmp)
	}
	return append(append([]string{}, env...), "CODEX_HOME="+tmp), cleanup, nil
}

var authLocks sync.Map // account dir -> *sync.Mutex

// saveRotatedLogin stores a login codex rotated during a run, unless the account's file changed since the run
// copied it: another run of the same account saved a newer one first, and that one is kept.
func saveRotatedLogin(src, srcAuth string, started, rotated []byte) {
	mu, _ := authLocks.LoadOrStore(src, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if cur, err := os.ReadFile(srcAuth); err == nil && !bytes.Equal(cur, started) {
		return
	}
	f, err := os.CreateTemp(src, ".auth-*")
	if err != nil {
		return
	}
	_ = f.Chmod(0o600)
	_, werr := f.Write(rotated)
	if cerr := f.Close(); werr != nil || cerr != nil || os.Rename(f.Name(), srcAuth) != nil {
		_ = os.Remove(f.Name())
	}
}
