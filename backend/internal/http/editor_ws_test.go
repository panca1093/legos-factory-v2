package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func (f wsFolders) Folder(_ context.Context, user, id string) (*model.EditorFolder, error) {
	if x, ok := f[user+"/"+id]; ok {
		return x, nil
	}
	return nil, errors.New("no rows")
}

type wsPaths struct {
	mu sync.Mutex
	m  map[string]string
}

func (p *wsPaths) ServerPaths(context.Context, string) (map[string]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]string{}
	for k, v := range p.m {
		out[k] = v
	}
	return out, nil
}
func (p *wsPaths) SetServerPath(_ context.Context, _, id, path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]string{}
	}
	p.m[id] = path
	return nil
}

type echoProc struct {
	out  chan []byte
	exit chan struct{}
	once sync.Once
}

func (p *echoProc) ReadMessage() ([]byte, error) {
	select {
	case m := <-p.out:
		return m, nil
	case <-p.exit:
		return nil, io.EOF
	}
}
func (p *echoProc) WriteMessage(msg []byte) error {
	var h struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.Unmarshal(msg, &h)
	if len(h.ID) > 0 && h.Method != "" {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": h.ID, "result": map[string]any{"method": h.Method}})
		p.out <- b
	}
	return nil
}
func (p *echoProc) Wait() error        { <-p.exit; return nil }
func (p *echoProc) Kill() error        { p.once.Do(func() { close(p.exit) }); return nil }
func (p *echoProc) StderrTail() string { return "" }

type echoHost struct{}

func (echoHost) Discover(_ context.Context, servers []model.LSPServerSpec, custom map[string]string) []model.LSPServerState {
	var out []model.LSPServerState
	for _, s := range servers {
		st := model.LSPServerState{ID: s.ID, State: model.LSPFound, Path: "/bin/" + s.Binary, Install: s.Install}
		if p := custom[s.ID]; p != "" {
			st.Path = p
		}
		out = append(out, st)
	}
	return out
}
func (echoHost) Start(context.Context, model.LSPServerSpec, string, string) (model.LSPProcess, error) {
	return &echoProc{out: make(chan []byte, 16), exit: make(chan struct{})}, nil
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
	host := echoHost{}
	srv.EditorLive = &usecase.EditorLive{
		Folders: wsFolders{
			"eng1/f1":   {ID: "f1", Path: root, Kind: model.FolderProject, Trusted: true},
			"eng1/fu":   {ID: "fu", Path: root, Kind: model.FolderProject, Trusted: false},
			"other1/f9": {ID: "f9", Path: root, Kind: model.FolderProject, Trusted: true},
		},
		FS: wsFS{}, Host: host, Servers: sandbox.LSPAllowlist, Store: &wsPaths{}, Runs: &fakeWSRuns{},
		Reg: &usecase.LSPRegistry{Host: host},
	}
	t.Cleanup(srv.EditorLive.KillAll)
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

func closeCode(t *testing.T, c *websocket.Conn) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	return int(websocket.CloseStatus(err))
}

func TestEditorSocketsRejectMissingOrForeignOrigin(t *testing.T) {
	e := newWSEnv(t, "dev")
	for _, path := range []string{"/editor/folders/f1/ws", "/editor/folders/f1/lsp/gopls"} {
		for _, origin := range []string{"", "http://evil.example", "http://localhost:9999", "null"} {
			_, resp, err := e.dial(t, path, origin, e.cookie)
			if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s origin %q: err=%v resp=%v", path, origin, err, resp)
			}
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
	if _, resp, err := e.dial(t, "/editor/folders/f9/lsp/gopls", frontendOrigin, e.cookie); err == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another user's folder (lsp): %v %v", err, resp)
	}
	os.RemoveAll(e.root)
	if _, resp, err := e.dial(t, "/editor/folders/f1/ws", frontendOrigin, e.cookie); err == nil || resp.StatusCode != http.StatusGone {
		t.Fatalf("gone folder: %v %v", err, resp)
	}

	oidc := newWSEnv(t, "oidc")
	if _, resp, err := oidc.dial(t, "/editor/folders/f1/ws", frontendOrigin, oidc.cookie); err == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("oidc mode must not expose the editor: %v %v", err, resp)
	}
	req, _ := http.NewRequest(http.MethodGet, oidc.ts.URL+"/api/v1/editor/servers", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: oidc.cookie})
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("oidc servers list: %v %v", err, resp)
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

