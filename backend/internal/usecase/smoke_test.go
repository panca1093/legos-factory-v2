package usecase

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
)

func TestPageTemplateMapsAppRouterPaths(t *testing.T) {
	cases := map[string]string{
		"web/app/page.tsx":                           "/",
		"web/app/login/page.tsx":                     "/login",
		"web/app/(shop)/catalog/page.tsx":            "/catalog",
		"web/app/student/kecermatan/[id]/page.tsx":   "/student/kecermatan/[id]",
		"web/app/a/[x]/b/[y]/page.tsx":               "/a/[x]/b/[y]",
		"web/app/(g)/(h)/deep/@modal/page.tsx":       "/deep",
		"web/app/docs/[...slug]/page.tsx":            "/docs/[...slug]",
		"web/app/student/(dash)/orders/[id]/page.js": "/student/orders/[id]",
	}
	for file, want := range cases {
		if got := pageTemplate("web/app", file); got != want {
			t.Errorf("%s → %q, want %q", file, got, want)
		}
	}
}

func TestMatchGlobAndBase(t *testing.T) {
	if !matchGlob("web/app/**/page.tsx", "web/app/page.tsx") || !matchGlob("web/app/**/page.tsx", "web/app/a/b/page.tsx") {
		t.Fatal("** must span zero or more segments")
	}
	if matchGlob("web/app/**/page.tsx", "web/app/a/layout.tsx") || matchGlob("web/app/**/page.tsx", "api/app/page.tsx") {
		t.Fatal("unrelated files must not match")
	}
	if got := globBase("web/app/**/page.tsx"); got != "web/app" {
		t.Fatalf("base = %q", got)
	}
}

