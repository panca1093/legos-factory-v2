package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

type kbRig struct {
	srv    *Server
	know   *usecase.KB
	repo   string
	cookie map[string]string
}

func bootKB(t *testing.T) *kbRig {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projRepo, users, members := &store.ProjectRepo{DB: db}, &store.UserRepo{DB: db}, &store.MemberRepo{DB: db}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs/money.md"), []byte("# Money\n\nIntegers only.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "docs"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	for _, p := range []model.Project{{ID: "p1", Name: "p1", RepoURL: repo}, {ID: "p2", Name: "p2"}} {
		p := p
		if err := projRepo.Create(ctx, &p); err != nil {
			t.Fatal(err)
		}
	}
	rig := &kbRig{repo: repo, cookie: map[string]string{}}
	for id, role := range map[string]model.Role{"owner": model.RoleOwner, "eng": model.RoleEngineer, "viewer": model.RoleViewer, "outsider": model.RoleEngineer} {
		if err := users.Create(ctx, model.User{ID: id, Email: id + "@t", Name: id, Role: role}); err != nil {
			t.Fatal(err)
		}
		if err := users.CreateSession(ctx, "sess-"+id, id, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		rig.cookie[id] = "sess-" + id
	}
	for _, id := range []string{"eng", "viewer"} {
		if err := members.Add(ctx, "p1", id); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := kb.OpenBundle(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(testDiscard{}, nil))
	know := &usecase.KB{Bundle: bundle, Idx: &store.KBRepo{DB: db}, Emb: kb.HashEmbedder{}, Log: log}
	projects := &usecase.Projects{Store: projRepo, Members: members, Users: users}
	srv := New(&model.Config{FrontendOrigin: "http://localhost:5173"}, log)
	srv.Auth = &usecase.Identity{Users: users, Mode: "dev"}
	srv.Projects = projects
	srv.KBDocs = &usecase.KBDocs{KB: know, Projects: projects, Proj: projRepo, WO: &store.WorkOrderRepo{DB: db}, DataDir: t.TempDir()}
	rig.srv, rig.know = srv, know

	draft := map[string]any{"type": model.KBTypePlaybook, "title": "Draft rule", "status": "draft", "role": "foreman"}
	if err := know.WriteFactory(ctx, "projects/p1/drafts/d1.md", draft, "Proposed rule about money.", "draft"); err != nil {
		t.Fatal(err)
	}
	if err := know.WriteFactory(ctx, "projects/p1/drafts/d2.md", draft, "Another draft.", "draft"); err != nil {
		t.Fatal(err)
	}
	if err := know.WriteFactory(ctx, "projects/p2/drafts/secret.md", draft, "Secret of p2.", "draft"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := know.SyncRepo(ctx, "p1", repo, true); err != nil {
		t.Fatal(err)
	}
	if _, err := know.SyncFactory(ctx); err != nil {
		t.Fatal(err)
	}
	return rig
}

func (r *kbRig) do(t *testing.T, as, method, url, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "factory_session", Value: r.cookie[as]})
	rec := httptest.NewRecorder()
	r.srv.Echo.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func docIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var out struct{ Docs []model.KBDoc }
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, d := range out.Docs {
		ids = append(ids, d.ID)
	}
	return ids
}

