// Package engine drives one work order through build, verify and PR using facts as the only state.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"legos-factory/factory/internal/autonomy"
	"legos-factory/factory/internal/catalog"
	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/profile"
	"legos-factory/factory/internal/sandbox"
	"legos-factory/factory/internal/scm"
	"legos-factory/factory/internal/workflow"
)

type BuilderConfig struct {
	Profile      harness.Profile   `json:"profile"`
	AgentCommand []string          `json:"agent_command"`
	Env          map[string]string `json:"env"`
}

type Project struct {
	ID            string
	RepoURL       string
	Repo          string
	DefaultBranch string
	SCMKind       string
	SCMBaseURL    string
	SCMUsername   string
	SCMToken      string // loaded from env at startup; never serialized or put in facts
	Env           Env
	Builder       BuilderConfig
	// Roles is the JSON fallback for roles without a profile binding: foreman, architect, inspector, cataloger.
	Roles          map[string]BuilderConfig
	RequiredChecks []string
	// ProtectedPaths always need a human (ADR-F09).
	ProtectedPaths []string
	// MergeMethod is squash|merge|rebase; empty means squash.
	MergeMethod string
	// MaxLevel caps the autonomy level of every work order (0-3); nil means 3 (ADR-F09).
	MaxLevel    *int
	MaxRequeues int // 0 means defaultMaxRequeues
	// Fallbacks lists profile ids per role, in the order retry_task other_model moves through them.
	Fallbacks map[string][]string
}

// Env is the project's environment binding (SPEC §11): the factory injects it and never provisions infrastructure.
type Env struct {
	Setup       string
	Verify      []string
	Integration *Integration
	// Docker mounts the host docker socket into the sandbox: the agent gets root-equivalent control of the host daemon.
	Docker bool
	// DockerTrustBuilder acknowledges that Docker lets builder code reach the host and the factory's environment; required with integration secrets.
	DockerTrustBuilder bool
	// TestPkg is the targeted-test command template, e.g. "go test ./{pkg}/..."; default derived from go.mod.
	TestPkg string
}

// Integration runs once after verify passes against an environment the project already has.
type Integration struct {
	Command   string
	Endpoints map[string]string // exposed as LF_ENDPOINT_<NAME>
	SecretEnv []string          // names of host env vars passed to this step only, by name
}

func (b BuilderConfig) set() bool { return b.Profile.Agent != "" || len(b.AgentCommand) > 0 }

type projectFile struct {
	ID            string   `json:"id"`
	RepoURL       string   `json:"repo_url"`
	Repo          string   `json:"repo"`
	DefaultBranch string   `json:"default_branch"`
	SCMKind       string   `json:"scm_kind"`
	SCMBaseURL    string   `json:"scm_base_url"`
	SCMUsername   string   `json:"scm_username"`
	SCMTokenEnv   string   `json:"scm_token_env"`
	Setup         string   `json:"setup"`
	Verify        []string `json:"verify"`
	Env           struct {
		Setup       string   `json:"setup"`
		Verify      []string `json:"verify"`
		Integration *struct {
			Command   string            `json:"command"`
			Endpoints map[string]string `json:"endpoints"`
			SecretEnv []string          `json:"secret_env"`
		} `json:"integration"`
		Docker             bool   `json:"docker"`
		DockerTrustBuilder bool   `json:"docker_trust_builder"`
		TestPkg            string `json:"test_pkg"`
	} `json:"env"`
	ProtectedPaths []string               `json:"protected_paths"`
	MergeMethod    string                 `json:"merge_method"`
	MaxLevel       *int                   `json:"max_level"`
	RequiredChecks []string               `json:"required_checks"`
	MaxRequeues    int                    `json:"max_requeues"`
	Fallbacks      map[string][]string    `json:"fallbacks"`
	Builder        builderFile            `json:"builder"`
	Roles          map[string]builderFile `json:"roles"`
}

type builderFile struct {
	Profile struct {
		Agent       string `json:"agent"`
		Provider    string `json:"provider"`
		Model       string `json:"model"`
		BillingKind string `json:"billing_kind"`
	} `json:"profile"`
	AgentCommand []string          `json:"agent_command"`
	Env          map[string]string `json:"env"` // values may reference ${VAR}
}

func (b builderFile) config() BuilderConfig {
	c := BuilderConfig{
		Profile:      harness.Profile{Agent: b.Profile.Agent, Provider: b.Profile.Provider, Model: b.Profile.Model, BillingKind: b.Profile.BillingKind},
		AgentCommand: b.AgentCommand,
	}
	if len(b.Env) > 0 {
		c.Env = map[string]string{}
		for k, v := range b.Env {
			c.Env[k] = os.ExpandEnv(v)
		}
	}
	return c
}

