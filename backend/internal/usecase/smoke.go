package usecase

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"legos-factory/backend/internal/model"
)

const (
	smokeCheckName     = "smoke"
	defaultReadyWait   = 90 * time.Second
	smokeBrowserBudget = 10 * time.Minute
	maxSmokeRoutes     = 20
)

//go:embed smoke_runner.cjs
var smokeRunnerJS []byte

// smokeProc is one started app process; Stop kills its whole process group and is safe to call twice.
type smokeProc interface {
	Stop()
	Exited() <-chan struct{}
	Tail() string
}

// smokeDeps are the edges of a smoke run; the zero value uses real processes and the real browser runner.
type smokeDeps struct {
	start   func(ctx context.Context, dir, command string, env []string) (smokeProc, error)
	browser func(ctx context.Context, runnerPath, specPath string) ([]byte, error)
}

// ---- route mapping ----

// matchGlob matches slash paths; "**" spans zero or more segments.
func matchGlob(pattern, name string) bool {
	return globSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func globSegs(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(name); i++ {
				if globSegs(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// globBase is the literal directory prefix of a pattern ("web/app/**/page.tsx" → "web/app").
func globBase(pattern string) string {
	var lit []string
	for _, s := range strings.Split(pattern, "/") {
		if strings.ContainsAny(s, "*?[") {
			break
		}
		lit = append(lit, s)
	}
	if len(lit) > 0 && len(lit) == len(strings.Split(pattern, "/")) {
		lit = lit[:len(lit)-1]
	}
	return strings.Join(lit, "/")
}

var routeParam = regexp.MustCompile(`^\[{1,2}(?:\.\.\.)?([A-Za-z0-9_]+)\]{1,2}$`)

// pageTemplate maps a Next app-router page file to its URL template: route groups and parallel slots drop out, [id] stays as a placeholder segment.
func pageTemplate(base, file string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(file, base), "/")
	dir := path.Dir(rel)
	var segs []string
	if dir != "." {
		for _, s := range strings.Split(dir, "/") {
			if (strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")) || strings.HasPrefix(s, "@") {
				continue
			}
			segs = append(segs, s)
		}
	}
	return "/" + strings.Join(segs, "/")
}

// fillTemplate substitutes [param] segments; missing names the first param without a value.
func fillTemplate(tmpl string, params map[string]string) (route, missing string) {
	segs := strings.Split(tmpl, "/")
	for i, s := range segs {
		m := routeParam.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		v, ok := params[m[1]]
		if !ok || v == "" {
			return "", m[1]
		}
		segs[i] = v
	}
	return strings.Join(segs, "/"), ""
}

var importSpec = regexp.MustCompile(`(?:from\s+|import\s*\(\s*|import\s+)["']([^"']+)["']`)
var sourceExts = []string{"", ".ts", ".tsx", ".js", ".jsx", ".mjs", "/index.ts", "/index.tsx", "/index.js", "/index.jsx"}

// importedChanged reports whether src imports (one level deep) a changed file; "@/" resolves to aliasRoot.
func importedChanged(src []byte, pageDir, aliasRoot string, changed map[string]bool) bool {
	for _, m := range importSpec.FindAllSubmatch(src, -1) {
		spec := string(m[1])
		var target string
		switch {
		case strings.HasPrefix(spec, "@/"):
			target = path.Join(aliasRoot, strings.TrimPrefix(spec, "@/"))
		case strings.HasPrefix(spec, "."):
			target = path.Join(pageDir, spec)
		default:
			continue
		}
		for _, ext := range sourceExts {
			if changed[target+ext] {
				return true
			}
		}
	}
	return false
}

var layoutFile = regexp.MustCompile(`^layout\.(tsx|ts|jsx|js)$`)

// affectedTemplates lists the route templates of pages whose page file, a layout above them, or a direct import changed.
func affectedTemplates(root, routesFrom string, changedFiles []string) []string {
	changed := map[string]bool{}
	for _, c := range changedFiles {
		changed[filepath.ToSlash(c)] = true
	}
	base := globBase(routesFrom)
	aliasRoot := path.Dir(base)
	if base == "" {
		aliasRoot = "."
	}
	var out []string
	seen := map[string]bool{}
	_ = filepath.WalkDir(filepath.Join(root, filepath.FromSlash(base)), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if n := d.Name(); n == "node_modules" || n == ".next" || n == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !matchGlob(routesFrom, rel) {
			return nil
		}
		hit := changed[rel]
		if !hit {
			pageDir := path.Dir(rel)
			for c := range changed {
				if layoutFile.MatchString(path.Base(c)) && (path.Dir(c) == pageDir || strings.HasPrefix(pageDir+"/", path.Dir(c)+"/")) {
					hit = true
					break
				}
			}
		}
		if !hit {
			if src, err := os.ReadFile(p); err == nil {
				hit = importedChanged(src, path.Dir(rel), aliasRoot, changed)
			}
		}
		if hit {
			if t := pageTemplate(base, rel); !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// ---- processes ----

type tailBuf struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 8192 {
		t.b = t.b[len(t.b)-8192:]
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

type execProc struct {
	cmd  *exec.Cmd
	done chan struct{}
	out  *tailBuf
	once sync.Once
}

func startExecProc(_ context.Context, dir, command string, env []string) (smokeProc, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &tailBuf{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &execProc{cmd: cmd, done: make(chan struct{}), out: out}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

func (p *execProc) Exited() <-chan struct{} { return p.done }
func (p *execProc) Tail() string            { return tail(p.out.String()) }

func (p *execProc) Stop() {
	p.once.Do(func() {
		pgid := p.cmd.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			<-p.done
		}
		_ = syscall.Kill(-pgid, syscall.SIGKILL) // children that outlived the leader
	})
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitReady(ctx context.Context, p smokeProc, port int, ready string, limit time.Duration) error {
	if ready == "" {
		ready = "/"
	}
	url := "http://127.0.0.1:" + strconv.Itoa(port) + ready
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	last := "no answer"
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		select {
		case <-p.Exited():
			return fmt.Errorf("exited before it was ready")
		case <-deadline.C:
			return fmt.Errorf("not ready on %s after %s (%s)", ready, limit, last)
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// ---- the run ----

type smokeSpecLogin struct {
	Path     string `json:"path"`
	User     string `json:"user"`
	Password string `json:"password"`
}

type smokeSpec struct {
	BaseURL       string          `json:"baseURL"`
	PlaywrightDir string          `json:"playwrightDir"`
	ChromePath    string          `json:"chromePath,omitempty"`
	NavTimeoutMs  int             `json:"navTimeoutMs"`
	OutDir        string          `json:"outDir"`
	Login         *smokeSpecLogin `json:"login,omitempty"`
	Routes        []string        `json:"routes"`
}

type smokeRouteResult struct {
	Path       string   `json:"path"`
	Status     int      `json:"status"`
	OK         bool     `json:"ok"`
	Problems   []string `json:"problems"`
	Screenshot string   `json:"screenshot"`
}

type smokeOut struct {
	Results    []smokeRouteResult `json:"results"`
	LoginError string             `json:"loginError"`
	Fatal      string             `json:"fatal"`
}

func (f *Factory) playwrightDir() string {
	if f.PlaywrightDir != "" {
		return f.PlaywrightDir
	}
	if d := os.Getenv("LEGOS_PLAYWRIGHT_DIR"); d != "" {
		return d
	}
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	for _, s := range starts {
		for d := s; ; d = filepath.Dir(d) {
			cand := filepath.Join(d, "frontend")
			if _, err := os.Stat(filepath.Join(cand, "node_modules", "playwright-core")); err == nil {
				return cand
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	return ""
}

func envValue(env []string, name string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v
		}
	}
	return ""
}

const smokeNoPages = "no affected pages"

// runSmoke runs the profile's Playwright smoke for one verify pass and returns it as the optional (or blocking) check "smoke".
func (f *Factory) runSmoke(ctx context.Context, wo *model.WorkOrder, p model.Profile) (model.Check, model.CheckResult) {
	c := model.Check{Name: smokeCheckName, Command: "playwright smoke", Optional: !p.Smoke.Blocking}
	start := time.Now()
	r := f.smokeRun(ctx, wo, p)
	r.Name, r.Command, r.Duration = c.Name, c.Command, time.Since(start)
	if r.Outcome == model.Pass && r.Output == smokeNoPages {
		c.Optional = true // nothing was opened, so the pass is not evidence even when smoke is blocking
	}
	return c, r
}

// requiredFailed reports whether a non-optional check did not pass; the smoke run is skipped then, since the Builder is sent back anyway.
func requiredFailed(checks []model.Check, results []model.CheckResult) bool {
	for i, c := range checks {
		if !c.Optional && results[i].Outcome != model.Pass {
			return true
		}
	}
	return false
}

func unverified(format string, a ...any) model.CheckResult {
	return model.CheckResult{Outcome: model.Unverified, Output: fmt.Sprintf(format, a...)}
}

func (f *Factory) smokeRun(ctx context.Context, wo *model.WorkOrder, p model.Profile) model.CheckResult {
	s := p.Smoke
	dir := f.workspaceDir(wo)
	changed, err := f.Sandbox.ChangedFiles(ctx, dir)
	if err != nil {
		return unverified("could not list the changed files: %v", err)
	}
	templates := affectedTemplates(dir, s.RoutesFrom, changed)
	if len(templates) == 0 {
		return model.CheckResult{Outcome: model.Pass, Output: smokeNoPages}
	}
	var notes []string
	if len(templates) > maxSmokeRoutes {
		notes = append(notes, fmt.Sprintf("%d more affected pages not opened (limit %d)", len(templates)-maxSmokeRoutes, maxSmokeRoutes))
		templates = templates[:maxSmokeRoutes]
	}

	env, lease, release, err := f.leaseRunL(ctx, wo.ProjectID, fmt.Sprintf("run_%s_%d_smoke", wo.ID, wo.Revision), dir, p)
	defer release()
	if err != nil {
		return unverified("check services unavailable: %v", err)
	}

	params := map[string]string{}
	for k, v := range s.Params {
		if q, ok := strings.CutPrefix(v, "sql:"); ok {
			if lease == nil || p.Database == nil {
				return unverified("param %s needs a sql query but the profile has no database", k)
			}
			val, err := f.stackMgr().queryValue(ctx, stackName(wo.ProjectID), lease, q)
			if err != nil {
				return unverified("param %s: %v", k, err)
			}
			v = val
		}
		params[k] = v
	}
	var routes []string
	for _, t := range templates {
		r, missing := fillTemplate(t, params)
		if missing != "" {
			notes = append(notes, fmt.Sprintf("skipped %s: no value for param %q", t, missing))
			continue
		}
		routes = append(routes, r)
	}
	if len(routes) == 0 {
		return unverified("every affected page needs a route param the profile does not give\n%s", strings.Join(notes, "\n"))
	}

	deps := f.smoke
	if deps.start == nil {
		deps.start = startExecProc
	}
	if deps.browser == nil {
		deps.browser = runNode
	}
	ports := map[string]int{}
	vals := map[string]string{}
	for _, sp := range s.Start {
		port, err := freePort()
		if err != nil {
			return unverified("no free port: %v", err)
		}
		ports[sp.Name] = port
		vals["port."+sp.Name] = strconv.Itoa(port)
	}
	baseProc := s.Base
	if baseProc == "" && len(s.Start) > 0 {
		baseProc = s.Start[len(s.Start)-1].Name
	}
	basePort, ok := ports[baseProc]
	if !ok {
		return unverified("smoke base %q is not one of the started processes", s.Base)
	}

	var procs []smokeProc
	defer func() {
		for i := len(procs) - 1; i >= 0; i-- {
			procs[i].Stop()
		}
	}()
	limit := defaultReadyWait
	if s.ReadyTimeoutSec > 0 {
		limit = time.Duration(s.ReadyTimeoutSec) * time.Second
	}
	for _, sp := range s.Start {
		full, ok := insideDir(dir, sp.Dir)
		if !ok {
			return unverified("process %s: dir %q is outside the workspace", sp.Name, sp.Dir)
		}
		penv := append([]string(nil), env...)
		keys := make([]string, 0, len(sp.Env))
		for k := range sp.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		leaseVals := map[string]string{}
		if lease != nil {
			leaseVals = lease.values(false)
		}
		for k, v := range vals {
			leaseVals[k] = v
		}
		for _, k := range keys {
			v, err := resolvePlaceholders(sp.Env[k], leaseVals)
			if err != nil {
				return unverified("process %s env %s: %v", sp.Name, k, err)
			}
			penv = append(penv, k+"="+v)
		}
		if sp.PortEnv != "" {
			penv = append(penv, sp.PortEnv+"="+strconv.Itoa(ports[sp.Name]))
		}
		proc, err := deps.start(ctx, full, sp.Command, penv)
		if err != nil {
			return unverified("could not start %s: %v", sp.Name, err)
		}
		procs = append(procs, proc)
		if err := waitReady(ctx, proc, ports[sp.Name], sp.Ready, limit); err != nil {
			return model.CheckResult{Outcome: model.Fail, Output: fmt.Sprintf("%s %v\n%s", sp.Name, err, proc.Tail())}
		}
	}

	outDir := filepath.Join(f.DataDir, "smoke", wo.ID, strconv.Itoa(wo.Revision))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return unverified("%v", err)
	}
	spec := smokeSpec{
		BaseURL: "http://localhost:" + strconv.Itoa(basePort), PlaywrightDir: f.playwrightDir(), ChromePath: os.Getenv("LEGOS_SMOKE_CHROME"),
		NavTimeoutMs: 60000, OutDir: outDir, Routes: routes,
	}
	if s.Login != nil {
		u, pw := envValue(env, s.Login.UserEnv), envValue(env, s.Login.PasswordEnv)
		if u == "" || pw == "" {
			return unverified("login needs %s and %s in the profile env", s.Login.UserEnv, s.Login.PasswordEnv)
		}
		spec.Login = &smokeSpecLogin{Path: s.Login.Path, User: u, Password: pw}
	}
	if spec.PlaywrightDir == "" {
		return unverified("no factory Playwright install found (set LEGOS_PLAYWRIGHT_DIR to a directory whose node_modules has playwright-core)")
	}
	runner := filepath.Join(f.DataDir, "smoke", "runner.cjs")
	specPath := filepath.Join(outDir, "spec.json")
	raw, _ := json.Marshal(spec)
	if err := writeFileAtomic(runner, smokeRunnerJS, 0o644); err != nil {
		return unverified("%v", err)
	}
	if err := os.WriteFile(specPath, raw, 0o600); err != nil { // holds the login credentials
		return unverified("%v", err)
	}
	defer os.Remove(specPath)
	bctx, cancel := context.WithTimeout(ctx, smokeBrowserBudget)
	defer cancel()
	out, err := deps.browser(bctx, runner, specPath)
	if err != nil {
		return unverified("browser run failed: %v", err)
	}
	var res smokeOut
	if err := json.Unmarshal(bytes.TrimSpace(out), &res); err != nil {
		return unverified("unreadable browser output: %v: %s", err, tail(string(out)))
	}
	return summarizeSmoke(res, notes)
}

// writeFileAtomic renames a finished temp file into place so a concurrent smoke run never starts node on a half-written runner.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func summarizeSmoke(res smokeOut, notes []string) model.CheckResult {
	if res.Fatal != "" {
		return unverified("%s", res.Fatal)
	}
	if res.LoginError != "" {
		return model.CheckResult{Outcome: model.Fail, Output: "login failed: " + res.LoginError}
	}
	outcome := model.Pass
	var b strings.Builder
	for _, r := range res.Results {
		if r.OK {
			fmt.Fprintf(&b, "PASS %s (%d)", r.Path, r.Status)
		} else {
			outcome = model.Fail
			fmt.Fprintf(&b, "FAIL %s (%d): %s", r.Path, r.Status, strings.Join(r.Problems, "; "))
		}
		if r.Screenshot != "" {
			fmt.Fprintf(&b, " [screenshot %s]", r.Screenshot)
		}
		b.WriteByte('\n')
	}
	for _, n := range notes {
		b.WriteString(n + "\n")
	}
	return model.CheckResult{Outcome: outcome, Output: strings.TrimSpace(b.String())}
}

func runNode(ctx context.Context, runnerPath, specPath string) ([]byte, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, fmt.Errorf("node is not installed: %v", err)
	}
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, node, runnerPath, specPath)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, tail(errb.String()))
	}
	return out.Bytes(), nil
}
