package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/sandbox"
	"legos-factory/backend/internal/usecase"
)

const frontendOrigin = "http://localhost:5173"

type wsFS struct{ usecase.EditorFS }

func (wsFS) Watch(root string) (model.Watcher, error) { return sandbox.NewWatcher(root) }
func (wsFS) GitStatus(context.Context, string) (model.GitStatus, error) {
	return model.GitStatus{Repo: true, Branch: "main"}, nil
}

type wsFolders map[string]*model.EditorFolder

func (f wsFolders) Folder(_ context.Context, u *model.User, id string) (*model.EditorFolder, error) {
	if x, ok := f[u.ID+"/"+id]; ok {
		if st, err := os.Stat(x.Path); err != nil || !st.IsDir() {
			return nil, model.ErrFolderGone
		}
		return x, nil
	}
	return nil, model.ErrEditorFolderNotFound
}

type wsEnv struct {
	srv     *Server
	ts      *httptest.Server
	root    string
	cookie  string
	cookie2 string
}

func newWSEnv(t *testing.T, mode string) *wsEnv {
	t.Helper()
	srv, db := bootMini(t)
	srv.Cfg.AuthMode = mode
	srv.Cfg.FrontendOrigin = frontendOrigin
	srv.Local = &usecase.Local{}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	srv.EditorLive = &usecase.EditorLive{
		Folders: wsFolders{
			"eng1/f1":   {ID: "f1", Path: root, Kind: model.FolderProject},
			"other1/f9": {ID: "f9", Path: root, Kind: model.FolderProject},
		},
		FS: wsFS{}, Runs: &fakeWSRuns{},
	}
	e := &wsEnv{srv: srv, root: root}
	e.cookie = srv.mkUserAndSession(t, db, "eng1", model.RoleEngineer)
	srv.mkUserAndSession(t, db, "view1", model.RoleViewer)
	e.cookie2 = "sess-view1"
	e.ts = httptest.NewServer(srv.Echo)
	t.Cleanup(e.ts.Close)
	return e
}

type fakeWSRuns struct{}

func (fakeWSRuns) RunningByWO(context.Context, string) ([]model.RunningRun, error) { return nil, nil }
func (fakeWSRuns) List(context.Context, string) ([]model.Run, error)               { return nil, nil }

func (e *wsEnv) header(origin, cookie string) http.Header {
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	if cookie != "" {
		h.Set("Cookie", sessionCookie+"="+cookie)
	}
	return h
}

func (e *wsEnv) dial(t *testing.T, path, origin, cookie string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.ts.URL, "http")+"/api/v1"+path, &websocket.DialOptions{HTTPHeader: e.header(origin, cookie)})
}

func readJSON(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json %s: %v", b, err)
	}
	return m
}

func readUntil(t *testing.T, c *websocket.Conn, typ string) map[string]any {
	t.Helper()
	for i := 0; i < 20; i++ {
		if m := readJSON(t, c); m["type"] == typ {
			return m
		}
	}
	t.Fatalf("no %s message", typ)
	return nil
}

func TestEditorSocketsRejectMissingOrForeignOrigin(t *testing.T) {
	e := newWSEnv(t, "dev")
	for _, origin := range []string{"", "http://evil.example", "http://localhost:9999", "null"} {
		_, resp, err := e.dial(t, "/editor/folders/f1/ws", origin, e.cookie)
		if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("origin %q: err=%v resp=%v", origin, err, resp)
		}
	}
	c, _, err := e.dial(t, "/editor/folders/f1/ws", frontendOrigin, e.cookie)
	if err != nil {
		t.Fatalf("the frontend origin must pass: %v", err)
	}
	c.CloseNow()
	c, _, err = e.dial(t, "/editor/folders/f1/ws", e.ts.URL, e.cookie)
	if err != nil {
		t.Fatalf("the server's own origin must pass: %v", err)
	}
	c.CloseNow()
}

func TestEditorSocketsRequireSessionLocalGateAndOwnFolder(t *testing.T) {
	e := newWSEnv(t, "dev")
	if _, resp, err := e.dial(t, "/editor/folders/f1/ws", frontendOrigin, ""); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no cookie: %v %v", err, resp)
	}
	if _, resp, err := e.dial(t, "/editor/folders/f1/ws", frontendOrigin, e.cookie2); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer: %v %v", err, resp)
	}
	if _, resp, err := e.dial(t, "/editor/folders/f9/ws", frontendOrigin, e.cookie); err == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another user's folder: %v %v", err, resp)
	}
	os.RemoveAll(e.root)
	if _, resp, err := e.dial(t, "/editor/folders/f1/ws", frontendOrigin, e.cookie); err == nil || resp.StatusCode != http.StatusGone {
		t.Fatalf("gone folder: %v %v", err, resp)
	}

	oidc := newWSEnv(t, "oidc")
	if _, resp, err := oidc.dial(t, "/editor/folders/f1/ws", frontendOrigin, oidc.cookie); err == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("oidc mode must not expose the editor: %v %v", err, resp)
	}
}

func TestEditorNotWiredAnswersNotAvailable(t *testing.T) {
	e := newWSEnv(t, "dev")
	e.srv.EditorLive = nil
	if _, resp, err := e.dial(t, "/editor/folders/f1/ws", frontendOrigin, e.cookie); err == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("%v %v", err, resp)
	}
}

func TestControlSocketStreamsGitStatusAndOpenFileChanges(t *testing.T) {
	e := newWSEnv(t, "dev")
	c, _, err := e.dial(t, "/editor/folders/f1/ws", frontendOrigin, e.cookie)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if m := readUntil(t, c, "git_status"); m["branch"] != "main" || m["repo"] != true {
		t.Fatalf("%v", m)
	}
	path := filepath.Join(e.root, "a.txt")
	os.WriteFile(path, []byte("one"), 0o644)
	ctx := context.Background()
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"watch","dirs":[],"files":["a.txt"]}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // the watch set is applied asynchronously to the socket read
	os.WriteFile(path, []byte("two"), 0o644)
	m := readUntil(t, c, "file_changed")
	if m["path"] != "a.txt" || m["hash"] == "" || (m["kind"] != "changed" && m["kind"] != "created") {
		t.Fatalf("%v", m)
	}
}
