// Package engine drives one work order through build, verify and PR using facts as the only state.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/profile"
	"legos-factory/factory/internal/sandbox"
	"legos-factory/factory/internal/scm"
)

type BuilderConfig struct {
	Profile      harness.Profile   `json:"profile"`
	AgentCommand []string          `json:"agent_command"`
	Env          map[string]string `json:"env"`
}

type Project struct {
	ID             string
	RepoURL        string
	Repo           string
	DefaultBranch  string
	SCMKind        string
	SCMBaseURL     string
	SCMUsername    string
	SCMToken       string // loaded from env at startup; never serialized or put in facts
	Setup          string
	Verify         []string
	Builder        BuilderConfig
	RequiredChecks []string
	MaxRequeues    int // 0 means defaultMaxRequeues
}

type projectFile struct {
	ID             string   `json:"id"`
	RepoURL        string   `json:"repo_url"`
	Repo           string   `json:"repo"`
	DefaultBranch  string   `json:"default_branch"`
	SCMKind        string   `json:"scm_kind"`
	SCMBaseURL     string   `json:"scm_base_url"`
	SCMUsername    string   `json:"scm_username"`
	SCMTokenEnv    string   `json:"scm_token_env"`
	Setup          string   `json:"setup"`
	Verify         []string `json:"verify"`
	RequiredChecks []string `json:"required_checks"`
	MaxRequeues    int      `json:"max_requeues"`
	Builder        struct {
		Profile struct {
			Agent       string `json:"agent"`
			Provider    string `json:"provider"`
			Model       string `json:"model"`
			BillingKind string `json:"billing_kind"`
		} `json:"profile"`
		AgentCommand []string          `json:"agent_command"`
		Env          map[string]string `json:"env"` // values may reference ${VAR}
	} `json:"builder"`
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
		SCMBaseURL: f.SCMBaseURL, SCMUsername: f.SCMUsername, Setup: f.Setup, Verify: f.Verify,
		RequiredChecks: f.RequiredChecks, MaxRequeues: f.MaxRequeues}
	if p.DefaultBranch == "" {
		p.DefaultBranch = "main"
	}
	if f.SCMTokenEnv != "" {
		if p.SCMToken = os.Getenv(f.SCMTokenEnv); p.SCMToken == "" {
			return Project{}, fmt.Errorf("env %s (scm_token_env) is empty", f.SCMTokenEnv)
		}
	}
	b := f.Builder
	p.Builder = BuilderConfig{
		Profile:      harness.Profile{Agent: b.Profile.Agent, Provider: b.Profile.Provider, Model: b.Profile.Model, BillingKind: b.Profile.BillingKind},
		AgentCommand: b.AgentCommand,
	}
	if len(b.Env) > 0 {
		p.Builder.Env = map[string]string{}
		for k, v := range b.Env {
			p.Builder.Env[k] = os.ExpandEnv(v)
		}
	}
	if p.ID == "" || p.RepoURL == "" || p.Repo == "" {
		return Project{}, fmt.Errorf("%s: id, repo_url and repo are required", path)
	}
	return p, nil
}

// Profiles resolves a role to a profile and builds its env; *profile.Store implements it.
type Profiles interface {
	Resolve(ctx context.Context, projectID string, role harness.Role) (profile.Profile, error)
	RunEnv(ctx context.Context, p profile.Profile) (profile.RunSpec, error)
	Acquire(ctx context.Context, credID string) (release func(), err error)
}

type Engine struct {
	Profiles Profiles // optional; used when the project has no explicit builder
	Facts    *fact.Store
	Runtime  sandbox.Runtime
	Harness  harness.Harness
	SCM      func(Project) (scm.Provider, error)
	Now      func() time.Time
	Log      *slog.Logger
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
	id := newID("wo-")
	_, err := e.Facts.Append(ctx, id, fact.KindWOCreated, fact.WOCreated{Project: p.ID, Objective: objective})
	return id, err
}

// Status derives the current status and whether the builder run has stalled.
func (e *Engine) Status(ctx context.Context, woID string) (fact.Status, bool, error) {
	facts, err := e.Facts.List(ctx, woID)
	if err != nil {
		return "", false, err
	}
	return fact.Derive(facts), fact.Stalled(facts, e.now()), nil
}

