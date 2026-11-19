package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"legos-factory/backend/internal/model"
)

const (
	MaxLSPFrame  = 16 << 20
	lspTailBytes = 4 << 10
)

// LSPAllowlist is code, not config: only these servers are ever started, with exactly these arguments.
var LSPAllowlist = []model.LSPServerSpec{
	{ID: "gopls", Languages: []string{"go", "go.mod"}, Binary: "gopls", Args: nil,
		Install: "go install golang.org/x/tools/gopls@latest"},
	{ID: "tsls", Languages: []string{"ts", "tsx", "js", "jsx"}, Binary: "typescript-language-server", Args: []string{"--stdio"},
		Install: "npm i -g typescript-language-server typescript", NeedsNode: true},
}

func LSPSpecByID(id string) (model.LSPServerSpec, bool) {
	for _, s := range LSPAllowlist {
		if s.ID == id {
			return s, true
		}
	}
	return model.LSPServerSpec{}, false
}

// LSPHost discovers and spawns language servers. The zero value works; the function fields exist for tests.
type LSPHost struct {
	Environ      func() []string
	Shell        string
	ShellTimeout time.Duration
	GoEnv        func(ctx context.Context) map[string]string
	NPMPrefix    func(ctx context.Context) string
	FixedDirs    []string

	loginMu   sync.Mutex
	loginDone bool
	loginPath string

	mu        sync.Mutex
	path      []string
	goenv     map[string]string
	builtWith map[string]string
}

func (h *LSPHost) environ() []string {
	if h.Environ != nil {
		return h.Environ()
	}
	return os.Environ()
}

