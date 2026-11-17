package usecase

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

const suggestCompose = "services:\n  postgres:\n    image: postgres:16\n  redis:\n    image: redis:7\n"

func suggestRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func baseRepo(t *testing.T) string {
	return suggestRepo(t, map[string]string{
		".github/workflows/ci.yml": "steps:\n  - run: go test ./...\n",
		"docker-compose.yml":       suggestCompose,
		"api/go.mod":               "module x\n",
		"web/package.json":         "{}\n",
		".env":                     "DB_PASSWORD=hunter2hunter2\nSHORT=ab\n",
	})
}

func validProfile() model.Profile {
	return model.Profile{
		Version: 2, Language: "go",
		Sources:  []model.ProfileSource{{Path: ".github/workflows/ci.yml"}},
		Services: &model.ProfileServices{Compose: "docker-compose.yml", Names: []string{"postgres", "redis"}, Ports: map[string]int{"postgres": 5432}},
		Database: &model.ProfileDatabase{Service: "postgres", AdminURL: "postgres://u:p@{{postgres}}/postgres"},
		Env:      map[string]string{"DATABASE_URL": "postgres://u:p@{{postgres}}/{{db}}", "REDIS_URL": "redis://{{redis}}/{{redis_db}}", "MODE": "test"},
		Checks:   []model.Check{{Name: "api test", Dir: "api", Command: "go test ./...", Source: ".github/workflows/ci.yml:2"}},
	}
}

