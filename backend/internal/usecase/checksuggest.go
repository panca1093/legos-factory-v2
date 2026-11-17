package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"legos-factory/backend/internal/model"
)

var (
	ErrCheckSuggestBusy        = errors.New("a check suggestion is already running for this project")
	ErrCheckSuggestUnavailable = errors.New("suggesting checks is unavailable: no agent runtime can read the repository; use the ACP harness")
)

const (
	checkOutputTail    = 2000
	suggestTimeout     = 30 * time.Minute
	suggestUsageRole   = "checks"
	suggestAttempts    = 2
	envFileScanDepth   = 3
	minDotenvValueSize = 4
	maxSmokeReadySec   = 600
)

// CheckProposalStore keeps one suggestion per project.
type CheckProposalStore interface {
	Get(ctx context.Context, projectID string) (*model.CheckProposalRow, error)
	Begin(ctx context.Context, projectID string) (bool, error)
	Finish(ctx context.Context, projectID, proposal, errMsg string) error
	Delete(ctx context.Context, projectID string) (bool, error)
	FailRunning(ctx context.Context, reason string) error
}

// CheckRunResult is one dry-run result of a proposed setup step or check.
type CheckRunResult struct {
	Name       string        `json:"name"`
	Kind       string        `json:"kind"` // setup | check
	Command    string        `json:"command"`
	Outcome    model.Outcome `json:"outcome"`
	ExitCode   *int          `json:"exit_code"`
	OutputTail string        `json:"output_tail"`
}

// CheckProposal is a suggested profile with its dry run on the default branch.
type CheckProposal struct {
	Profile   model.Profile     `json:"profile"`
	Results   []CheckRunResult  `json:"results"`
	Notes     map[string]string `json:"notes,omitempty"`
	BaseSHA   string            `json:"base_sha"`
	Model     string            `json:"model"`
	CreatedAt time.Time         `json:"created_at"`
}

