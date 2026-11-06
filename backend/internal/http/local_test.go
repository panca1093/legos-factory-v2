package http

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

type noHost struct{ usecase.LocalHost }

func (noHost) PickFolder(context.Context) (string, error) { return "", model.ErrCancelled }

func TestLocalRoutesAreDevOnlyAndEngineerOnly(t *testing.T) {
	srv, db := bootMini(t)
	srv.Local = &usecase.Local{Host: noHost{}}
	eng := srv.mkUserAndSession(t, db, "eng", model.RoleEngineer)
	viewer := srv.mkUserAndSession(t, db, "view", model.RoleViewer)

	call := func(method, path, sess string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "factory_session", Value: sess})
		rec := httptest.NewRecorder()
		srv.Echo.ServeHTTP(rec, req)
		return rec
	}
	routes := [][2]string{
		{http.MethodPost, "/api/v1/local/pick-folder"},
		{http.MethodPost, "/api/v1/local/pick-folder/cancel"},
		{http.MethodGet, "/api/v1/local/inspect?path=/"},
		{http.MethodPost, "/api/v1/local/clone"},
		{http.MethodPost, "/api/v1/local/init"},
		{http.MethodGet, "/api/v1/local/gh"},
	}

	srv.Cfg.AuthMode = "oidc"
	for _, r := range routes {
		if rec := call(r[0], r[1], eng); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "NOT_AVAILABLE") {
			t.Errorf("oidc %s %s: %d %s", r[0], r[1], rec.Code, rec.Body)
		}
	}

	srv.Cfg.AuthMode = "dev"
	if rec := call(http.MethodGet, "/api/v1/local/gh", viewer); rec.Code != http.StatusForbidden {
		t.Errorf("viewer: %d", rec.Code)
	}
	if rec := call(http.MethodPost, "/api/v1/local/pick-folder", eng); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cancelled":true`) {
		t.Errorf("pick-folder cancel: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, "/api/v1/local/pick-folder/cancel", viewer); rec.Code != http.StatusForbidden {
		t.Errorf("viewer cancel: %d", rec.Code)
	}
	if rec := call(http.MethodPost, "/api/v1/local/pick-folder/cancel", eng); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("idle cancel: %d %q", rec.Code, rec.Body)
	}
	if rec := call(http.MethodGet, "/api/v1/local/gh", eng); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"installed":false`) {
		t.Errorf("gh: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodGet, "/api/v1/local/inspect?path=relative", eng); rec.Code != http.StatusBadRequest {
		t.Errorf("inspect relative: %d %s", rec.Code, rec.Body)
	}
}

func TestDevModeRejectsForeignHosts(t *testing.T) {
	for mode, tc := range map[string]struct {
		host string
		want int
	}{
		"dev":  {"evil.example.com:8080", http.StatusForbidden},
		"dev2": {"localhost:5173", http.StatusOK},
		"dev3": {"127.0.0.1:8080", http.StatusOK},
		"dev4": {"[::1]:8080", http.StatusOK},
		"dev5": {"localhost", http.StatusOK},
		"oidc": {"app.example.com", http.StatusOK},
	} {
		auth := strings.TrimRight(mode, "012345")
		srv := New(&model.Config{AuthMode: auth, FrontendOrigin: "http://localhost:5173"}, slog.New(slog.NewTextHandler(testDiscard{}, nil)))
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Host = tc.host
		rec := httptest.NewRecorder()
		srv.Echo.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s host %q: %d, want %d", auth, tc.host, rec.Code, tc.want)
		}
	}
}
