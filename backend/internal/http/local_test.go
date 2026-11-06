package http

import (
	"context"
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
	if rec := call(http.MethodGet, "/api/v1/local/gh", eng); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"installed":false`) {
		t.Errorf("gh: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodGet, "/api/v1/local/inspect?path=relative", eng); rec.Code != http.StatusBadRequest {
		t.Errorf("inspect relative: %d %s", rec.Code, rec.Body)
	}
}