type CheckRunStatus struct {
	Status    string     `json:"status"` // idle | running | ready | failed
	Error     string     `json:"error,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// CheckStatus is what the project's checks screen shows.
type CheckStatus struct {
	Profile  *model.Profile `json:"profile"`
	Proposal *CheckProposal `json:"proposal"`
	Run      CheckRunStatus `json:"run"`
	Stale    bool           `json:"stale"`
}

// CheckSuggester derives a project's check profile with one read-only agent run, then dry-runs it on a clean checkout.
// It is a project-level run, not a work order.
type CheckSuggester struct {
	Projects interface {
		Authorize(ctx context.Context, u *model.User, projectID string) error
	}
	Proj interface {
		Get(ctx context.Context, id string) (*model.Project, error)
		SetQualityProfile(ctx context.Context, id, profile string) error
	}
	Store CheckProposalStore
	Usage Budget
	KB    *KB // nil: the prompt carries no playbook
	Log   *slog.Logger
	// Checkout makes a fresh clone of the default branch for one run; cleanup removes it.
	Checkout func(ctx context.Context, projectID string) (dir, sha string, cleanup func(), err error)
	// SourceDir is a read-only view of the repository, for validating and hashing approved profiles.
	SourceDir func(ctx context.Context, projectID string) (string, error)
	// Agent runs one read-only prompt in dir with the Builder-tier model and names the model it used.
	Agent func(ctx context.Context, userID, dir, prompt string) (modelRef string, res model.AgentResult, err error)
	// DryRun runs the profile's checks in dir.
	DryRun  func(ctx context.Context, projectID, runName, dir string, p model.Profile) ([]model.CheckResult, error)
	Timeout time.Duration

	wg sync.WaitGroup
}

// DryRunChecks runs a proposed profile's checks in dir for CheckSuggester.
func (f *Factory) DryRunChecks(ctx context.Context, projectID, runName, dir string, p model.Profile) ([]model.CheckResult, error) {
	return f.runProfileChecks(ctx, projectID, runName, dir, p)
}

// Start begins a suggestion in the background; its outcome is read with Status.
func (s *CheckSuggester) Start(ctx context.Context, u *model.User, projectID string) error {
	if err := s.Projects.Authorize(ctx, u, projectID); err != nil {
		return err
	}
	if s.Agent == nil || s.Checkout == nil {
		return ErrCheckSuggestUnavailable
	}
	ok, err := s.Store.Begin(ctx, projectID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCheckSuggestBusy
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(context.WithoutCancel(ctx), u.ID, projectID)
	}()
	return nil
}

func (s *CheckSuggester) run(ctx context.Context, userID, projectID string) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = suggestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var prop *CheckProposal
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("check suggestion crashed: %v", r)
			}
		}()
		prop, err = s.suggest(ctx, userID, projectID)
	}()
	fin := context.WithoutCancel(ctx)
	if err != nil {
		s.Log.Warn("check suggestion failed", "project", projectID, "err", err)
		_ = s.Store.Finish(fin, projectID, "", err.Error())
		return
	}
	b, _ := json.Marshal(prop)
	if err := s.Store.Finish(fin, projectID, string(b), ""); err != nil {
		s.Log.Error("check suggestion not stored", "project", projectID, "err", err)
	}
}

func (s *CheckSuggester) suggest(ctx context.Context, userID, projectID string) (*CheckProposal, error) {
	dir, sha, cleanup, err := s.Checkout(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("could not check out the repository: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	prompt := suggestPrompt(s.playbook(ctx, projectID, dir, sha))
	var r suggestReply
	var warnings map[string]string
	var ref string
	var lastErr error
	for range suggestAttempts {
		p := prompt
		if lastErr != nil {
			p += "\n\nYour previous reply was rejected: " + lastErr.Error() + "\nReply again with ONLY the JSON object."
		}
		var res model.AgentResult
		var rerr error
		ref, res, rerr = s.Agent(ctx, userID, dir, p)
		s.record(ctx, userID, ref, res)
		if rerr != nil {
			return nil, rerr
		}
		if lastErr = decodeReply(res.Text, &r); lastErr == nil {
			warnings, lastErr = validateProfile(dir, &r.Profile)
		}
		if lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("%w: suggestion invalid after retry: %v", errReplyInvalid, lastErr)
	}
	p := r.Profile
	if p.Smoke != nil {
		p.Smoke.Blocking = false
	}
	notes := r.Notes
	if len(warnings) > 0 && notes == nil {
		notes = map[string]string{}
	}
	for k, w := range warnings {
		notes[k] = w
	}
	if p.Sources, err = hashSources(dir, sourcePaths(p)); err != nil {
		return nil, err
	}
	prop := &CheckProposal{Profile: p, Notes: notes, BaseSHA: sha, Model: ref, CreatedAt: time.Now()}
	prop.Results, err = s.dryRun(ctx, projectID, dir, p)
	if err != nil {
		return nil, err
	}
	return prop, nil
}

// dryRun runs the install steps then the checks, as one list so the stack-aware runner sees them together.
func (s *CheckSuggester) dryRun(ctx context.Context, projectID, dir string, p model.Profile) ([]CheckRunResult, error) {
	if s.DryRun == nil {
		return nil, nil
	}
	var setup []model.Check
	for _, st := range p.Setup {
		if !st.PerRun {
			setup = append(setup, st)
		}
	}
	run := p
	run.Checks = append(slices.Clone(setup), p.Checks...)
	got, err := s.DryRun(ctx, projectID, "suggest_"+sanitizeRunName(projectID), dir, run)
	if err != nil {
		return nil, fmt.Errorf("dry run failed: %w", err)
	}
	out := make([]CheckRunResult, 0, len(got))
	for i, r := range got {
		kind := "check"
		if i < len(setup) {
			kind = "setup"
		}
		out = append(out, CheckRunResult{Name: r.Name, Kind: kind, Command: r.Command, Outcome: r.Outcome, ExitCode: r.ExitCode, OutputTail: tailN(r.Output, checkOutputTail)})
	}
	return out, nil
}

func (s *CheckSuggester) record(ctx context.Context, userID, ref string, res model.AgentResult) {
	if s.Usage == nil || (res.TokensIn == 0 && res.TokensOut == 0 && res.CostMicros == 0) {
		return
	}
	provider, modelID := SplitModelRef(ref)
	_ = s.Usage.Record(context.WithoutCancel(ctx), model.UsageRecord{
		UserID: userID, Role: suggestUsageRole, Provider: provider, Model: modelID,
		TokensIn: res.TokensIn, TokensOut: res.TokensOut, CacheRead: res.CacheRead, CacheWrite: res.CacheWrite,
		CostMicros: res.CostMicros, Source: res.CostSource,
	})
}

func (s *CheckSuggester) playbook(ctx context.Context, projectID, dir, sha string) string {
	if s.KB == nil {
		return ""
	}
	var b strings.Builder
	for _, d := range s.KB.Playbooks(ctx, projectID, string(model.AgentForeman), "checks", dir, sha) {
		if metaStr(d.Meta, "specialty") != "checks" {
			continue
		}
		body, err := s.KB.playbookBody(d, dir, sha)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", d.Title, truncate(strings.TrimSpace(body), maxPlaybookChars))
	}
	return b.String()
}

// Status reports the approved profile, the pending proposal and the run, with a stale flag when a source file changed.
func (s *CheckSuggester) Status(ctx context.Context, u *model.User, projectID string) (*CheckStatus, error) {
	if err := s.Projects.Authorize(ctx, u, projectID); err != nil {
		return nil, err
	}
	proj, err := s.Proj.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	st := &CheckStatus{Run: CheckRunStatus{Status: "idle"}}
	if q := strings.TrimSpace(proj.QualityProfile); q != "" && q != "{}" && q != "[]" {
		if p, err := model.ParseProfile(q, proj.Language); err == nil {
			st.Profile = &p
		}
	}
	row, err := s.Store.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if row != nil {
		t := row.UpdatedAt
		st.Run = CheckRunStatus{Status: row.Status, Error: row.Error, UpdatedAt: &t}
		if row.Proposal != "" {
			var p CheckProposal
			if json.Unmarshal([]byte(row.Proposal), &p) == nil {
				st.Proposal = &p
			}
		}
	}
	st.Stale = s.stale(ctx, projectID, st.Profile)
	return st, nil
}

func (s *CheckSuggester) stale(ctx context.Context, projectID string, p *model.Profile) bool {
	if p == nil || len(p.Sources) == 0 || s.SourceDir == nil {
		return false
	}
	root, err := s.SourceDir(ctx, projectID)
	if err != nil {
		return false
	}
	for _, src := range p.Sources {
		if h, err := hashFile(root, src.Path); err != nil || h != src.SHA256 {
			return true
		}
	}
	return false
}

// Approve validates p, fills missing source hashes and stores it as the project's profile.
func (s *CheckSuggester) Approve(ctx context.Context, u *model.User, projectID string, p model.Profile) (*model.Profile, error) {
	if err := s.Projects.Authorize(ctx, u, projectID); err != nil {
		return nil, err
	}
	if s.SourceDir == nil {
		return nil, ErrCheckSuggestUnavailable
	}
	root, err := s.SourceDir(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("could not read the repository: %w", err)
	}
	if _, err := validateProfile(root, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	paths := sourcePaths(p)
	known := map[string]string{}
	for _, src := range p.Sources {
		known[src.Path] = src.SHA256
	}
	p.Sources = nil
	for _, path := range paths {
		h := known[path]
		if h == "" {
			if h, err = hashFile(root, path); err != nil {
				return nil, fmt.Errorf("%w: source %s: %v", ErrInvalid, path, err)
			}
		}
		p.Sources = append(p.Sources, model.ProfileSource{Path: path, SHA256: h})
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if err := s.Proj.SetQualityProfile(ctx, projectID, string(b)); err != nil {
		return nil, err
	}
	_, _ = s.Store.Delete(ctx, projectID)
	return &p, nil
}

// Discard drops the pending proposal; a run in flight cannot be discarded.
func (s *CheckSuggester) Discard(ctx context.Context, u *model.User, projectID string) error {
	if err := s.Projects.Authorize(ctx, u, projectID); err != nil {
		return err
	}
	ok, err := s.Store.Delete(ctx, projectID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCheckSuggestBusy
	}
	return nil
}

// ---- prompt and reply ----

// suggestReply is a profile v2 object plus optional per-check notes; unknown keys are rejected.
type suggestReply struct {
	model.Profile
	Notes map[string]string `json:"notes,omitempty"`
}

func (r *suggestReply) UnmarshalJSON(b []byte) error {
	type plain suggestReply
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode((*plain)(r))
}

func (r *suggestReply) validate() error {
	if len(r.Checks) == 0 {
		return errors.New("checks is empty")
	}
	return nil
}

func suggestPrompt(playbook string) string {
	return "You are deriving the deterministic verification checks for the repository in your working directory, which is a read-only checkout. " +
		"Read and search it; do not run commands or change files.\n\n" +
		"Reply with a single profile v2 JSON object and nothing else:\n" +
		`{"version":2,"language":"go+typescript","sources":[{"path":".github/workflows/ci.yml"}],` +
		`"services":{"compose":"docker-compose.yml","names":["postgres"],"ports":{"postgres":5432}},` +
		`"database":{"service":"postgres","admin_url":"postgres://u:p@{{postgres}}/postgres?sslmode=disable"},` +
		`"env":{"DATABASE_URL":"postgres://u:p@{{postgres}}/{{db}}?sslmode=disable"},` +
		`"setup":[{"name":"web deps","dir":"web","command":"npm ci"},{"name":"migrate","dir":"api","command":"make migrate-up","per_run":true}],` +
		`"checks":[{"name":"api test","dir":"api","command":"go test ./...","source":".github/workflows/ci.yml:68"},{"name":"smtp","dir":"api","command":"go test ./internal/infra -run SMTP","optional":true,"source":"api/Makefile:12"}],` +
		`"notes":{"smtp":"needs a mail server; not in compose"}}` + "\n\n" +
		"Placeholders are filled by the factory: {{<service>}} is the service's host:port, {{db}} the run's own database name, {{bucket}} that name made bucket-safe, {{redis_db}} its Redis index. " +
		"Only inside smoke.start[].env, {{port.<process>}} is the port the factory gives another smoke process. Use no other placeholder. " +
		"Leave services and database out when the repository has no compose file. " +
		"Add an optional \"smoke\" block only when the repository has a web app with a login and a dev seed; the playbook says how. Omit it otherwise." + playbook
}

// ---- validation ----

var (
	placeholderRe = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)
	smokeNameRe   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	envKeyRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	longTokenRe   = regexp.MustCompile(`^[A-Za-z0-9+/=_-]{32,}$`)
	secretPrefix  = []string{"sk_live", "sk-live", "rk_live", "ghp_", "gho_", "ghs_", "github_pat_", "AKIA", "ASIA", "xox", "AIza", "-----BEGIN"}
	dotenvNames   = map[string]bool{".env": true, ".env.local": true, ".env.production": true, ".env.prod": true, ".env.development": true}
)

// liveSecret flags values that start like real credentials; these are always rejected.
func liveSecret(v string) bool {
	v = strings.TrimSpace(v)
	for _, p := range secretPrefix {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return false
}

// looksLikeToken flags long random-looking values; a profile may legitimately carry throwaway ones (JWT_SECRET), so it only warns.
func looksLikeToken(v string) bool {
	v = strings.TrimSpace(v)
	if !longTokenRe.MatchString(v) {
		return false
	}
	var letter, digit bool
	for _, r := range v {
		letter = letter || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
		digit = digit || r >= '0' && r <= '9'
	}
	return letter && digit
}

// validateProfile checks p against the repository at root and fixes its version; every problem is reported at once.
// Warnings (keyed by where they apply) do not reject the profile.
func validateProfile(root string, p *model.Profile) (warnings map[string]string, err error) {
	var errs []string
	warnings = map[string]string{}
	bad := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	p.Version = 2
	if len(p.Checks) == 0 {
		bad("checks is empty")
	}
	names := map[string]bool{}
	for _, group := range []struct {
		label string
		steps []model.Check
	}{{"setup", p.Setup}, {"checks", p.Checks}} {
		for i, c := range group.steps {
			switch {
			case strings.TrimSpace(c.Name) == "":
				bad("%s[%d] has no name", group.label, i)
			case names[group.label+"/"+c.Name]:
				bad("%s name %q is used twice", group.label, c.Name)
			}
			names[group.label+"/"+c.Name] = true
			if strings.TrimSpace(c.Command) == "" {
				bad("%s %q has no command", group.label, c.Name)
			}
			if c.Dir != "" {
				if full, err := safeJoin(root, c.Dir); err != nil {
					bad("%s %q dir %q: %v", group.label, c.Name, c.Dir, err)
				} else if fi, err := os.Stat(full); err != nil || !fi.IsDir() {
					bad("%s %q dir %q is not a directory in the repository", group.label, c.Name, c.Dir)
				}
			}
		}
	}
	for _, src := range p.Sources {
		if _, err := hashFile(root, src.Path); err != nil {
			bad("source %q: %v", src.Path, err)
		}
	}

	var services []string
	if sv := p.Services; sv != nil {
		if len(sv.Names) == 0 && sv.Compose == "" && len(sv.Ports) == 0 {
			p.Services = nil
		} else {
			services = sv.Names
			errs = append(errs, validateServices(root, sv)...)
		}
	}
	pc := placeholderCtx{services: services, needsStack: p.NeedsStack(), hasDB: p.Database != nil}
	if p.Services != nil {
		pc.servicePorts = p.Services.Ports
	}
	if db := p.Database; db != nil {
		if !slices.Contains(services, db.Service) {
			bad("database.service %q is not in services.names", db.Service)
		} else if pc.servicePorts[db.Service] == 0 {
			bad("database.service %q needs an entry in services.ports", db.Service)
		}
		if strings.TrimSpace(db.AdminURL) == "" {
			bad("database.admin_url is empty")
		}
		errs = append(errs, checkPlaceholders("database.admin_url", db.AdminURL, pc, nil)...)
	}
	dotenv := dotenvValues(root)
	checkEnv := func(where, k, v string, ports []string) {
		if !envKeyRe.MatchString(k) {
			bad("%s key %q is not a valid variable name", where, k)
		}
		errs = append(errs, checkPlaceholders(where+"."+k, v, pc, ports)...)
		plain := placeholderRe.ReplaceAllString(v, "")
		switch {
		case liveSecret(plain):
			bad("%s.%s looks like a live secret; use only test values from compose, CI or test config", where, k)
		case dotenv[v]:
			bad("%s.%s has a value read from a .env file; use only compose, CI or test config", where, k)
		case looksLikeToken(plain):
			warnings[where+"."+k] = "looks like a random token; fine if it is a throwaway test value, never a real secret"
		}
	}
	for _, k := range sortedKeys(p.Env) {
		checkEnv("env", k, p.Env[k], nil)
	}
	if sm := p.Smoke; sm != nil {
		errs = append(errs, validateSmoke(root, p, sm, checkEnv)...)
	}
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	return warnings, nil
}

// validateSmoke checks the smoke block; checkEnv validates one env value the way the profile env is validated.
func validateSmoke(root string, p *model.Profile, sm *model.Smoke, checkEnv func(where, k, v string, ports []string)) []string {
	var errs []string
	bad := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	if len(sm.Start) == 0 {
		bad("smoke.start is empty")
	}
	var names []string
	for _, sp := range sm.Start {
		names = append(names, sp.Name)
	}
	seen := map[string]bool{}
	for i, sp := range sm.Start {
		who := fmt.Sprintf("smoke.start[%d]", i)
		switch {
		case strings.TrimSpace(sp.Name) == "":
			bad("%s has no name", who)
		case seen[sp.Name]:
			bad("smoke process name %q is used twice", sp.Name)
		case !smokeNameRe.MatchString(sp.Name):
			bad("smoke process name %q may only use letters, digits, - and _", sp.Name)
		default:
			who = "smoke process " + sp.Name
		}
		seen[sp.Name] = true
		if strings.TrimSpace(sp.Command) == "" {
			bad("%s has no command", who)
		}
		if sp.Dir != "" {
			if full, err := safeJoin(root, sp.Dir); err != nil {
				bad("%s dir %q: %v", who, sp.Dir, err)
			} else if fi, err := os.Stat(full); err != nil || !fi.IsDir() {
				bad("%s dir %q is not a directory in the repository", who, sp.Dir)
			}
		}
		if !envKeyRe.MatchString(sp.PortEnv) {
			bad("%s port_env %q is not a valid variable name", who, sp.PortEnv)
		}
		if !strings.HasPrefix(sp.Ready, "/") {
			bad("%s ready %q must start with /", who, sp.Ready)
		}
		for _, k := range sortedKeys(sp.Env) {
			checkEnv("smoke.start."+sp.Name+".env", k, sp.Env[k], names)
		}
	}
	if !slices.Contains(names, sm.Base) {
		bad("smoke.base %q is not a process in smoke.start", sm.Base)
	}
	if rf := strings.TrimSpace(sm.RoutesFrom); rf == "" {
		bad("smoke.routes_from is empty")
	} else if _, err := safeJoin(root, strings.TrimPrefix(filepath.ToSlash(filepath.Clean(rf)), "./")); err != nil || strings.HasPrefix(rf, "/") {
		bad("smoke.routes_from %q must be a glob inside the repository", rf)
	}
	if l := sm.Login; l != nil {
		if !strings.HasPrefix(l.Path, "/") {
			bad("smoke.login.path %q must start with /", l.Path)
		}
		for label, name := range map[string]string{"user_env": l.UserEnv, "password_env": l.PasswordEnv} {
			if _, ok := p.Env[name]; !ok {
				bad("smoke.login.%s %q is not set in the profile env", label, name)
			}
		}
	}
	for _, k := range sortedKeys(sm.Params) {
		v := sm.Params[k]
		q, isSQL := strings.CutPrefix(v, "sql:")
		switch {
		case strings.TrimSpace(k) == "":
			bad("smoke.params has an empty name")
		case strings.TrimSpace(v) == "", isSQL && strings.TrimSpace(q) == "":
			bad("smoke.params.%s is empty", k)
		case isSQL && p.Database == nil:
			bad("smoke.params.%s uses sql: but the profile has no database block", k)
		}
	}
	if sm.ReadyTimeoutSec < 0 || sm.ReadyTimeoutSec > maxSmokeReadySec {
		bad("smoke.ready_timeout_sec must be 0-%d", maxSmokeReadySec)
	}
	return errs
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func validateServices(root string, sv *model.ProfileServices) []string {
	var errs []string
	if sv.Compose == "" {
		return []string{"services.compose is required when services are listed"}
	}
	full, err := safeJoin(root, sv.Compose)
	if err != nil {
		return []string{fmt.Sprintf("services.compose %q: %v", sv.Compose, err)}
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return []string{fmt.Sprintf("services.compose %q is not a file in the repository", sv.Compose)}
	}
	var doc struct {
		Services map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return []string{fmt.Sprintf("services.compose %q is not valid YAML: %v", sv.Compose, err)}
	}
	for _, n := range sv.Names {
		if _, ok := doc.Services[n]; !ok {
			errs = append(errs, fmt.Sprintf("service %q is not in %s", n, sv.Compose))
		}
	}
	for n, port := range sv.Ports {
		if !slices.Contains(sv.Names, n) {
			errs = append(errs, fmt.Sprintf("services.ports names %q, which is not in services.names", n))
		}
		if port < 1 || port > 65535 {
			errs = append(errs, fmt.Sprintf("services.ports.%s must be 1-65535", n))
		}
	}
	return errs
}

// placeholderCtx is what the profile offers placeholders to resolve against.
type placeholderCtx struct {
	services     []string
	servicePorts map[string]int
	needsStack   bool
	hasDB        bool
}

// checkPlaceholders reports placeholders v may not use; ports are the smoke processes whose {{port.<name>}} is allowed here.
func checkPlaceholders(where, v string, pc placeholderCtx, ports []string) []string {
	var errs []string
	for _, m := range placeholderRe.FindAllStringSubmatch(v, -1) {
		name := m[1]
		isLease, isService := slices.Contains(leasePlaceholders, name), slices.Contains(pc.services, name)
		switch {
		case isLease && !pc.needsStack, isService && !pc.needsStack:
			errs = append(errs, fmt.Sprintf("%s uses {{%s}} but the profile has no services to lease; add services or drop the placeholder", where, name))
			continue
		case (name == "db" || name == "bucket") && !pc.hasDB:
			errs = append(errs, fmt.Sprintf("%s uses {{%s}} but the profile has no database block", where, name))
			continue
		case isService && !isLease && pc.servicePorts[name] == 0:
			errs = append(errs, fmt.Sprintf("%s uses {{%s}} but services.ports has no entry for it", where, name))
			continue
		}
		if isLease || isService {
			continue
		}
		if proc, ok := strings.CutPrefix(name, "port."); ok && slices.Contains(ports, proc) {
			continue
		}
		errs = append(errs, fmt.Sprintf("%s uses unknown placeholder {{%s}}; allowed are %s, {{<service in services.names>}} and, in smoke.start env only, {{port.<process in smoke.start>}}", where, name, placeholderList()))
	}
	return errs
}

func placeholderList() string {
	out := make([]string, len(leasePlaceholders))
	for i, n := range leasePlaceholders {
		out[i] = "{{" + n + "}}"
	}
	return strings.Join(out, ", ")
}

// dotenvValues collects the values of committed .env files, which suggested env must not copy.
func dotenvValues(root string) map[string]bool {
	out := map[string]bool{}
	rootDepth := strings.Count(filepath.Clean(root), string(filepath.Separator))
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor" || strings.Count(p, string(filepath.Separator))-rootDepth > envFileScanDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if !dotenvNames[d.Name()] {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if _, v, ok := strings.Cut(line, "="); ok {
				if v = strings.Trim(strings.TrimSpace(v), `"'`); len(v) >= minDotenvValueSize {
					out[v] = true
				}
			}
		}
		return nil
	})
	return out
}