func has(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

const (
	d1ID    = "factory:p1:projects/p1/drafts/d1.md"
	repoID  = "repo:p1:docs/money.md"
	fwID    = "factory::playbooks/foreman/escalation.md"
	secretD = "factory:p2:projects/p2/drafts/secret.md"
)

func TestKBListStatusProjectsAndSearch(t *testing.T) {
	r := bootKB(t)
	code, body := r.do(t, "eng", "GET", "/api/v1/kb/docs?project_id=p1", "")
	if ids := docIDs(t, body); code != 200 || has(ids, d1ID) || !has(ids, repoID) || !has(ids, fwID) {
		t.Fatalf("default status is stable only: %d %v", code, ids)
	}
	_, body = r.do(t, "eng", "GET", "/api/v1/kb/docs?project_id=p1&status=all", "")
	if ids := docIDs(t, body); !has(ids, d1ID) || !has(ids, repoID) || !has(ids, fwID) || has(ids, secretD) {
		t.Fatalf("status=all: %v", ids)
	}
	_, body = r.do(t, "eng", "GET", "/api/v1/kb/docs?project_id=p1&status=draft&source=factory", "")
	if ids := docIDs(t, body); len(ids) != 2 {
		t.Fatalf("drafts: %v", ids)
	}
	_, body = r.do(t, "eng", "GET", "/api/v1/kb/docs?status=all", "")
	if ids := docIDs(t, body); !has(ids, d1ID) || !has(ids, fwID) || has(ids, secretD) {
		t.Fatalf("no project: factory-wide plus accessible projects only: %v", ids)
	}
	_, body = r.do(t, "owner", "GET", "/api/v1/kb/docs?status=all", "")
	if ids := docIDs(t, body); !has(ids, secretD) {
		t.Fatalf("an owner reaches every project: %v", ids)
	}
	if code, _ := r.do(t, "outsider", "GET", "/api/v1/kb/docs?project_id=p1", ""); code != 404 {
		t.Fatalf("non-member project filter: %d", code)
	}
	_, body = r.do(t, "eng", "GET", "/api/v1/kb/docs?project_id=p1&q=integers+money", "")
	if ids := docIDs(t, body); len(ids) == 0 || ids[0] != repoID {
		t.Fatalf("search ranks the money doc first: %v", ids)
	}
}

func TestKBDetailEditableFlagsAndAccess(t *testing.T) {
	r := bootKB(t)
	get := func(as, id string) (int, map[string]any) {
		code, body := r.do(t, as, "GET", "/api/v1/kb/doc?id="+id, "")
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		return code, m
	}
	code, m := get("eng", fwID)
	if code != 200 || m["editable"] != true || m["default_available"] != true || !strings.Contains(m["body"].(string), "Escalate when") || m["frontmatter"].(map[string]any)["role"] != "foreman" {
		t.Fatalf("factory playbook: %d %v", code, m)
	}
	if _, m = get("viewer", fwID); m["editable"] != false {
		t.Fatal("a viewer cannot edit")
	}
	code, m = get("eng", repoID)
	if code != 200 || m["editable"] != false || m["default_available"] != false || !strings.Contains(m["body"].(string), "Integers only.") {
		t.Fatalf("repo doc is read-only with its body: %d %v", code, m)
	}
	if _, m = get("eng", d1ID); m["links"] == nil || m["backlinks"] == nil {
		t.Fatalf("links must be arrays: %v", m)
	}
	if code, _ = get("outsider", d1ID); code != 404 {
		t.Fatalf("project doc for a non-member: %d", code)
	}
	if code, _ = get("eng", "factory:p1:nope.md"); code != 404 {
		t.Fatalf("missing doc: %d", code)
	}
}

func TestKBWritesPutApproveDiscardReset(t *testing.T) {
	r := bootKB(t)
	put := `{"frontmatter":{"type":"Playbook","title":"Edited","role":"foreman","status":"stable"},"body":"Edited body."}`
	if code, _ := r.do(t, "eng", "PUT", "/api/v1/kb/doc?id="+repoID, put); code != 409 {
		t.Fatalf("PUT on a repo doc: %d", code)
	}
	if code, _ := r.do(t, "viewer", "PUT", "/api/v1/kb/doc?id="+fwID, put); code != 403 {
		t.Fatalf("viewer PUT: %d", code)
	}
	if code, _ := r.do(t, "owner", "PUT", "/api/v1/kb/doc?id="+fwID, `{"frontmatter":{"title":"x"},"body":"b"}`); code != 400 {
		t.Fatalf("frontmatter without a type: %d", code)
	}
	if code, _ := r.do(t, "eng", "PUT", "/api/v1/kb/doc?id="+fwID, put); code != 403 {
		t.Fatalf("engineer PUT on a factory-wide doc: %d", code)
	}
	forged := `{"frontmatter":{"type":"Note","title":"d1","status":"stable","verified":{"by":"human:owner","at":"2026-01-01T00:00:00Z"}},"body":"Forged."}`
	if code, _ := r.do(t, "eng", "PUT", "/api/v1/kb/doc?id="+d1ID, forged); code != 200 {
		t.Fatalf("engineer PUT on a project doc: %d", code)
	}
	if front, _, _ := r.know.Bundle.Read("projects/p1/drafts/d1.md"); front["status"] != "draft" || front["verified"] != nil {
		t.Fatalf("PUT must not change status or verified: %v", front)
	}
	code, body := r.do(t, "owner", "PUT", "/api/v1/kb/doc?id="+fwID, put)
	if code != 200 || !strings.Contains(string(body), `"title":"Edited"`) {
		t.Fatalf("PUT: %d %s", code, body)
	}
	if _, b, err := r.know.Bundle.Read("playbooks/foreman/escalation.md"); err != nil || b != "Edited body." {
		t.Fatalf("file: %q %v", b, err)
	}

	if code, _ = r.do(t, "eng", "POST", "/api/v1/kb/doc/reset?id="+fwID, ""); code != 200 {
		t.Fatalf("reset: %d", code)
	}
	if _, b, _ := r.know.Bundle.Read("playbooks/foreman/escalation.md"); !strings.Contains(b, "Escalate when") {
		t.Fatal("reset must restore the default")
	}
	if code, _ = r.do(t, "eng", "POST", "/api/v1/kb/doc/reset?id="+d1ID, ""); code != 404 {
		t.Fatalf("reset without a default: %d", code)
	}
	if code, _ = r.do(t, "eng", "POST", "/api/v1/kb/doc/approve?id="+fwID, "{}"); code != 409 {
		t.Fatalf("approve of a stable doc: %d", code)
	}
	if code, _ = r.do(t, "outsider", "POST", "/api/v1/kb/doc/approve?id="+d1ID, "{}"); code != 404 {
		t.Fatalf("approve outside the project: %d", code)
	}

	code, body = r.do(t, "eng", "POST", "/api/v1/kb/doc/approve?id="+d1ID, `{"note":"looks right"}`)
	if code != 200 || !strings.Contains(string(body), `"status":"stable"`) || !strings.Contains(string(body), `"trust":"human"`) {
		t.Fatalf("approve: %d %s", code, body)
	}
	front, _, _ := r.know.Bundle.Read("projects/p1/drafts/d1.md")
	if v, _ := front["verified"].(map[string]any); v["by"] != "human:eng" {
		t.Fatalf("verified: %v", front["verified"])
	}
	if code, _ = r.do(t, "eng", "POST", "/api/v1/kb/doc/discard?id="+d1ID, ""); code != 409 {
		t.Fatalf("discard of a stable doc: %d", code)
	}
	d2 := "factory:p1:projects/p1/drafts/d2.md"
	if code, _ = r.do(t, "eng", "POST", "/api/v1/kb/doc/discard?id="+d2, ""); code != 204 {
		t.Fatalf("discard: %d", code)
	}
	if code, _ = r.do(t, "eng", "GET", "/api/v1/kb/doc?id="+d2, ""); code != 404 {
		t.Fatalf("discarded doc must be gone: %d", code)
	}
}

func TestKBDelete(t *testing.T) {
	r := bootKB(t)
	if code, _ := r.do(t, "viewer", "DELETE", "/api/v1/kb/doc?id="+d1ID, ""); code != 403 {
		t.Fatalf("viewer DELETE: %d", code)
	}
	if code, _ := r.do(t, "outsider", "DELETE", "/api/v1/kb/doc?id="+d1ID, ""); code != 404 {
		t.Fatalf("DELETE outside the project: %d", code)
	}
	if code, _ := r.do(t, "eng", "DELETE", "/api/v1/kb/doc?id="+repoID, ""); code != 409 {
		t.Fatalf("DELETE of a repo doc: %d", code)
	}
	if code, _ := r.do(t, "owner", "DELETE", "/api/v1/kb/doc?id="+fwID, ""); code != 409 {
		t.Fatalf("DELETE of a default playbook: %d", code)
	}
	if code, _ := r.do(t, "eng", "DELETE", "/api/v1/kb/doc?id=factory:p1:nope.md", ""); code != 404 {
		t.Fatalf("DELETE of a missing doc: %d", code)
	}
	if code, _ := r.do(t, "eng", "POST", "/api/v1/kb/doc/approve?id="+d1ID, "{}"); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	before := r.know.Bundle.Head()
	if code, _ := r.do(t, "eng", "DELETE", "/api/v1/kb/doc?id="+d1ID, ""); code != 204 {
		t.Fatalf("DELETE of an approved doc: %d", code)
	}
	if code, _ := r.do(t, "eng", "GET", "/api/v1/kb/doc?id="+d1ID, ""); code != 404 {
		t.Fatalf("deleted doc must be gone: %d", code)
	}
	if _, _, err := r.know.Bundle.Read("projects/p1/drafts/d1.md"); err == nil {
		t.Fatal("deleted doc must leave the bundle")
	}
	if r.know.Bundle.Head() == before {
		t.Fatal("the deletion must be committed to the bundle")
	}
}
