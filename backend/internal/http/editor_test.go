package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/sandbox"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

type editorBoot struct {
	srv     *Server
	project string
	data    string
}

func bootEditor(t *testing.T) *editorBoot {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	users := &store.UserRepo{DB: db}
	for _, id := range []string{"ana", "bob", "vic"} {
		role := model.RoleEngineer
		if id == "vic" {
			role = model.RoleViewer
		}
		if err := users.Create(ctx, model.User{ID: id, Email: id + "@t", Name: id, Role: role}); err != nil {
			t.Fatal(err)
		}
		if err := users.CreateSession(ctx, "sess-"+id, id, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	b := &editorBoot{project: t.TempDir(), data: t.TempDir()}
	projRepo, members, woRepo := &store.ProjectRepo{DB: db}, &store.MemberRepo{DB: db}, &store.WorkOrderRepo{DB: db}
	if err := projRepo.Create(ctx, &model.Project{ID: "p1", Name: "p1", RepoURL: b.project}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ana", "vic"} {
		if err := members.Add(ctx, "p1", id); err != nil {
			t.Fatal(err)
		}
	}
	if err := woRepo.Create(ctx, &model.WorkOrder{ID: "wo_1", ProjectID: "p1", Title: "t", Objective: "o", State: model.StateCreated, CreatedBy: "ana"}); err != nil {
		t.Fatal(err)
	}
	srv := New(&model.Config{FrontendOrigin: "http://localhost:5173"}, slog.New(slog.NewTextHandler(testDiscard{}, nil)))
	srv.Cfg.AuthMode = "dev"
	srv.Auth = &usecase.Identity{Users: users, Mode: "dev"}
	srv.Local = &usecase.Local{}
	srv.Editor = &usecase.Editor{
		Store: &store.EditorRepo{DB: db}, FS: sandbox.EditorFS{},
		Projects: &usecase.Projects{Store: projRepo, Members: members, Users: users},
		Proj:     projRepo, WO: woRepo, DataDir: b.data, Home: t.TempDir(),
	}
	b.srv = srv
	return b
}

func (b *editorBoot) do(as, method, url, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	if as != "" {
		req.AddCookie(&http.Cookie{Name: "factory_session", Value: "sess-" + as})
	}
	rec := httptest.NewRecorder()
	b.srv.Echo.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	return v
}

func (b *editorBoot) folder(t *testing.T) string {
	t.Helper()
	rec := b.do("ana", "GET", "/api/v1/projects/p1/editor", "")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	f := decode[struct{ Folder model.EditorFolder }](t, rec)
	if f.Folder.Kind != "project" {
		t.Fatalf("%+v", f)
	}
	return f.Folder.ID
}

func hasCode(rec *httptest.ResponseRecorder, code string) bool {
	return strings.Contains(rec.Body.String(), `"code":"`+code+`"`)
}

func TestEditorRoutesAre404OutsideDevMode(t *testing.T) {
	b := bootEditor(t)
	fid := b.folder(t)
	routes := [][2]string{
		{"GET", "/api/v1/projects/p1/editor"},
		{"POST", "/api/v1/editor/folders"},
		{"GET", "/api/v1/editor/folders/" + fid + "/tree"},
		{"GET", "/api/v1/editor/folders/" + fid + "/files"},
		{"GET", "/api/v1/editor/folders/" + fid + "/file?path=a"},
		{"PUT", "/api/v1/editor/folders/" + fid + "/file"},
		{"POST", "/api/v1/editor/folders/" + fid + "/mkdir"},
		{"POST", "/api/v1/editor/folders/" + fid + "/rename"},
	}
	b.srv.Cfg.AuthMode = "oidc"
	for _, r := range routes {
		for _, ct := range []string{"application/json", "text/plain"} {
			if rec := b.do("ana", r[0], r[1], `{}`, "Content-Type", ct); rec.Code != 404 || !hasCode(rec, "NOT_AVAILABLE") {
				t.Errorf("oidc %s %s (%s): %d %s", r[0], r[1], ct, rec.Code, rec.Body)
			}
		}
	}
	b.srv.Cfg.AuthMode = "dev"
	b.srv.Local = nil
	if rec := b.do("ana", "GET", "/api/v1/projects/p1/editor", ""); rec.Code != 404 {
		t.Errorf("no Local: %d", rec.Code)
	}
}

func TestEditorRolesAndAuth(t *testing.T) {
	b := bootEditor(t)
	fid := b.folder(t)
	if rec := b.do("", "GET", "/api/v1/editor/folders/"+fid+"/tree", ""); rec.Code != 401 {
		t.Fatalf("anonymous: %d", rec.Code)
	}
	if rec := b.do("vic", "GET", "/api/v1/editor/folders/"+fid+"/tree", ""); rec.Code != 403 {
		t.Fatalf("viewer: %d", rec.Code)
	}
}

func TestEditorMutationsNeedJSONContentType(t *testing.T) {
	b := bootEditor(t)
	fid := b.folder(t)
	base := "/api/v1/editor/folders/" + fid
	for _, r := range [][2]string{
		{"PUT", base + "/file"}, {"POST", base + "/mkdir"}, {"POST", base + "/rename"},
		{"POST", "/api/v1/editor/folders"},
	} {
		for _, ct := range []string{"application/x-www-form-urlencoded", "text/plain", "multipart/form-data; boundary=x", ""} {
			rec := b.do("ana", r[0], r[1], `path=x`, "Content-Type", ct)
			if rec.Code != 415 || !hasCode(rec, "UNSUPPORTED_MEDIA_TYPE") {
				t.Errorf("%s %s with %q: %d %s", r[0], r[1], ct, rec.Code, rec.Body)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(b.project, "x")); err == nil {
		t.Fatal("a rejected request changed the disk")
	}
	rec := b.do("ana", "POST", base+"/mkdir", `{"path":"ok"}`, "Content-Type", "application/json; charset=utf-8")
	if rec.Code != 201 {
		t.Fatalf("json with charset: %d %s", rec.Code, rec.Body)
	}
}

func TestEditorOriginGuard(t *testing.T) {
	b := bootEditor(t)
	fid := b.folder(t)
	body := `{"path":"o.txt","content":"x","create":true}`
	url := "/api/v1/editor/folders/" + fid + "/file"
	for _, o := range []string{"http://evil.example", "null", "http://localhost:9999", "https://localhost:5173"} {
		if rec := b.do("ana", "PUT", url, body, "Origin", o); rec.Code != 403 || !hasCode(rec, "FORBIDDEN_ORIGIN") {
			t.Errorf("origin %q: %d %s", o, rec.Code, rec.Body)
		}
		if rec := b.do("ana", "GET", "/api/v1/editor/folders/"+fid+"/tree", "", "Origin", o); rec.Code != 403 {
			t.Errorf("GET origin %q: %d", o, rec.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(b.project, "o.txt")); err == nil {
		t.Fatal("a forbidden origin wrote a file")
	}
	for i, o := range []string{"http://localhost:5173", "http://example.com", ""} { // frontend, own origin, none
		body := strings.Replace(body, "o.txt", "ok"+string(rune('a'+i))+".txt", 1)
		if rec := b.do("ana", "PUT", url, body, "Origin", o); rec.Code != 200 {
			t.Errorf("origin %q: %d %s", o, rec.Code, rec.Body)
		}
	}
}

func TestEditorFolderOfAnotherUserIs404(t *testing.T) {
	b := bootEditor(t)
	fid := b.folder(t)
	base := "/api/v1/editor/folders/" + fid
	for _, r := range [][3]string{
		{"GET", base + "/tree", ""}, {"GET", base + "/files", ""}, {"GET", base + "/file?path=a", ""},
		{"PUT", base + "/file", `{"path":"a","content":"x","create":true}`},
		{"POST", base + "/mkdir", `{"path":"d"}`}, {"POST", base + "/rename", `{"from":"a","to":"b"}`},
	} {
		if rec := b.do("bob", r[0], r[1], r[2]); rec.Code != 404 || !hasCode(rec, "NOT_FOUND") {
			t.Errorf("%s %s: %d %s", r[0], r[1], rec.Code, rec.Body)
		}
	}
	if rec := b.do("bob", "GET", "/api/v1/projects/p1/editor", ""); rec.Code != 404 {
		t.Errorf("non-member project: %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(b.project, "a")); err == nil {
		t.Fatal("bob wrote into ana's folder")
	}
}

func TestEditorFileFlow(t *testing.T) {
	b := bootEditor(t)
	fid := b.folder(t)
	base := "/api/v1/editor/folders/" + fid

	rec := b.do("ana", "PUT", base+"/file", `{"path":"src/a.txt","content":"x","create":true}`)
	if rec.Code != 404 {
		t.Fatalf("create in a missing dir: %d %s", rec.Code, rec.Body)
	}
	if rec = b.do("ana", "POST", base+"/mkdir", `{"path":"src"}`); rec.Code != 201 {
		t.Fatalf("mkdir: %d %s", rec.Code, rec.Body)
	}
	if rec = b.do("ana", "POST", base+"/mkdir", `{"path":"src"}`); rec.Code != 409 || !hasCode(rec, "CONFLICT") {
		t.Fatalf("mkdir twice: %d %s", rec.Code, rec.Body)
	}
	if rec = b.do("ana", "PUT", base+"/file", `{"path":"src/a.txt","content":"one\n","create":true}`); rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode[model.FileStat](t, rec)
	if rec = b.do("ana", "PUT", base+"/file", `{"path":"src/a.txt","content":"again","create":true}`); rec.Code != 409 {
		t.Fatalf("create existing: %d %s", rec.Code, rec.Body)
	}

	rec = b.do("ana", "GET", base+"/file?path=src/a.txt", "")
	fc := decode[model.FileContent](t, rec)
	if rec.Code != 200 || fc.Content != "one\n" || fc.Hash != created.Hash || fc.EOL != "LF" || fc.ReadOnly {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	if rec = b.do("ana", "PUT", base+"/file", `{"path":"src/a.txt","content":"two\n","base_hash":"`+fc.Hash+`"}`); rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}

	rec = b.do("ana", "PUT", base+"/file", `{"path":"src/a.txt","content":"mine\n","base_hash":"`+fc.Hash+`"}`)
	conf := decode[struct {
		Error       model.APIError `json:"error"`
		CurrentHash string         `json:"current_hash"`
		Size        int64          `json:"size"`
		Actor       *model.EditorActor
	}](t, rec)
	if rec.Code != 409 || conf.Error.Code != "CONFLICT" || conf.CurrentHash == "" || conf.CurrentHash == fc.Hash || conf.Size != 4 || conf.Actor != nil {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body)
	}
	if got, _ := os.ReadFile(filepath.Join(b.project, "src/a.txt")); string(got) != "two\n" {
		t.Fatalf("%q", got)
	}

	rec = b.do("ana", "GET", base+"/tree?path=src", "")
	tree := decode[struct {
		Entries   []model.FileEntry
		Truncated int
	}](t, rec)
	if rec.Code != 200 || len(tree.Entries) != 1 || tree.Entries[0].Name != "a.txt" || tree.Entries[0].Kind != "file" {
		t.Fatalf("tree: %d %s", rec.Code, rec.Body)
	}

	if rec = b.do("ana", "POST", base+"/rename", `{"from":"src/a.txt","to":"src/b.txt"}`); rec.Code != 200 {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	b.do("ana", "PUT", base+"/file", `{"path":"c.txt","content":"c","create":true}`)
	if rec = b.do("ana", "POST", base+"/rename", `{"from":"c.txt","to":"src/b.txt"}`); rec.Code != 409 {
		t.Fatalf("rename onto existing: %d %s", rec.Code, rec.Body)
	}
	rec = b.do("ana", "GET", base+"/files", "")
	paths := decode[[]string](t, rec)
	if rec.Code != 200 || strings.Join(paths, ",") != "c.txt,src/b.txt" {
		t.Fatalf("files: %d %s", rec.Code, rec.Body)
	}
	rec = b.do("ana", "GET", base+"/tree?path=.", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"src"`) {
		t.Fatalf("tree root as '.': %d %s", rec.Code, rec.Body)
	}
	rec = b.do("ana", "GET", base, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"`+fid+`"`) {
		t.Fatalf("folder reload: %d %s", rec.Code, rec.Body)
	}
	if rec = b.do("bob", "GET", base, ""); rec.Code != 404 {
		t.Fatalf("other user's folder: %d", rec.Code)
	}
}

func TestEditorErrorCodes(t *testing.T) {
	b := bootEditor(t)
	fid := b.folder(t)
	base := "/api/v1/editor/folders/" + fid
	must := func(rel string, data []byte) {
		if err := os.WriteFile(filepath.Join(b.project, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("bin", []byte("a\x00b"))
	must("huge", []byte(strings.Repeat("a", usecase.EditorReadMax+1)))
	if err := os.MkdirAll(filepath.Join(b.project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(b.project, "out")); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, method, url, body string
		status                  int
		code                    string
	}{
		{"binary", "GET", base + "/file?path=bin", "", 415, "BINARY"},
		{"too large", "GET", base + "/file?path=huge", "", 413, "TOO_LARGE"},
		{"missing", "GET", base + "/file?path=nope", "", 404, "NOT_FOUND"},
		{"dotdot", "GET", base + "/file?path=../x", "", 403, "OUTSIDE_FOLDER"},
		{"absolute", "GET", base + "/file?path=/etc/passwd", "", 403, "OUTSIDE_FOLDER"},
		{"through symlink", "GET", base + "/file?path=out/f", "", 403, "OUTSIDE_FOLDER"},
		{"git", "GET", base + "/file?path=.git/HEAD", "", 403, "OUTSIDE_FOLDER"},
		{"git write", "PUT", base + "/file", `{"path":".git/config","content":"x","create":true}`, 403, "OUTSIDE_FOLDER"},
		{"nul", "GET", base + "/file?path=a%00b", "", 400, "VALIDATION_FAILED"},
		{"empty path", "GET", base + "/file?path=", "", 400, "VALIDATION_FAILED"},
		{"directory", "GET", base + "/file?path=.git", "", 403, "OUTSIDE_FOLDER"},
		{"binary write", "PUT", base + "/file", `{"path":"bin","content":"x","base_hash":"h"}`, 415, "BINARY"},
		{"bad json", "PUT", base + "/file", `{`, 400, "INVALID_REQUEST"},
		{"mkdir outside", "POST", base + "/mkdir", `{"path":"out/new"}`, 403, "OUTSIDE_FOLDER"},
		{"rename outside", "POST", base + "/rename", `{"from":"bin","to":"../bin"}`, 403, "OUTSIDE_FOLDER"},
		{"worktree gone", "POST", "/api/v1/editor/folders", `{"wo_id":"wo_1"}`, 404, "FOLDER_GONE"},
		{"unknown wo", "POST", "/api/v1/editor/folders", `{"wo_id":"wo_9"}`, 404, "NOT_FOUND"},
		{"bad task id", "POST", "/api/v1/editor/folders", `{"wo_id":"wo_1","task_id":"../x"}`, 400, "VALIDATION_FAILED"},
		{"too large write", "PUT", base + "/file", `{"path":"w","content":"` + strings.Repeat("a", usecase.EditorEditableMax+1) + `","create":true}`, 413, "TOO_LARGE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := b.do("ana", tc.method, tc.url, tc.body)
			if rec.Code != tc.status || !hasCode(rec, tc.code) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	rec := b.do("ana", "GET", base+"/file?path=huge", "")
	if sz := decode[struct{ Size int64 }](t, rec).Size; sz != usecase.EditorReadMax+1 {
		t.Fatalf("413 body size: %d", sz)
	}
	if sz := decode[struct{ Size int64 }](t, b.do("ana", "GET", base+"/file?path=bin", "")).Size; sz != 3 {
		t.Fatalf("415 body size: %d", sz)
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); err == nil {
		t.Fatal("mkdir escaped the folder")
	}
	if _, err := os.Stat(filepath.Join(b.project, "w")); err == nil {
		t.Fatal("an over-limit write created a file")
	}
}

func TestEditorWriteBodyLimitIs12MB(t *testing.T) {
	b := bootEditor(t)
	fid := b.folder(t)
	url := "/api/v1/editor/folders/" + fid + "/file"
	over := `{"path":"w","content":"` + strings.Repeat("a", 13<<20) + `","create":true}`
	if rec := b.do("ana", "PUT", url, over); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over 12 MiB: %d", rec.Code)
	}
	mid := `{"path":"w","content":"` + strings.Repeat("a", 3<<20) + `","create":true}`
	if rec := b.do("ana", "PUT", url, mid); rec.Code != 413 || !hasCode(rec, "TOO_LARGE") {
		t.Fatalf("a 3 MB body must reach the handler (TOO_LARGE envelope), not the 1 MB global limit: %d %.200s", rec.Code, rec.Body)
	}
	if rec := b.do("ana", "POST", "/api/v1/editor/folders/"+fid+"/mkdir", `{"path":"`+strings.Repeat("a", 2<<20)+`"}`); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("other editor routes keep the 1 MB limit: %d", rec.Code)
	}
}

func TestEditorWorktreeFolderRoute(t *testing.T) {
	b := bootEditor(t)
	wt := model.WorktreeDir(b.data, "p1", "wo_1")
	if err := os.MkdirAll(wt+".tasks/t1", 0o755); err != nil {
		t.Fatal(err)
	}
	rec := b.do("ana", "POST", "/api/v1/editor/folders", `{"wo_id":"wo_1","task_id":"t1"}`)
	f := decode[struct{ Folder model.EditorFolder }](t, rec)
	if rec.Code != 200 || f.Folder.Kind != "worktree" || f.Folder.TaskID != "t1" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec = b.do("bob", "POST", "/api/v1/editor/folders", `{"wo_id":"wo_1","task_id":"t1"}`); rec.Code != 404 {
		t.Fatalf("non-member: %d", rec.Code)
	}
}