// LoadProject reads a project JSON file and resolves secrets from the environment.
func LoadProject(path string) (Project, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Project{}, err
	}
	var f projectFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return Project{}, fmt.Errorf("parse %s: %w", path, err)
	}
	p := Project{ID: f.ID, RepoURL: f.RepoURL, Repo: f.Repo, DefaultBranch: f.DefaultBranch, SCMKind: f.SCMKind,
		SCMBaseURL: f.SCMBaseURL, SCMUsername: f.SCMUsername,
		Env:            Env{Setup: f.Env.Setup, Verify: f.Env.Verify, Docker: f.Env.Docker, DockerTrustBuilder: f.Env.DockerTrustBuilder, TestPkg: f.Env.TestPkg},
		ProtectedPaths: f.ProtectedPaths, MergeMethod: f.MergeMethod, RequiredChecks: f.RequiredChecks, MaxRequeues: f.MaxRequeues, Fallbacks: f.Fallbacks}
	if f.MaxLevel != nil {
		if *f.MaxLevel < 0 || *f.MaxLevel > workflow.DefaultLevelCap {
			return Project{}, fmt.Errorf("%s: max_level must be 0..%d, got %d", path, workflow.DefaultLevelCap, *f.MaxLevel)
		}
		p.MaxLevel = f.MaxLevel
	}
	for _, pat := range p.ProtectedPaths {
		if err := autonomy.ValidatePattern(pat); err != nil {
			return Project{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	if p.DefaultBranch == "" {
		p.DefaultBranch = "main"
	}
	if f.SCMTokenEnv != "" {
		if p.SCMToken = os.Getenv(f.SCMTokenEnv); p.SCMToken == "" {
			return Project{}, fmt.Errorf("env %s (scm_token_env) is empty", f.SCMTokenEnv)
		}
	}
	if p.Env.Setup == "" {
		p.Env.Setup = f.Setup
	}
	if len(p.Env.Verify) == 0 {
		p.Env.Verify = f.Verify
	}
	if i := f.Env.Integration; i != nil && i.Command != "" {
		p.Env.Integration = &Integration{Command: i.Command, Endpoints: i.Endpoints, SecretEnv: i.SecretEnv}
	}
	if p.Env.Docker && p.Env.Integration != nil && len(p.Env.Integration.SecretEnv) > 0 && !p.Env.DockerTrustBuilder {
		return Project{}, fmt.Errorf("%s: env.docker with env.integration.secret_env is refused: the docker socket gives builder code root on the host and access to the factory's environment, secrets included; set env.docker_trust_builder to true to accept that", path)
	}
	p.Builder = f.Builder.config()
	if len(f.Roles) > 0 {
		p.Roles = map[string]BuilderConfig{}
		for role, b := range f.Roles {
			p.Roles[role] = b.config()
		}
	}
	if insp, ok := p.Roles[string(harness.RoleInspector)]; ok && insp.set() && p.Builder.set() && (insp.Profile.Model == "" || p.Builder.Profile.Model == "") {
		return Project{}, fmt.Errorf("%s: roles.inspector and builder both need profile.model so the engine can verify they differ", path)
	}
	if !slices.Contains([]string{"", "squash", "merge", "rebase"}, p.MergeMethod) {
		return Project{}, fmt.Errorf("%s: merge_method must be squash, merge or rebase, got %q", path, p.MergeMethod)
	}
	if p.ID == "" || p.RepoURL == "" || p.Repo == "" {
		return Project{}, fmt.Errorf("%s: id, repo_url and repo are required", path)
	}
	return p, nil
}

// Profiles resolves a role to a profile and builds its env; *profile.Store implements it.
type Profiles interface {
	Resolve(ctx context.Context, projectID string, role harness.Role) (profile.Profile, error)
	GetProfile(ctx context.Context, id string) (profile.Profile, error)
	RunEnv(ctx context.Context, p profile.Profile) (profile.RunSpec, error)
	Acquire(ctx context.Context, credID string) (release func(), err error)
}

type Engine struct {
	Profiles Profiles // optional; used when the project has no explicit builder
	// Catalog caches context packs per (repo, commit); nil skips the cataloging phase.
	Catalog  *catalog.Store
	catLocks sync.Map // "repo@commit" -> chan struct{}
	Facts    *fact.Store
	Runtime  sandbox.Runtime
	Harness  harness.Harness
	SCM      func(Project) (scm.Provider, error)
	Now      func() time.Time
	Log      *slog.Logger
	// Ports allocates the PORT of each builder worktree; nil means a default range.
	Ports *sandbox.Ports
	// RetryBase is the first backoff after a transient provider failure (doubling, capped); TransientRetries bounds the retries.
	RetryBase        time.Duration
	TransientRetries int
	portsMu          sync.Mutex
	// StableAfter is how long an all-success check set must stay unchanged before ci_green when no required checks are configured; 0 means 60s.
	StableAfter time.Duration
}

const (
	task               = "main"
	defaultMaxRequeues = 3
	defaultStableAfter = 60 * time.Second
)

func (e *Engine) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func (e *Engine) ports() *sandbox.Ports {
	e.portsMu.Lock()
	defer e.portsMu.Unlock()
	if e.Ports == nil {
		e.Ports = sandbox.NewPorts(30000, 30999)
	}
	return e.Ports
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func newID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// IsTransient reports whether err is a provider failure worth retrying later; harness errors qualify by exposing Transient().
func IsTransient(err error) bool {
	var t interface{ Transient() bool }
	return errors.As(err, &t) && t.Transient()
}

func isProviderErr(err error) bool {
	var t interface{ Transient() bool }
	return errors.As(err, &t)
}

func (e *Engine) Create(ctx context.Context, p Project, objective string) (string, error) {
	return e.CreateAt(ctx, p, objective, workflow.DefaultLevelCap)
}

// CreateAt creates a work order with an autonomy cap; 0 makes the plan gate wait for a human.
func (e *Engine) CreateAt(ctx context.Context, p Project, objective string, level int) (string, error) {
	id := newID("wo-")
	_, err := e.Facts.Append(ctx, id, fact.KindWOCreated, fact.WOCreated{Project: p.ID, Objective: objective, LevelCap: level, Line: e.lineConfigured(ctx, p)})
	return id, err
}

// Status derives the current status and whether the builder run has stalled.
func (e *Engine) Status(ctx context.Context, woID string) (fact.Status, bool, error) {
	facts, err := e.Facts.List(ctx, woID)
	if err != nil {
		return "", false, err
	}
	st := fact.Derive(facts)
	if !st.Terminal() && workflow.Fold(facts).Phase == workflow.PhaseAwaitingHuman {
		st = fact.StatusAwaitingHuman
	}
	return st, fact.Stalled(facts, e.now()), nil
}

type view struct {
	facts      []fact.Fact
	cycle      []fact.Fact // facts since the latest wo.requeued, inclusive
	objective  string
	line       bool
	requeue    *fact.WORequeued
	sessionID  string
	prOpened   bool
	lastHead   string
	baseBranch string
	openRun    string // run.started in the cycle without a matching run.finished
	finished   bool   // cycle has a finished run
}

func (v *view) cycleHas(kind string) bool {
	for _, f := range v.cycle {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

func load(facts []fact.Fact) (*view, error) {
	v := &view{facts: facts}
	start := 0
	var lastFailed *fact.WOFailed
	var lastVerify *fact.VerifyFailed
	for i, f := range facts {
		switch f.Kind {
		case fact.KindWOCreated:
			c, err := fact.Decode[fact.WOCreated](f)
			if err != nil {
				return nil, err
			}
			v.objective, v.line, v.baseBranch = c.Objective, c.Line, c.BaseBranch
		case fact.KindWORequeued:
			start = i
			r, err := fact.Decode[fact.WORequeued](f)
			if err != nil {
				return nil, err
			}
			v.requeue = &r
		case fact.KindVerifyFailed:
			if vf, err := fact.Decode[fact.VerifyFailed](f); err == nil {
				lastVerify = &vf
			}
		case fact.KindWOFailed:
			if wf, err := fact.Decode[fact.WOFailed](f); err == nil {
				lastFailed = &wf
			}
		case fact.KindWORetried:
			// a retry after a verify failure rebuilds from the verify output instead of re-verifying the same commit
			if lastFailed != nil && lastFailed.Class == fact.ClassVerify && lastVerify != nil && !v.line {
				start = i
				v.requeue = &fact.WORequeued{Reason: "verify_failed", Failing: lastVerify.Commands, Details: lastVerify.Output}
			}
			lastFailed = nil
		case fact.KindRunStarted:
			r, err := fact.Decode[fact.RunStarted](f)
			if err != nil {
				return nil, err
			}
			if r.SessionID != "" && (r.Role == "" || r.Role == string(harness.RoleBuilder)) && (r.Task == "" || r.Task == task) {
				v.sessionID = r.SessionID
			}
		case fact.KindPROpened:
			p, err := fact.Decode[fact.PROpened](f)
			if err != nil {
				return nil, err
			}
			v.prOpened, v.lastHead = true, p.HeadSHA
		case fact.KindPRPushed:
			p, err := fact.Decode[fact.PRPushed](f)
			if err != nil {
				return nil, err
			}
			v.lastHead = p.HeadSHA
		}
	}
	v.cycle = facts[start:]
	for _, f := range v.cycle {
		switch f.Kind {
		case fact.KindWORetried:
			v.openRun, v.finished = "", false
		case fact.KindRunStarted:
			r, _ := fact.Decode[fact.RunStarted](f)
			v.openRun = r.Run
		case fact.KindRunFinished:
			r, _ := fact.Decode[fact.RunFinished](f)
			if r.Run == v.openRun {
				v.openRun, v.finished = "", true
			}
		}
	}
	return v, nil
}

func (e *Engine) fail(ctx context.Context, woID string, f fact.WOFailed) error {
	e.log().Warn("work order failed", "wo", woID, "class", f.Class, "message", f.Message)
	_, err := e.Facts.Append(ctx, woID, fact.KindWOFailed, f)
	return err
}

// failProvider records a permanent provider failure; transient ones are returned for the caller to retry.
func (e *Engine) failProvider(ctx context.Context, woID string, err error, prof harness.Profile) error {
	if IsTransient(err) {
		return err
	}
	return e.fail(ctx, woID, fact.WOFailed{Class: fact.ClassProvider, Message: err.Error(), Model: prof.Model, Provider: prof.Provider})
}

func requeuePrompt(r fact.WORequeued) string {
	if r.Reason == workflow.ReasonNeedsRebase {
		return rebaseRequeuePrompt(r)
	}
	if r.Reason == "verify_failed" {
		return "Verification failed in the sandbox. Failing commands: " + strings.Join(r.Failing, ", ") + ".\n" + r.Details + "\nFix the cause in the repository, then stop."
	}
	s := "CI failed on your pull request. Failing checks: " + strings.Join(r.Failing, ", ") + "."
	if r.Details != "" {
		s += "\n" + r.Details
	}
	return s + "\nFix the cause in the repository, then stop."
}

// builderPrompt: a fresh session has no memory of the objective, so it is restated ahead of any re-queue reason.
func builderPrompt(v *view, fresh bool, testCmd string, pack *catalog.Pack) string {
	prompt := v.objective + packBlock(pack) + testPrompt(testCmd)
	if v.requeue != nil {
		if fresh {
			prompt += "\n\n"
		} else {
			prompt = ""
		}
		prompt += requeuePrompt(*v.requeue)
	}
	if v.openRun != "" {
		prompt = "Your previous run was interrupted and the worktree may hold partial work. " + prompt
	}
	return prompt
}

var nonShellTools = []string{"edit", "multiedit", "write", "read", "patch", "apply_patch", "notebookedit", "glob", "grep", "ls", "webfetch", "websearch"}

// isTestCall reports whether a tool call is a shell command that runs tests; file tools merely mentioning "test" do not count.
func isTestCall(ev harness.Event, testCmd string) bool {
	if slices.Contains(nonShellTools, strings.ToLower(strings.TrimSpace(ev.Tool))) {
		return false
	}
	text := strings.ToLower(ev.Tool + " " + ev.Text)
	prefix, _, _ := strings.Cut(strings.ToLower(testCmd), "{pkg}")
	if prefix = strings.TrimSpace(prefix); len(prefix) >= 6 && strings.Contains(text, prefix) {
		return true
	}
	return slices.ContainsFunc([]string{"go test", "npm test", "make test"}, func(c string) bool { return strings.Contains(text, c) })
}

// testCommand is the tier-1 targeted test command shown to the builder; "" when neither configured nor derivable.
func (e *Engine) testCommand(ctx context.Context, p Project, sb sandbox.Sandbox, dir string) string {
	if c := ownerTestPkg(p); c != "" {
		return c
	}
	if _, _, code, err := sb.Exec(ctx, dir, nil, "test", "-f", "go.mod"); err == nil && code == 0 {
		return "go test ./{pkg}/..."
	}
	return ""
}

func testPrompt(cmd string) string {
	s := "\n\nBefore finishing, run targeted tests for the packages you touched, without -race"
	if cmd != "" {
		s += ", with `" + strings.ReplaceAll(cmd, "{pkg}", "<pkg>") + "`"
	}
	return s + ". State the exact command and its result in your final message."
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func firstLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// Run is idempotent: it skips every step already recorded in the current cycle (creation to PR, or requeue to PR).
// Transient provider failures and sandbox restarts are bounded across the whole work order, not per call.
func (e *Engine) Run(ctx context.Context, p Project, woID string) error {
	err := e.run(ctx, p, woID)
	if err != nil && IsTransient(err) && ctx.Err() == nil {
		if wf, over := e.overBudget(ctx, woID); over {
			return e.fail(ctx, woID, wf)
		}
	}
	return err
}

func (e *Engine) run(ctx context.Context, p Project, woID string) error {
	facts, err := e.Facts.List(ctx, woID)
	if err != nil {
		return err
	}
	if len(facts) == 0 {
		return fmt.Errorf("unknown work order %s", woID)
	}
	if fact.Derive(facts).Terminal() {
		return nil
	}
	v, err := load(facts)
	if err != nil {
		return err
	}
	if v.cycleHas(fact.KindPROpened) || v.cycleHas(fact.KindPRPushed) {
		return nil
	}
	if v.baseBranch != "" {
		p.DefaultBranch = v.baseBranch
	}

	prov, err := e.SCM(p)
	if err != nil {
		return err
	}
	auth := prov.GitAuth()
	gitAuth := &sandbox.GitAuth{Username: auth.Username, Token: auth.Token}

	ws, err := e.Runtime.CreateWorkspace(ctx, woID)
	if err != nil {
		return err
	}
	ws.Docker = p.Env.Docker
	sb, err := e.Runtime.Start(ctx, ws, nil)
	if err != nil {
		return err
	}
	if _, err := e.Facts.Append(ctx, woID, fact.KindSandboxStarted, map[string]string{"workspace": ws.ID}); err != nil {
		_ = sb.Stop(ctx)
		return err
	}
	defer func() { // sb is the current sandbox: restartSandbox replaces it
		stopCtx := context.WithoutCancel(ctx)
		if err := sb.Stop(stopCtx); err != nil {
			e.log().Warn("stop sandbox", "wo", woID, "err", err)
		}
		_, _ = e.Facts.Append(stopCtx, woID, fact.KindSandboxStopped, map[string]string{"workspace": ws.ID})
	}()

	if err := sandbox.Clone(ctx, sb, p.RepoURL, gitAuth, p.DefaultBranch); err != nil {
		return err
	}
	branch := "lf/" + woID
	dir, err := sandbox.AddWorktree(ctx, sb, task, branch, p.DefaultBranch)
	if err != nil {
		return err
	}
	if failed, err := e.setup(ctx, p, woID, sb, dir, task); err != nil || failed {
		return err
	}

	co := &orch{e: e, p: p, woID: woID, sb: sb, dir: dir, objective: v.objective}
	pack, err := e.catalogPack(ctx, co, v.facts)
	if errors.Is(err, errWOFailed) {
		return nil
	}
	if err != nil {
		return err
	}
	co.pack = pack

	line := v.requeue == nil && v.line
	var o *orch
	if line {
		o = co
		o.lim = workflow.DefaultLimits()
		o.lim.BuilderFallbacks = len(p.Fallbacks[string(harness.RoleBuilder)])
	} else if !v.cycleHas(fact.KindTaskCommitted) {
		if rebasing(v) {
			if done, err := e.rebaseStep(ctx, p, woID, sb, dir, gitAuth, v); err != nil || done {
				return err
			}
		} else if !v.finished {
			if done, err := e.build(ctx, p, woID, sb, dir, v, pack); err != nil || done {
				return err
			}
		}
		sha, changed, err := sandbox.CommitAll(ctx, sb, dir, "lf: "+firstLine(v.objective, 60))
		if err != nil {
			return err
		}
		files, err := sandbox.ChangedFiles(ctx, sb, dir, p.DefaultBranch)
		if err != nil {
			return err
		}
		if len(files) == 0 || (!changed && sha == v.lastHead && !rebasing(v)) {
			return e.fail(ctx, woID, fact.WOFailed{Class: fact.ClassTask, Reason: "no_changes", Message: "builder produced no changes"})
		}
		if err := e.warnLockfileNoise(ctx, woID, "", files); err != nil {
			return err
		}
		if _, err := e.Facts.Append(ctx, woID, fact.KindTaskCommitted, fact.TaskCommitted{Task: task, SHA: sha}); err != nil {
			return err
		}
	}

	for {
		next, err := e.verifyStep(ctx, p, woID, sb, dir, o, v)
		if err != nil && e.infraFailure(ctx, sb, err) {
			restarts, _, lerr := e.budgetUsed(ctx, woID)
			if lerr != nil {
				return lerr
			}
			if restarts >= maxInfraRestarts {
				return e.fail(ctx, woID, fact.WOFailed{Class: fact.ClassInfra, Reason: "sandbox_restarts_exhausted",
					Message: fmt.Sprintf("sandbox failed %d times: %v", restarts+1, err)})
			}
			nsb, rerr := e.restartSandbox(ctx, woID, sb, err, restarts+1)
			if rerr != nil {
				return rerr
			}
			sb = nsb
			if o != nil {
				o.sb = sb
			}
			continue
		}
		if err != nil || next == stepStop {
			return err
		}
		if next == stepPublish {
			break
		}
	}

	return e.publish(ctx, p, woID, sb, dir, branch, gitAuth, prov, v)
}

type step int

const (
	stepPublish step = iota
	stepAgain
	stepStop
)

const (
	tierVerify      = "verify"
	tierIntegration = "integration"
)

// verifyStep drives the line (when there is one) to a verifiable branch and runs the verify commands once. A verify
// failure in the line goes back to the owning builders (stepAgain); on the single-builder path it fails the work order.
func (e *Engine) verifyStep(ctx context.Context, p Project, woID string, sb sandbox.Sandbox, dir string, o *orch, v *view) (step, error) {
	if o != nil {
		if ready, err := o.run(ctx); err != nil || !ready {
			return stepStop, err
		}
	}
	if v.cycleHas(fact.KindVerifyPassed) {
		return stepPublish, nil
	}
	type check struct {
		tier, cmd string
		env       map[string]string
	}
	var checks []check
	for _, cmd := range p.Env.Verify {
		checks = append(checks, check{tier: tierVerify, cmd: cmd})
	}
	var secrets []string
	// The integration command runs builder-authored repo code with these secrets in its environment; output redaction is best effort.
	if in := p.Env.Integration; in != nil && in.Command != "" {
		env := map[string]string{}
		for name, url := range in.Endpoints {
			env["LF_ENDPOINT_"+strings.ToUpper(name)] = url
		}
		var short []string
		for _, name := range in.SecretEnv {
			val := os.Getenv(name)
			if val == "" {
				return stepStop, e.fail(ctx, woID, fact.WOFailed{Class: fact.ClassConfig, Reason: "integration_secret_missing", Message: "host env " + name + " (env.integration.secret_env) is empty"})
			}
			env[name] = val
			if len(val) < minRedactableSecret {
				short = append(short, name)
				continue
			}
			secrets = append(secrets, val)
		}
		if len(short) > 0 {
			msg := fmt.Sprintf("secrets %s are shorter than %d characters and cannot be redacted from integration output", strings.Join(short, ", "), minRedactableSecret)
			if _, err := e.Facts.Append(ctx, woID, fact.KindRunStep, fact.RunStep{Step: "integration_secret_unredactable", Summary: msg}); err != nil {
				return stepStop, err
			}
		}
		checks = append(checks, check{tier: tierIntegration, cmd: in.Command, env: env})
	}
	for _, c := range checks {
		stdout, stderr, code, err := sb.Exec(ctx, dir, c.env, "sh", "-c", c.cmd)
		if err != nil {
			return stepStop, err
		}
		if code == 0 {
			continue
		}
		out := stdout + stderr
		if c.tier == tierIntegration {
			out = redactSecrets(out, secrets)
		}
		out = tail(out, 4000)
		if o != nil {
			return stepAgain, o.verifyFailed(ctx, c.tier, c.cmd, out)
		}
		if _, err := e.Facts.Append(ctx, woID, fact.KindVerifyFailed, fact.VerifyFailed{Tier: c.tier, Commands: []string{c.cmd}, Output: out}); err != nil {
			return stepStop, err
		}
		return stepStop, e.fail(ctx, woID, fact.WOFailed{Class: fact.ClassVerify, Reason: "verify_failed", Message: fmt.Sprintf("%s (%s): exit %d", c.cmd, c.tier, code)})
	}
	_, err := e.Facts.Append(ctx, woID, fact.KindVerifyPassed, struct{}{})
	return stepPublish, err
}

const minRedactableSecret = 8

// redactSecrets masks each secret as the integration step may print it: raw, base64 (standard and URL alphabets,
// padded or not) and URL-encoded. Redact before truncating so a secret cut by the tail window cannot leak a prefix.
// TODO: uncertain — base64 of a secret embedded at a non-aligned offset inside a larger encoded blob is not matched
func redactSecrets(out string, secrets []string) string {
	var forms []string
	for _, sec := range secrets {
		b := []byte(sec)
		forms = append(forms, sec, url.QueryEscape(sec), url.PathEscape(sec),
			base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
			base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b))
	}
	slices.SortFunc(forms, func(a, b string) int { return len(b) - len(a) })
	for _, f := range forms {
		if len(f) >= minRedactableSecret {
			out = strings.ReplaceAll(out, f, "***")
		}
	}
	return out
}

// infraFailure reports whether err means the sandbox died, as opposed to a provider or task error.
func (e *Engine) infraFailure(ctx context.Context, sb sandbox.Sandbox, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	switch f := Classify(err, false); f.Class {
	case fact.ClassInfra:
		return true
	case fact.ClassTask:
		return Classify(err, sandboxDown(ctx, sb)).Class == fact.ClassInfra
	}
	return false
}

// restartSandbox replaces a dead sandbox with a new one on the same volume; the facts say where to resume.
func (e *Engine) restartSandbox(ctx context.Context, woID string, old sandbox.Sandbox, cause error, attempt int) (sandbox.Sandbox, error) {
	ws := old.Workspace()
	_ = old.Stop(context.WithoutCancel(ctx))
	if _, err := e.Facts.Append(ctx, woID, fact.KindSandboxRestarted, fact.SandboxRestarted{Workspace: ws.ID, Attempt: attempt, Message: firstLine(cause.Error(), 300)}); err != nil {
		return nil, err
	}
	return e.Runtime.Start(ctx, ws, nil)
}

func (e *Engine) setup(ctx context.Context, p Project, woID string, sb sandbox.Sandbox, dir, task string) (failed bool, err error) {
	if p.Env.Setup == "" {
		return false, nil
	}
	ws := sb.Workspace()
	marker := ws.Root() + "/.lf-setup-done-" + task
	if _, _, code, err := sb.Exec(ctx, "", nil, "test", "-f", marker); err != nil {
		return false, err
	} else if code == 0 {
		return false, nil
	}
	stdout, stderr, code, err := sb.Exec(ctx, dir, nil, "sh", "-c", p.Env.Setup)
	if err != nil {
		return false, err
	}
	if code != 0 {
		return true, e.fail(ctx, woID, fact.WOFailed{Class: fact.ClassTask, Reason: "setup_failed", Message: fmt.Sprintf("exit %d: %s", code, tail(stdout+stderr, 2000))})
	}
	_, _, _, err = sb.Exec(ctx, "", nil, "touch", marker)
	return false, err
}

// agentCfg is a resolved role configuration; release must always be called.
type agentCfg struct {
	BuilderConfig
	files   map[string][]byte
	price   *profile.Price
	release func()
}

// agentConfig picks the explicit JSON config when it names an agent, else the profile bound to role.
// The builder keeps its M1 fallback to an empty config; other roles have none.
func (e *Engine) agentConfig(ctx context.Context, p Project, role harness.Role) (agentCfg, error) {
	return e.agentConfigN(ctx, p, role, 0)
}

// agentConfigN with fallback n > 0 uses the n-th profile of p.Fallbacks[role] instead of the primary one.
func (e *Engine) agentConfigN(ctx context.Context, p Project, role harness.Role, n int) (agentCfg, error) {
	if n > 0 {
		ids := p.Fallbacks[string(role)]
		if e.Profiles == nil || n > len(ids) {
			return agentCfg{}, fmt.Errorf("no fallback %d configured for role %s", n, role)
		}
		prof, err := e.Profiles.GetProfile(ctx, ids[n-1])
		if err != nil {
			return agentCfg{}, fmt.Errorf("fallback profile %s: %w", ids[n-1], err)
		}
		return e.profileConfig(ctx, prof, role)
	}
	explicit := p.Builder
	if role != harness.RoleBuilder {
		explicit = p.Roles[string(role)]
	}
	usable := explicit.Profile.Agent != ""
	if role != harness.RoleBuilder {
		usable = explicit.set()
	}
	if usable || e.Profiles == nil {
		if !usable && role != harness.RoleBuilder {
			return agentCfg{}, fmt.Errorf("no profile configured for role %s", role)
		}
		return agentCfg{BuilderConfig: explicit, release: func() {}}, nil
	}
	prof, err := e.Profiles.Resolve(ctx, p.ID, role)
	if err != nil {
		return agentCfg{}, fmt.Errorf("resolve %s profile: %w", role, err)
	}
	return e.profileConfig(ctx, prof, role)
}

// roleModel names the model a role would run on without acquiring a credential slot; fallback n > 0 selects the
// n-th fallback profile. An empty or unresolvable model is an error: callers must not treat it as "different".
func (e *Engine) roleModel(ctx context.Context, p Project, role harness.Role, n int) (string, error) {
	var model string
	switch explicit := p.Builder; {
	case n > 0:
		ids := p.Fallbacks[string(role)]
		if e.Profiles == nil || n > len(ids) {
			return "", fmt.Errorf("no fallback %d configured for role %s", n, role)
		}
		prof, err := e.Profiles.GetProfile(ctx, ids[n-1])
		if err != nil {
			return "", fmt.Errorf("fallback profile %s: %w", ids[n-1], err)
		}
		model = prof.Model
	default:
		if role != harness.RoleBuilder {
			explicit = p.Roles[string(role)]
		}
		usable := explicit.Profile.Agent != ""
		if role != harness.RoleBuilder {
			usable = explicit.set()
		}
		switch {
		case usable:
			model = explicit.Profile.Model
		case e.Profiles != nil:
			prof, err := e.Profiles.Resolve(ctx, p.ID, role)
			if err != nil {
				return "", fmt.Errorf("resolve %s profile: %w", role, err)
			}
			model = prof.Model
		}
	}
	if model == "" {
		return "", fmt.Errorf("the %s model is not known", role)
	}
	return model, nil
}

func (e *Engine) profileConfig(ctx context.Context, prof profile.Profile, role harness.Role) (agentCfg, error) {
	rs, err := e.Profiles.RunEnv(ctx, prof)
	if err != nil {
		return agentCfg{}, fmt.Errorf("%s profile env: %w", role, err)
	}
	release, err := e.Profiles.Acquire(ctx, prof.CredentialID)
	if err != nil {
		return agentCfg{}, err
	}
	return agentCfg{BuilderConfig: BuilderConfig{Profile: rs.Profile, AgentCommand: profile.AgentCommand(prof.Agent), Env: rs.Env},
		files: rs.Files, price: &prof.Price, release: release}, nil
}

// convo is one agent turn: a session for (role, task) resumed from sessionID when possible.
type convo struct {
	role      harness.Role
	task      string
	dir       string
	cfg       agentCfg
	policy    harness.ToolPolicy
	sessionID string
	openRun   string // an interrupted run to close before starting
	env       map[string]string
	prompt    func(fresh bool) string
	testCmd   string // the builder's targeted-test command; shell calls running it (or go/npm/make test) count as testing
}

// converse runs the turn, retrying transient provider failures with exponential backoff (bounded, outside any task
// retry budget). agentErr is the agent's failure for the caller to classify; err is infrastructure (fact store, context,
// a dead sandbox, a transient failure that outlived the retries) and is returned as is.
func (e *Engine) converse(ctx context.Context, woID string, sb sandbox.Sandbox, c convo) (reply string, agentErr, err error) {
	for attempt := 1; ; attempt++ {
		reply, agentErr, err = e.converseOnce(ctx, woID, sb, c)
		failure := err
		if failure == nil {
			failure = agentErr
		}
		if failure == nil || !IsTransient(failure) || attempt > e.transientTries() {
			return reply, agentErr, err
		}
		delay := backoff(e.retryBase(), attempt)
		if _, ferr := e.Facts.Append(ctx, woID, fact.KindRunStep, fact.RunStep{Step: "provider_retry",
			Summary: firstLine(fmt.Sprintf("attempt %d of %d in %s: %v", attempt, e.transientTries(), delay, failure), 200)}); ferr != nil {
			return "", nil, ferr
		}
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-time.After(delay):
		}
		c.openRun = ""
	}
}

func (e *Engine) converseOnce(ctx context.Context, woID string, sb sandbox.Sandbox, c convo) (reply string, agentErr, err error) {
	if c.openRun != "" {
		if _, err := e.Facts.Append(ctx, woID, fact.KindRunFinished, fact.RunFinished{Run: c.openRun, StopReason: "interrupted"}); err != nil {
			return "", nil, err
		}
	}
	b := c.cfg
	env := map[string]string{"CLAUDE_CONFIG_DIR": path.Join(sb.Workspace().HomeDir(), string(c.role)+"-"+c.task)}
	for k, val := range c.env {
		env[k] = val
	}
	for k, val := range b.Env {
		env[k] = val
	}
	spec := harness.SessionSpec{
		Role: c.role, Profile: b.Profile, AgentCommand: b.AgentCommand, ToolPolicy: c.policy,
		Dir: c.dir, Env: env, Files: b.files, SessionID: c.sessionID, Sandbox: sb,
	}
	prompt := c.prompt(false)
	sess, serr := e.Harness.Start(ctx, spec)
	if errors.Is(serr, harness.ErrNoLoad) {
		if _, ferr := e.Facts.Append(ctx, woID, fact.KindRunStep, fact.RunStep{Step: "resume_failed", Summary: firstLine(serr.Error(), 200)}); ferr != nil {
			return "", nil, ferr
		}
		prompt = c.prompt(true)
		spec.SessionID = ""
		sess, serr = e.Harness.Start(ctx, spec)
	}
	if serr != nil {
		return "", serr, nil
	}
	run := newID("run-")
	if _, err := e.Facts.Append(ctx, woID, fact.KindRunStarted, fact.RunStarted{
		Run: run, Role: string(c.role), Task: c.task, Agent: b.Profile.Agent, Provider: b.Profile.Provider,
		Model: b.Profile.Model, BillingKind: b.Profile.BillingKind, SessionID: sess.ID(),
	}); err != nil {
		_ = sess.Close()
		return "", nil, err
	}

	var usage harness.Usage
	var last string
	var testSeen, edited bool
	reads := 0
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for ev := range sess.Events() {
			switch ev.Kind {
			case harness.EventStep, harness.EventToolCall:
				step := string(ev.Kind)
				if ev.Tool != "" {
					step = ev.Tool
				}
				if ev.Kind == harness.EventStep {
					last = ev.Text
				}
				if ev.Kind == harness.EventToolCall && isTestCall(ev, c.testCmd) {
					testSeen = true
				}
				if ev.Kind == harness.EventToolCall && !edited {
					switch toolKind(ev) {
					case "edit":
						edited = true
					case "read":
						reads++
					}
				}
				if _, err := e.Facts.Append(ctx, woID, fact.KindRunStep, fact.RunStep{Run: run, Step: step, Summary: firstLine(ev.Text, 200)}); err != nil {
					e.log().Warn("record run step", "wo", woID, "err", err)
				}
			case harness.EventUsage:
				usage.InputTokens += ev.Usage.InputTokens
				usage.OutputTokens += ev.Usage.OutputTokens
				usage.CacheReadTokens += ev.Usage.CacheReadTokens
				usage.CacheCreateTokens += ev.Usage.CacheCreateTokens
				usage.Models = mergeModels(usage.Models, ev.Usage.Models)
			}
		}
	}()

	stop, perr := sess.Prompt(ctx, prompt)
	_ = sess.Close()
	<-drained

	if perr != nil {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		stop := "error"
		if IsTransient(perr) {
			stop = "transient_error"
		}
		_, _ = e.Facts.Append(ctx, woID, fact.KindRunFinished, fact.RunFinished{Run: run, StopReason: stop})
		if IsTransient(perr) {
			return "", nil, perr
		}
		if !isProviderErr(perr) && sandboxDown(context.WithoutCancel(ctx), sb) {
			return "", nil, classed(fact.ClassInfra, "sandbox_down", perr)
		}
		return "", perr, nil
	}
	if c.role == harness.RoleBuilder && !testSeen {
		if _, err := e.Facts.Append(ctx, woID, fact.KindRunStep, fact.RunStep{Run: run, Step: "no_targeted_tests", Summary: "builder finished without running any test command"}); err != nil {
			return "", nil, err
		}
	}
	raw, _ := json.Marshal(usage)
	fin := fact.RunFinished{Run: run, StopReason: stop, Usage: raw}
	if c.role == harness.RoleBuilder {
		fin.ReadsBeforeEdit, fin.Edited = reads, edited
	}
	if b.price != nil {
		fin.CostUSD = profile.Cost(*b.price, usage)
	}
	_, err = e.Facts.Append(ctx, woID, fact.KindRunFinished, fin)
	return last, nil, err
}

// build runs one builder turn; done is true when the work order was failed.
func (e *Engine) build(ctx context.Context, p Project, woID string, sb sandbox.Sandbox, dir string, v *view, pack *catalog.Pack) (done bool, err error) {
	cfg, err := e.agentConfig(ctx, p, harness.RoleBuilder)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, e.fail(ctx, woID, fact.WOFailed{Class: fact.ClassTask, Reason: "agent_error", Message: err.Error()})
	}
	defer cfg.release()
	testCmd := e.testCommand(ctx, p, sb, dir)
	_, agentErr, err := e.converse(ctx, woID, sb, convo{
		role: harness.RoleBuilder, task: task, dir: dir, cfg: cfg, sessionID: v.sessionID, openRun: v.openRun, testCmd: testCmd,
		prompt: func(fresh bool) string { return builderPrompt(v, fresh, testCmd, pack) },
	})
	if err != nil {
		return false, err
	}
	if agentErr != nil {
		return e.agentFailure(ctx, woID, agentErr, cfg.Profile)
	}
	return false, nil
}

func mergeModels(dst, src []harness.ModelUsage) []harness.ModelUsage {
	for _, m := range src {
		i := slices.IndexFunc(dst, func(d harness.ModelUsage) bool { return d.Model == m.Model })
		if i < 0 {
			dst = append(dst, m)
			continue
		}
		dst[i].InputTokens += m.InputTokens
		dst[i].OutputTokens += m.OutputTokens
		dst[i].CacheReadTokens += m.CacheReadTokens
		dst[i].CacheWriteTokens += m.CacheWriteTokens
	}
	return dst
}

func (e *Engine) agentFailure(ctx context.Context, woID string, err error, prof harness.Profile) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if isProviderErr(err) {
		if IsTransient(err) {
			return false, err
		}
		return true, e.failProvider(ctx, woID, err, prof)
	}
	return true, e.fail(ctx, woID, fact.WOFailed{Class: fact.ClassTask, Reason: "agent_error", Message: err.Error(), Model: prof.Model, Provider: prof.Provider})
}

func (e *Engine) publish(ctx context.Context, p Project, woID string, sb sandbox.Sandbox, dir, branch string, gitAuth *sandbox.GitAuth, prov scm.Provider, v *view) error {
	push := func() error { return sandbox.Push(ctx, sb, dir, branch, gitAuth) }
	if rebasing(v) {
		push = func() error { return sandbox.PushLease(ctx, sb, dir, branch, v.lastHead, gitAuth) }
	}
	if err := push(); err != nil {
		return err
	}
	sha, err := headSHA(ctx, sb, dir)
	if err != nil {
		return err
	}
	facts, err := e.Facts.List(ctx, woID)
	if err != nil {
		return err
	}
	level := computeLevel(ctx, p, sb, dir, sha, facts)
	if _, err := e.Facts.Append(ctx, woID, fact.KindAutonomyLevel, level); err != nil {
		return err
	}
	pr, found, err := prov.FindPR(ctx, p.Repo, branch)
	if err != nil {
		return e.failProvider(ctx, woID, err, harness.Profile{})
	}
	if found && v.prOpened {
		_, err = e.Facts.Append(ctx, woID, fact.KindPRPushed, fact.PRPushed{HeadSHA: sha})
		return err
	}
	if !found {
		pr, err = prov.OpenPR(ctx, scm.PRRequest{Repo: p.Repo, Head: branch, Base: p.DefaultBranch,
			Title: firstLine(v.objective, 70), Body: prBody(v.objective, level)})
		if err != nil {
			return e.failProvider(ctx, woID, err, harness.Profile{})
		}
	}
	_, err = e.Facts.Append(ctx, woID, fact.KindPROpened, fact.PROpened{Number: pr.Number, URL: pr.URL, Branch: branch, HeadSHA: sha})
	return err
}

func headSHA(ctx context.Context, sb sandbox.Sandbox, dir string) (string, error) {
	out, stderr, code, err := sb.Exec(ctx, dir, nil, "git", "rev-parse", "HEAD")
	if err != nil || code != 0 {
		return "", fmt.Errorf("rev-parse HEAD: %v %s", err, stderr)
	}
	return strings.TrimSpace(out), nil
}

// Observe does one polling pass over the PR: new check states, merge/close, and CI-failure requeue.
func (e *Engine) Observe(ctx context.Context, p Project, woID string) error {
	facts, err := e.Facts.List(ctx, woID)
	if err != nil {
		return err
	}
	switch st := fact.Derive(facts); st {
	case fact.StatusPROpen, fact.StatusCIFailed, fact.StatusCIGreen:
	default:
		return nil
	}
	var number int
	var head string
	var lastChange time.Time
	requeues, rebases := 0, 0
	known := map[string]string{} // check name -> last observed state for head
	for _, f := range facts {
		switch f.Kind {
		case fact.KindPROpened:
			o, err := fact.Decode[fact.PROpened](f)
			if err != nil {
				return err
			}
			number, head, known, lastChange = o.Number, o.HeadSHA, map[string]string{}, time.Time{}
		case fact.KindPRPushed:
			o, err := fact.Decode[fact.PRPushed](f)
			if err != nil {
				return err
			}
			head, known, lastChange = o.HeadSHA, map[string]string{}, time.Time{}
		case fact.KindWORequeued:
			if r, err := fact.Decode[fact.WORequeued](f); err == nil && r.Reason == workflow.ReasonNeedsRebase {
				rebases++
			} else {
				requeues++
			}
		case fact.KindWORetried:
			requeues, rebases = 0, 0
		case fact.KindCheckObserved:
			c, err := fact.Decode[fact.CheckObserved](f)
			if err != nil {
				return err
			}
			if c.HeadSHA == head {
				known[c.Name] = c.State
				lastChange = f.At
			}
		}
	}
	prov, err := e.SCM(p)
	if err != nil {
		return err
	}
	checks, err := prov.Checks(ctx, p.Repo, head)
	if err != nil {
		return e.failProvider(ctx, woID, err, harness.Profile{})
	}
	var failing []string
	var details []string
	changed, allSuccess := false, len(checks) > 0
	observed := map[string]string{}
	for _, c := range checks {
		observed[c.Name] = c.State
		allSuccess = allSuccess && c.State == "success"
		if known[c.Name] != c.State {
			changed = true
			if _, err := e.Facts.Append(ctx, woID, fact.KindCheckObserved, fact.CheckObserved{Name: c.Name, State: c.State, HeadSHA: head}); err != nil {
				return err
			}
		}
		if c.State == "failure" {
			failing = append(failing, c.Name)
			if c.URL != "" {
				details = append(details, c.Name+": "+c.URL)
			}
		}
	}
	state, err := prov.PRState(ctx, p.Repo, number)
	if err != nil {
		return e.failProvider(ctx, woID, err, harness.Profile{})
	}
	switch state {
	case "merged":
		_, err = e.Facts.Append(ctx, woID, fact.KindPRMerged, struct{}{})
		return err
	case "closed":
		_, err = e.Facts.Append(ctx, woID, fact.KindPRClosed, struct{}{})
		return err
	}
	facts, err = e.Facts.List(ctx, woID)
	if err != nil {
		return err
	}
	derived := fact.Derive(facts)
	wf := workflow.Fold(facts)
	if wf.Phase == workflow.PhaseAwaitingHuman && wf.Gate != workflow.GatePR {
		return nil
	}
	if derived != fact.StatusCIFailed && e.conflictsWithBase(ctx, p, prov, number) {
		if rebases >= maxRebases {
			_, err = e.Facts.Append(ctx, woID, workflow.KindEscalated, workflow.Escalated{Class: "merge", Reason: fmt.Sprintf("the pull request still conflicts with %s after %d rebases", p.DefaultBranch, rebases)})
			return err
		}
		_, err = e.Facts.Append(ctx, woID, fact.KindWORequeued, fact.WORequeued{Reason: workflow.ReasonNeedsRebase, Details: "base branch moved and the pull request conflicts"})
		return err
	}
	switch derived {
	case fact.StatusCIGreen:
		err = e.observeGreen(ctx, p, woID, prov, facts, number, head)
	case fact.StatusCIFailed:
		limit := p.MaxRequeues
		if limit <= 0 {
			limit = defaultMaxRequeues
		}
		if requeues >= limit {
			return e.fail(ctx, woID, fact.WOFailed{Class: fact.ClassVerify, Reason: "ci_retry_exhausted", Message: fmt.Sprintf("CI still failing (%s) after %d re-queues", strings.Join(failing, ", "), requeues)})
		}
		_, err = e.Facts.Append(ctx, woID, fact.KindWORequeued, fact.WORequeued{Reason: "ci_failed", Failing: failing, Details: strings.Join(details, "\n")})
	case fact.StatusPROpen:
		if e.checksFinal(p, observed, allSuccess, changed, lastChange) {
			_, err = e.Facts.Append(ctx, woID, fact.KindChecksStable, fact.ChecksStable{HeadSHA: head})
		}
	}
	return err
}

// checksFinal: with required checks, all of them present and passing; otherwise the same all-success set seen unchanged
// on a previous pass at least StableAfter ago, since a second workflow may not have registered yet.
func (e *Engine) checksFinal(p Project, observed map[string]string, allSuccess, changed bool, lastChange time.Time) bool {
	if len(p.RequiredChecks) > 0 {
		for _, name := range p.RequiredChecks {
			if observed[name] != "success" {
				return false
			}
		}
		return true
	}
	after := e.StableAfter
	if after <= 0 {
		after = defaultStableAfter
	}
	return allSuccess && !changed && !lastChange.IsZero() && e.now().Sub(lastChange) >= after
}

// validTemplate accepts only single-line templates with a {pkg} placeholder and no shell control syntax.
func validTemplate(t string) bool {
	return len(t) <= 200 && strings.Contains(t, "{pkg}") && !strings.ContainsAny(t, "\n\r`;|") && !strings.Contains(t, "$(") && !strings.Contains(t, "&&")
}

// ownerTestPkg is the only configured source of an executable test command; an invalid template is ignored.
func ownerTestPkg(p Project) string {
	if validTemplate(p.Env.TestPkg) {
		return p.Env.TestPkg
	}
	return ""
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