// ---- repository files ----

// safeJoin resolves rel under root, refusing absolute paths, .. segments and symlinks that leave the root.
func safeJoin(root, rel string) (string, error) {
	if filepath.IsAbs(rel) || rel == "" {
		return "", errors.New("must be a relative path inside the repository")
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("must stay inside the repository")
	}
	full := filepath.Join(root, clean)
	realRoot, err1 := filepath.EvalSymlinks(root)
	real, err2 := filepath.EvalSymlinks(full)
	if err2 != nil { // missing paths are reported by the caller's own stat
		return full, nil
	}
	if err1 != nil || (real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator))) {
		return "", errors.New("must stay inside the repository")
	}
	return full, nil
}

func hashFile(root, rel string) (string, error) {
	full, err := safeJoin(root, rel)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(full)
	if err != nil || !fi.Mode().IsRegular() {
		return "", errors.New("is not a file in the repository")
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// sourcePaths lists the files a profile was derived from: its sources plus the compose file, once each.
func sourcePaths(p model.Profile) []string {
	var out []string
	add := func(path string) {
		if path = filepath.ToSlash(filepath.Clean(path)); path != "." && !slices.Contains(out, path) {
			out = append(out, path)
		}
	}
	for _, s := range p.Sources {
		add(s.Path)
	}
	if p.Services != nil && p.Services.Compose != "" {
		add(p.Services.Compose)
	}
	return out
}

func hashSources(root string, paths []string) ([]model.ProfileSource, error) {
	out := make([]model.ProfileSource, 0, len(paths))
	for _, path := range paths {
		h, err := hashFile(root, path)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", path, err)
		}
		out = append(out, model.ProfileSource{Path: path, SHA256: h})
	}
	return out, nil
}

func tailN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

