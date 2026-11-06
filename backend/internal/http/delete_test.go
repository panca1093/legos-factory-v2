package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

type jobQueue struct{ *store.JobRepo }

func (jobQueue) Enqueue(context.Context, string, string, string, map[string]any) (string, error) {
	return "", nil
}

type brokenMembers struct{ usecase.MemberStore }

func (brokenMembers) IsMember(context.Context, string, string) (bool, error) {
	return false, errors.New("db down")
}

func bootDelete(t *testing.T) (*Server, *store.WorkOrderRepo, string) {
	t.Helper()
	srv := bootChat(t)
	ctx := context.Background()
	db := srv.Chat.Store.(*store.ChatRepo).DB
	projRepo, members := &store.ProjectRepo{DB: db}, &store.MemberRepo{DB: db}
	if err := members.Add(ctx, "p1", "vic"); err != nil {
		t.Fatal(err)
	}
	wos := &store.WorkOrderRepo{DB: db}
	srv.WORepo = wos
	srv.Projects = &usecase.Projects{Store: projRepo, Members: members, Users: &store.UserRepo{DB: db}}
	data := t.TempDir()
	srv.Factory = &usecase.Factory{WO: wos, Proj: projRepo, Sched: jobQueue{&store.JobRepo{DB: db}}, DataDir: data, Log: slog.Default()}
	return srv, wos, data
}

func mkWO(t *testing.T, wos *store.WorkOrderRepo, data, id, project string, state model.State) string {
	t.Helper()
	if err := wos.Create(context.Background(), &model.WorkOrder{ID: id, ProjectID: project, Title: "t", Objective: "o", State: state, CreatedBy: "ana"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(data, "workspaces", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func present(p string) bool { _, err := os.Stat(p); return err == nil }

func TestDeleteWorkOrderEndpoint(t *testing.T) {
	srv, wos, data := bootDelete(t)
	ctx := context.Background()
	done := mkWO(t, wos, data, "wo_done", "p1", model.StateCancelled)
	active := mkWO(t, wos, data, "wo_active", "p1", model.StateImplementing)
	openPR := mkWO(t, wos, data, "wo_pr", "p1", model.StateReady)
	if err := wos.UpdateFields(ctx, "wo_pr", map[string]any{"pr_ref": "acme/app#7"}); err != nil {
		t.Fatal(err)
	}

	if code, _ := chatDo(srv, "", "DELETE", "/api/v1/work-orders/wo_done", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _ := chatDo(srv, "vic", "DELETE", "/api/v1/work-orders/wo_done", ""); code != http.StatusForbidden {
		t.Fatalf("viewer: %d", code)
	}
	if code, _ := chatDo(srv, "bob", "DELETE", "/api/v1/work-orders/wo_done", ""); code != http.StatusNotFound {
		t.Fatalf("non-member: %d", code)
	}
	if code, _ := chatDo(srv, "ana", "DELETE", "/api/v1/work-orders/nope", ""); code != http.StatusNotFound {
		t.Fatalf("unknown: %d", code)
	}
	for id, dir := range map[string]string{"wo_active": active, "wo_pr": openPR} {
		if code, body := chatDo(srv, "ana", "DELETE", "/api/v1/work-orders/"+id, ""); code != http.StatusConflict {
			t.Fatalf("%s: %d %s", id, code, body)
		}
		if _, err := wos.Get(ctx, id); err != nil || !present(dir) {
			t.Fatalf("%s was touched", id)
		}
	}
	if code, body := chatDo(srv, "ana", "DELETE", "/api/v1/work-orders/wo_done", ""); code != http.StatusNoContent || body != "" {
		t.Fatalf("terminal: %d %s", code, body)
	}
	if _, err := wos.Get(ctx, "wo_done"); err == nil || present(done) {
		t.Fatal("terminal order or workspace survived")
	}
}

func TestDeleteProjectEndpoint(t *testing.T) {
	srv, wos, data := bootDelete(t)
	ctx := context.Background()
	projRepo := srv.Projects.Store
	folder := t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "keep.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := projRepo.Create(ctx, &model.Project{ID: "p_local", Name: "local", RepoURL: folder}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Projects.Members.Add(ctx, "p_local", "ana"); err != nil {
		t.Fatal(err)
	}
	done := mkWO(t, wos, data, "wo_done", "p_local", model.StateCancelled)
	busy := mkWO(t, wos, data, "wo_busy", "p_local", model.StateQueued)

	if code, _ := chatDo(srv, "vic", "DELETE", "/api/v1/projects/p_local", ""); code != http.StatusForbidden {
		t.Fatalf("viewer: %d", code)
	}
	if code, _ := chatDo(srv, "bob", "DELETE", "/api/v1/projects/p_local", ""); code != http.StatusNotFound {
		t.Fatalf("non-member: %d", code)
	}
	if code, _ := chatDo(srv, "ana", "DELETE", "/api/v1/projects/nope", ""); code != http.StatusNotFound {
		t.Fatalf("unknown: %d", code)
	}
	if code, body := chatDo(srv, "ana", "DELETE", "/api/v1/projects/p_local", ""); code != http.StatusConflict {
		t.Fatalf("active order: %d %s", code, body)
	}
	if _, err := projRepo.Get(ctx, "p_local"); err != nil || !present(done) || !present(busy) {
		t.Fatal("refused delete removed something")
	}

	if err := wos.UpdateFields(ctx, "wo_busy", map[string]any{"state": string(model.StateCancelled)}); err != nil {
		t.Fatal(err)
	}
	if code, body := chatDo(srv, "ana", "DELETE", "/api/v1/projects/p_local", ""); code != http.StatusNoContent || body != "" {
		t.Fatalf("terminal only: %d %s", code, body)
	}
	if _, err := projRepo.Get(ctx, "p_local"); err == nil {
		t.Fatal("project survived")
	}
	for _, id := range []string{"wo_done", "wo_busy"} {
		if _, err := wos.Get(ctx, id); err == nil {
			t.Fatalf("%s survived", id)
		}
	}
	if present(done) || present(busy) {
		t.Fatal("workspaces survived")
	}
	if b, err := os.ReadFile(filepath.Join(folder, "keep.txt")); err != nil || string(b) != "mine" {
		t.Fatal("the user's own folder was touched")
	}
}

func TestDeleteProjectAccessCheckFailureIs500(t *testing.T) {
	srv, _, _ := bootDelete(t)
	srv.Projects.Members = brokenMembers{srv.Projects.Members}
	if code, body := chatDo(srv, "ana", "DELETE", "/api/v1/projects/p1", ""); code != http.StatusInternalServerError {
		t.Fatalf("db error: %d %s", code, body)
	}
}
