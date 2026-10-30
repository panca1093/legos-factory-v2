package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/harness/harnesstest"
	"legos-factory/factory/internal/profile"
	"legos-factory/factory/internal/sandbox"
	"legos-factory/factory/internal/scm"
)

func TestMain(m *testing.M) {
	if harnesstest.RunIfFakeAgent() {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRemote(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare, seed := filepath.Join(root, "remote.git"), filepath.Join(root, "seed")
	sh(t, root, "git", "init", "-q", "--bare", "-b", "main", bare)
	sh(t, root, "git", "init", "-q", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, seed, "git", "add", "-A")
	sh(t, seed, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	sh(t, seed, "git", "push", "-q", bare, "main")
	return bare
}

type fakeHarness struct {
	mu       sync.Mutex
	edit     func(call int, dir string) error
	noLoad   bool // resuming a session fails with harness.ErrNoLoad
	usage    harness.Usage
	toolText string // tool call text; default "edit file"
	specs    []harness.SessionSpec
	prompts  []string
}

func (h *fakeHarness) Start(_ context.Context, s harness.SessionSpec) (harness.Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.specs = append(h.specs, s)
	if h.noLoad && s.SessionID != "" {
		return nil, fmt.Errorf("%w: gone", harness.ErrNoLoad)
	}
	id := s.SessionID
	if id == "" {
		id = "sess-1"
	}
	return &fakeSession{h: h, id: id, spec: s, call: len(h.specs), events: make(chan harness.Event, 16)}, nil
}

func (h *fakeHarness) starts() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.specs) }

type fakeSession struct {
	h      *fakeHarness
	id     string
	spec   harness.SessionSpec
	call   int
	events chan harness.Event
}

func (s *fakeSession) ID() string                   { return s.id }
func (s *fakeSession) Events() <-chan harness.Event { return s.events }
func (s *fakeSession) Close() error                 { close(s.events); return nil }
func (s *fakeSession) Prompt(_ context.Context, text string) (string, error) {
	s.h.mu.Lock()
	s.h.prompts = append(s.h.prompts, text)
	s.h.mu.Unlock()
	s.events <- harness.Event{Kind: harness.EventStep, Text: "thinking"}
	tool, name := "edit file", "edit"
	if s.h.toolText != "" {
		tool, name = s.h.toolText, "bash"
	}
	s.events <- harness.Event{Kind: harness.EventToolCall, Tool: name, Text: tool}
	if !reflect.DeepEqual(s.h.usage, harness.Usage{}) {
		s.events <- harness.Event{Kind: harness.EventUsage, Usage: s.h.usage}
	}
	if s.h.edit != nil {
		if err := s.h.edit(s.call, s.spec.Dir); err != nil {
			return "", err
		}
	}
	return "end_turn", nil
}

type fakeSCM struct {
	mu        sync.Mutex
	opened    int
	pr        *scm.PR
	checks    []scm.Check
	state     string
	checksSHA string
}

func (f *fakeSCM) OpenPR(_ context.Context, r scm.PRRequest) (scm.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened++
	f.pr = &scm.PR{Number: 7, URL: "http://pr/7", Head: r.Head}
	return *f.pr, nil
}
func (f *fakeSCM) FindPR(context.Context, string, string) (scm.PR, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pr == nil {
		return scm.PR{}, false, nil
	}
	return *f.pr, true, nil
}
func (f *fakeSCM) Checks(_ context.Context, _, sha string) ([]scm.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checksSHA = sha
	return f.checks, nil
}
func (f *fakeSCM) PRState(context.Context, string, int) (string, error) {
	if f.state == "" {
		return "open", nil
	}
	return f.state, nil
}
func (f *fakeSCM) GitAuth() scm.GitAuth { return scm.GitAuth{} }

type env struct {
	e    *Engine
	h    *fakeHarness
	scm  *fakeSCM
	p    Project
	db   string
	root string
}

func writeFile(name, content string) func(int, string) error {
	return func(_ int, dir string) error { return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644) }
}

func newEnv(t *testing.T, verify ...string) *env {
	t.Helper()
	dir := t.TempDir()
	ev := &env{h: &fakeHarness{edit: writeFile("out.txt", "built\n")}, scm: &fakeSCM{}, db: filepath.Join(dir, "f.db"), root: filepath.Join(dir, "work")}
	ev.p = Project{ID: "p1", RepoURL: newRemote(t), Repo: "o/r", DefaultBranch: "main", Env: Env{Verify: verify, Setup: `echo x >> "$HOME/setup.log"`}}
	ev.e = ev.engine(t)
	return ev
}

