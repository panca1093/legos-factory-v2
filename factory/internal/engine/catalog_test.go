package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"legos-factory/factory/internal/catalog"
	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
)

const packReply = "Surveyed.\n```json\n" + `{"summary":"Tiny repo for tests","files":[{"path":"README.md","why":"overview"}],"conventions":["keep it small"],"commands":{"test_pkg":"echo test {pkg}"}}` + "\n```"

func withCatalog(t *testing.T, ev *env, withCataloger bool) {
	t.Helper()
	cat, err := catalog.Open(ev.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	ev.e.Catalog = cat
	if withCataloger {
		if ev.p.Roles == nil {
			ev.p.Roles = map[string]BuilderConfig{}
		}
		ev.p.Roles["cataloger"] = BuilderConfig{Profile: harness.Profile{Agent: "claude-agent-acp", Model: "haiku"}, AgentCommand: []string{"claude-agent-acp"}}
	}
}

func catalogReadyFacts(t *testing.T, ev *env, id string) []fact.CatalogReady {
	t.Helper()
	var out []fact.CatalogReady
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindCatalogReady {
			r, err := fact.Decode[fact.CatalogReady](f)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
	}
	return out
}

func singleBuilderEnv(t *testing.T, h *scripted) *env {
	t.Helper()
	ev := newEnv(t)
	ev.e.Harness = h
	h.build = writeIn("out.txt", "built\n")
	return ev
}

func TestCatalogCacheHitOnSecondWorkOrder(t *testing.T) {
	ctx := context.Background()
	h := newScripted().say(harness.RoleCataloger, packReply)
	ev := singleBuilderEnv(t, h)
	withCatalog(t, ev, true)
	first, second := ev.create(t), ev.create(t)
	for _, id := range []string{first, second} {
		if err := ev.e.Run(ctx, ev.p, id); err != nil {
			t.Fatal(err)
		}
		if st := status(t, ev.e, id); st != fact.StatusPROpen {
			t.Fatalf("%s status = %s (%s)", id, st, failedClass(t, ev.e, id))
		}
	}
	if n := len(h.promptsFor(harness.RoleCataloger)); n != 1 {
		t.Fatalf("cataloger prompted %d times, want 1", n)
	}
	r1, r2 := catalogReadyFacts(t, ev, first), catalogReadyFacts(t, ev, second)
	if len(r1) != 1 || r1[0].CacheHit || r1[0].Files < 1 || r1[0].Repo != "o/r" || len(r1[0].Commit) < 40 {
		t.Fatalf("first catalog.ready = %+v", r1)
	}
	if len(r2) != 1 || !r2[0].CacheHit || r2[0].Commit != r1[0].Commit {
		t.Fatalf("second catalog.ready = %+v", r2)
	}
	if _, ok, _ := ev.e.Catalog.Get(ctx, "o/r", r1[0].Commit); !ok {
		t.Fatal("pack not stored")
	}
	for _, p := range h.promptsFor(harness.RoleBuilder) {
		if !strings.Contains(p, "Tiny repo for tests") || !strings.Contains(p, "<<<PACK-") || strings.Contains(p, "`echo test <pkg>`") {
			t.Fatalf("builder prompt lacks the pack: %q", p)
		}
	}
	// re-running a finished pass must not append another catalog.ready
	if err := ev.e.Run(ctx, ev.p, first); err != nil {
		t.Fatal(err)
	}
	if n := len(catalogReadyFacts(t, ev, first)); n != 1 {
		t.Fatalf("catalog.ready repeated: %d", n)
	}
}

func TestCatalogUnparseableFallsBackToDeterministic(t *testing.T) {
	ctx := context.Background()
	h := newScripted().say(harness.RoleCataloger, "no json here", "still none")
	ev := singleBuilderEnv(t, h)
	withCatalog(t, ev, true)
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	if n := len(h.promptsFor(harness.RoleCataloger)); n != 2 {
		t.Fatalf("cataloger prompted %d times, want 2 (one re-ask)", n)
	}
	steps := 0
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindRunStep {
			if s, _ := fact.Decode[fact.RunStep](f); s.Step == "catalog_unparseable" {
				steps++
			}
		}
	}
	if steps != 1 {
		t.Fatalf("catalog_unparseable steps = %d", steps)
	}
	p := h.promptsFor(harness.RoleBuilder)[0]
	if !strings.Contains(p, "<<<PACK-") || !strings.Contains(p, "README.md") || strings.Contains(p, "Tiny repo") {
		t.Fatalf("builder prompt = %q", p)
	}
	r := catalogReadyFacts(t, ev, id)
	if len(r) != 1 || r[0].CacheHit {
		t.Fatalf("catalog.ready = %+v", r)
	}
}

