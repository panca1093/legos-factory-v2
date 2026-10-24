package usecase

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type fixture struct {
	cat      *Cataloger
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
		cat:      &Cataloger{Knowledge: &store.KnowledgeRepo{DB: db}, Proj: proj, Settings: &store.SettingsRepo{DB: db}, Log: slog.Default()},
		projects: &Projects{Store: proj, Members: &store.MemberRepo{DB: db}, Users: users},
		users:    users,
	}
}

func TestRelevantStaysInProjectAndSkipsOwnWorkOrder(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	// prior records carry no WOID (nullable FK) so no work_orders rows are needed
	prev := &model.WorkOrder{ProjectID: "p1", Title: "Payments retries", CommitSHA: "abc123"}
	other := &model.WorkOrder{ProjectID: "p2", Title: "Payments elsewhere"}
	cur := &model.WorkOrder{ID: "wo_cur", ProjectID: "p1", Title: "Payments retries v2", Objective: "extend payments retries"}
	fx.cat.Record(ctx, prev, model.KnowledgeClarification, model.TrustHumanDecision, "human", "Payments retry window", "24 hours")
	fx.cat.Record(ctx, other, model.KnowledgeClarification, model.TrustHumanDecision, "human", "Payments in p2", "secret project")

	got := fx.cat.Relevant(ctx, cur)
	if len(got) != 1 || got[0].ProjectID != "p1" || got[0].CommitSHA != "abc123" || got[0].RepoURL != "/repos/p1" {
		t.Fatalf("expected the single p1 record with commit and repo links, got %+v", got)
	}
	if !strings.Contains(knowledgePrompt(got), "[human_decision] Payments retry window") {
		t.Fatalf("prompt should label trust: %s", knowledgePrompt(got))
	}
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