func (ev *env) engine(t *testing.T) *Engine {
	t.Helper()
	store, err := fact.Open(ev.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &Engine{Facts: store, Runtime: sandbox.Local{Root: ev.root}, Harness: ev.h,
		SCM: func(Project) (scm.Provider, error) { return ev.scm, nil }}
}

func (ev *env) create(t *testing.T) string {
	t.Helper()
	id, err := ev.e.Create(context.Background(), ev.p, "add out.txt")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (ev *env) kinds(t *testing.T, e *Engine, id string) []string {
	t.Helper()
	facts, err := e.Facts.List(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range facts {
		out = append(out, f.Kind)
	}
	return out
}

func count(kinds []string, k string) (n int) {
	for _, x := range kinds {
		if x == k {
			n++
		}
	}
	return
}

func status(t *testing.T, e *Engine, id string) fact.Status {
	t.Helper()
	st, _, err := e.Status(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func failedClass(t *testing.T, e *Engine, id string) string {
	t.Helper()
	facts, _ := e.Facts.List(context.Background(), id)
	for _, f := range facts {
		if f.Kind == fact.KindWOFailed {
			w, _ := fact.Decode[fact.WOFailed](f)
			return w.Class
		}
	}
	return ""
}

func TestHappyPath(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t, "test -f out.txt")
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s", st)
	}
	k := ev.kinds(t, ev.e, id)
	for _, want := range []string{fact.KindSandboxStarted, fact.KindRunStarted, fact.KindRunStep, fact.KindRunFinished,
		fact.KindTaskCommitted, fact.KindVerifyPassed, fact.KindPROpened, fact.KindSandboxStopped} {
		if count(k, want) == 0 {
			t.Errorf("missing %s in %v", want, k)
		}
	}
	if sh(t, ev.p.RepoURL, "git", "rev-parse", "--verify", "lf/"+id) == "" {
		t.Error("branch not pushed")
	}
	if ev.h.specs[0].Role != harness.RoleBuilder || !strings.HasPrefix(ev.h.prompts[0], "add out.txt\n\nBefore finishing") {
		t.Errorf("spec/prompt = %+v %q", ev.h.specs[0], ev.h.prompts[0])
	}
}

func TestNoChanges(t *testing.T) {
	ev := newEnv(t)
	ev.h.edit = nil
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusFailed {
		t.Fatalf("status = %s", st)
	}
	if c := failedClass(t, ev.e, id); c != "task" {
		t.Fatalf("class = %s", c)
	}
}

func TestVerifyFailed(t *testing.T) {
	ev := newEnv(t, "echo boom; false")
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if c := failedClass(t, ev.e, id); c != "verify" {
		t.Fatalf("class = %q", c)
	}
	facts, _ := ev.e.Facts.List(context.Background(), id)
	for _, f := range facts {
		if f.Kind == fact.KindVerifyFailed {
			v, _ := fact.Decode[fact.VerifyFailed](f)
			if !strings.Contains(v.Output, "boom") || v.Commands[0] != "echo boom; false" {
				t.Errorf("verify.failed = %+v", v)
			}
			return
		}
	}
	t.Fatal("no verify.failed fact")
}

func TestCIFailureRequeueResumesSession(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	ev.h.edit = func(call int, dir string) error {
		return writeFile("out"+string(rune('0'+call))+".txt", "x\n")(call, dir)
	}
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	facts, _ := ev.e.Facts.List(ctx, id)
	firstHead := lastHead(t, facts)

	ev.scm.checks = []scm.Check{{Name: "unit-tests", State: "failure", URL: "http://ci/1"}, {Name: "lint", State: "success"}}
	for i := 0; i < 2; i++ { // second pass must not duplicate facts
		if err := ev.e.Observe(ctx, ev.p, id); err != nil {
			t.Fatal(err)
		}
	}
	k := ev.kinds(t, ev.e, id)
	if count(k, fact.KindCheckObserved) != 2 || count(k, fact.KindWORequeued) != 1 {
		t.Fatalf("facts = %v", k)
	}
	if ev.scm.checksSHA != firstHead {
		t.Errorf("checks fetched for %q, want %q", ev.scm.checksSHA, firstHead)
	}
	if st := status(t, ev.e, id); st != fact.StatusBuilding {
		t.Fatalf("status after requeue = %s", st)
	}

	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if ev.h.starts() != 2 {
		t.Fatalf("harness starts = %d", ev.h.starts())
	}
	if ev.h.specs[1].SessionID != "sess-1" {
		t.Errorf("resume session = %q", ev.h.specs[1].SessionID)
	}
	if ev.h.specs[0].Dir == "" || ev.h.specs[0].Dir != ev.h.specs[1].Dir {
		t.Errorf("dirs differ: %q vs %q", ev.h.specs[0].Dir, ev.h.specs[1].Dir)
	}
	if c := ev.h.specs[0].Env["CLAUDE_CONFIG_DIR"]; c == "" || c != ev.h.specs[1].Env["CLAUDE_CONFIG_DIR"] {
		t.Errorf("CLAUDE_CONFIG_DIR differs: %q vs %q", c, ev.h.specs[1].Env["CLAUDE_CONFIG_DIR"])
	}
	if !strings.Contains(ev.h.prompts[1], "unit-tests") || strings.Contains(ev.h.prompts[1], "lint") {
		t.Errorf("prompt = %q", ev.h.prompts[1])
	}
	k = ev.kinds(t, ev.e, id)
	if count(k, fact.KindPRPushed) != 1 || count(k, fact.KindPROpened) != 1 || ev.scm.opened != 1 {
		t.Fatalf("facts = %v opened=%d", k, ev.scm.opened)
	}
	facts, _ = ev.e.Facts.List(ctx, id)
	if h := lastHead(t, facts); h == firstHead || h != sh(t, ev.p.RepoURL, "git", "rev-parse", "lf/"+id) {
		t.Errorf("head = %s, first = %s", h, firstHead)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s", st)
	}
}

func TestSetupRunsInWorktree(t *testing.T) {
	ev := newEnv(t)
	ev.p.Env.Setup = `pwd > "$HOME/setup.pwd"`
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(ev.root, id, "home", "setup.pwd"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(ev.root, id, "wt", task))
	if g, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got))); g != want {
		t.Fatalf("setup ran in %s, want %s", g, want)
	}
}