func TestCatalogDeterministicOnlyWithoutCataloger(t *testing.T) {
	h := newScripted()
	ev := singleBuilderEnv(t, h)
	withCatalog(t, ev, false)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if len(h.promptsFor(harness.RoleCataloger)) != 0 || len(catalogReadyFacts(t, ev, id)) != 1 {
		t.Fatalf("cataloger prompts %v, ready %v", h.promptsFor(harness.RoleCataloger), catalogReadyFacts(t, ev, id))
	}
	if p := h.promptsFor(harness.RoleBuilder)[0]; !strings.Contains(p, "README.md") {
		t.Fatalf("builder prompt = %q", p)
	}
}

func TestCatalogNilStoreSkipsPhase(t *testing.T) {
	h := newScripted()
	ev := singleBuilderEnv(t, h)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.promptsFor(harness.RoleBuilder)[0], "PACK") || len(catalogReadyFacts(t, ev, id)) != 0 {
		t.Fatal("pack present without a store")
	}
}

func TestCatalogPackReachesArchitectAndLineBuilder(t *testing.T) {
	ctx := context.Background()
	h := newScripted()
	h.build = writeIn("pkg/out.txt", "built\n")
	h.say(harness.RoleCataloger, packReply)
	h.say(harness.RoleArchitect, "The spec.", planJSON(`{"id":"t1","description":"add out","owned_paths":["pkg/"]}`))
	h.say(harness.RoleForeman,
		call("approve_spec", `{"note":"ok"}`), call("approve_plan", `{"note":"ok"}`),
		call("dispatch_build", `{"task_ids":["t1"]}`), call("request_inspection", `{"task_id":"t1"}`))
	ev := lineEnv(t, h, "test -f pkg/out.txt")
	withCatalog(t, ev, true)
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	for role, ps := range map[harness.Role][]string{harness.RoleArchitect: h.promptsFor(harness.RoleArchitect)[:1], harness.RoleBuilder: h.promptsFor(harness.RoleBuilder)} {
		for _, p := range ps {
			if !strings.Contains(p, "Tiny repo for tests") || !strings.Contains(p, "<<<PACK-") {
				t.Errorf("%s prompt lacks the pack: %q", role, p)
			}
		}
	}
	cat := h.specs[0]
	if cat.Role != harness.RoleCataloger || !contains(cat.ToolPolicy.Deny, "Edit") || !strings.HasSuffix(cat.Dir, "_cataloger") {
		t.Errorf("cataloger spec = %+v", cat)
	}
	if phase(t, ev, id) == "" || len(catalogReadyFacts(t, ev, id)) != 1 {
		t.Errorf("phase %q ready %v", phase(t, ev, id), catalogReadyFacts(t, ev, id))
	}
}

