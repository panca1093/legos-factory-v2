package http

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/sandbox"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

func TestWorkOrderDiffEndpoint(t *testing.T) {
	srv := bootChat(t)
	ctx := context.Background()
	db := srv.Chat.Store.(*store.ChatRepo).DB
	projRepo, members := &store.ProjectRepo{DB: db}, &store.MemberRepo{DB: db}
	users := &store.UserRepo{DB: db}
	if err := projRepo.Create(ctx, &model.Project{ID: "p2", Name: "p2"}); err != nil {
		t.Fatal(err)
	}
	wos := &store.WorkOrderRepo{DB: db}
	for id, proj := range map[string]string{"wo_mine": "p1", "wo_other": "p2"} {
		if err := wos.Create(ctx, &model.WorkOrder{ID: id, ProjectID: proj, Title: "t", Objective: "o", State: model.StateReady, CreatedBy: "ana"}); err != nil {
			t.Fatal(err)
		}
	}
	srv.WORepo = wos
	srv.Projects = &usecase.Projects{Store: projRepo, Members: members, Users: users}
	data := t.TempDir()
	srv.Factory = &usecase.Factory{DataDir: data, Sandbox: sandbox.Local{DataDir: data}}

	ws := filepath.Join(data, "workspaces", "wo_mine")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = ws
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "base")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	if err := os.WriteFile(filepath.Join(ws, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "wo")

	code, body := chatDo(srv, "ana", "GET", "/api/v1/work-orders/wo_mine/diff", "")
	var d model.WODiff
	if code != http.StatusOK || json.Unmarshal([]byte(body), &d) != nil || !d.Available || len(d.Files) != 1 || !strings.Contains(d.Files[0].Patch, "+hi") {
		t.Fatalf("mine: %d %s", code, body)
	}
	if code, _ := chatDo(srv, "ana", "GET", "/api/v1/work-orders/wo_other/diff", ""); code != http.StatusNotFound {
		t.Fatalf("another project's WO: %d", code)
	}
	if code, _ := chatDo(srv, "bob", "GET", "/api/v1/work-orders/wo_mine/diff", ""); code != http.StatusNotFound {
		t.Fatalf("non-member: %d", code)
	}
}