func TestResumeFailureStartsFreshSession(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	ev.h.edit = func(call int, dir string) error {
		return writeFile("out"+string(rune('0'+call))+".txt", "x\n")(call, dir)
	}
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	ev.scm.checks = []scm.Check{{Name: "unit-tests", State: "failure"}}
	if err := ev.e.Observe(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	ev.h.noLoad = true
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (class %q)", st, failedClass(t, ev.e, id))
	}
	if ev.h.starts() != 3 || ev.h.specs[1].SessionID != "sess-1" || ev.h.specs[2].SessionID != "" {
		t.Fatalf("starts = %d specs = %+v", ev.h.starts(), ev.h.specs)
	}
	if p := ev.h.prompts[1]; !strings.Contains(p, "add out.txt") || !strings.Contains(p, "unit-tests") {
		t.Errorf("prompt = %q", p)
	}
	facts, _ := ev.e.Facts.List(ctx, id)
	found := false
	for _, f := range facts {
		if r, _ := fact.Decode[fact.RunStep](f); f.Kind == fact.KindRunStep && r.Step == "resume_failed" {
			found = true
		}
	}
	if !found {
		t.Error("no resume_failed run.step")
	}
}

func observeN(t *testing.T, ev *env, id string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := ev.e.Observe(context.Background(), ev.p, id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCIGreenRequiresRequiredChecks(t *testing.T) {
	ev := newEnv(t)
	ev.p.RequiredChecks = []string{"ci", "lint"}
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	ev.scm.checks = []scm.Check{{Name: "ci", State: "success"}}
	observeN(t, ev, id, 2)
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("green before lint registered: %s", st)
	}
	ev.scm.checks = append(ev.scm.checks, scm.Check{Name: "lint", State: "success"})
	observeN(t, ev, id, 1)
	if st := status(t, ev.e, id); st != fact.StatusCIGreen {
		t.Fatalf("status = %s", st)
	}
}

func TestCIGreenNeedsStableSetWithoutRequiredChecks(t *testing.T) {
	ev := newEnv(t)
	now := time.Now()
	ev.e.Now = func() time.Time { return now }
	ev.e.StableAfter = time.Minute
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	ev.scm.checks = []scm.Check{{Name: "ci", State: "success"}}
	observeN(t, ev, id, 2) // first sighting, then too soon
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("green on first sightings: %s", st)
	}
	now = now.Add(2 * time.Minute)
	ev.scm.checks = []scm.Check{{Name: "ci", State: "success"}, {Name: "e2e", State: "pending"}} // late workflow appears
	observeN(t, ev, id, 1)
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("green despite a late pending check: %s", st)
	}
	ev.scm.checks = []scm.Check{{Name: "ci", State: "success"}, {Name: "e2e", State: "success"}}
	observeN(t, ev, id, 1) // change pass: not yet stable
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("green on the change pass: %s", st)
	}
	now = now.Add(2 * time.Minute)
	observeN(t, ev, id, 2)
	if st := status(t, ev.e, id); st != fact.StatusCIGreen {
		t.Fatalf("status = %s", st)
	}
	if n := count(ev.kinds(t, ev.e, id), fact.KindChecksStable); n != 1 {
		t.Fatalf("checks.stable recorded %d times", n)
	}
}

