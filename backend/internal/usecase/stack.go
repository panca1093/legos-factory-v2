package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

const (
	stackPrefix    = "legos-ck-"
	stackIdleAfter = 2 * time.Hour
	stackReapEvery = 5 * time.Minute
	redisIndexes   = 15 // index 0 is never used
	maxDBName      = 63
	// TODO: uncertain — the first compose release with !override is believed to be 2.24.0, confirm before relying on the floor
	minComposeMajor, minComposeMinor = 2, 24
)

// DockerCLI is the only way the stack talks to docker; args exclude the leading "docker".
type DockerCLI interface {
	Run(ctx context.Context, args ...string) (string, error)
}

type execDocker struct{}

func (execDocker) Run(ctx context.Context, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker %s: %v: %s", strings.Join(args[:min(len(args), 4)], " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

type projStack struct {
	mu        sync.Mutex // guards every field below
	up        bool
	ports     map[string]int // service → host port
	leases    int
	redis     [redisIndexes + 1]bool
	idleSince time.Time
	freed     chan struct{} // closed and replaced when a Redis index frees
}

type stackMgr struct {
	docker  DockerCLI
	dataDir string
	now     func() time.Time
	mu      sync.Mutex // guards stacks
	stacks  map[string]*projStack
}

func newStackMgr(d DockerCLI, dataDir string) *stackMgr {
	if d == nil {
		d = execDocker{}
	}
	return &stackMgr{docker: d, dataDir: dataDir, now: time.Now, stacks: map[string]*projStack{}}
}

var badNameChars = regexp.MustCompile(`[^a-z0-9_-]+`)

func stackName(projectID string) string {
	return stackPrefix + strings.Trim(badNameChars.ReplaceAllString(strings.ToLower(projectID), "-"), "-")
}

func (m *stackMgr) stack(name string) *projStack {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stacks[name]
	if s == nil {
		s = &projStack{freed: make(chan struct{})}
		m.stacks[name] = s
	}
	return s
}

// runLease is one run's slice of a project stack.
type runLease struct {
	hostPorts   map[string]int
	DB          string
	Redis       int
	profile     model.Profile
	release     func()
	containerOf map[string]int
}

// leasePlaceholders are the per-run values every lease provides, besides {{<service>}}; checksuggest validates against this list.
var leasePlaceholders = []string{"db", "redis_db", "bucket"}

func (l *runLease) values(admin bool) map[string]string {
	v := map[string]string{"db": l.DB, "redis_db": strconv.Itoa(l.Redis), "bucket": strings.ReplaceAll(l.DB, "_", "-")}
	for svc, port := range l.hostPorts {
		if admin {
			port = l.containerOf[svc]
		}
		v[svc] = "127.0.0.1:" + strconv.Itoa(port)
	}
	return v
}

// sanitizeRunName makes a run name a valid, collision-safe Postgres identifier.
func sanitizeRunName(name string) string {
	n := strings.Trim(badNameChars.ReplaceAllString(strings.ToLower(name), "_"), "_")
	n = strings.ReplaceAll(n, "-", "_")
	if len(n) > maxDBName {
		sum := sha256.Sum256([]byte(n))
		n = n[:maxDBName-9] + "_" + hex.EncodeToString(sum[:4])
	}
	return n
}

func usesPlaceholder(p model.Profile, key string) bool {
	has := func(v string) bool { return strings.Contains(v, "{{"+key+"}}") }
	for _, v := range p.Env {
		if has(v) {
			return true
		}
	}
	if p.Smoke != nil {
		for _, sp := range p.Smoke.Start {
			for _, v := range sp.Env {
				if has(v) {
					return true
				}
			}
		}
	}
	return false
}

// acquire brings the project's stack up if needed and leases the run its own database and Redis index.
func (m *stackMgr) acquire(ctx context.Context, projectID, runName, dir string, p model.Profile) (*runLease, error) {
	name := stackName(projectID)
	s := m.stack(name)
	s.mu.Lock()
	if !s.up || s.leases == 0 {
		if err := m.bringUp(ctx, name, dir, p, s); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	s.leases++
	ports := maps.Clone(s.ports)
	s.mu.Unlock()

	l := &runLease{hostPorts: ports, containerOf: p.Services.Ports, profile: p}
	var once sync.Once
	l.release = func() {
		once.Do(func() { m.releaseLease(name, s, l) })
	}
	if usesPlaceholder(p, "redis_db") {
		idx, err := s.leaseRedis(ctx)
		if err != nil {
			l.release()
			return nil, err
		}
		l.Redis = idx
	}
	if p.Database != nil {
		l.DB = sanitizeRunName(runName)
		if err := m.createDB(ctx, name, l); err != nil {
			l.release()
			return nil, err
		}
	}
	return l, nil
}

func (m *stackMgr) releaseLease(name string, s *projStack, l *runLease) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if l.DB != "" {
		_ = m.dropDB(ctx, name, l)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.Redis > 0 {
		s.redis[l.Redis] = false
		close(s.freed)
		s.freed = make(chan struct{})
	}
	s.leases--
	if s.leases == 0 {
		s.idleSince = m.now()
	}
}

// leaseRedis takes the lowest free index, waiting while all are leased.
func (s *projStack) leaseRedis(ctx context.Context) (int, error) {
	for {
		s.mu.Lock()
		for i := 1; i <= redisIndexes; i++ {
			if !s.redis[i] {
				s.redis[i] = true
				s.mu.Unlock()
				return i, nil
			}
		}
		ch := s.freed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

func (m *stackMgr) preflight(ctx context.Context) error {
	out, err := m.docker.Run(ctx, "compose", "version", "--short")
	if err != nil {
		return fmt.Errorf("docker compose is not available (%v); the check services cannot start", err)
	}
	major, minor, ok := parseComposeVersion(out)
	if !ok {
		return fmt.Errorf("could not read the docker compose version from %q", strings.TrimSpace(out))
	}
	if major < minComposeMajor || (major == minComposeMajor && minor < minComposeMinor) {
		return fmt.Errorf("docker compose %d.%d is too old; %d.%d or newer is needed for the service stack", major, minor, minComposeMajor, minComposeMinor)
	}
	return nil
}

func parseComposeVersion(s string) (major, minor int, ok bool) {
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	var err1, err2 error
	major, err1 = strconv.Atoi(parts[0])
	minor, err2 = strconv.Atoi(parts[1])
	return major, minor, err1 == nil && err2 == nil
}

// overrideYAML maps every service port to a random loopback host port.
func overrideYAML(ports map[string]int) string {
	names := make([]string, 0, len(ports))
	for n := range ports {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("services:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  %s:\n    ports: !override [\"127.0.0.1::%d\"]\n", n, ports[n])
	}
	return b.String()
}

func insideDir(dir, rel string) (string, bool) {
	full := filepath.Join(dir, rel)
	r, err := filepath.Rel(dir, full)
	return full, err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

func (m *stackMgr) stackDir(name string) string {
	return filepath.Join(m.dataDir, "stacks", strings.TrimPrefix(name, stackPrefix))
}

var errComposeChanged = errors.New("compose file changed since the checks were approved; Suggest again")

// approvedCompose copies the run's compose file into the factory-owned stack dir, only when it matches the approved hash.
func (m *stackMgr) approvedCompose(dir, stackDir string, p model.Profile) (string, error) {
	src, ok := insideDir(dir, p.Services.Compose)
	if !ok || p.Services.Compose == "" {
		return "", fmt.Errorf("compose file %q is not inside the workspace", p.Services.Compose)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("compose file %q not found in the workspace", p.Services.Compose)
	}
	approved := ""
	for _, s := range p.Sources {
		if filepath.Clean(s.Path) == filepath.Clean(p.Services.Compose) {
			approved = s.SHA256
		}
	}
	sum := sha256.Sum256(b)
	if approved == "" || hex.EncodeToString(sum[:]) != approved {
		return "", errComposeChanged
	}
	dst := filepath.Join(stackDir, "compose.yml")
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return "", err
	}
	return dst, nil
}

// bringUp runs while the project's mutex is held.
func (m *stackMgr) bringUp(ctx context.Context, name, dir string, p model.Profile, s *projStack) error {
	if err := m.preflight(ctx); err != nil {
		return err
	}
	stackDir := m.stackDir(name)
	if err := os.MkdirAll(stackDir, 0o755); err != nil {
		return err
	}
	compose, err := m.approvedCompose(dir, stackDir, p)
	if err != nil {
		return err
	}
	override := filepath.Join(stackDir, "override.yml")
	if err := os.WriteFile(override, []byte(overrideYAML(p.Services.Ports)), 0o644); err != nil {
		return err
	}
	s.up, s.idleSince = true, m.now() // a failed up can leave containers behind; the idle reaper must see them
	args := append([]string{"compose", "-p", name, "--project-directory", filepath.Dir(filepath.Join(dir, p.Services.Compose)), "-f", compose, "-f", override, "up", "-d", "--wait"}, p.Services.Names...)
	if out, err := m.docker.Run(ctx, args...); err != nil {
		return fmt.Errorf("starting the check services failed: %v: %s", err, tail(out))
	}
	ports := map[string]int{}
	for svc, cport := range p.Services.Ports {
		out, err := m.docker.Run(ctx, "compose", "-p", name, "port", svc, strconv.Itoa(cport))
		if err != nil {
			return fmt.Errorf("could not find the host port of %s: %v", svc, err)
		}
		hp, ok := parseHostPort(out)
		if !ok {
			return fmt.Errorf("could not read the host port of %s from %q", svc, strings.TrimSpace(out))
		}
		ports[svc] = hp
	}
	s.ports = ports
	return nil
}

func parseHostPort(out string) (int, bool) {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
	i := strings.LastIndex(line, ":")
	if i < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(line[i+1:])
	return n, err == nil && n > 0
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		s = "..." + s[len(s)-500:]
	}
	return s
}

func (m *stackMgr) psql(ctx context.Context, name string, l *runLease, sql string) error {
	adminURL, err := resolvePlaceholders(l.profile.Database.AdminURL, l.values(true))
	if err != nil {
		return fmt.Errorf("database admin_url: %v", err)
	}
	out, err := m.docker.Run(ctx, "compose", "-p", name, "exec", "-T", l.profile.Database.Service,
		"psql", adminURL, "-v", "ON_ERROR_STOP=1", "-c", sql)
	if err != nil {
		return fmt.Errorf("%v: %s", err, tail(out))
	}
	return nil
}

// queryValue returns the first column of the first row of sql, run against the lease's own database.
func (m *stackMgr) queryValue(ctx context.Context, name string, l *runLease, sql string) (string, error) {
	adminURL, err := resolvePlaceholders(l.profile.Database.AdminURL, l.values(true))
	if err != nil {
		return "", fmt.Errorf("database admin_url: %v", err)
	}
	u, err := url.Parse(adminURL)
	if err != nil {
		return "", fmt.Errorf("database admin_url: %v", err)
	}
	u.Path = "/" + l.DB
	out, err := m.docker.Run(ctx, "compose", "-p", name, "exec", "-T", l.profile.Database.Service,
		"psql", u.String(), "-At", "-v", "ON_ERROR_STOP=1", "-c", sql)
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, tail(out))
	}
	v := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
	if v == "" {
		return "", fmt.Errorf("the query returned no row")
	}
	return v, nil
}

func (m *stackMgr) createDB(ctx context.Context, name string, l *runLease) error {
	if err := m.psql(ctx, name, l, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, l.DB)); err != nil {
		return fmt.Errorf("could not reset database %s: %v", l.DB, err)
	}
	if err := m.psql(ctx, name, l, fmt.Sprintf(`CREATE DATABASE "%s"`, l.DB)); err != nil {
		return fmt.Errorf("could not create database %s: %v", l.DB, err)
	}
	return nil
}

