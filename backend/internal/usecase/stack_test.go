package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type fakeDocker struct {
	mu      sync.Mutex
	calls   []string
	version string
	missing bool
	ls      string
	fail    func(args string) error
}

func (d *fakeDocker) Run(_ context.Context, args ...string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	line := strings.Join(args, " ")
	d.calls = append(d.calls, line)
	if d.missing {
		return "", errors.New(`exec: "docker": executable file not found in $PATH`)
	}
	if d.fail != nil {
		if err := d.fail(line); err != nil {
			return "", err
		}
	}
	switch {
	case strings.HasPrefix(line, "compose version"):
		if d.version == "" {
			return "v5.5.0\n", nil
		}
		return d.version, nil
	case strings.Contains(line, " port "):
		f := strings.Fields(line)
		return "0.0.0.0:3" + f[len(f)-1] + "\n", nil
	case strings.HasPrefix(line, "compose ls"):
		return d.ls, nil
	}
	return "", nil
}

func (d *fakeDocker) count(sub string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

type envSandbox struct {
	dirSandbox
	mu      sync.Mutex
	runs    []envRun
	outcome func(model.Check) model.Outcome
}

type envRun struct {
	dir   string
	check model.Check
	env   []string
}

func (s *envSandbox) RunCheck(_ context.Context, dir string, c model.Check, _ time.Duration, env []string) model.CheckResult {
	s.mu.Lock()
	s.runs = append(s.runs, envRun{dir, c, env})
	s.mu.Unlock()
	o := model.Pass
	if s.outcome != nil {
		o = s.outcome(c)
	}
	return model.CheckResult{Name: c.Name, Command: c.Command, Outcome: o}
}

const composeBody = "services: {}\n"

var composeSHA = func() string { h := sha256.Sum256([]byte(composeBody)); return hex.EncodeToString(h[:]) }()

func stackProfile() model.Profile {
	return model.Profile{
		Services: &model.ProfileServices{Compose: "docker-compose.yml", Names: []string{"postgres", "redis"}, Ports: map[string]int{"postgres": 5432, "redis": 6379}},
		Database: &model.ProfileDatabase{Service: "postgres", AdminURL: "postgres://u:p@{{postgres}}/u?sslmode=disable"},
		Env: map[string]string{
			"DATABASE_URL": "postgres://u:p@{{postgres}}/{{db}}?sslmode=disable",
			"REDIS_URL":    "redis://{{redis}}/{{redis_db}}",
			"BUCKET":       "{{db}}",
		},
		Sources: []model.ProfileSource{{Path: "docker-compose.yml", SHA256: composeSHA}},
		Setup:   model.SetupSteps{{Name: "migrate", Dir: "api", Command: "make migrate-up", PerRun: true}},
		Checks:  []model.Check{{Name: "api test", Dir: "api", Command: "go test ./..."}, {Name: "smtp", Command: "go test -run SMTP", Optional: true}},
	}
}

func stackFactory(t *testing.T, d *fakeDocker, sb Sandbox) (*Factory, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(composeBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Factory{Docker: d, Sandbox: sb, DataDir: t.TempDir()}, dir
}

func TestRunProfileChecksResolvesPlaceholdersAndLeases(t *testing.T) {
	p := stackProfile()
	d := &fakeDocker{}
	sb := &envSandbox{}
	f, dir := stackFactory(t, d, sb)
	res, err := f.runProfileChecks(context.Background(), "prj_1", "run_wo-1_2", dir, p)
	if err != nil || len(res) != 2 {
		t.Fatalf("res=%v err=%v", res, err)
	}
	want := []string{
		"BUCKET=run_wo_1_2",
		"DATABASE_URL=postgres://u:p@127.0.0.1:35432/run_wo_1_2?sslmode=disable",
		"REDIS_URL=redis://127.0.0.1:36379/1",
	}
	if len(sb.runs) != 3 {
		t.Fatalf("want the per-run migration plus 2 checks, got %d runs", len(sb.runs))
	}
	if sb.runs[0].check.Name != "migrate" || sb.runs[0].dir != dir {
		t.Fatalf("per-run setup ran %+v", sb.runs[0])
	}
	for i, r := range sb.runs {
		w := want
		if i == 0 { // per-run setup also gets the stack coordinates
			w = append(append([]string(nil), want...), "LEGOS_COMPOSE_PROJECT="+stackName("prj_1"), "LEGOS_DB=run_wo_1_2")
		}
		if strings.Join(r.env, "|") != strings.Join(w, "|") {
			t.Fatalf("env = %v\nwant %v", r.env, w)
		}
	}
	if d.count(`CREATE DATABASE "run_wo_1_2"`) != 1 || d.count(`DROP DATABASE IF EXISTS "run_wo_1_2"`) != 2 {
		t.Fatalf("db lifecycle wrong: %v", d.calls)
	}
	if !strings.Contains(strings.Join(d.calls, "\n"), "postgres://u:p@127.0.0.1:5432/u") {
		t.Fatalf("admin url must use the container port inside the container: %v", d.calls)
	}
	s := f.stackMgr().stack("legos-ck-prj_1")
	if s.leases != 0 || s.redis[1] {
		t.Fatalf("lease not released: leases=%d redis1=%v", s.leases, s.redis[1])
	}
}

func TestStackUpArgsAndOverrideFile(t *testing.T) {
	d := &fakeDocker{}
	f, dir := stackFactory(t, d, &envSandbox{})
	if _, err := f.runProfileChecks(context.Background(), "prj_1", "run_a", dir, stackProfile()); err != nil {
		t.Fatal(err)
	}
	override := filepath.Join(f.DataDir, "stacks", "prj_1", "override.yml")
	b, err := os.ReadFile(override)
	if err != nil {
		t.Fatal(err)
	}
	wantYAML := "services:\n  postgres:\n    ports: !override [\"127.0.0.1::5432\"]\n  redis:\n    ports: !override [\"127.0.0.1::6379\"]\n"
	if string(b) != wantYAML {
		t.Fatalf("override:\n%s", b)
	}
	copied := filepath.Join(f.DataDir, "stacks", "prj_1", "compose.yml")
	if b, err := os.ReadFile(copied); err != nil || string(b) != composeBody {
		t.Fatalf("factory-owned compose copy missing: %q %v", b, err)
	}
	up := "compose -p legos-ck-prj_1 --project-directory " + dir + " -f " + copied + " -f " + override + " up -d --wait postgres redis"
	if d.count(up) != 1 {
		t.Fatalf("up call missing, want %q in %v", up, d.calls)
	}
	if d.count("compose -p legos-ck-prj_1 port postgres 5432") != 1 {
		t.Fatalf("port discovery missing: %v", d.calls)
	}
}

func TestStackIsReusedWhileLeased(t *testing.T) {
	d := &fakeDocker{}
	f, dir := stackFactory(t, d, &envSandbox{})
	m := f.stackMgr()
	p := stackProfile()
	a, err := m.acquire(context.Background(), "prj_1", "run_a", dir, p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.acquire(context.Background(), "prj_1", "run_b", dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if d.count(" up -d") != 1 {
		t.Fatalf("overlapping runs must share one up: %v", d.calls)
	}
	if a.DB == b.DB || a.Redis == b.Redis || a.Redis == 0 || b.Redis == 0 {
		t.Fatalf("runs must not share db or redis index: %+v %+v", a, b)
	}
	a.release()
	b.release()
	b.release()
	if s := m.stack("legos-ck-prj_1"); s.leases != 0 {
		t.Fatalf("leases = %d", s.leases)
	}
}

func TestLeaseReleasedWhenSetupFailsOrChecksFail(t *testing.T) {
	cases := []struct {
		name    string
		outcome func(model.Check) model.Outcome
		want    model.Outcome
	}{
		{"checks fail", func(c model.Check) model.Outcome {
			if c.PerRun {
				return model.Pass
			}
			return model.Fail
		}, model.Fail},
		{"per-run setup fails", func(c model.Check) model.Outcome {
			if c.PerRun {
				return model.Fail
			}
			return model.Pass
		}, model.Unverified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDocker{}
			f, dir := stackFactory(t, d, &envSandbox{outcome: tc.outcome})
			res, _ := f.runProfileChecks(context.Background(), "prj_1", "run_x", dir, stackProfile())
			for _, r := range res {
				if r.Outcome != tc.want {
					t.Fatalf("outcome %s, want %s: %q", r.Outcome, tc.want, r.Output)
				}
			}
			if d.count(`DROP DATABASE IF EXISTS "run_x"`) != 2 {
				t.Fatalf("database not dropped: %v", d.calls)
			}
			if s := f.stackMgr().stack("legos-ck-prj_1"); s.leases != 0 || s.redis[1] {
				t.Fatalf("lease leaked: %d %v", s.leases, s.redis[1])
			}
		})
	}
}

func TestLeaseReleasedWhenDatabaseCreateFails(t *testing.T) {
	d := &fakeDocker{fail: func(a string) error {
		if strings.Contains(a, "CREATE DATABASE") {
			return errors.New("boom")
		}
		return nil
	}}
	f, dir := stackFactory(t, d, &envSandbox{})
	res, _ := f.runProfileChecks(context.Background(), "prj_1", "run_x", dir, stackProfile())
	if len(res) != 2 || res[0].Outcome != model.Unverified || !strings.Contains(res[0].Output, "could not create database") {
		t.Fatalf("res = %+v", res)
	}
	if s := f.stackMgr().stack("legos-ck-prj_1"); s.leases != 0 || s.redis[1] {
		t.Fatalf("lease leaked: %d %v", s.leases, s.redis[1])
	}
}

func TestPreflightFailuresAreUnverifiedNeverPass(t *testing.T) {
	cases := []struct {
		name string
		d    *fakeDocker
		want string
	}{
		{"docker missing", &fakeDocker{missing: true}, "docker compose is not available"},
		{"compose too old", &fakeDocker{version: "2.23.3\n"}, "too old"},
		{"compose version unreadable", &fakeDocker{version: "weird"}, "could not read"},
		{"up fails", &fakeDocker{fail: func(a string) error {
			if strings.Contains(a, " up ") {
				return errors.New("unhealthy")
			}
			return nil
		}}, "starting the check services failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sb := &envSandbox{}
			f, dir := stackFactory(t, tc.d, sb)
			res, err := f.runProfileChecks(context.Background(), "prj_1", "run_x", dir, stackProfile())
			if err != nil || len(res) != 2 {
				t.Fatalf("res=%v err=%v", res, err)
			}
			for _, r := range res {
				if r.Outcome != model.Unverified || !strings.Contains(r.Output, tc.want) {
					t.Fatalf("got %s %q, want UNVERIFIED containing %q", r.Outcome, r.Output, tc.want)
				}
			}
			if len(sb.runs) != 0 {
				t.Fatalf("no command may run without its services: %+v", sb.runs)
			}
		})
	}
}

func TestComposeFileMustBeInsideWorkspace(t *testing.T) {
	d := &fakeDocker{}
	f, dir := stackFactory(t, d, &envSandbox{})
	p := stackProfile()
	p.Services.Compose = "../outside.yml"
	res, _ := f.runProfileChecks(context.Background(), "prj_1", "run_x", dir, p)
	if res[0].Outcome != model.Unverified || !strings.Contains(res[0].Output, "not inside the workspace") {
		t.Fatalf("res = %+v", res[0])
	}
}

func TestUnknownPlaceholderIsUnverified(t *testing.T) {
	d := &fakeDocker{}
	f, dir := stackFactory(t, d, &envSandbox{})
	p := stackProfile()
	p.Env["X"] = "{{rustfs}}"
	res, _ := f.runProfileChecks(context.Background(), "prj_1", "run_x", dir, p)
	if res[0].Outcome != model.Unverified || !strings.Contains(res[0].Output, "{{rustfs}}") {
		t.Fatalf("res = %+v", res[0])
	}
	if s := f.stackMgr().stack("legos-ck-prj_1"); s.leases != 0 {
		t.Fatal("lease leaked")
	}
}

func TestProfileWithoutStackRunsPlainly(t *testing.T) {
	d := &fakeDocker{}
	sb := &envSandbox{}
	f, dir := stackFactory(t, d, sb)
	p := model.Profile{Env: map[string]string{"A": "b"}, Checks: []model.Check{{Name: "t", Command: "true", Dir: "api"}}}
	res, err := f.runProfileChecks(context.Background(), "prj_1", "run_x", dir, p)
	if err != nil || len(res) != 1 || res[0].Outcome != model.Pass {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if len(d.calls) != 0 || len(sb.runs) != 1 || sb.runs[0].dir != dir || strings.Join(sb.runs[0].env, ",") != "A=b" {
		t.Fatalf("calls=%v runs=%+v", d.calls, sb.runs)
	}
}

func TestRedisIndexesAreLeasedAndWaitWhenExhausted(t *testing.T) {
	m := newStackMgr(&fakeDocker{}, t.TempDir())
	s := m.stack("legos-ck-x")
	var got []int
	for i := 0; i < redisIndexes; i++ {
		n, err := s.leaseRedis(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	if got[0] != 1 || got[redisIndexes-1] != redisIndexes {
		t.Fatalf("indexes %v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := s.leaseRedis(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exhausted pool must wait, got %v", err)
	}
	done := make(chan int, 1)
	go func() {
		n, _ := s.leaseRedis(context.Background())
		done <- n
	}()
	time.Sleep(20 * time.Millisecond)
	s.mu.Lock()
	s.redis[7] = false
	close(s.freed)
	s.freed = make(chan struct{})
	s.mu.Unlock()
	select {
	case n := <-done:
		if n != 7 {
			t.Fatalf("waiter got %d, want the freed 7", n)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter never woke")
	}
}

func TestIdleStackGoesDownAfterTwoHoursWithoutLease(t *testing.T) {
	d := &fakeDocker{}
	f, dir := stackFactory(t, d, &envSandbox{})
	m := f.stackMgr()
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	l, err := m.acquire(context.Background(), "prj_1", "run_x", dir, stackProfile())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Hour)
	m.reap(context.Background())
	if d.count(" down ") != 0 {
		t.Fatal("a leased stack must stay up")
	}
	l.release()
	now = now.Add(2*time.Hour - time.Minute)
	m.reap(context.Background())
	if d.count(" down ") != 0 {
		t.Fatal("stack went down before 2h idle")
	}
	now = now.Add(time.Minute)
	m.reap(context.Background())
	if d.count("compose -p legos-ck-prj_1 down -v") != 1 {
		t.Fatalf("idle stack not taken down: %v", d.calls)
	}
	m.reap(context.Background())
	if d.count(" down ") != 1 {
		t.Fatal("a stack already down must not be downed again")
	}
	if _, err := m.acquire(context.Background(), "prj_1", "run_y", dir, stackProfile()); err != nil {
		t.Fatal(err)
	}
	if d.count(" up -d") != 2 {
		t.Fatalf("next run must bring the stack back: %v", d.calls)
	}
}

func TestOrphanedStacksStartIdleClockAtStartup(t *testing.T) {
	data := t.TempDir()
	override := filepath.Join(data, "stacks", "prj_9", "override.yml")
	if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ls, _ := json.Marshal([]map[string]string{
		{"Name": "legos-ck-prj_9", "ConfigFiles": "/x/compose.yml," + override},
		{"Name": "bimbel-tryout-dev", "ConfigFiles": "/y/docker-compose.yml"},
		{"Name": "legos-factory-v2", "ConfigFiles": "/z/docker-compose.yml"},
		{"Name": "legos-ck-x", "ConfigFiles": "/z/docker-compose.yml"},
		{"Name": "legos-ck-gone", "ConfigFiles": filepath.Join(data, "stacks", "gone", "override.yml")},
	})
	d := &fakeDocker{ls: string(ls)}
	m := newStackMgr(d, data)
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	m.adoptOrphans(context.Background())
	now = now.Add(2*time.Hour - time.Second)
	m.reap(context.Background())
	if d.count(" down ") != 0 {
		t.Fatal("orphan went down early")
	}
	now = now.Add(time.Second)
	m.reap(context.Background())
	if d.count("compose -p legos-ck-prj_9 down -v") != 1 {
		t.Fatalf("calls: %v", d.calls)
	}
	for _, other := range []string{"bimbel-tryout-dev", "legos-factory-v2", "legos-ck-x", "legos-ck-gone"} {
		if d.count("-p "+other) != 0 {
			t.Fatalf("%s is not a factory stack and must never be touched: %v", other, d.calls)
		}
	}
}

func TestStopStackTakesDownNow(t *testing.T) {
	d := &fakeDocker{}
	f := &Factory{Docker: d, DataDir: t.TempDir()}
	if err := f.StopStack(context.Background(), "prj_1"); err != nil {
		t.Fatal(err)
	}
	if d.count("compose -p legos-ck-prj_1 down -v") != 1 {
		t.Fatalf("calls: %v", d.calls)
	}
}

func TestSanitizeRunName(t *testing.T) {
	long := sanitizeRunName("run_wo_" + strings.Repeat("a", 80))
	other := sanitizeRunName("run_wo_" + strings.Repeat("a", 79) + "b")
	for in, want := range map[string]string{
		"run_wo_731fdd9d-e79_2": "run_wo_731fdd9d_e79_2",
		"Run.A b":               "run_a_b",
	} {
		if got := sanitizeRunName(in); got != want {
			t.Fatalf("%q → %q, want %q", in, got, want)
		}
	}
	if len(long) > maxDBName || long == other {
		t.Fatalf("long names must stay unique and within the identifier limit: %q %q", long, other)
	}
}

func TestParseComposeVersion(t *testing.T) {
	for in, want := range map[string][3]int{"v5.5.0\n": {5, 5, 1}, "2.24.6-desktop.1": {2, 24, 1}, "x": {0, 0, 0}} {
		a, b, ok := parseComposeVersion(in)
		got := [3]int{a, b, 0}
		if ok {
			got[2] = 1
		}
		if got != want {
			t.Fatalf("%q → %v, want %v", in, got, want)
		}
	}
}

func TestOptionalChecksNeverSendWorkBackNorCountAsEvidence(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, nil)
	q := &fakeQueue{}
	f.Sched, f.DataDir = q, t.TempDir()
	f.Sandbox = &envSandbox{outcome: func(c model.Check) model.Outcome {
		if c.Name == "smtp" {
			return model.Fail
		}
		return model.Pass
	}}
	f.Quality, f.Findings = &store.QualityRepo{DB: wos.DB}, &store.FindingRepo{DB: wos.DB}
	prof, _ := json.Marshal(model.Profile{Checks: []model.Check{{Name: "unit", Command: "x"}, {Name: "smtp", Command: "y", Optional: true}}})
	if err := wos.UpdateFields(ctx, wo.ID, map[string]any{"check_profile": string(prof)}); err != nil {
		t.Fatal(err)
	}
	if err := wos.UpdateState(ctx, wo.ID, model.StateVerifying); err != nil {
		t.Fatal(err)
	}
	if err := f.handleVerify(ctx, model.Job{WOID: wo.ID}); err != nil {
		t.Fatal(err)
	}
	got, _ := wos.Get(ctx, wo.ID)
	if got.State != model.StateSpecialistReview || got.Revision != 0 {
		t.Fatalf("an optional failure sent work back: state=%s rev=%d", got.State, got.Revision)
	}
	stored, _ := f.Quality.ForRevision(ctx, wo.ID, 0)
	if len(stored) != 2 || stored[0].Optional || !stored[1].Optional || stored[1].Outcome != "FAIL" {
		t.Fatalf("optional flag not persisted: %+v", stored)
	}
	if n, _ := f.Quality.CountForRevision(ctx, wo.ID, 0); n != 1 {
		t.Fatalf("evidence = %d, want only the non-optional PASS", n)
	}
	if _, failed := f.checksText(ctx, got); failed {
		t.Fatal("an optional failure must not read as a failed check")
	}
}

func TestOnlyOptionalChecksLeaveNoEvidence(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, nil)
	f.Quality = &store.QualityRepo{DB: wos.DB}
	if err := f.Quality.Save(ctx, model.QualityCheck{ID: "a", WOID: wo.ID, Name: "smtp", Outcome: "PASS", Optional: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.Quality.Save(ctx, model.QualityCheck{ID: "b", WOID: wo.ID, Name: "unit", Outcome: "FAIL"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.Quality.CountForRevision(ctx, wo.ID, 0); n != 0 {
		t.Fatalf("evidence = %d, want 0", n)
	}
	if n, _ := f.Quality.Count(ctx, wo.ID); n != 2 {
		t.Fatalf("count = %d: baseline detection still sees every row", n)
	}
}

func TestBuilderServicesLeaseEnvAndRelease(t *testing.T) {
	ctx := context.Background()
	d := &fakeDocker{}
	sb := &envSandbox{}
	f, wo, wos := monitorFactory(t, envHarness{})
	f.Docker, f.Sandbox, f.DataDir = d, sb, t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(composeBody), 0o644); err != nil {
		t.Fatal(err)
	}
	prof, _ := json.Marshal(stackProfile())
	if err := wos.UpdateFields(ctx, wo.ID, map[string]any{"check_profile": string(prof)}); err != nil {
		t.Fatal(err)
	}
	wo.CheckProfile = string(prof)
	opts := runOpts{Dir: dir}
	line, release := f.builderServices(ctx, wo, model.Job{TaskID: "T1"}, &opts)
	if release == nil {
		t.Fatalf("no lease: %v", d.calls)
	}
	for _, name := range []string{"DATABASE_URL", "REDIS_URL", "BUCKET", "disposable"} {
		if !strings.Contains(line, name) {
			t.Fatalf("prompt line missing %q: %s", name, line)
		}
	}
	if strings.Contains(line, "127.0.0.1") || strings.Contains(line, "run_wo1_T1") {
		t.Fatalf("prompt line must list names, not values: %s", line)
	}
	if len(opts.Env) != 3 || !strings.Contains(strings.Join(opts.Env, ","), "BUCKET=run_wo1_t1") {
		t.Fatalf("env = %v", opts.Env)
	}
	if sb.runs[0].check.Name != "migrate" || sb.runs[0].dir != dir {
		t.Fatalf("per-run setup must run in the task dir: %+v", sb.runs[0])
	}
	release()
	if d.count(`DROP DATABASE IF EXISTS "run_wo1_t1"`) != 2 || f.stackMgr().stack("legos-ck-p1").leases != 0 {
		t.Fatalf("not released: %v", d.calls)
	}
}

func TestBuilderServicesWithoutProfileServices(t *testing.T) {
	f, wo, _ := monitorFactory(t, envHarness{})
	opts := runOpts{Dir: t.TempDir()}
	if line, release := f.builderServices(context.Background(), wo, model.Job{TaskID: "T1"}, &opts); release != nil || line != "" || opts.Env != nil {
		t.Fatal("a project without services must run the Builder untouched")
	}
}

func TestBuilderServicesUnavailableDoesNotBlockBuilder(t *testing.T) {
	ctx := context.Background()
	f, wo, wos := monitorFactory(t, envHarness{})
	f.Docker, f.Sandbox, f.DataDir = &fakeDocker{missing: true}, &envSandbox{}, t.TempDir()
	prof, _ := json.Marshal(stackProfile())
	if err := wos.UpdateFields(ctx, wo.ID, map[string]any{"check_profile": string(prof)}); err != nil {
		t.Fatal(err)
	}
	wo.CheckProfile = string(prof)
	opts := runOpts{Dir: t.TempDir()}
	line, release := f.builderServices(ctx, wo, model.Job{TaskID: "T1"}, &opts)
	if release != nil || line != "" {
		t.Fatalf("line=%q release=%v", line, release != nil)
	}
	var saw bool
	for _, e := range agentEvents(t, wos) {
		saw = saw || e.Kind == "services_unavailable"
	}
	if !saw {
		t.Fatal("the failure must be reported")
	}
}

func TestComposeFileMustMatchApprovedHash(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(p *model.Profile, dir string)
	}{
		{"builder rewrote the file", func(p *model.Profile, dir string) {
			_ = os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {evil: {}}\n"), 0o644)
		}},
		{"no approved hash for the path", func(p *model.Profile, dir string) { p.Sources = nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &fakeDocker{}
			f, dir := stackFactory(t, d, &envSandbox{})
			p := stackProfile()
			c.mutate(&p, dir)
			_, err := f.stackMgr().acquire(context.Background(), "prj_1", "run_x", dir, p)
			if !errors.Is(err, errComposeChanged) {
				t.Fatalf("err = %v", err)
			}
			if d.count(" up ") != 0 {
				t.Fatalf("docker must not start from an unapproved compose file: %v", d.calls)
			}
		})
	}
}

func TestAcquireBringsStackBackUpAfterStopWhileLeased(t *testing.T) {
	d := &fakeDocker{}
	f, dir := stackFactory(t, d, &envSandbox{})
	m := f.stackMgr()
	p := stackProfile()
	a, err := m.acquire(context.Background(), "prj_1", "run_a", dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.stop(context.Background(), "prj_1"); err != nil {
		t.Fatal(err)
	}
	b, err := m.acquire(context.Background(), "prj_1", "run_b", dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if d.count(" up -d") != 2 || len(b.hostPorts) == 0 {
		t.Fatalf("a stopped stack with a lease held must come back up: %v ports=%v", d.calls, b.hostPorts)
	}
	a.release()
	b.release()
}

func TestAcquireAndStopRaceFree(t *testing.T) {
	d := &fakeDocker{}
	f, dir := stackFactory(t, d, &envSandbox{})
	m := f.stackMgr()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := m.acquire(context.Background(), "prj_1", "run_"+strconv.Itoa(i), dir, stackProfile())
			if err != nil {
				return
			}
			_ = l.values(false)
			l.release()
		}()
		_ = m.stop(context.Background(), "prj_1")
	}
	wg.Wait()
}
