// Package sandbox manages per-work-order workspace volumes, the sandbox
// containers that mount them, git worktrees inside, and per-worktree ports.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Workspace is one work order's volume; paths are in the sandbox's path space.
type Workspace struct {
	ID   string
	root string
}

func (w Workspace) Root() string                   { return w.root }
func (w Workspace) RepoDir() string                { return filepath.Join(w.root, "repo") }
func (w Workspace) WorktreeDir(task string) string { return filepath.Join(w.root, "wt", task) }
func (w Workspace) HomeDir() string                { return filepath.Join(w.root, "home") }

// Cmd is a process to spawn with stdio attached by the caller.
type Cmd struct {
	Argv []string
	Dir  string
	Env  []string
}

// Exec builds an *exec.Cmd the caller can wire stdio to.
func (c Cmd) Exec(ctx context.Context) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir = c.Dir
	if c.Env != nil {
		cmd.Env = c.Env
	}
	return cmd
}

type Sandbox interface {
	Workspace() Workspace
	Exec(ctx context.Context, dir string, env map[string]string, args ...string) (stdout, stderr string, exitCode int, err error)
	Command(dir string, env map[string]string, args ...string) Cmd
	Stop(ctx context.Context) error
}

type Runtime interface {
	CreateWorkspace(ctx context.Context, woID string) (Workspace, error)
	DeleteWorkspace(ctx context.Context, woID string) error
	Start(ctx context.Context, ws Workspace, env map[string]string) (Sandbox, error)
}

func run(cmd *exec.Cmd) (stdout, stderr string, exitCode int, err error) {
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return o.String(), e.String(), ee.ExitCode(), nil
	}
	if err != nil {
		return o.String(), e.String(), -1, err
	}
	return o.String(), e.String(), 0, nil
}

func envList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// Local runs commands on the host inside directories under Root; for tests and laptop dev.
type Local struct{ Root string }

func (l Local) wsRoot(woID string) (string, error) {
	root, err := filepath.Abs(l.Root)
	return filepath.Join(root, woID), err
}

func (l Local) CreateWorkspace(_ context.Context, woID string) (Workspace, error) {
	root, err := l.wsRoot(woID)
	if err != nil {
		return Workspace{}, err
	}
	ws := Workspace{ID: woID, root: root}
	return ws, os.MkdirAll(ws.HomeDir(), 0o755)
}