func TestFillTemplate(t *testing.T) {
	if r, m := fillTemplate("/student/tryout/[id]/result", map[string]string{"id": "42"}); r != "/student/tryout/42/result" || m != "" {
		t.Fatalf("got %q %q", r, m)
	}
	if r, m := fillTemplate("/docs/[...slug]", map[string]string{"slug": "a/b"}); r != "/docs/a/b" || m != "" {
		t.Fatalf("catch-all: got %q %q", r, m)
	}
	if _, m := fillTemplate("/x/[id]", nil); m != "id" {
		t.Fatalf("missing = %q", m)
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAffectedTemplatesChangedPageImportLayoutAndNoise(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"web/app/page.tsx":                         `import { Hero } from "@/components/hero";`,
		"web/app/login/page.tsx":                   `import { AuthLogin } from "@/components/auth-login";`,
		"web/app/(shop)/catalog/page.tsx":          `import x from "./local-card"; import "@/lib/unrelated";`,
		"web/app/(shop)/catalog/local-card.tsx":    `export default 1`,
		"web/app/student/layout.tsx":               `export default 1`,
		"web/app/student/orders/page.tsx":          ``,
		"web/app/student/kecermatan/[id]/page.tsx": ``,
		"web/app/admin/page.tsx":                   `import { Table } from "@/components/table";`,
		"web/node_modules/pkg/app/page.tsx":        ``,
		"web/components/hero.tsx":                  ``,
		"web/components/auth-login/index.tsx":      ``,
		"web/components/table.tsx":                 ``,
	})
	changed := []string{
		"web/components/hero.tsx",               // imported by / (one level, alias)
		"web/components/auth-login/index.tsx",   // imported by /login via a directory index
		"web/app/(shop)/catalog/local-card.tsx", // relative import
		"web/app/student/layout.tsx",            // layout: every page below it
		"api/internal/x.go",
	}
	got := affectedTemplates(root, "web/app/**/page.tsx", changed)
	want := []string{"/", "/catalog", "/login", "/student/kecermatan/[id]", "/student/orders"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if got := affectedTemplates(root, "web/app/**/page.tsx", []string{"web/app/admin/page.tsx"}); !reflect.DeepEqual(got, []string{"/admin"}) {
		t.Fatalf("changed page itself: %v", got)
	}
	if got := affectedTemplates(root, "web/app/**/page.tsx", []string{"README.md"}); len(got) != 0 {
		t.Fatalf("nothing affected, got %v", got)
	}
}

func TestExecProcStopKillsTheWholeProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	p, err := startExecProc(context.Background(), t.TempDir(), "sleep 300 & echo $! > "+pidFile+"; wait", nil)
	if err != nil {
		t.Fatal(err)
	}
	var child int
	for i := 0; i < 100 && child == 0; i++ {
		if b, err := os.ReadFile(pidFile); err == nil {
			child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("child never started")
	}
	p.Stop()
	p.Stop() // idempotent
	select {
	case <-p.Exited():
	default:
		t.Fatal("leader still running")
	}
	for i := 0; i < 100; i++ {
		if err := syscall.Kill(child, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild %d survived Stop", child)
}

// fakeProc serves 200 on the PORT it was given until stopped.
type fakeProc struct {
	ln          net.Listener
	done        chan struct{}
	once        sync.Once
	stopped     bool
	name, ready string
}

func (p *fakeProc) Stop()                   { p.once.Do(func() { p.stopped = true; _ = p.ln.Close(); close(p.done) }) }
func (p *fakeProc) Exited() <-chan struct{} { return p.done }
func (p *fakeProc) Tail() string            { return "fake log of " + p.name }

type smokeRig struct {
	f       *Factory
	wo      *model.WorkOrder
	mu      sync.Mutex
	procs   []*fakeProc
	envs    map[string][]string
	spec    smokeSpec
	browser string
	deaf    map[string]bool // process names that never listen
}

func newSmokeRig(t *testing.T, changed []string, tree map[string]string) *smokeRig {
	t.Helper()
	r := &smokeRig{wo: &model.WorkOrder{ID: "wo_1", ProjectID: "prj_1", Revision: 2}, envs: map[string][]string{}, deaf: map[string]bool{}}
	data := t.TempDir()
	r.f = &Factory{DataDir: data, PlaywrightDir: t.TempDir(), Sandbox: changedSandbox{files: changed}}
	writeTree(t, r.f.workspaceDir(r.wo.ID), tree)
	r.f.smoke = smokeDeps{
		start: func(_ context.Context, dir, command string, env []string) (smokeProc, error) {
			name := strings.Fields(command)[0]
			r.mu.Lock()
			defer r.mu.Unlock()
			r.envs[name] = env
			p := &fakeProc{done: make(chan struct{}), name: name}
			if r.deaf[name] {
				p.ln, _ = net.Listen("tcp", "127.0.0.1:0")
			} else {
				port := envValue(env, "PORT")
				ln, err := net.Listen("tcp", "127.0.0.1:"+port)
				if err != nil {
					return nil, err
				}
				p.ln = ln
				go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
			}
			r.procs = append(r.procs, p)
			return p, nil
		},
		browser: func(_ context.Context, _, specPath string) ([]byte, error) {
			raw, err := os.ReadFile(specPath)
			if err != nil {
				return nil, err
			}
			_ = json.Unmarshal(raw, &r.spec)
			return []byte(r.browser), nil
		},
	}
	return r
}

type changedSandbox struct {
	dirSandbox
	files []string
}

func (s changedSandbox) ChangedFiles(context.Context, string) ([]string, error) { return s.files, nil }

func smokeProfile() model.Profile {
	return model.Profile{
		Env: map[string]string{"SMOKE_USER": "naila@example.com", "SMOKE_PASSWORD": "password"},
		Smoke: &model.Smoke{
			Start: []model.SmokeProcess{
				{Name: "api", Command: "api-server", PortEnv: "PORT", Ready: "/healthz"},
				{Name: "web", Command: "web-dev", PortEnv: "PORT", Ready: "/", Env: map[string]string{"NEXT_PUBLIC_API_URL": "http://localhost:{{port.api}}"}},
			},
			Base:       "web",
			Login:      &model.SmokeLogin{Path: "/login", UserEnv: "SMOKE_USER", PasswordEnv: "SMOKE_PASSWORD"},
			RoutesFrom: "web/app/**/page.tsx",
			Params:     map[string]string{"id": "abc"},
		},
	}
}

var smokeTree = map[string]string{
	"web/app/student/page.tsx":             ``,
	"web/app/student/tryout/[id]/page.tsx": ``,
	"web/app/student/other/page.tsx":       ``,
}

func (r *smokeRig) stoppedAll() bool {
	for _, p := range r.procs {
		if !p.stopped {
			return false
		}
	}
	return len(r.procs) > 0
}

func TestSmokeRunPassesAndStopsEveryProcess(t *testing.T) {
	r := newSmokeRig(t, []string{"web/app/student/page.tsx", "web/app/student/tryout/[id]/page.tsx"}, smokeTree)
	r.browser = `{"results":[{"path":"/student","status":200,"ok":true,"problems":[],"screenshot":"/s/a.png"},{"path":"/student/tryout/abc","status":200,"ok":true,"problems":[],"screenshot":"/s/b.png"}]}`
	c, res := r.f.runSmoke(context.Background(), r.wo, smokeProfile())
	if res.Outcome != model.Pass || c.Name != "smoke" || !c.Optional || res.Name != "smoke" {
		t.Fatalf("check %+v result %+v", c, res)
	}
	for _, want := range []string{"PASS /student (200)", "PASS /student/tryout/abc (200)", "/s/b.png"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("output lacks %q:\n%s", want, res.Output)
		}
	}
	if !r.stoppedAll() || len(r.procs) != 2 {
		t.Fatalf("processes not all stopped: %+v", r.procs)
	}
	if want := []string{"/student", "/student/tryout/abc"}; !reflect.DeepEqual(r.spec.Routes, want) {
		t.Fatalf("routes %v", r.spec.Routes)
	}
	if r.spec.Login == nil || r.spec.Login.User != "naila@example.com" || r.spec.Login.Password != "password" || r.spec.Login.Path != "/login" {
		t.Fatalf("login %+v", r.spec.Login)
	}
	apiPort := envValue(r.envs["api-server"], "PORT")
	if !strings.HasSuffix(r.spec.BaseURL, ":"+envValue(r.envs["web-dev"], "PORT")) || !strings.HasPrefix(r.spec.BaseURL, "http://localhost:") {
		t.Fatalf("base %q", r.spec.BaseURL)
	}
	if got := envValue(r.envs["web-dev"], "NEXT_PUBLIC_API_URL"); got != "http://localhost:"+apiPort || apiPort == "" {
		t.Fatalf("web env API url = %q (api port %q)", got, apiPort)
	}
	if _, err := os.Stat(filepath.Join(r.f.DataDir, "smoke", "wo_1", "2", "spec.json")); err == nil {
		t.Fatal("spec.json holds credentials and must be removed after the run")
	}
}

func TestSmokeRunFailedRouteFailsAndBlockingFlipsOptional(t *testing.T) {
	r := newSmokeRig(t, []string{"web/app/student/page.tsx"}, smokeTree)
	r.browser = `{"results":[{"path":"/student","status":200,"ok":false,"problems":["Next error overlay: boom"],"screenshot":""}]}`
	p := smokeProfile()
	p.Smoke.Blocking = true
	c, res := r.f.runSmoke(context.Background(), r.wo, p)
	if res.Outcome != model.Fail || c.Optional || !strings.Contains(res.Output, "FAIL /student (200): Next error overlay: boom") {
		t.Fatalf("check %+v result %+v", c, res)
	}
}

func TestSmokeRunNoAffectedPagesPassesWithoutStartingAnything(t *testing.T) {
	r := newSmokeRig(t, []string{"api/main.go"}, smokeTree)
	_, res := r.f.runSmoke(context.Background(), r.wo, smokeProfile())
	if res.Outcome != model.Pass || res.Output != "no affected pages" || len(r.procs) != 0 {
		t.Fatalf("%+v procs=%d", res, len(r.procs))
	}
}

func TestSmokeRunReadyTimeoutFailsAndStopsProcesses(t *testing.T) {
	r := newSmokeRig(t, []string{"web/app/student/page.tsx"}, smokeTree)
	r.deaf["api-server"] = true
	p := smokeProfile()
	p.Smoke.ReadyTimeoutSec = 1
	_, res := r.f.runSmoke(context.Background(), r.wo, p)
	if res.Outcome != model.Fail || !strings.Contains(res.Output, "api not ready") && !strings.Contains(res.Output, "api not ready on /healthz") {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Output, "fake log of api-server") || !r.stoppedAll() {
		t.Fatalf("log tail missing or processes left running: %+v", res)
	}
}

func TestSmokeRunLoginFailureAndMissingParamAndMissingCreds(t *testing.T) {
	r := newSmokeRig(t, []string{"web/app/student/page.tsx"}, smokeTree)
	r.browser = `{"loginError":"waitForURL timeout","results":[]}`
	if _, res := r.f.runSmoke(context.Background(), r.wo, smokeProfile()); res.Outcome != model.Fail || !strings.Contains(res.Output, "login failed") {
		t.Fatalf("login: %+v", res)
	}

	r2 := newSmokeRig(t, []string{"web/app/student/tryout/[id]/page.tsx"}, smokeTree)
	p := smokeProfile()
	p.Smoke.Params = nil
	if _, res := r2.f.runSmoke(context.Background(), r2.wo, p); res.Outcome != model.Unverified || !strings.Contains(res.Output, `"id"`) || len(r2.procs) != 0 {
		t.Fatalf("param: %+v", res)
	}

	r3 := newSmokeRig(t, []string{"web/app/student/page.tsx"}, smokeTree)
	p = smokeProfile()
	delete(p.Env, "SMOKE_PASSWORD")
	if _, res := r3.f.runSmoke(context.Background(), r3.wo, p); res.Outcome != model.Unverified || !strings.Contains(res.Output, "SMOKE_PASSWORD") || !r3.stoppedAll() {
		t.Fatalf("creds: %+v", res)
	}
}

func TestSmokeRunNoPlaywrightIsUnverifiedNeverPass(t *testing.T) {
	r := newSmokeRig(t, []string{"web/app/student/page.tsx"}, smokeTree)
	r.f.PlaywrightDir = ""
	t.Setenv("LEGOS_PLAYWRIGHT_DIR", "")
	t.Chdir(t.TempDir())
	if _, res := r.f.runSmoke(context.Background(), r.wo, smokeProfile()); res.Outcome == model.Pass {
		t.Fatalf("a run that could not use a browser must not pass: %+v", res)
	}
}

func TestRequiredFailedIgnoresOptional(t *testing.T) {
	checks := []model.Check{{Name: "a"}, {Name: "b", Optional: true}}
	if requiredFailed(checks, []model.CheckResult{{Outcome: model.Pass}, {Outcome: model.Fail}}) {
		t.Fatal("an optional failure must not skip smoke")
	}
	if !requiredFailed(checks, []model.CheckResult{{Outcome: model.Unverified}, {Outcome: model.Pass}}) {
		t.Fatal("a required non-pass must skip smoke")
	}
}

func TestVerifyRecordsSmokeAsOptionalCheck(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	passing := true
	r.f.Sandbox = smokeVerifySandbox{flipSandbox{passing: &passing}, []string{"web/app/student/page.tsx"}}
	r.workspaceFor(t, r.wo.ID, smokeTree)
	prof := smokeProfile()
	prof.Checks = []model.Check{{Name: "types", Command: "true"}}
	raw, _ := json.Marshal(prof)
	if err := r.wos.UpdateFields(ctx, r.wo.ID, map[string]any{"check_profile": string(raw)}); err != nil {
		t.Fatal(err)
	}
	r.f.PlaywrightDir = t.TempDir()
	r.f.smoke = smokeDeps{
		start: func(_ context.Context, _, _ string, env []string) (smokeProc, error) {
			ln, err := net.Listen("tcp", "127.0.0.1:"+envValue(env, "PORT"))
			if err != nil {
				return nil, err
			}
			go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
			return &fakeProc{ln: ln, done: make(chan struct{})}, nil
		},
		browser: func(context.Context, string, string) ([]byte, error) {
			return []byte(`{"results":[{"path":"/student","status":200,"ok":false,"problems":["console error: x"]}]}`), nil
		},
	}
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateVerifying); err != nil {
		t.Fatal(err)
	}
	if err := r.f.handleVerify(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	rows, err := r.f.Quality.ForRevision(ctx, r.wo.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var smoke *model.QualityCheck
	for i := range rows {
		if rows[i].Name == "smoke" {
			smoke = &rows[i]
		}
	}
	if smoke == nil || smoke.Outcome != "FAIL" || !smoke.Optional {
		t.Fatalf("smoke row %+v of %+v", smoke, rows)
	}
	got, _ := r.wos.Get(ctx, r.wo.ID)
	if got.State == model.StateImplementing {
		t.Fatal("an optional smoke failure must not send work back")
	}
}

type queryDocker struct{ *fakeDocker }

func (d queryDocker) Run(ctx context.Context, args ...string) (string, error) {
	out, err := d.fakeDocker.Run(ctx, args...)
	if strings.Contains(strings.Join(args, " "), " -At ") {
		return "id-42\nignored\n", err
	}
	return out, err
}

func TestSmokeSQLParamQueriesTheRunDatabaseAndKeepsStackEnvOut(t *testing.T) {
	d := &fakeDocker{}
	r := newSmokeRig(t, []string{"web/app/student/tryout/[id]/page.tsx"}, smokeTree)
	sp := stackProfile()
	p := smokeProfile()
	p.Services, p.Database, p.Sources, p.Env, p.Setup = sp.Services, sp.Database, sp.Sources, map[string]string{"SMOKE_USER": "u", "SMOKE_PASSWORD": "p", "DATABASE_URL": "x/{{db}}"}, nil
	p.Smoke.Params = map[string]string{"id": "sql:SELECT id FROM tryouts"}
	writeTree(t, r.f.workspaceDir(r.wo.ID), map[string]string{"docker-compose.yml": "services: {}\n"})
	r.f.Docker = queryDocker{d}
	r.browser = `{"results":[{"path":"/student/tryout/id-42","status":200,"ok":true,"problems":[]}]}`
	_, res := r.f.runSmoke(context.Background(), r.wo, p)
	if res.Outcome != model.Pass {
		t.Fatalf("%+v", res)
	}
	if want := []string{"/student/tryout/id-42"}; !reflect.DeepEqual(r.spec.Routes, want) {
		t.Fatalf("routes %v", r.spec.Routes)
	}
	if d.count("exec -T postgres psql postgres://u:p@127.0.0.1:5432/run_wo_1_2_smoke?sslmode=disable -At") != 1 {
		t.Fatalf("query must run against the run database: %v", d.calls)
	}
	if d.count(`DROP DATABASE IF EXISTS "run_wo_1_2_smoke"`) == 0 {
		t.Fatalf("smoke database not dropped: %v", d.calls)
	}
	if strings.Contains(strings.Join(r.envs["api-server"], " "), "LEGOS_") {
		t.Fatal("LEGOS_* is for per-run setup only")
	}
}

type smokeVerifySandbox struct {
	flipSandbox
	files []string
}

func (s smokeVerifySandbox) ChangedFiles(context.Context, string) ([]string, error) {
	return s.files, nil
}