func TestLSPSocketCloseCodes(t *testing.T) {
	e := newWSEnv(t, "dev")
	c, _, err := e.dial(t, "/editor/folders/fu/lsp/gopls", frontendOrigin, e.cookie)
	if err != nil {
		t.Fatal(err)
	}
	if code := closeCode(t, c); code != 4003 {
		t.Fatalf("untrusted folder: %d", code)
	}
	c, _, _ = e.dial(t, "/editor/folders/f1/lsp/rust-analyzer", frontendOrigin, e.cookie)
	if code := closeCode(t, c); code != 4002 {
		t.Fatalf("outside the allowlist: %d", code)
	}
}

func TestLSPSocketRoundTripAndTakeover(t *testing.T) {
	e := newWSEnv(t, "dev")
	c1, _, err := e.dial(t, "/editor/folders/f1/lsp/gopls", frontendOrigin, e.cookie)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.CloseNow()
	ctx := context.Background()
	if err := c1.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{}}}`)); err != nil {
		t.Fatal(err)
	}
	if m := readJSON(t, c1); m["id"] != float64(1) || m["result"] == nil {
		t.Fatalf("%v", m)
	}
	if err := c1.Write(ctx, websocket.MessageBinary, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if code := closeCode(t, c1); code != int(websocket.StatusUnsupportedData) {
		t.Fatalf("binary frame: %d", code)
	}

	c2, _, _ := e.dial(t, "/editor/folders/f1/lsp/gopls", frontendOrigin, e.cookie)
	defer c2.CloseNow()
	time.Sleep(50 * time.Millisecond)
	c3, _, err := e.dial(t, "/editor/folders/f1/lsp/gopls", frontendOrigin, e.cookie)
	if err != nil {
		t.Fatal(err)
	}
	defer c3.CloseNow()
	if code := closeCode(t, c2); code != 4001 {
		t.Fatalf("takeover: %d", code)
	}
	if err := c3.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","id":9,"method":"initialize","params":{}}`)); err != nil {
		t.Fatal(err)
	}
	if m := readJSON(t, c3); m["id"] != float64(9) {
		t.Fatalf("new tab served: %v", m)
	}
	if err := c3.Write(ctx, websocket.MessageText, []byte(`not json`)); err != nil {
		t.Fatal(err)
	}
	if code := closeCode(t, c3); code != int(websocket.StatusInvalidFramePayloadData) {
		t.Fatalf("invalid JSON-RPC: %d", code)
	}
}

func (e *wsEnv) call(t *testing.T, method, path, contentType, origin, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+"/api/v1"+path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: e.cookie})
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestServerRoutes(t *testing.T) {
	e := newWSEnv(t, "dev")
	resp, body := e.call(t, http.MethodGet, "/editor/servers", "", "", "")
	var list []model.LSPServerState
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &list) != nil || len(list) != 2 || list[0].ID != "gopls" || list[1].ID != "tsls" || list[0].Install == "" {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ = e.call(t, http.MethodPost, "/editor/servers/check", "", "", ""); resp.StatusCode != 200 {
		t.Fatalf("check: %d", resp.StatusCode)
	}
	if resp, _ = e.call(t, http.MethodPost, "/editor/servers/check", "", "http://evil.example", ""); resp.StatusCode != 403 {
		t.Fatalf("check from a foreign origin: %d", resp.StatusCode)
	}

	put := `{"path":"/opt/mine/gopls","confirm":true}`
	if resp, _ = e.call(t, http.MethodPut, "/editor/servers/gopls", "text/plain", "", put); resp.StatusCode != 415 {
		t.Fatalf("non-JSON content type: %d", resp.StatusCode)
	}
	if resp, _ = e.call(t, http.MethodPut, "/editor/servers/gopls", "application/json", "http://evil.example", put); resp.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
	if resp, _ = e.call(t, http.MethodPut, "/editor/servers/gopls", "application/json", "", `{"path":"/opt/mine/gopls"}`); resp.StatusCode != 400 {
		t.Fatalf("confirm missing: %d", resp.StatusCode)
	}
	if resp, _ = e.call(t, http.MethodPut, "/editor/servers/zsh", "application/json", "", put); resp.StatusCode != 400 {
		t.Fatalf("server outside the allowlist: %d", resp.StatusCode)
	}
	resp, body = e.call(t, http.MethodPut, "/editor/servers/gopls", "application/json", frontendOrigin, put)
	var st model.LSPServerState
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &st) != nil || st.Path != "/opt/mine/gopls" {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}
