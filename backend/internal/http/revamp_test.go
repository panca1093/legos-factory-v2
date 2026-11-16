package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

func bootRevamp(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	users := &store.UserRepo{DB: db}
	for id, role := range map[string]model.Role{"eng": model.RoleEngineer, "eng2": model.RoleEngineer, "adm": model.RoleAdmin} {
		if err := users.Create(context.Background(), model.User{ID: id, Email: id + "@t", Name: id, Role: role}); err != nil {
			t.Fatal(err)
		}
		if err := users.CreateSession(context.Background(), "sess-"+id, id, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(&model.Config{FrontendOrigin: "http://localhost:5173"}, slog.New(slog.NewTextHandler(testDiscard{}, nil)))
	srv.Auth = &usecase.Identity{Users: users, Mode: "dev"}
	srv.Settings = &store.SettingsRepo{DB: db}
	srv.Usage = usecase.NewUsageTracker(&store.UsageRepo{DB: db})
	srv.Agents = usecase.NewAgents(&store.AgentAccountRepo{DB: db}, t.TempDir())
	srv.Agents.LookPath = func(string) (string, error) { return "/bin/x", nil }
	srv.Agents.Run = func(_ context.Context, _ []string, name string, _ ...string) ([]byte, error) {
		if name == "claude" {
			return []byte(`{"loggedIn":false}`), nil
		}
		return []byte("Not logged in"), nil
	}
	srv.Agents.CodexLimits = func(context.Context, string) (usecase.CodexLimits, error) { return usecase.CodexLimits{}, nil }
	srv.Agents.Keychain = func(context.Context, string) (string, error) { return "", errors.New("no keychain in tests") }
	projects := &usecase.Projects{Store: &store.ProjectRepo{DB: db}, Members: &store.MemberRepo{DB: db}, Users: users}
	srv.Projects = projects
	srv.Integrations = &usecase.Integrations{Store: &store.ConnectionRepo{DB: db}, Projects: projects}
	return srv, db
}

func TestAgentEndpointsArePerUser(t *testing.T) {
	srv, _ := bootRevamp(t)
	code, body := chatDo(srv, "eng", "POST", "/api/v1/agents/claude/accounts", `{"label":"Work"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var created struct {
		Account      struct{ ID string }
		LoginCommand string `json:"login_command"`
		EnvHint      string `json:"env_hint"`
	}
	_ = json.Unmarshal([]byte(body), &created)
	if !strings.Contains(created.LoginCommand, "claude auth login") || !strings.HasPrefix(created.EnvHint, "CLAUDE_CONFIG_DIR=") {
		t.Fatalf("login: %s", body)
	}
	if code, _ := chatDo(srv, "eng2", "POST", "/api/v1/agents/claude/accounts/"+created.Account.ID+"/activate", ""); code != http.StatusNotFound {
		t.Fatalf("another user's account: %d", code)
	}
	if code, _ := chatDo(srv, "eng", "POST", "/api/v1/agents/claude/accounts/"+created.Account.ID+"/activate", ""); code != 200 {
		t.Fatalf("activate: %d", code)
	}
	_, body = chatDo(srv, "eng", "GET", "/api/v1/agents", "")
	var list struct {
		Agents []struct {
			ID           string
			Installed    bool
			RolesAllowed []string `json:"roles_allowed"`
			Accounts     []struct {
				ID     string
				System bool
				Active bool
				Status string
			}
		}
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list.Agents) != 3 {
		t.Fatalf("list: %s", body)
	}
	cl := list.Agents[0]
	if cl.ID != "claude" || len(cl.Accounts) != 2 || !cl.Accounts[1].Active || cl.Accounts[0].Status != "signed_out" || !cl.Accounts[0].System {
		t.Fatalf("claude: %+v", cl)
	}
	_, body = chatDo(srv, "eng2", "GET", "/api/v1/agents", "")
	if strings.Contains(body, created.Account.ID) {
		t.Fatal("accounts leak across users")
	}
	if code, _ := chatDo(srv, "eng", "DELETE", "/api/v1/agents/claude/accounts/system", ""); code != http.StatusBadRequest {
		t.Fatalf("system delete: %d", code)
	}
	if code, _ := chatDo(srv, "eng", "DELETE", "/api/v1/agents/claude/accounts/"+created.Account.ID, ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := chatDo(srv, "eng", "POST", "/api/v1/agents/claude/enabled", `{"enabled":false}`); code != http.StatusForbidden {
		t.Fatalf("enabled needs admin: %d", code)
	}
	if code, _ := chatDo(srv, "adm", "POST", "/api/v1/agents/codex/enabled", `{"enabled":false}`); code != 200 {
		t.Fatalf("enabled: %d", code)
	}
	if code, _ := chatDo(srv, "", "GET", "/api/v1/agents", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _ := chatDo(srv, "eng", "GET", "/api/v1/models?agent=nope", ""); code != http.StatusBadRequest && code != http.StatusServiceUnavailable {
		t.Fatalf("unknown agent models: %d", code)
	}
}

func TestConnectionsGroupedPerProvider(t *testing.T) {
	srv, db := bootRevamp(t)
	ctx := context.Background()
	conns := &store.ConnectionRepo{DB: db}
	gh := &model.Connection{Provider: model.ProviderGitHub, Name: "Acme", Scope: model.ScopeShared, OwnerID: "adm"}
	gh2 := &model.Connection{Provider: model.ProviderGitHub, Name: "Personal", Scope: model.ScopeShared, OwnerID: "adm"}
	jira := &model.Connection{Provider: model.ProviderJira, Name: "Jira", Scope: model.ScopeShared, OwnerID: "adm"}
	for _, c := range []*model.Connection{gh, gh2, jira} {
		if err := conns.Create(ctx, c, []byte("sealed")); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&store.ProjectRepo{DB: db}).Create(ctx, &model.Project{ID: "p1", Name: "Shop", GitConnection: gh.ID, RepoURL: "x"}); err != nil {
		t.Fatal(err)
	}
	code, body := chatDo(srv, "adm", "GET", "/api/v1/connections", "")
	if code != 200 || strings.Contains(body, "sealed") {
		t.Fatalf("%d %s", code, body)
	}
	var got struct {
		Providers []struct {
			ID       string
			Group    string
			Accounts []struct {
				ID     string
				UsedBy []struct {
					ProjectID string `json:"project_id"`
					Name      string
				} `json:"used_by"`
			}
		}
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || len(got.Providers) != 4 {
		t.Fatalf("%s", body)
	}
	ids := []string{}
	for _, p := range got.Providers {
		ids = append(ids, p.ID+":"+p.Group)
	}
	if strings.Join(ids, ",") != "github:code,gitlab:code,bitbucket:code,jira:work" {
		t.Fatalf("providers %v", ids)
	}
	if len(got.Providers[0].Accounts) != 2 || len(got.Providers[1].Accounts) != 0 || len(got.Providers[3].Accounts) != 1 {
		t.Fatalf("accounts: %s", body)
	}
	if u := got.Providers[0].Accounts[0].UsedBy; got.Providers[0].Accounts[0].ID != gh.ID || len(u) != 1 || u[0].ProjectID != "p1" || u[0].Name != "Shop" {
		t.Fatalf("used_by: %s", body)
	}
	if !strings.Contains(body, `"used_by":[]`) || strings.Contains(body, `"used_by":null`) || strings.Contains(body, `"accounts":null`) {
		t.Fatalf("empty lists must be []: %s", body)
	}
}

func TestContractTestsFeatureIsGone(t *testing.T) {
	srv, db := bootRevamp(t)
	if code, _ := chatDo(srv, "eng", "PUT", "/api/v1/projects/p1/contract-tests", `{"contract_tests":"on"}`); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Fatalf("route must be gone: %d", code)
	}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('projects')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		if name == "contract_tests" {
			t.Fatal("projects.contract_tests column must be dropped")
		}
	}
	// events written by the old red-proof logic must still be readable
	wos := &store.WorkOrderRepo{DB: db}
	_ = (&store.ProjectRepo{DB: db}).Create(context.Background(), &model.Project{ID: "p1", Name: "P", RepoURL: "x"})
	wo := &model.WorkOrder{ID: "wo1", ProjectID: "p1", Title: "t", State: model.StateCreated, CreatedBy: "adm"}
	if err := wos.Create(context.Background(), wo); err != nil {
		t.Fatal(err)
	}
	if _, err := wos.AppendEvent(context.Background(), "wo1", "builder", "red_proof", "Red proof passed", map[string]any{"locked": []string{"a_test.go"}}); err != nil {
		t.Fatal(err)
	}
	if code, body := chatDo(srv, "adm", "GET", "/api/v1/projects", ""); code != 200 || strings.Contains(body, "contract_tests") {
		t.Fatalf("%d %s", code, body)
	}
	evs, _, err := wos.LatestEvents(context.Background(), "wo1", 10)
	if err != nil || len(evs) == 0 {
		t.Fatalf("old events unreadable: %v", err)
	}
}
