package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

const checksReply = `{"version":2,"language":"go","sources":[{"path":"ci.yml"}],"checks":[{"name":"vet","command":"go vet ./...","source":"ci.yml:1"}]}`

func bootChecks(t *testing.T, reply string) (*Server, chan struct{}) {
	t.Helper()
	srv := bootChat(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "checks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	users, projRepo, members := &store.UserRepo{DB: db}, &store.ProjectRepo{DB: db}, &store.MemberRepo{DB: db}
	for _, id := range []string{"ana", "bob", "vic"} {
		role := model.RoleEngineer
		if id == "vic" {
			role = model.RoleViewer
		}
		_ = users.Create(context.Background(), model.User{ID: id, Email: id + "@t", Name: id, Role: role})
		_ = users.CreateSession(context.Background(), "sess-"+id, id, time.Now().Add(time.Hour))
	}
	_ = projRepo.Create(context.Background(), &model.Project{ID: "p1", Name: "p1"})
	_ = members.Add(context.Background(), "p1", "ana")
	_ = members.Add(context.Background(), "p1", "vic")
	srv.Auth = &usecase.Identity{Users: users, Mode: "dev"}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "ci.yml"), []byte("run: go vet ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	close(gate)
	srv.Checks = &usecase.CheckSuggester{
		Projects: &usecase.Projects{Store: projRepo, Members: members, Users: users}, Proj: projRepo,
		Store: &store.CheckProposalRepo{DB: db}, Log: slog.New(slog.NewTextHandler(testDiscard{}, nil)),
		Checkout:  func(context.Context, string) (string, string, func(), error) { return repo, "sha1", nil, nil },
		SourceDir: func(context.Context, string) (string, error) { return repo, nil },
		Agent: func(context.Context, string, string, string) (string, model.AgentResult, error) {
			<-gate
			return "mock/m", model.AgentResult{Text: reply}, nil
		},
		DryRun: func(_ context.Context, _, _, _ string, p model.Profile) ([]model.CheckResult, error) {
			zero := 0
			return []model.CheckResult{{Name: p.Checks[0].Name, Command: p.Checks[0].Command, Outcome: model.Pass, ExitCode: &zero}}, nil
		},
	}
	return srv, gate
}

func waitChecks(t *testing.T, srv *Server, want string) map[string]any {
	t.Helper()
	for range 200 {
		_, body := chatDo(srv, "ana", "GET", "/api/v1/projects/p1/checks", "")
		var st map[string]any
		_ = json.Unmarshal([]byte(body), &st)
		if st["run"].(map[string]any)["status"] == want {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run never reached %s", want)
	return nil
}

func TestChecksAPI(t *testing.T) {
	srv, _ := bootChecks(t, checksReply)

	if code, _ := chatDo(srv, "", "GET", "/api/v1/projects/p1/checks", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _ := chatDo(srv, "bob", "GET", "/api/v1/projects/p1/checks", ""); code != http.StatusNotFound {
		t.Fatalf("non-member read: %d", code)
	}
	if code, _ := chatDo(srv, "bob", "POST", "/api/v1/projects/p1/checks/suggest", ""); code != http.StatusNotFound {
		t.Fatalf("non-member suggest: %d", code)
	}
	if code, _ := chatDo(srv, "vic", "GET", "/api/v1/projects/p1/checks", ""); code != http.StatusOK {
		t.Fatalf("viewer read: %d", code)
	}
	for _, c := range []struct{ method, url string }{
		{"POST", "/api/v1/projects/p1/checks/suggest"},
		{"PUT", "/api/v1/projects/p1/checks"},
		{"DELETE", "/api/v1/projects/p1/checks/proposal"},
		{"POST", "/api/v1/projects/p1/checks/stop"},
	} {
		if code, _ := chatDo(srv, "vic", c.method, c.url, `{}`); code != http.StatusForbidden {
			t.Fatalf("viewer %s %s: %d", c.method, c.url, code)
		}
	}
	if code, _ := chatDo(srv, "ana", "GET", "/api/v1/projects/nope/checks", ""); code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", code)
	}

	code, body := chatDo(srv, "ana", "POST", "/api/v1/projects/p1/checks/suggest", "")
	if code != http.StatusAccepted || !strings.Contains(body, `"status":"running"`) {
		t.Fatalf("suggest: %d %s", code, body)
	}
	st := waitChecks(t, srv, "ready")
	prop := st["proposal"].(map[string]any)
	if prop["base_sha"] != "sha1" || len(prop["results"].([]any)) != 1 || st["profile"] != nil || st["stale"] != false {
		t.Fatalf("status = %v", st)
	}
	profile, _ := json.Marshal(map[string]any{"profile": prop["profile"]})

	if code, body = chatDo(srv, "ana", "PUT", "/api/v1/projects/p1/checks", `{"profile":{"checks":[{"name":"x","command":"y","dir":"../out"}]}}`); code != http.StatusBadRequest {
		t.Fatalf("invalid approve: %d %s", code, body)
	}
	if code, body = chatDo(srv, "ana", "PUT", "/api/v1/projects/p1/checks", string(profile)); code != http.StatusOK {
		t.Fatalf("approve: %d %s", code, body)
	}
	st = waitChecks(t, srv, "idle")
	if st["proposal"] != nil || st["profile"].(map[string]any)["checks"].([]any)[0].(map[string]any)["name"] != "vet" {
		t.Fatalf("after approve: %v", st)
	}
}

func TestChecksBusyAndDiscard(t *testing.T) {
	srv, _ := bootChecks(t, checksReply)
	gate := make(chan struct{})
	agent := srv.Checks.Agent
	srv.Checks.Agent = func(ctx context.Context, a, b, c string) (string, model.AgentResult, error) {
		<-gate
		return agent(ctx, a, b, c)
	}
	if code, _ := chatDo(srv, "ana", "POST", "/api/v1/projects/p1/checks/suggest", ""); code != http.StatusAccepted {
		t.Fatalf("start: %d", code)
	}
	if code, body := chatDo(srv, "ana", "POST", "/api/v1/projects/p1/checks/suggest", ""); code != http.StatusConflict || !strings.Contains(body, "CHECK_SUGGEST_RUNNING") {
		t.Fatalf("second start: %d %s", code, body)
	}
	if code, _ := chatDo(srv, "ana", "DELETE", "/api/v1/projects/p1/checks/proposal", ""); code != http.StatusConflict {
		t.Fatalf("discard while running: %d", code)
	}
	close(gate)
	waitChecks(t, srv, "ready")
	if code, _ := chatDo(srv, "ana", "DELETE", "/api/v1/projects/p1/checks/proposal", ""); code != http.StatusNoContent {
		t.Fatalf("discard: %d", code)
	}
	st := waitChecks(t, srv, "idle")
	if st["proposal"] != nil {
		t.Fatalf("proposal survived discard: %v", st)
	}
}

func TestChecksStopTakesServicesDown(t *testing.T) {
	srv, _ := bootChecks(t, checksReply)
	var stopped []string
	srv.Checks.StopStack = func(_ context.Context, id string) error { stopped = append(stopped, id); return nil }
	if code, _ := chatDo(srv, "bob", "POST", "/api/v1/projects/p1/checks/stop", ""); code != http.StatusNotFound {
		t.Fatalf("non-member: %d", code)
	}
	if code, _ := chatDo(srv, "ana", "POST", "/api/v1/projects/p1/checks/stop", ""); code != http.StatusNoContent || len(stopped) != 1 || stopped[0] != "p1" {
		t.Fatalf("stop: %d %v", code, stopped)
	}
}

func TestChecksUnavailable(t *testing.T) {
	srv, _ := bootChecks(t, checksReply)
	srv.Checks.Agent = nil
	if code, body := chatDo(srv, "ana", "POST", "/api/v1/projects/p1/checks/suggest", ""); code != http.StatusServiceUnavailable || !strings.Contains(body, "CHECK_SUGGEST_UNAVAILABLE") {
		t.Fatalf("no agent runtime: %d %s", code, body)
	}
	srv.Checks = nil
	if code, _ := chatDo(srv, "ana", "GET", "/api/v1/projects/p1/checks", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("not wired: %d", code)
	}
}

func TestChecksFailedRunReportsError(t *testing.T) {
	srv, _ := bootChecks(t, `not json at all`)
	if code, _ := chatDo(srv, "ana", "POST", "/api/v1/projects/p1/checks/suggest", ""); code != http.StatusAccepted {
		t.Fatal(code)
	}
	st := waitChecks(t, srv, "failed")
	if !strings.Contains(st["run"].(map[string]any)["error"].(string), "invalid after retry") {
		t.Fatalf("run = %v", st["run"])
	}
}