func envGet(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

var goEnvKeys = []string{"GOBIN", "GOPATH", "GOMODCACHE", "GOFLAGS", "GOPROXY", "GOPRIVATE", "GONOSUMDB", "GOTOOLCHAIN"}

func (h *LSPHost) loginShellPath(ctx context.Context) string {
	h.loginMu.Lock()
	defer h.loginMu.Unlock()
	if h.loginDone {
		return h.loginPath
	}
	h.loginDone = true
	shell := h.Shell
	if shell == "" {
		shell = envGet(h.environ(), "SHELL")
	}
	if shell == "" {
		return ""
	}
	d := h.ShellTimeout
	if d == 0 {
		d = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-lic", `printf '__LEGOS_PATH__%s__LEGOS_PATH__' "$PATH"`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	s := string(out)
	const m = "__LEGOS_PATH__"
	i := strings.Index(s, m)
	j := strings.LastIndex(s, m)
	if i < 0 || j <= i {
		return ""
	}
	h.loginPath = s[i+len(m) : j]
	return h.loginPath
}

func (h *LSPHost) runGoEnv(ctx context.Context, path []string) map[string]string {
	if h.GoEnv != nil {
		return h.GoEnv(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", append([]string{"env", "-json"}, goEnvKeys...)...)
	cmd.Env = h.processEnv(path)
	out, err := cmd.Output()
	if err != nil {
		return map[string]string{}
	}
	m := map[string]string{}
	_ = json.Unmarshal(out, &m)
	return m
}

func (h *LSPHost) processEnv(path []string) []string {
	env := h.environ()
	if len(path) > 0 {
		env = append(env, "PATH="+strings.Join(path, string(os.PathListSeparator)))
	}
	return env
}

func (h *LSPHost) runNPMPrefix(ctx context.Context, path []string) string {
	if h.NPMPrefix != nil {
		return h.NPMPrefix(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "npm", "prefix", "-g")
	cmd.Env = h.processEnv(path)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// refresh recomputes the augmented PATH and Go environment; the login-shell PATH is read once per process.
func (h *LSPHost) refresh(ctx context.Context) {
	var dirs []string
	add := func(list string) {
		for _, d := range filepath.SplitList(list) {
			if d != "" && filepath.IsAbs(d) {
				dirs = append(dirs, d)
			}
		}
	}
	add(envGet(h.environ(), "PATH"))
	add(h.loginShellPath(ctx))
	goenv := h.runGoEnv(ctx, dirs)
	if d := goenv["GOBIN"]; d != "" {
		add(d)
	}
	for _, p := range filepath.SplitList(goenv["GOPATH"]) {
		add(filepath.Join(p, "bin"))
	}
	if h.FixedDirs != nil {
		dirs = append(dirs, h.FixedDirs...)
	} else {
		dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
		if home := envGet(h.environ(), "HOME"); home != "" {
			dirs = append(dirs, filepath.Join(home, ".volta", "bin"), filepath.Join(home, ".bun", "bin"))
		}
	}
	if p := h.runNPMPrefix(ctx, dirs); p != "" {
		dirs = append(dirs, filepath.Join(p, "bin"))
	}
	seen := map[string]bool{}
	var path []string
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			path = append(path, d)
		}
	}
	h.mu.Lock()
	h.path, h.goenv, h.builtWith = path, goenv, map[string]string{}
	h.mu.Unlock()
}

func (h *LSPHost) lookPath(name string) string {
	h.mu.Lock()
	dirs := append([]string(nil), h.path...)
	h.mu.Unlock()
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if executable(p) {
			return p
		}
	}
	return ""
}

func executable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

var versionRe = regexp.MustCompile(`v?(\d+\.\d+\.\d+(?:[-+][\w.]+)?)`)

func (h *LSPHost) probeVersion(ctx context.Context, spec model.LSPServerSpec, bin string) (string, error) {
	args := []string{"--version"}
	if spec.ID == "gopls" {
		args = []string{"version"}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = h.serverEnv(spec, bin)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s did not run: %v", bin, err)
	}
	m := versionRe.FindStringSubmatch(string(out))
	if m == nil {
		return strings.TrimSpace(string(out)), nil
	}
	return m[1], nil
}

// Discover reports each allowlisted server's state for the given custom paths; it recomputes the PATH.
func (h *LSPHost) Discover(ctx context.Context, servers []model.LSPServerSpec, custom map[string]string) []model.LSPServerState {
	h.refresh(ctx)
	out := make([]model.LSPServerState, 0, len(servers))
	for _, s := range servers {
		st := model.LSPServerState{ID: s.ID, Languages: s.Languages, Install: s.Install, State: model.LSPNotFound}
		bin := ""
		if p := custom[s.ID]; p != "" {
			if !executable(p) {
				st.Path, st.Error = p, "custom path is not an executable file"
				out = append(out, st)
				continue
			}
			bin = p
		} else {
			bin = h.lookPath(s.Binary)
		}
		if bin == "" {
			out = append(out, st)
			continue
		}
		if s.NeedsNode && h.lookPath("node") == "" {
			st.Path, st.Error = bin, "needs Node.js"
			out = append(out, st)
			continue
		}
		st.Path = bin
		v, err := h.probeVersion(ctx, s, bin)
		if err != nil {
			st.Error = err.Error()
			out = append(out, st)
			continue
		}
		st.State, st.Version = model.LSPFound, v
		out = append(out, st)
	}
	return out
}

var goVerRe = regexp.MustCompile(`go(\d+)\.(\d+)(?:\.(\d+))?`)

func parseGoVersion(s string) (v [3]int, ok bool) {
	m := goVerRe.FindStringSubmatch(s)
	if m == nil {
		return v, false
	}
	for i := 0; i < 3; i++ {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

// GoModGoLine returns the `go` directive of root/go.mod as "go1.N[.M]", or "".
func GoModGoLine(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "go" {
			return "go" + f[1]
		}
	}
	return ""
}

// GoplsOutdated reports whether the gopls binary was built with an older Go than root's go.mod requires.
func (h *LSPHost) GoplsOutdated(ctx context.Context, bin, root string) (outdated bool, builtWith, need string) {
	need = GoModGoLine(root)
	needV, ok := parseGoVersion(need)
	if !ok {
		return false, "", need
	}
	h.mu.Lock()
	builtWith, cached := h.builtWith[bin]
	h.mu.Unlock()
	if !cached {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, "go", "version", "-m", bin)
		h.mu.Lock()
		cmd.Env = h.processEnv(h.path)
		h.mu.Unlock()
		if out, err := cmd.Output(); err == nil {
			first, _, _ := strings.Cut(string(out), "\n")
			if m := goVerRe.FindString(first); m != "" {
				builtWith = m
			}
		}
		h.mu.Lock()
		if h.builtWith == nil {
			h.builtWith = map[string]string{}
		}
		h.builtWith[bin] = builtWith
		h.mu.Unlock()
	}
	haveV, ok := parseGoVersion(builtWith)
	if !ok {
		return false, builtWith, need
	}
	for i := 0; i < 3; i++ {
		if haveV[i] != needV[i] {
			return haveV[i] < needV[i], builtWith, need
		}
	}
	return false, builtWith, need
}

var envKeep = []string{"HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR"}

// scrubbedEnv builds a server environment from an allowlist, so FACTORY_*, TYPESAFE_*, GH_TOKEN, ANTHROPIC_* and
// the like never reach it.
func scrubbedEnv(environ []string, path string, goenv map[string]string) []string {
	env := []string{"PATH=" + path}
	for _, k := range envKeep {
		if v := envGet(environ, k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	for _, k := range goEnvKeys {
		if v := goenv[k]; v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func (h *LSPHost) serverEnv(spec model.LSPServerSpec, bin string) []string {
	h.mu.Lock()
	dirs := append([]string(nil), h.path...)
	goenv := h.goenv
	h.mu.Unlock()
	if len(dirs) == 0 {
		dirs = filepath.SplitList(envGet(h.environ(), "PATH"))
	}
	dirs = append([]string{filepath.Dir(bin)}, dirs...)
	if spec.NeedsNode {
		for _, d := range dirs {
			if executable(filepath.Join(d, "node")) {
				dirs = append([]string{d}, dirs...)
				break
			}
		}
	}
	return scrubbedEnv(h.environ(), strings.Join(dirs, string(os.PathListSeparator)), goenv)
}

// Start spawns the allowlisted server spec.ID at bin with root as its working directory.
func (h *LSPHost) Start(ctx context.Context, spec model.LSPServerSpec, bin, root string) (model.LSPProcess, error) {
	allowed, ok := LSPSpecByID(spec.ID)
	if !ok {
		return nil, fmt.Errorf("language server %q is not allowed", spec.ID)
	}
	if bin == "" || !executable(bin) {
		return nil, fmt.Errorf("language server binary %q is not executable", bin)
	}
	h.mu.Lock()
	haveEnv := h.goenv != nil
	h.mu.Unlock()
	if !haveEnv {
		h.refresh(ctx)
	}
	cmd := exec.Command(bin, allowed.Args...)
	cmd.Dir = root
	cmd.Env = h.serverEnv(allowed, bin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	tail := &tailBuf{max: lspTailBytes}
	cmd.Stderr = tail
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &lspProc{cmd: cmd, in: in, r: bufio.NewReaderSize(out, 64<<10), tail: tail, waited: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.waited)
	}()
	return p, nil
}

type lspProc struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	r       *bufio.Reader
	wmu     sync.Mutex
	tail    *tailBuf
	waited  chan struct{}
	waitErr error
}

func (p *lspProc) ReadMessage() ([]byte, error) { return ReadFrame(p.r) }

func (p *lspProc) WriteMessage(msg []byte) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	return WriteFrame(p.in, msg)
}

func (p *lspProc) Wait() error {
	<-p.waited
	return p.waitErr
}

// Kill signals the whole process group the server leads, so tsserver and gopls workers die with it.
func (p *lspProc) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (p *lspProc) StderrTail() string { return p.tail.String() }

// ReadFrame reads one Content-Length framed message.
func ReadFrame(r *bufio.Reader) ([]byte, error) {
	n := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if n < 0 {
				return nil, errors.New("lsp frame without Content-Length")
			}
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			n, err = strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 {
				return nil, errors.New("lsp frame with bad Content-Length")
			}
		}
	}
	if n > MaxLSPFrame {
		return nil, fmt.Errorf("lsp frame of %d bytes exceeds the %d byte limit", n, MaxLSPFrame)
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

// WriteFrame writes msg with a Content-Length header in one write.
func WriteFrame(w io.Writer, msg []byte) error {
	var b bytes.Buffer
	b.Grow(len(msg) + 32)
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(msg))
	b.Write(msg)
	_, err := w.Write(b.Bytes())
	return err
}

type tailBuf struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