func (l Local) DeleteWorkspace(_ context.Context, woID string) error {
	root, err := l.wsRoot(woID)
	if err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func (l Local) Start(_ context.Context, ws Workspace, env map[string]string) (Sandbox, error) {
	root, err := filepath.Abs(ws.root)
	if err != nil {
		return nil, err
	}
	ws.root = root
	return &localSandbox{ws: ws, env: env}, nil
}

type localSandbox struct {
	ws  Workspace
	env map[string]string
}

func (s *localSandbox) Workspace() Workspace       { return s.ws }
func (s *localSandbox) Stop(context.Context) error { return nil }

func (s *localSandbox) Command(dir string, env map[string]string, args ...string) Cmd {
	merged := map[string]string{"HOME": s.ws.HomeDir()}
	for k, v := range s.env {
		merged[k] = v
	}
	for k, v := range env {
		merged[k] = v
	}
	return Cmd{Argv: args, Dir: dir, Env: append(os.Environ(), envList(merged)...)}
}

func (s *localSandbox) Exec(ctx context.Context, dir string, env map[string]string, args ...string) (string, string, int, error) {
	return run(s.Command(dir, env, args...).Exec(ctx))
}

// Docker runs sandboxes as containers through the docker CLI.
type Docker struct {
	Image       string
	CPUs        float64
	MemoryBytes int64
	Bin         string // defaults to "docker"
}

const wsMount = "/ws"

func (d Docker) bin() string {
	if d.Bin != "" {
		return d.Bin
	}
	return "docker"
}

func volumeName(woID string) string { return "lf-ws-" + woID }

func (d Docker) docker(ctx context.Context, args ...string) (string, error) {
	stdout, stderr, code, err := run(exec.CommandContext(ctx, d.bin(), args...))
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("docker %s: exit %d: %s", args[0], code, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(stdout), nil
}

func (d Docker) CreateWorkspace(ctx context.Context, woID string) (Workspace, error) {
	_, err := d.docker(ctx, "volume", "create", "--label", "legos-factory=1", volumeName(woID))
	return Workspace{ID: woID, root: wsMount}, err
}

func (d Docker) DeleteWorkspace(ctx context.Context, woID string) error {
	var err error
	for i := 0; i < 20; i++ {
		if _, err = d.docker(ctx, "volume", "rm", "-f", volumeName(woID)); err == nil || !strings.Contains(err.Error(), "in use") {
			return err
		}
		select { // --rm removes the stopped container asynchronously
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return err
}

func (d Docker) Start(ctx context.Context, ws Workspace, env map[string]string) (Sandbox, error) {
	args := []string{"run", "-d", "--rm", "--label", "legos-factory=1", "--label", "legos-factory-wo=" + ws.ID,
		"-v", volumeName(ws.ID) + ":" + wsMount,
		"-v", "lf-cache-go:/cache/go", "-v", "lf-cache-gomod:/cache/gomod", "-v", "lf-cache-npm:/cache/npm",
		"-w", wsMount,
		"-e", "HOME=" + ws.HomeDir(), "-e", "GOCACHE=/cache/go", "-e", "GOMODCACHE=/cache/gomod", "-e", "npm_config_cache=/cache/npm"}
	if d.CPUs > 0 {
		args = append(args, "--cpus", fmt.Sprint(d.CPUs))
	}
	if d.MemoryBytes > 0 {
		args = append(args, "--memory", fmt.Sprint(d.MemoryBytes))
	}
	for _, kv := range envList(env) {
		args = append(args, "-e", kv)
	}
	args = append(args, d.Image, "sleep", "infinity")
	id, err := d.docker(ctx, args...)
	if err != nil {
		return nil, err
	}
	sb := &dockerSandbox{d: d, id: id, ws: ws}
	if _, stderr, code, err := sb.Exec(ctx, "", nil, "mkdir", "-p", ws.HomeDir()); err != nil || code != 0 {
		_ = sb.Stop(ctx)
		return nil, fmt.Errorf("init workspace home: %v %s", err, stderr)
	}
	return sb, nil
}

type dockerSandbox struct {
	d  Docker
	id string
	ws Workspace
}

func (s *dockerSandbox) Workspace() Workspace { return s.ws }

func (s *dockerSandbox) Command(dir string, env map[string]string, args ...string) Cmd {
	argv := []string{s.d.bin(), "exec", "-i"}
	if dir != "" {
		argv = append(argv, "-w", dir)
	}
	for _, kv := range envList(env) {
		argv = append(argv, "-e", kv)
	}
	argv = append(argv, s.id)
	return Cmd{Argv: append(argv, args...)}
}

func (s *dockerSandbox) Exec(ctx context.Context, dir string, env map[string]string, args ...string) (string, string, int, error) {
	return run(s.Command(dir, env, args...).Exec(ctx))
}

func (s *dockerSandbox) Stop(ctx context.Context) error {
	_, err := s.d.docker(ctx, "stop", "-t", "2", s.id)
	return err
}

// Ports hands out one PORT per worktree key from [lo, hi].
type Ports struct {
	mu     sync.Mutex
	lo, hi int
	used   map[string]int
}

func NewPorts(lo, hi int) *Ports { return &Ports{lo: lo, hi: hi, used: map[string]int{}} }

// Acquire is idempotent per key.
func (p *Ports) Acquire(key string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if port, ok := p.used[key]; ok {
		return port, nil
	}
	taken := map[int]bool{}
	for _, port := range p.used {
		taken[port] = true
	}
	for port := p.lo; port <= p.hi; port++ {
		if !taken[port] {
			p.used[key] = port
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port in %d-%d", p.lo, p.hi)
}

func (p *Ports) Release(key string) {
	p.mu.Lock()
	delete(p.used, key)
	p.mu.Unlock()
}