func (m *stackMgr) dropDB(ctx context.Context, name string, l *runLease) error {
	return m.psql(ctx, name, l, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, l.DB))
}

var placeholder = regexp.MustCompile(`\{\{[^{}]*\}\}`)

// resolvePlaceholders fills {{key}} from vals; an unknown placeholder is an error, never left in a value.
func resolvePlaceholders(s string, vals map[string]string) (string, error) {
	var missing []string
	out := placeholder.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := vals[strings.TrimSpace(m[2:len(m)-2])]; ok {
			return v
		}
		missing = append(missing, m)
		return m
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("unknown placeholder %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// resolveEnv renders env as sorted KEY=VALUE pairs.
func resolveEnv(env map[string]string, vals map[string]string) ([]string, error) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		v, err := resolvePlaceholders(env[k], vals)
		if err != nil {
			return nil, fmt.Errorf("env %s: %v", k, err)
		}
		out = append(out, k+"="+v)
	}
	return out, nil
}

// reap takes down every stack that has had no lease for stackIdleAfter.
func (m *stackMgr) reap(ctx context.Context) {
	m.mu.Lock()
	names := make([]string, 0, len(m.stacks))
	for n := range m.stacks {
		names = append(names, n)
	}
	m.mu.Unlock()
	for _, n := range names {
		s := m.stack(n)
		s.mu.Lock()
		if s.up && s.leases == 0 && m.now().Sub(s.idleSince) >= stackIdleAfter {
			if _, err := m.docker.Run(ctx, "compose", "-p", n, "down", "-v"); err == nil {
				s.up, s.ports = false, nil
			}
		}
		s.mu.Unlock()
	}
}