func TestRequeueCapFailsWorkOrder(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	ev.p.MaxRequeues = 1
	ev.h.edit = func(call int, dir string) error {
		return writeFile("out"+string(rune('0'+call))+".txt", "x\n")(call, dir)
	}
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	ev.scm.checks = []scm.Check{{Name: "unit-tests", State: "failure"}}
	observeN(t, ev, id, 1)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	observeN(t, ev, id, 1)
	if st := status(t, ev.e, id); st != fact.StatusFailed || failedClass(t, ev.e, id) != "verify" {
		t.Fatalf("status = %s class = %q", st, failedClass(t, ev.e, id))
	}
	if n := count(ev.kinds(t, ev.e, id), fact.KindWORequeued); n != 1 {
		t.Fatalf("requeues = %d", n)
	}
}

func lastHead(t *testing.T, facts []fact.Fact) string {
	t.Helper()
	var head string
	for _, f := range facts {
		switch f.Kind {
		case fact.KindPROpened:
			p, _ := fact.Decode[fact.PROpened](f)
			head = p.HeadSHA
		case fact.KindPRPushed:
			p, _ := fact.Decode[fact.PRPushed](f)
			head = p.HeadSHA
		}
	}
	return head
}

func TestObserveMerged(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	ev.scm.state = "merged"
	if err := ev.e.Observe(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusMerged {
		t.Fatalf("status = %s", st)
	}
}

func TestRunIsIdempotentAcrossEngines(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	ev.e.Facts.Close()
	marker := filepath.Join(ev.root, id, "repo", ".git", "lf-marker")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	h2 := &fakeHarness{edit: ev.h.edit}
	ev.h = h2
	e2 := ev.engine(t)
	if err := e2.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	k := ev.kinds(t, e2, id)
	if count(k, fact.KindPROpened) != 1 || ev.scm.opened != 1 || h2.starts() != 0 {
		t.Fatalf("facts = %v opened=%d starts=%d", k, ev.scm.opened, h2.starts())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("repo was recloned: %v", err)
	}
	if got := sh(t, ev.root, "wc", "-l", filepath.Join(ev.root, id, "home", "setup.log")); !strings.HasPrefix(got, "1 ") {
		t.Errorf("setup ran more than once: %s", got)
	}
}

func TestResumeAfterCrashMidRun(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t)
	id := ev.create(t)
	if _, err := ev.e.Facts.Append(ctx, id, fact.KindRunStarted, fact.RunStarted{Run: "run-dead", Role: "builder", SessionID: "sess-dead"}); err != nil {
		t.Fatal(err)
	}
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if ev.h.specs[0].SessionID != "sess-dead" || !strings.Contains(ev.h.prompts[0], "interrupted") {
		t.Errorf("spec=%q prompt=%q", ev.h.specs[0].SessionID, ev.h.prompts[0])
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s", st)
	}
}

type fakeProfiles struct {
	mu                 sync.Mutex
	released, acquired int
	err                error
	byID               map[string]profile.Profile // GetProfile lookups, for fallbacks
}

func (f *fakeProfiles) GetProfile(_ context.Context, id string) (profile.Profile, error) {
	if p, ok := f.byID[id]; ok {
		return p, nil
	}
	return profile.Profile{}, profile.ErrNotFound
}

func (f *fakeProfiles) Resolve(_ context.Context, _ string, role harness.Role) (profile.Profile, error) {
	if role != harness.RoleBuilder {
		return profile.Profile{}, profile.ErrNoBinding
	}
	return profile.Profile{ID: "prof-1", Agent: "claude-agent-acp", CredentialID: "cred-1", Model: "m",
		Price: profile.Price{InputPerMTok: 3, OutputPerMTok: 15}}, f.err
}