func TestLockfileNoise(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  []string
	}{
		{"npm lock without manifest", []string{"web/package-lock.json", "web/a.js"}, []string{"web/package-lock.json"}},
		{"npm lock with manifest", []string{"web/package-lock.json", "web/package.json"}, nil},
		{"manifest in other dir", []string{"web/package-lock.json", "package.json"}, []string{"web/package-lock.json"}},
		{"go.sum alone", []string{"go.sum", "a.go"}, []string{"go.sum"}},
		{"go.sum with go.mod", []string{"go.sum", "go.mod"}, nil},
		{"yarn", []string{"yarn.lock"}, []string{"yarn.lock"}},
		{"clean", []string{"a.go"}, nil},
	}
	for _, tc := range cases {
		if got := lockfileNoise(tc.files); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestLockfileNoiseWarningOnSingleBuilder(t *testing.T) {
	h := newScripted()
	ev := singleBuilderEnv(t, h)
	h.build = func(_ string, _ int, dir string) error {
		for _, f := range []string{"out.txt", "package-lock.json", "go.sum"} {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("lockfile noise must warn, not fail: %s", st)
	}
	var got string
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindRunStep {
			if s, _ := fact.Decode[fact.RunStep](f); s.Step == "lockfile_noise" {
				got = s.Summary
			}
		}
	}
	if !strings.Contains(got, "package-lock.json") || !strings.Contains(got, "go.sum") || !strings.HasPrefix(got, "warning:") {
		t.Fatalf("lockfile_noise summary = %q", got)
	}
}

func TestToolKindPrefersACPKind(t *testing.T) {
	for _, c := range []struct{ tool, kind, want string }{
		{"Fetching things", "read", "read"}, {"Reading x", "search", "read"}, {"Patching", "edit", "edit"}, {"Removing x", "delete", "edit"},
		{"Renaming", "move", "edit"}, {"edit", "execute", ""}, {"read x", "think", ""}, {"write x", "fetch", ""},
		{"Edit x", "other", "edit"}, {"Read x", "", "read"}, {"Edit x", "read", "read"},
	} {
		if got := toolKind(harness.Event{Tool: c.tool, ToolKind: c.kind}); got != c.want {
			t.Errorf("toolKind(%q, kind %q) = %q, want %q", c.tool, c.kind, got, c.want)
		}
	}
}

func TestToolKind(t *testing.T) {
	for tool, want := range map[string]string{
		"Read /a/b.go": "read", "grep foo": "read", "List dir": "read", "glob **/*.go": "read", "ls": "read",
		"Edit /a": "edit", "write built.txt": "edit", "MultiEdit": "edit", "apply_patch": "edit",
		"bash": "", "Bash: cat x": "", "": "", "think": "",
	} {
		if got := toolKind(harness.Event{Tool: tool}); got != want {
			t.Errorf("toolKind(%q) = %q, want %q", tool, got, want)
		}
	}
}

// readerHarness scripts a builder that explores before editing: it reads many files unless the prompt already carries a
// context pack. It checks the mechanics of the measurement; whether real agents behave this way needs live runs.
type readerHarness struct{}

func (readerHarness) Start(_ context.Context, s harness.SessionSpec) (harness.Session, error) {
	return &readerSession{spec: s, events: make(chan harness.Event, 64)}, nil
}

type readerSession struct {
	spec   harness.SessionSpec
	events chan harness.Event
}

func (s *readerSession) ID() string                   { return "reader" }
func (s *readerSession) Events() <-chan harness.Event { return s.events }
func (s *readerSession) Close() error                 { close(s.events); return nil }
func (s *readerSession) Prompt(_ context.Context, text string) (string, error) {
	reads := 8
	if strings.Contains(text, "context pack") {
		reads = 2
	}
	for range reads {
		s.events <- harness.Event{Kind: harness.EventToolCall, Tool: "Read some/file.go"}
	}
	s.events <- harness.Event{Kind: harness.EventToolCall, Tool: "Edit out.txt"}
	s.events <- harness.Event{Kind: harness.EventToolCall, Tool: "Read after/edit.go"}
	return "end_turn", os.WriteFile(filepath.Join(s.spec.Dir, "out.txt"), []byte("built\n"), 0o644)
}

func readsBeforeEdit(t *testing.T, ev *env, id string) []int {
	t.Helper()
	var out []int
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindRunFinished {
			r, _ := fact.Decode[fact.RunFinished](f)
			out = append(out, r.ReadsBeforeEdit)
		}
	}
	return out
}

func TestReadsBeforeEditRecordedAndPackLowersIt(t *testing.T) {
	ctx := context.Background()
	with, without := newEnv(t), newEnv(t)
	with.e.Harness, without.e.Harness = readerHarness{}, readerHarness{}
	withCatalog(t, with, false)
	ids := map[*env]string{with: with.create(t), without: without.create(t)}
	for ev, id := range ids {
		if err := ev.e.Run(ctx, ev.p, id); err != nil {
			t.Fatal(err)
		}
	}
	if got := readsBeforeEdit(t, with, ids[with]); !reflect.DeepEqual(got, []int{2}) {
		t.Errorf("with pack: reads before edit = %v, want [2]", got)
	}
	if got := readsBeforeEdit(t, without, ids[without]); !reflect.DeepEqual(got, []int{8}) {
		t.Errorf("without pack: reads before edit = %v, want [8]", got)
	}
}