// stop takes a project's stack down now; leases in flight lose their services.
func (m *stackMgr) stop(ctx context.Context, projectID string) error {
	name := stackName(projectID)
	s := m.stack(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := m.docker.Run(ctx, "compose", "-p", name, "down", "-v"); err != nil {
		return err
	}
	s.up, s.ports = false, nil
	return nil
}

// adoptOrphans registers running compose projects this factory created (their config files include our override.yml), starting their idle clock now.
func (m *stackMgr) adoptOrphans(ctx context.Context) {
	out, err := m.docker.Run(ctx, "compose", "ls", "--format", "json")
	if err != nil {
		return
	}
	var list []struct{ Name, ConfigFiles string }
	if json.Unmarshal([]byte(out), &list) != nil {
		return
	}
	for _, p := range list {
		if !strings.HasPrefix(p.Name, stackPrefix) {
			continue
		}
		override := filepath.Join(m.stackDir(p.Name), "override.yml")
		if !slices.Contains(strings.Split(p.ConfigFiles, ","), override) {
			continue
		}
		if _, err := os.Stat(override); err != nil {
			continue
		}
		s := m.stack(p.Name)
		s.mu.Lock()
		if !s.up {
			s.up, s.idleSince = true, m.now()
		}
		s.mu.Unlock()
	}
}

func (f *Factory) stackMgr() *stackMgr {
	f.stackOnce.Do(func() {
		if f.stacks == nil {
			f.stacks = newStackMgr(f.Docker, f.DataDir)
		}
	})
	return f.stacks
}

// WatchStacks adopts stacks orphaned by a restart and tears down idle ones until ctx ends.
func (f *Factory) WatchStacks(ctx context.Context) {
	m := f.stackMgr()
	m.adoptOrphans(ctx)
	go func() {
		t := time.NewTicker(stackReapEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.reap(ctx)
			}
		}
	}()
}