func (f *fakeProfiles) RunEnv(_ context.Context, p profile.Profile) (profile.RunSpec, error) {
	model := "m"
	if p.Model != "" {
		model = p.Model
	}
	return profile.RunSpec{Profile: harness.Profile{Agent: "claude-agent-acp", Provider: "anthropic", Model: model, BillingKind: "api_key"},
		Env: map[string]string{"ANTHROPIC_API_KEY": "k"}}, nil
}

func (f *fakeProfiles) Acquire(context.Context, string) (func(), error) {
	f.mu.Lock()
	f.acquired++
	f.mu.Unlock()
	return func() { f.mu.Lock(); f.released++; f.mu.Unlock() }, nil
}

func TestBuilderFromProfile(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t, "test -f out.txt")
	ev.h.usage = harness.Usage{InputTokens: 5, Models: []harness.ModelUsage{{Model: "m", InputTokens: 1_000_000, OutputTokens: 100_000}}, CostUSD: 42}
	fp := &fakeProfiles{}
	ev.e.Profiles = fp
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s", st)
	}
	spec := ev.h.specs[0]
	if spec.Env["ANTHROPIC_API_KEY"] != "k" || spec.Profile.Provider != "anthropic" || spec.AgentCommand[0] != "claude-agent-acp" {
		t.Errorf("spec = %+v", spec)
	}
	if fp.acquired != 1 || fp.released != 1 {
		t.Errorf("acquire/release = %d/%d", fp.acquired, fp.released)
	}
	facts, _ := ev.e.Facts.List(ctx, id)
	for _, f := range facts {
		if f.Kind == fact.KindRunFinished {
			r, _ := fact.Decode[fact.RunFinished](f)
			if want := 3.0 + 1.5; r.CostUSD != want || strings.Contains(string(r.Usage), "42") {
				t.Errorf("cost = %v, want %v (harness estimate must be ignored)", r.CostUSD, want)
			}
			return
		}
	}
	t.Fatal("no run.finished")
}

func TestExplicitBuilderBeatsProfiles(t *testing.T) {
	ev := newEnv(t, "test -f out.txt")
	fp := &fakeProfiles{}
	ev.e.Profiles = fp
	ev.p.Builder = BuilderConfig{Profile: harness.Profile{Agent: "claude-agent-acp", Model: "json"}, AgentCommand: []string{"x"}}
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if fp.acquired != 0 || ev.h.specs[0].Profile.Model != "json" {
		t.Errorf("profiles used despite explicit builder: %+v", ev.h.specs[0])
	}
}

func TestNoBuilderBindingFailsWorkOrder(t *testing.T) {
	ev := newEnv(t)
	ev.e.Profiles = &fakeProfiles{err: profile.ErrNoBinding}
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusFailed {
		t.Fatalf("status = %s", st)
	}
}

func TestRunThroughRealACPHarness(t *testing.T) {
	ctx := context.Background()
	ev := newEnv(t, "test -f built.txt")
	ev.e.Harness = harness.NewACP()
	ev.p.Builder = BuilderConfig{AgentCommand: []string{os.Args[0]}, Env: map[string]string{"ACP_FAKE_AGENT": "build"}}
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (class %q)", st, failedClass(t, ev.e, id))
	}
	facts, _ := ev.e.Facts.List(ctx, id)
	var steps []string
	var fin fact.RunFinished
	for _, f := range facts {
		switch f.Kind {
		case fact.KindRunStep:
			r, _ := fact.Decode[fact.RunStep](f)
			steps = append(steps, r.Step)
		case fact.KindRunFinished:
			fin, _ = fact.Decode[fact.RunFinished](f)
		}
	}
	if len(steps) != 2 || steps[0] != "write built.txt" || steps[1] != "no_targeted_tests" {
		t.Errorf("steps = %v", steps)
	}
	var u harness.Usage
	if err := json.Unmarshal(fin.Usage, &u); err != nil || u.InputTokens != 11 || u.OutputTokens != 7 || fin.StopReason != "end_turn" {
		t.Errorf("run.finished = %+v usage = %+v err = %v", fin, u, err)
	}
	if sh(t, ev.p.RepoURL, "git", "show", "lf/"+id+":built.txt") == "" {
		t.Error("builder output not pushed")
	}
}
