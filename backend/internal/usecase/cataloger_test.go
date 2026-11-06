package usecase

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type fixture struct {
	projects *Projects
	users    *store.UserRepo
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	proj := &store.ProjectRepo{DB: db}
	users := &store.UserRepo{DB: db}
	ctx := context.Background()
	for _, id := range []string{"p1", "p2"} {
		if err := proj.Create(ctx, &model.Project{ID: id, Name: id, RepoURL: "/repos/" + id}); err != nil {
			t.Fatal(err)
		}
	}
	return fixture{
		projects: &Projects{Store: proj, Members: &store.MemberRepo{DB: db}, Users: users},
		users:    users,
	}
}

func newLogCataloger(t *testing.T) (*Cataloger, *kb.Bundle) {
	t.Helper()
	bundle, err := kb.OpenBundle(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	return &Cataloger{Bundle: bundle, Log: slog.Default()}, bundle
}

func readLog(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRecordAppendsOneLineToProjectLog(t *testing.T) {
	cat, bundle := newLogCataloger(t)
	ctx := context.Background()
	wo := &model.WorkOrder{ID: "wo_c6680cdd-92d", ProjectID: "p1", Title: "t"}
	long := strings.Repeat("word ", 200)

	if id := cat.Record(ctx, wo, model.KnowledgePRDecision, model.TrustHumanDecision, "human", `PR approved: "t"`, "Reason: ok\nsecond line "+long+" password=hunter2"); id != "" {
		t.Fatalf("Record returns no id any more, got %q", id)
	}
	cat.Record(ctx, wo, model.KnowledgeFindings, model.TrustAIInterpretation, "inspector", "6 finding(s)", "")

	got := readLog(t, bundle.Root(), "projects/p1/log.md")
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if lines[0] != "# Log" || !regexp.MustCompile(`^## \d{4}-\d{2}-\d{2}$`).MatchString(lines[2]) || len(lines) != 6 {
		t.Fatalf("log shape:\n%s", got)
	}
	if !regexp.MustCompile("^\\* \\d{2}:\\d{2} `wo_c6680cdd-92d` \\*\\*findings\\*\\* \\(inspector\\) — 6 finding\\(s\\)$").MatchString(lines[4]) {
		t.Fatalf("newest line first: %q", lines[4])
	}
	pr := lines[5]
	if !strings.Contains(pr, "**pr_decision** (human) — PR approved: \"t\" — Reason: ok second line word") || strings.Contains(pr, "hunter2") || strings.Contains(pr, "\n") {
		t.Fatalf("pr line: %q", pr)
	}
	if body := pr[strings.LastIndex(pr, " — ")+len(" — "):]; len(body) > maxLogBody {
		t.Fatalf("body not clipped: %d bytes", len(body))
	}
	entries, _ := os.ReadDir(filepath.Join(bundle.Root(), "projects", "p1"))
	if len(entries) != 1 {
		t.Fatalf("no record, kn_ file or index may be written beside log.md: %v", entries)
	}
}

func TestRecordFailureIsNotPropagated(t *testing.T) {
	cat, _ := newLogCataloger(t)
	cat.Record(context.Background(), &model.WorkOrder{ID: "wo", ProjectID: "../escape"}, "k", model.TrustVerified, "x", "t", "b")
}

func TestNilCatalogerIsNoop(t *testing.T) {
	var c *Cataloger
	wo := &model.WorkOrder{ProjectID: "p1"}
	if c.Record(context.Background(), wo, "k", model.TrustVerified, "x", "t", "b") != "" || c.Relevant(context.Background(), wo) != nil {
		t.Fatal("nil cataloger must be a no-op")
	}
}

func TestSanitizeRedactsSecretsAndBoundsSize(t *testing.T) {
	in := `deploy with API_KEY=abc123 and password: "hunter2" token ghp_abcdefghijklmnopqrstuvwxyz012345
-----BEGIN RSA PRIVATE KEY-----
MIIEow
-----END RSA PRIVATE KEY-----`
	out := sanitize(in, 2000)
	for _, leak := range []string{"abc123", "hunter2", "ghp_abcdefghijklmnopqrstuvwxyz012345", "MIIEow"} {
		if strings.Contains(out, leak) {
			t.Fatalf("secret %q leaked: %s", leak, out)
		}
	}
	long := sanitize(strings.Repeat("é", 3000), 101)
	if len(long) > 101+len("… [truncated]") || !strings.HasSuffix(long, "[truncated]") || !strings.HasPrefix(long, "é") {
		t.Fatalf("bad truncation: %q", long[:20])
	}
	if strings.ContainsRune(long[:len(long)-len("… [truncated]")], '�') {
		t.Fatal("truncation split a UTF-8 rune")
	}
}

func TestProjectAccessRule(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	eng := model.User{ID: "u_eng", Email: "eng@t", Name: "Eng", Role: model.RoleEngineer}
	admin := model.User{ID: "u_adm", Email: "adm@t", Name: "Adm", Role: model.RoleAdmin}
	for _, u := range []model.User{eng, admin} {
		if err := fx.users.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := fx.projects.Authorize(ctx, &eng, "p1"); !IsNotFound(err) {
		t.Fatalf("non-member must get not-found, got %v", err)
	}
	if err := fx.projects.Authorize(ctx, &admin, "p1"); err != nil {
		t.Fatalf("admin reaches every project: %v", err)
	}
	if err := fx.projects.AddMember(ctx, "p1", "eng@t"); err != nil {
		t.Fatal(err)
	}
	list, err := fx.projects.List(ctx, &eng)
	if err != nil || len(list) != 1 || list[0].ID != "p1" {
		t.Fatalf("member sees only p1: %+v %v", list, err)
	}
	if scope, _ := fx.projects.Scope(ctx, &admin); scope != nil {
		t.Fatalf("admin scope must be unrestricted, got %v", scope)
	}
	created := &model.Project{Name: "mine"}
	if err := fx.projects.Create(ctx, &eng, created); err != nil {
		t.Fatal(err)
	}
	if err := fx.projects.Authorize(ctx, &eng, created.ID); err != nil {
		t.Fatalf("creator must be a member: %v", err)
	}
	if err := fx.projects.AddMember(ctx, "p1", "nobody@t"); !IsNotFound(err) {
		t.Fatalf("unknown email: %v", err)
	}
}