// StopStack takes a project's check services down.
func (f *Factory) StopStack(ctx context.Context, projectID string) error {
	return f.stackMgr().stop(ctx, projectID)
}

// removeStack takes a deleted project's services down and drops its stack dir; a project that never started services is left alone.
func (f *Factory) removeStack(ctx context.Context, projectID string) {
	m := f.stackMgr()
	dir := m.stackDir(stackName(projectID))
	if _, err := os.Stat(dir); err != nil {
		return
	}
	if err := m.stop(ctx, projectID); err != nil {
		f.Log.Warn("check services not stopped on project delete", "project", projectID, "err", err)
		return
	}
	if err := removeInside(filepath.Join(f.DataDir, "stacks"), dir); err != nil {
		f.Log.Warn("stack dir cleanup failed", "project", projectID, "err", err)
	}
}

// leaseRun gives one run its env: the leased services' values when the profile needs a stack, then runs the per-run setup in dir.
// release must be called when the run ends.
func (f *Factory) leaseRun(ctx context.Context, projectID, runName, dir string, p model.Profile) (env []string, release func(), err error) {
	env, _, release, err = f.leaseRunL(ctx, projectID, runName, dir, p)
	return env, release, err
}

// leaseRunL is leaseRun that also returns the lease (nil when the profile has no stack).
// With a stack, per-run setup (not the returned env) also sees LEGOS_COMPOSE_PROJECT and LEGOS_DB, so it can reach the services without a host client.
func (f *Factory) leaseRunL(ctx context.Context, projectID, runName, dir string, p model.Profile) (env []string, lease *runLease, release func(), err error) {
	release = func() {}
	vals := map[string]string{}
	if p.NeedsStack() {
		lease, err = f.stackMgr().acquire(ctx, projectID, runName, dir, p)
		if err != nil {
			return nil, nil, release, err
		}
		release, vals = lease.release, lease.values(false)
	}
	env, err = resolveEnv(p.Env, vals)
	if err != nil {
		release()
		return nil, nil, func() {}, err
	}
	setupEnv := env
	if lease != nil {
		setupEnv = append(append([]string(nil), env...), "LEGOS_COMPOSE_PROJECT="+stackName(projectID), "LEGOS_DB="+lease.DB)
	}
	for _, step := range p.Setup {
		if !step.PerRun {
			continue
		}
		if r := f.Sandbox.RunCheck(ctx, dir, step, f.QualTimeout, setupEnv); r.Outcome != model.Pass {
			release()
			return nil, nil, func() {}, fmt.Errorf("per-run setup %q %s: %s", step.Name, r.Outcome, tail(r.Output))
		}
	}
	return env, lease, release, nil
}
