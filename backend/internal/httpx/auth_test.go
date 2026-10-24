package httpx

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/config"
	"legos-factory/backend/internal/identity"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
	_ "modernc.org/sqlite"
)

// bootMini spins a minimal server with a test database for authz tests.
func bootMini(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT UNIQUE, name TEXT, role TEXT, created_at INTEGER);
	CREATE TABLE sessions (id TEXT PRIMARY KEY, user_id TEXT, expires_at INTEGER, created_at INTEGER);`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{FrontendOrigin: "http://localhost:5173"}
	srv := New(cfg, slog.New(slog.NewTextHandler(testDiscard{}, nil)))
	srv.Auth = &identity.Service{Users: &store.UserRepo{DB: db}, Mode: "dev", DevUser: model.User{ID: "u1", Email: "a@b", Name: "A", Role: model.RoleOwner}}
	return srv, db
}

func (s *Server) mkUserAndSession(t *testing.T, db *sql.DB, id string, role model.Role) string {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users (id, email, name, role, created_at) VALUES (?,?,?,?,?)`, id, id+"@t", id, string(role), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, user_id, expires_at, created_at) VALUES (?,?,?,?)`, "sess-"+id, id, time.Now().Add(time.Hour).Unix(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	return "sess-" + id
}

func TestUnauthenticatedGets401(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	cfg := &config.Config{}
	srv := New(cfg, slog.New(slog.NewTextHandler(testDiscard{}, nil)))
	srv.Auth = &identity.Service{Users: &store.UserRepo{DB: db}, Mode: "dev"}
	// register projects route with a handler that would 500 without auth
	token, _ := json.Marshal(map[string]string{})
	_ = token
	srv.Echo.GET("/api/v1/projects", srv.requireAuth(func(c echo.Context) error { return c.JSON(http.StatusOK, map[string]string{}) }))
	e := srv.Echo
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", rec.Code)
	}
}

func TestViewerCannotWrite(t *testing.T) {
	srv, db := bootMini(t)
	tok := srv.mkUserAndSession(t, db, "viewer1", model.RoleViewer)
	srv.Echo.POST("/api/v1/projects", srv.requireAuth(srv.requireRole(model.RoleEngineer, func(c echo.Context) error { return c.JSON(http.StatusOK, map[string]string{}) })))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", nil)
	req.AddCookie(&http.Cookie{Name: "factory_session", Value: tok})
	rec := httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer write, got %d", rec.Code)
	}
	// admin may write
	tokA := srv.mkUserAndSession(t, db, "admin1", model.RoleAdmin)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/projects", nil)
	req.AddCookie(&http.Cookie{Name: "factory_session", Value: tokA})
	rec = httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin write, got %d", rec.Code)
	}
}

func TestSessionExpiryRejected(t *testing.T) {
	srv, db := bootMini(t)
	if _, err := db.Exec(`INSERT INTO users (id, email, name, role, created_at) VALUES ('u9','u9@t','u9','ENGINEER',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id, user_id, expires_at, created_at) VALUES ('expired','u9', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	srv.Echo.GET("/api/v1/whoami", func(c echo.Context) error {
		_ = srv.setUser(c)
		if currentUser(c) == nil {
			return errorJSON(c, http.StatusUnauthorized, "UNAUTHENTICATED", "sign in")
		}
		return c.JSON(http.StatusOK, map[string]string{"user": currentUser(c).ID})
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	req.AddCookie(&http.Cookie{Name: "factory_session", Value: "expired"})
	rec := httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired session, got %d", rec.Code)
	}
}

func TestNSErrorEnvelopeHasRequestID(t *testing.T) {
	srv, db := bootMini(t)
	tok := srv.mkUserAndSession(t, db, "eng1", model.RoleEngineer)
	srv.Echo.GET("/api/v1/work-orders/:id", func(c echo.Context) error {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "work order not found")
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/work-orders/missing", nil)
	req.Header.Set(echo.HeaderXRequestID, "req_test_123")
	req.AddCookie(&http.Cookie{Name: "factory_session", Value: tok})
	rec := httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	var env struct {
		Error struct {
			Code      string `json:"code"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Error.Code != "NOT_FOUND" || env.Error.RequestID != "req_test_123" {
		t.Fatalf("error envelope missing code/request_id: %s", rec.Body.String())
	}
}

type testDiscard struct{}

func (testDiscard) Write(p []byte) (int, error) { return len(p), nil }