func TestValidateProfile(t *testing.T) {
	root := baseRepo(t)
	cases := []struct {
		name string
		edit func(p *model.Profile)
		want string // substring of the error; empty means valid
	}{
		{"valid", func(*model.Profile) {}, ""},
		{"no checks", func(p *model.Profile) { p.Checks = nil }, "checks is empty"},
		{"dir escapes", func(p *model.Profile) { p.Checks[0].Dir = "../other" }, "must stay inside"},
		{"dir absolute", func(p *model.Profile) { p.Checks[0].Dir = "/etc" }, "relative path"},
		{"dir missing", func(p *model.Profile) { p.Checks[0].Dir = "nope" }, "not a directory"},
		{"setup dir escapes", func(p *model.Profile) { p.Setup = model.SetupSteps{{Name: "i", Dir: "../x", Command: "npm ci"}} }, "must stay inside"},
		{"empty command", func(p *model.Profile) { p.Checks[0].Command = " " }, "no command"},
		{"duplicate name", func(p *model.Profile) { p.Checks = append(p.Checks, p.Checks[0]) }, "used twice"},
		{"source missing", func(p *model.Profile) { p.Sources = []model.ProfileSource{{Path: "nope.yml"}} }, "source \"nope.yml\""},
		{"unknown placeholder", func(p *model.Profile) { p.Env["X"] = "{{kafka}}" }, "unknown placeholder {{kafka}}"},
		{"placeholder of a service outside names", func(p *model.Profile) { p.Services.Names = []string{"postgres"}; p.Env["REDIS_URL"] = "{{redis}}" }, "unknown placeholder {{redis}}"},
		{"service not in compose", func(p *model.Profile) { p.Services.Names = []string{"postgres", "kafka"} }, `service "kafka" is not in docker-compose.yml`},
		{"compose missing", func(p *model.Profile) { p.Services.Compose = "compose.yml" }, "not a file in the repository"},
		{"compose escapes", func(p *model.Profile) { p.Services.Compose = "../compose.yml" }, "must stay inside"},
		{"services without compose", func(p *model.Profile) { p.Services.Compose = "" }, "services.compose is required"},
		{"port of unlisted service", func(p *model.Profile) { p.Services.Ports["kafka"] = 9092 }, "not in services.names"},
		{"database service unlisted", func(p *model.Profile) { p.Database.Service = "mysql" }, "database.service"},
		{"admin url placeholder", func(p *model.Profile) { p.Database.AdminURL = "{{nope}}" }, "unknown placeholder {{nope}}"},
		{"env key invalid", func(p *model.Profile) { p.Env["bad key"] = "x" }, "not a valid variable name"},
		{"live secret prefix", func(p *model.Profile) { p.Env["STRIPE"] = "sk_live_abc" }, "live secret"},
		{"slack token prefix", func(p *model.Profile) { p.Env["SLACK"] = "xoxa-123" }, "live secret"},
		{"long random token only warns", func(p *model.Profile) { p.Env["TOKEN"] = "a1B2c3D4e5F6a1B2c3D4e5F6a1B2c3D4e5F6" }, ""},
		{"bucket placeholder", func(p *model.Profile) { p.Env["S3_BUCKET"] = "{{bucket}}" }, ""},
		{"port placeholder outside smoke", func(p *model.Profile) { p.Env["X"] = "http://localhost:{{port.web}}" }, "unknown placeholder {{port.web}}"},
		{"value from .env", func(p *model.Profile) { p.Env["PW"] = "hunter2hunter2" }, "read from a .env file"},
		{"short .env value is ignored", func(p *model.Profile) { p.Env["S"] = "ab" }, ""},
		{"test values pass", func(p *model.Profile) { p.Env["S3_SECRET_KEY"] = "tryout123" }, ""},
		{"no compose, no services", func(p *model.Profile) { p.Services, p.Database, p.Env = nil, nil, nil }, ""},
		{"no compose but a service placeholder", func(p *model.Profile) { p.Services, p.Database = nil, nil }, "unknown placeholder {{postgres}}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validProfile()
			tc.edit(&p)
			_, err := validateProfile(root, &p)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("valid profile rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateProfileSymlinkEscape(t *testing.T) {
	root := baseRepo(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	p := validProfile()
	p.Checks[0].Dir = "link"
	if _, err := validateProfile(root, &p); err == nil || !strings.Contains(err.Error(), "must stay inside") {
		t.Fatalf("symlink out of the repo accepted: %v", err)
	}
}

func TestSecretRules(t *testing.T) {
	for v, want := range map[string]bool{
		"sk_live_51Habc": true, "ghp_abcdefghijklmnopqrstuvwxyz0123456789": true, "github_pat_x": true,
		"AKIAIOSFODNN7EXAMPLE": true, "-----BEGIN PRIVATE KEY-----": true, "xoxb-1": true, "xoxa-1": true,
		"a1B2c3D4e5F6a1B2c3D4e5F6a1B2c3D4e5F6": false, "tryout123": false, "": false,
	} {
		if got := liveSecret(v); got != want {
			t.Errorf("liveSecret(%q) = %v, want %v", v, got, want)
		}
	}
	for v, want := range map[string]bool{
		"a1B2c3D4e5F6a1B2c3D4e5F6a1B2c3D4e5F6": true,
		"tryout123":                            false,
		"postgres://tryout:tryout@:5432/db":    false,
		"abcdefghijklmnopqrstuvwxyzabcdefghij": false, // long but no digit: a word, not a token
		"false":                                false,
	} {
		if got := looksLikeToken(v); got != want {
			t.Errorf("looksLikeToken(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestTokenLikeEnvIsAWarning(t *testing.T) {
	p := validProfile()
	p.Env["JWT_SECRET"] = "a1B2c3D4e5F6a1B2c3D4e5F6a1B2c3D4e5F6"
	w, err := validateProfile(baseRepo(t), &p)
	if err != nil || w["env.JWT_SECRET"] == "" {
		t.Fatalf("warnings %v, err %v", w, err)
	}
}

func validSmoke() *model.Smoke {
	return &model.Smoke{
		Start: []model.SmokeProcess{
			{Name: "api", Dir: "api", Command: "go run .", PortEnv: "PORT", Ready: "/healthz", Env: map[string]string{"APP_URL": "http://localhost:{{port.web}}", "BUCKET": "{{bucket}}"}},
			{Name: "web", Dir: "web", Command: "npm run dev", PortEnv: "PORT", Ready: "/", Env: map[string]string{"API_URL": "http://localhost:{{port.api}}"}},
		},
		Base: "web", Login: &model.SmokeLogin{Path: "/login", UserEnv: "SMOKE_USER", PasswordEnv: "SMOKE_PASSWORD"},
		RoutesFrom: "web/app/**/page.tsx", Params: map[string]string{"id": "sql:SELECT id FROM t", "slug": "intro"}, ReadyTimeoutSec: 90,
	}
}

func TestValidateSmoke(t *testing.T) {
	root := baseRepo(t)
	cases := []struct {
		name string
		edit func(p *model.Profile)
		want string
	}{
		{"valid", func(*model.Profile) {}, ""},
		{"no processes", func(p *model.Profile) { p.Smoke.Start = nil }, "smoke.start is empty"},
		{"duplicate process", func(p *model.Profile) { p.Smoke.Start[1].Name = "api" }, "used twice"},
		{"base unknown", func(p *model.Profile) { p.Smoke.Base = "admin" }, "smoke.base"},
		{"dir escapes", func(p *model.Profile) { p.Smoke.Start[0].Dir = "../x" }, "must stay inside"},
		{"dir missing", func(p *model.Profile) { p.Smoke.Start[0].Dir = "nope" }, "not a directory"},
		{"port_env invalid", func(p *model.Profile) { p.Smoke.Start[0].PortEnv = "my port" }, "port_env"},
		{"ready without slash", func(p *model.Profile) { p.Smoke.Start[0].Ready = "healthz" }, "must start with /"},
		{"routes_from empty", func(p *model.Profile) { p.Smoke.RoutesFrom = " " }, "routes_from is empty"},
		{"routes_from escapes", func(p *model.Profile) { p.Smoke.RoutesFrom = "../x/**/page.tsx" }, "routes_from"},
		{"login env missing", func(p *model.Profile) { p.Smoke.Login.UserEnv = "NOPE" }, `user_env "NOPE"`},
		{"login path", func(p *model.Profile) { p.Smoke.Login.Path = "login" }, "login.path"},
		{"empty param", func(p *model.Profile) { p.Smoke.Params["id"] = " " }, "params.id is empty"},
		{"empty sql", func(p *model.Profile) { p.Smoke.Params["id"] = "sql:" }, "params.id is empty"},
		{"sql without database", func(p *model.Profile) { p.Database = nil; p.Env["DATABASE_URL"] = "x" }, "no database block"},
		{"timeout too long", func(p *model.Profile) { p.Smoke.ReadyTimeoutSec = 601 }, "ready_timeout_sec"},
		{"timeout negative", func(p *model.Profile) { p.Smoke.ReadyTimeoutSec = -1 }, "ready_timeout_sec"},
		{"port of unknown process", func(p *model.Profile) { p.Smoke.Start[1].Env["X"] = "{{port.admin}}" }, "unknown placeholder {{port.admin}}"},
		{"live secret in process env", func(p *model.Profile) { p.Smoke.Start[0].Env["K"] = "sk_live_abc" }, "live secret"},
		{"dotenv value in process env", func(p *model.Profile) { p.Smoke.Start[0].Env["K"] = "hunter2hunter2" }, ".env file"},
		{"no login is fine", func(p *model.Profile) { p.Smoke.Login = nil }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validProfile()
			p.Env["SMOKE_USER"], p.Env["SMOKE_PASSWORD"] = "seed@example.test", "seedpass1"
			p.Smoke = validSmoke()
			tc.edit(&p)
			_, err := validateProfile(root, &p)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("valid smoke rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestLeasePlaceholdersAreResolvable(t *testing.T) {
	vals := (&runLease{DB: "run_x", profile: model.Profile{}}).values(false)
	for _, n := range leasePlaceholders {
		if _, ok := vals[n]; !ok {
			t.Errorf("placeholder {{%s}} is allowed by validation but the lease does not resolve it", n)
		}
	}
	if vals["bucket"] != "run-x" {
		t.Errorf("bucket = %q", vals["bucket"])
	}
}

func TestSmokeEnvRedisPlaceholderLeasesAnIndex(t *testing.T) {
	p := model.Profile{Smoke: &model.Smoke{Start: []model.SmokeProcess{{Name: "api", Env: map[string]string{"R": "redis://x/{{redis_db}}"}}}}}
	if !usesPlaceholder(p, "redis_db") {
		t.Fatal("a redis_db placeholder in smoke env must lease an index")
	}
}

func TestSuggestReplyIsStrict(t *testing.T) {
	var r suggestReply
	if err := decodeReply(`{"version":2,"checks":[{"name":"a","command":"b"}],"bogus":1}`, &r); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	var ok suggestReply
	if err := decodeReply("```json\n"+`{"version":2,"checks":[{"name":"a","command":"b"}],"notes":{"a":"why"}}`+"\n```", &ok); err != nil {
		t.Fatalf("fenced reply rejected: %v", err)
	}
	if ok.Notes["a"] != "why" || len(ok.Checks) != 1 {
		t.Fatalf("decoded %+v", ok)
	}
	if err := decodeReply(`{"version":2,"checks":[]}`, &r); err == nil {
		t.Fatal("empty checks accepted")
	}
}

// ---- the suggestion run ----

type suggestUsage struct {
	mu   sync.Mutex
	recs []model.UsageRecord
}

func (u *suggestUsage) Reserve(int64) (func(int64), bool) { return func(int64) {}, true }
func (u *suggestUsage) Record(_ context.Context, r model.UsageRecord) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.recs = append(u.recs, r)
	return nil
}

type allowAll struct{}

func (allowAll) Authorize(context.Context, *model.User, string) error { return nil }

type suggestRig struct {
	s       *CheckSuggester
	repo    string
	usage   *suggestUsage
	replies []string
	prompts []string
	dryRuns [][]model.Check
	removed bool
}

func newSuggestRig(t *testing.T, replies ...string) *suggestRig {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projects := &store.ProjectRepo{DB: db}
	if err := projects.Create(context.Background(), &model.Project{ID: "p1", Name: "p1"}); err != nil {
		t.Fatal(err)
	}
	r := &suggestRig{repo: baseRepo(t), usage: &suggestUsage{}, replies: replies}
	r.s = &CheckSuggester{
		Projects: allowAll{}, Proj: projects, Store: &store.CheckProposalRepo{DB: db}, Usage: r.usage,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Checkout:  func(context.Context, string) (string, string, func(), error) { return r.repo, "abc123", func() { r.removed = true }, nil },
		SourceDir: func(context.Context, string) (string, error) { return r.repo, nil },
		Agent: func(_ context.Context, _, _, prompt string) (string, model.AgentResult, error) {
			r.prompts = append(r.prompts, prompt)
			if len(r.replies) == 0 {
				return "", model.AgentResult{}, errors.New("no scripted reply")
			}
			text := r.replies[0]
			r.replies = r.replies[1:]
			return "anthropic/sonnet", model.AgentResult{Text: text, TokensIn: 100, TokensOut: 50, CostMicros: 9, CostSource: "measured"}, nil
		},
		DryRun: func(_ context.Context, _, _, _ string, p model.Profile) ([]model.CheckResult, error) {
			r.dryRuns = append(r.dryRuns, p.Checks)
			out := make([]model.CheckResult, len(p.Checks))
			zero, one := 0, 1
			for i, c := range p.Checks {
				out[i] = model.CheckResult{Name: c.Name, Command: c.Command, Outcome: model.Pass, ExitCode: &zero, Output: "ok"}
				if c.Name == "api test" {
					out[i].Outcome, out[i].ExitCode, out[i].Output = model.Fail, &one, strings.Repeat("x", 3000)+"boom"
				}
			}
			return out, nil
		},
	}
	return r
}

func (r *suggestRig) wait(t *testing.T) *CheckStatus {
	t.Helper()
	r.s.wg.Wait()
	st, err := r.s.Status(context.Background(), &model.User{ID: "u1"}, "p1")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

const goodReply = `{"version":2,"language":"go","sources":[{"path":".github/workflows/ci.yml"}],
"services":{"compose":"docker-compose.yml","names":["postgres"],"ports":{"postgres":5432}},
"database":{"service":"postgres","admin_url":"postgres://u:p@{{postgres}}/postgres"},
"env":{"DATABASE_URL":"postgres://u:p@{{postgres}}/{{db}}"},
"setup":[{"name":"web deps","dir":"web","command":"npm ci"},{"name":"migrate","dir":"api","command":"make migrate-up","per_run":true}],
"checks":[{"name":"api test","dir":"api","command":"go test ./...","source":".github/workflows/ci.yml:2"},{"name":"web lint","dir":"web","command":"npm run lint","source":"web/package.json:1"}],
"notes":{"web lint":"from package.json"}}`

func TestSuggestProducesProposal(t *testing.T) {
	r := newSuggestRig(t, goodReply)
	u := &model.User{ID: "u1"}
	if err := r.s.Start(context.Background(), u, "p1"); err != nil {
		t.Fatal(err)
	}
	st := r.wait(t)
	if st.Run.Status != "ready" || st.Proposal == nil {
		t.Fatalf("run = %+v", st.Run)
	}
	prop := st.Proposal
	if prop.BaseSHA != "abc123" || prop.Model != "anthropic/sonnet" || prop.Notes["web lint"] == "" {
		t.Fatalf("proposal %+v", prop)
	}
	gotSources := map[string]string{}
	for _, s := range prop.Profile.Sources {
		gotSources[s.Path] = s.SHA256
	}
	if len(gotSources["docker-compose.yml"]) != 64 || len(gotSources[".github/workflows/ci.yml"]) != 64 {
		t.Fatalf("sources not hashed (compose is added from services): %+v", prop.Profile.Sources)
	}
	// install steps run before the checks; per_run steps are left to the stack-aware runner
	if len(r.dryRuns) != 1 || len(r.dryRuns[0]) != 3 || r.dryRuns[0][0].Name != "web deps" {
		t.Fatalf("dry run checks: %+v", r.dryRuns)
	}
	kinds := ""
	for _, res := range prop.Results {
		kinds += res.Kind + ":" + res.Name + ":" + string(res.Outcome) + " "
	}
	if kinds != "setup:web deps:PASS check:api test:FAIL check:web lint:PASS " {
		t.Fatalf("results: %s", kinds)
	}
	if got := prop.Results[1].OutputTail; len(got) != checkOutputTail || !strings.HasSuffix(got, "boom") {
		t.Fatalf("output tail is %d bytes", len(got))
	}
	if !r.removed {
		t.Fatal("the clean checkout was not removed")
	}
	if len(r.usage.recs) != 1 || r.usage.recs[0].WOID != "" || r.usage.recs[0].Role != "checks" || r.usage.recs[0].UserID != "u1" || r.usage.recs[0].CostMicros != 9 {
		t.Fatalf("usage = %+v", r.usage.recs)
	}
}

func TestSuggestRetriesInvalidReplyOnce(t *testing.T) {
	bad := strings.Replace(goodReply, `"names":["postgres"]`, `"names":["postgres","kafka"]`, 1)
	r := newSuggestRig(t, bad, goodReply)
	if err := r.s.Start(context.Background(), &model.User{ID: "u1"}, "p1"); err != nil {
		t.Fatal(err)
	}
	st := r.wait(t)
	if st.Run.Status != "ready" {
		t.Fatalf("run = %+v", st.Run)
	}
	if len(r.prompts) != 2 || !strings.Contains(r.prompts[1], `service "kafka" is not in docker-compose.yml`) {
		t.Fatalf("the retry prompt does not carry the validation error: %q", r.prompts)
	}
	if len(r.usage.recs) != 2 {
		t.Fatalf("both attempts must be recorded, got %d", len(r.usage.recs))
	}

	r = newSuggestRig(t, bad, bad)
	_ = r.s.Start(context.Background(), &model.User{ID: "u1"}, "p1")
	st = r.wait(t)
	if st.Run.Status != "failed" || !strings.Contains(st.Run.Error, "invalid after retry") || st.Proposal != nil {
		t.Fatalf("run = %+v proposal = %v", st.Run, st.Proposal)
	}
	if len(r.dryRuns) != 0 {
		t.Fatal("an invalid suggestion was dry-run")
	}
}

func TestSuggestBusyAndDiscard(t *testing.T) {
	r := newSuggestRig(t, goodReply)
	u := &model.User{ID: "u1"}
	gate := make(chan struct{})
	agent := r.s.Agent
	r.s.Agent = func(ctx context.Context, a, b, c string) (string, model.AgentResult, error) {
		<-gate
		return agent(ctx, a, b, c)
	}
	if err := r.s.Start(context.Background(), u, "p1"); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Start(context.Background(), u, "p1"); !errors.Is(err, ErrCheckSuggestBusy) {
		t.Fatalf("second start: %v", err)
	}
	if err := r.s.Discard(context.Background(), u, "p1"); !errors.Is(err, ErrCheckSuggestBusy) {
		t.Fatalf("discard while running: %v", err)
	}
	if st, _ := r.s.Status(context.Background(), u, "p1"); st.Run.Status != "running" {
		t.Fatalf("status = %+v", st.Run)
	}
	close(gate)
	if st := r.wait(t); st.Proposal == nil {
		t.Fatal("no proposal")
	}
	if err := r.s.Discard(context.Background(), u, "p1"); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.s.Status(context.Background(), u, "p1"); st.Proposal != nil || st.Run.Status != "idle" {
		t.Fatalf("after discard: %+v", st)
	}
}

func TestSuggestRestartFailsRunning(t *testing.T) {
	r := newSuggestRig(t)
	if ok, err := r.s.Store.Begin(context.Background(), "p1"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := r.s.Store.FailRunning(context.Background(), "interrupted by restart"); err != nil {
		t.Fatal(err)
	}
	st := r.wait(t)
	if st.Run.Status != "failed" || st.Run.Error != "interrupted by restart" {
		t.Fatalf("run = %+v", st.Run)
	}
	if err := r.s.Start(context.Background(), &model.User{ID: "u1"}, "p1"); err != nil {
		t.Fatalf("a failed run must be restartable: %v", err)
	}
	r.wait(t)
}

func TestApproveStoresProfileAndFlagsStale(t *testing.T) {
	r := newSuggestRig(t, goodReply)
	u := &model.User{ID: "u1"}
	_ = r.s.Start(context.Background(), u, "p1")
	prop := r.wait(t).Proposal

	edited := prop.Profile
	edited.Checks[0].Optional = true
	got, err := r.s.Approve(context.Background(), u, "p1", edited)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || !got.Checks[0].Optional || len(got.Sources) != 2 || got.Sources[0].SHA256 == "" {
		t.Fatalf("approved %+v", got)
	}
	st, _ := r.s.Status(context.Background(), u, "p1")
	if st.Profile == nil || !st.Profile.Checks[0].Optional {
		t.Fatalf("stored profile %+v", st.Profile)
	}
	if st.Proposal != nil || st.Run.Status != "idle" {
		t.Fatalf("approving must clear the proposal: %+v", st)
	}
	if st.Stale {
		t.Fatal("fresh profile is stale")
	}

	if err := os.WriteFile(filepath.Join(r.repo, ".github/workflows/ci.yml"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ = r.s.Status(context.Background(), u, "p1"); !st.Stale {
		t.Fatal("a changed source did not mark the profile stale")
	}
}

func TestApproveRejectsInvalidProfile(t *testing.T) {
	r := newSuggestRig(t)
	p := validProfile()
	p.Env["TOKEN"] = "sk_live_123"
	if _, err := r.s.Approve(context.Background(), &model.User{ID: "u1"}, "p1", p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
	st, _ := r.s.Status(context.Background(), &model.User{ID: "u1"}, "p1")
	if st.Profile != nil {
		t.Fatal("an invalid profile was stored")
	}
}

func TestStatusV1ProfileAndEmpty(t *testing.T) {
	r := newSuggestRig(t)
	st, err := r.s.Status(context.Background(), &model.User{ID: "u1"}, "p1")
	if err != nil || st.Profile != nil || st.Proposal != nil || st.Run.Status != "idle" || st.Stale {
		t.Fatalf("empty status = %+v, %v", st, err)
	}
	if err := r.s.Proj.SetQualityProfile(context.Background(), "p1", `{"language":"go","setup":"go mod download","checks":[{"name":"vet","command":"go vet ./..."}]}`); err != nil {
		t.Fatal(err)
	}
	st, _ = r.s.Status(context.Background(), &model.User{ID: "u1"}, "p1")
	if st.Profile == nil || len(st.Profile.Checks) != 1 || st.Stale {
		t.Fatalf("v1 profile status = %+v", st)
	}
}