type view struct {
	facts     []fact.Fact
	cycle     []fact.Fact // facts since the latest wo.requeued, inclusive
	objective string
	requeue   *fact.WORequeued
	sessionID string
	prOpened  bool
	lastHead  string
	openRun   string // run.started in the cycle without a matching run.finished
	finished  bool   // cycle has a finished run
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
	for i, f := range facts {
		switch f.Kind {
		case fact.KindWOCreated:
			c, err := fact.Decode[fact.WOCreated](f)
			if err != nil {
				return nil, err
			}
			v.objective = c.Objective
		case fact.KindWORequeued:
			start = i
			r, err := fact.Decode[fact.WORequeued](f)
			if err != nil {
				return nil, err
			}
			v.requeue = &r
		case fact.KindRunStarted:
			r, err := fact.Decode[fact.RunStarted](f)
			if err != nil {
				return nil, err
			}
			if r.SessionID != "" {
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
	return e.fail(ctx, woID, fact.WOFailed{Class: "provider", Message: err.Error(), Model: prof.Model, Provider: prof.Provider})
}

func requeuePrompt(r fact.WORequeued) string {
	s := "CI failed on your pull request. Failing checks: " + strings.Join(r.Failing, ", ") + "."
	if r.Details != "" {
		s += "\n" + r.Details
	}
	return s + "\nFix the cause in the repository, then stop."
}

// builderPrompt: a fresh session has no memory of the objective, so it is restated ahead of any re-queue reason.
func builderPrompt(v *view, fresh bool) string {
	prompt := v.objective
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
func (e *Engine) Run(ctx context.Context, p Project, woID string) error {
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
	sb, err := e.Runtime.Start(ctx, ws, nil)
	if err != nil {
		return err
	}
	if _, err := e.Facts.Append(ctx, woID, fact.KindSandboxStarted, map[string]string{"workspace": ws.ID}); err != nil {
		_ = sb.Stop(ctx)
		return err
	}
	defer func() {
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
	if failed, err := e.setup(ctx, p, woID, sb, dir); err != nil || failed {
		return err
	}

	if !v.cycleHas(fact.KindTaskCommitted) {
		if !v.finished {
			if done, err := e.build(ctx, p, woID, sb, dir, v); err != nil || done {
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
		if len(files) == 0 || (!changed && sha == v.lastHead) {
			return e.fail(ctx, woID, fact.WOFailed{Class: "task_no_changes", Message: "builder produced no changes"})
		}
		if _, err := e.Facts.Append(ctx, woID, fact.KindTaskCommitted, fact.TaskCommitted{Task: task, SHA: sha}); err != nil {
			return err
		}
	}

	if !v.cycleHas(fact.KindVerifyPassed) {
		for _, cmd := range p.Verify {
			stdout, stderr, code, err := sb.Exec(ctx, dir, nil, "sh", "-c", cmd)
			if err != nil {
				return err
			}
			if code != 0 {
				out := tail(stdout+stderr, 4000)
				if _, err := e.Facts.Append(ctx, woID, fact.KindVerifyFailed, fact.VerifyFailed{Commands: []string{cmd}, Output: out}); err != nil {
					return err
				}
				return e.fail(ctx, woID, fact.WOFailed{Class: "verify_failed", Message: fmt.Sprintf("%s: exit %d", cmd, code)})
			}
		}
		if _, err := e.Facts.Append(ctx, woID, fact.KindVerifyPassed, struct{}{}); err != nil {
			return err
		}
	}

	return e.publish(ctx, p, woID, sb, dir, branch, gitAuth, prov, v)
}

func (e *Engine) setup(ctx context.Context, p Project, woID string, sb sandbox.Sandbox, dir string) (failed bool, err error) {
	if p.Setup == "" {
		return false, nil
	}
	ws := sb.Workspace()
	marker := ws.Root() + "/.lf-setup-done-" + task
	if _, _, code, err := sb.Exec(ctx, "", nil, "test", "-f", marker); err != nil {
		return false, err
	} else if code == 0 {
		return false, nil
	}
	stdout, stderr, code, err := sb.Exec(ctx, dir, nil, "sh", "-c", p.Setup)
	if err != nil {
		return false, err
	}
	if code != 0 {
		return true, e.fail(ctx, woID, fact.WOFailed{Class: "setup_failed", Message: fmt.Sprintf("exit %d: %s", code, tail(stdout+stderr, 2000))})
	}
	_, _, _, err = sb.Exec(ctx, "", nil, "touch", marker)
	return false, err
}

// build runs one builder turn; done is true when the work order was failed.
func (e *Engine) build(ctx context.Context, p Project, woID string, sb sandbox.Sandbox, dir string, v *view) (done bool, err error) {
	prompt := builderPrompt(v, false)
	if v.openRun != "" {
		if _, err := e.Facts.Append(ctx, woID, fact.KindRunFinished, fact.RunFinished{Run: v.openRun, StopReason: "interrupted"}); err != nil {
			return false, err
		}
	}
	b := p.Builder
	var price *profile.Price
	var files map[string][]byte
	if b.Profile.Agent == "" && e.Profiles != nil {
		prof, err := e.Profiles.Resolve(ctx, p.ID, harness.RoleBuilder)
		if err != nil {
			return true, e.fail(ctx, woID, fact.WOFailed{Class: "agent_error", Message: "resolve builder profile: " + err.Error()})
		}
		rs, err := e.Profiles.RunEnv(ctx, prof)
		if err != nil {
			return true, e.fail(ctx, woID, fact.WOFailed{Class: "agent_error", Message: "builder profile env: " + err.Error()})
		}
		b = BuilderConfig{Profile: rs.Profile, AgentCommand: profile.AgentCommand(prof.Agent), Env: rs.Env}
		files = rs.Files
		price = &prof.Price
		release, err := e.Profiles.Acquire(ctx, prof.CredentialID)
		if err != nil {
			return false, err
		}
		defer release()
	}
	env := map[string]string{"CLAUDE_CONFIG_DIR": path.Join(sb.Workspace().HomeDir(), "builder-"+task)}
	for k, val := range b.Env {
		env[k] = val
	}
	spec := harness.SessionSpec{
		Role: harness.RoleBuilder, Profile: b.Profile, AgentCommand: b.AgentCommand,
		Dir: dir, Env: env, Files: files, SessionID: v.sessionID, Sandbox: sb,
	}
	sess, err := e.Harness.Start(ctx, spec)
	if errors.Is(err, harness.ErrNoLoad) {
		if _, ferr := e.Facts.Append(ctx, woID, fact.KindRunStep, fact.RunStep{Step: "resume_failed", Summary: firstLine(err.Error(), 200)}); ferr != nil {
			return false, ferr
		}
		prompt = builderPrompt(v, true)
		spec.SessionID = ""
		sess, err = e.Harness.Start(ctx, spec)
	}
	if err != nil {
		return e.agentFailure(ctx, woID, err, b.Profile)
	}
	run := newID("run-")
	if _, err := e.Facts.Append(ctx, woID, fact.KindRunStarted, fact.RunStarted{
		Run: run, Role: string(harness.RoleBuilder), Task: task, Agent: b.Profile.Agent, Provider: b.Profile.Provider,
		Model: b.Profile.Model, BillingKind: b.Profile.BillingKind, SessionID: sess.ID(),
	}); err != nil {
		_ = sess.Close()
		return false, err
	}

	var usage harness.Usage
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
			return false, ctx.Err()
		}
		if IsTransient(perr) {
			return false, perr
		}
		_, _ = e.Facts.Append(ctx, woID, fact.KindRunFinished, fact.RunFinished{Run: run, StopReason: "error"})
		return e.agentFailure(ctx, woID, perr, b.Profile)
	}
	raw, _ := json.Marshal(usage)
	fin := fact.RunFinished{Run: run, StopReason: stop, Usage: raw}
	if price != nil {
		fin.CostUSD = profile.Cost(*price, usage)
	}
	_, err = e.Facts.Append(ctx, woID, fact.KindRunFinished, fin)
	return false, err
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
	return true, e.fail(ctx, woID, fact.WOFailed{Class: "agent_error", Message: err.Error(), Model: prof.Model, Provider: prof.Provider})
}

func (e *Engine) publish(ctx context.Context, p Project, woID string, sb sandbox.Sandbox, dir, branch string, gitAuth *sandbox.GitAuth, prov scm.Provider, v *view) error {
	if err := sandbox.Push(ctx, sb, dir, branch, gitAuth); err != nil {
		return err
	}
	sha, err := headSHA(ctx, sb, dir)
	if err != nil {
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
			Title: firstLine(v.objective, 70), Body: v.objective})
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
	requeues := 0
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
			requeues++
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
	switch fact.Derive(facts) {
	case fact.StatusCIFailed:
		limit := p.MaxRequeues
		if limit <= 0 {
			limit = defaultMaxRequeues
		}
		if requeues >= limit {
			return e.fail(ctx, woID, fact.WOFailed{Class: "ci_retry_exhausted", Message: fmt.Sprintf("CI still failing (%s) after %d re-queues", strings.Join(failing, ", "), requeues)})
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
