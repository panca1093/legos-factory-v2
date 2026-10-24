// Package httpx wires the Echo HTTP server: middleware, routes, handlers
// and SSE streaming. Handlers stay thin; logic lives in domain packages.
package httpx

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"

	"legos-factory/backend/internal/config"
	"legos-factory/backend/internal/eventbus"
	"legos-factory/backend/internal/identity"
	"legos-factory/backend/internal/orchestration"
	"legos-factory/backend/internal/project"
	"legos-factory/backend/internal/usage"
	"legos-factory/backend/internal/workorder"
)

type Server struct {
	Echo    *echo.Echo
	Cfg     *config.Config
	WORepo  *workorder.Repository
	ProjRepo *project.Repository
	Foreman *orchestration.Foreman
	Bus     *eventbus.Bus
	Auth    *identity.Service
	Usage   *usage.Tracker
	Models  func(ctx echo.Context) ([]ModelInfo, error)
	log     *slog.Logger
}

type ModelInfo struct {
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
	Name       string `json:"name"`
	Harness    string `json:"harness"`
}

func New(cfg *config.Config, log *slog.Logger) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(echomw.RequestIDWithConfig(echomw.RequestIDConfig{
		TargetHeader: echo.HeaderXRequestID,
	}))
	e.Use(echomw.Recover())
	e.Use(requestLogger(log))
	e.Use(echomw.BodyLimit("1MB"))
	e.Use(echomw.CORSWithConfig(echomw.CORSConfig{
		AllowOrigins:     []string{cfg.FrontendOrigin},
		AllowCredentials: true,
		AllowHeaders:     []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAccept, "Last-Event-Id"},
	}))

	s := &Server{Echo: e, Cfg: cfg, log: log}
	s.routes()
	return s
}

func requestLogger(log *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			if c.Request().URL.Path == "/api/v1/work-orders/events" || c.Request().URL.Path == "/api/v1/events" {
				return err // long-lived streams: don't log per completion
			}
			log.Info("http", "method", c.Request().Method, "path", c.Request().URL.Path,
				"status", c.Response().Status, "duration", time.Since(start).String(),
				"request_id", c.Response().Header().Get(echo.HeaderXRequestID))
			return err
		}
	}
}

const sessionCookie = "factory_session"

func (s *Server) setUser(c echo.Context) error {
	cookie, err := c.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return nil
	}
	u, err := s.Auth.UserForSession(c.Request().Context(), cookie.Value)
	if err != nil {
		return nil
	}
	c.Set("user", u)
	return nil
}

func currentUser(c echo.Context) *identity.User {
	u, _ := c.Get("user").(*identity.User)
	return u
}

func (s *Server) requireAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		_ = s.setUser(c)
		if currentUser(c) == nil {
			return errorJSON(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in required")
		}
		return next(c)
	}
}

// requireRole enforces the given role floor on the current user.
func (s *Server) requireRole(min identity.Role, next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := currentUser(c)
		if u == nil {
			return errorJSON(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in required")
		}
		switch min {
		case identity.RoleAdmin:
			if !u.Role.IsAdmin() {
				return errorJSON(c, http.StatusForbidden, "FORBIDDEN", "Admin role required")
			}
		default:
			if !u.Role.CanWrite() {
				return errorJSON(c, http.StatusForbidden, "FORBIDDEN", "Write access required")
			}
		}
		return next(c)
	}
}

// ---- error envelope ----

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

func errorJSON(c echo.Context, status int, code, message string) error {
	return c.JSON(status, map[string]any{
		"error":      apiError{Code: code, Message: message, RequestID: c.Response().Header().Get(echo.HeaderXRequestID)},
		"request_id": c.Response().Header().Get(echo.HeaderXRequestID),
	})
}

// routes registers all API routes.
func (s *Server) routes() {
	e := s.Echo
	e.GET("/health", func(c echo.Context) error { return c.JSON(http.StatusOK, map[string]string{"status": "ok"}) })
	e.GET("/ready", func(c echo.Context) error { return c.JSON(http.StatusOK, map[string]string{"status": "ready"}) })

	e.GET("/api/v1/auth/session", s.handleAuthSession)
	e.POST("/api/v1/auth/dev-login", s.handleDevLogin)
	e.POST("/api/v1/auth/logout", s.handleLogout)
	e.GET("/api/v1/auth/oidc/start", s.handleOIDCStart)
	e.GET("/auth/oidc/callback", s.handleOIDCCallback)

	api := e.Group("/api/v1")
	api.Use(s.requireAuth)

	api.GET("/projects", s.handleListProjects)
	api.POST("/projects", s.requireRole(identity.RoleEngineer, s.handleCreateProject))
	api.GET("/projects/:id", s.handleGetProject)

	api.GET("/work-orders", s.handleListWorkOrders)
	api.POST("/work-orders", s.requireRole(identity.RoleEngineer, s.handleCreateWorkOrder))
	api.GET("/work-orders/:id", s.handleGetWorkOrder)
	api.POST("/work-orders/:id/plan/generate", s.requireRole(identity.RoleEngineer, s.handlePlanGenerate))
	api.GET("/work-orders/:id/plan", s.handleGetPlan)
	api.POST("/work-orders/:id/plan/decisions", s.requireRole(identity.RoleEngineer, s.handlePlanDecision))
	api.POST("/work-orders/:id/clarifications", s.requireRole(identity.RoleEngineer, s.handleClarification))
	api.POST("/work-orders/:id/pr-decision", s.requireRole(identity.RoleEngineer, s.handlePRDecision))
	api.POST("/work-orders/:id/cancel", s.requireRole(identity.RoleEngineer, s.handleCancel))
	api.GET("/work-orders/:id/runs", s.handleListRuns)
	api.GET("/work-orders/:id/activity", s.handleListEvents)
	api.GET("/work-orders/:id/quality", s.handleListQuality)
	api.GET("/work-orders/:id/findings", s.handleListFindings)
	api.GET("/work-orders/:id/events", s.handleWOEvents)
	api.GET("/events", s.handleGlobalEvents)

	api.GET("/models", s.handleModels)
	api.GET("/usage", s.handleUsage)
	api.GET("/overview", s.handleOverview)

	settings := api.Group("/settings")
	settings.GET("", s.handleGetSettings)
	settings.PUT("", s.requireRole(identity.RoleAdmin, s.handlePutSettings))
}

// ---- auth handlers ----

func (s *Server) handleAuthSession(c echo.Context) error {
	_ = s.setUser(c)
	u := currentUser(c)
	if u == nil {
		return errorJSON(c, http.StatusUnauthorized, "UNAUTHENTICATED", "not signed in")
	}
	return c.JSON(http.StatusOK, map[string]any{"user": u, "mode": s.Cfg.AuthMode})
}

func (s *Server) handleDevLogin(c echo.Context) error {
	if s.Cfg.AuthMode != "dev" {
		return errorJSON(c, http.StatusNotFound, "NOT_AVAILABLE", "dev login disabled (auth mode is oidc)")
	}
	u, err := s.Auth.EnsureDevUser(c.Request().Context())
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not ensure dev user")
	}
	sid, _, err := s.Auth.Login(c.Request().Context(), u)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not create session")
	}
	c.SetCookie(&http.Cookie{
		Name: sessionCookie, Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: s.Cfg.SessionTTL,
	})
	return c.JSON(http.StatusOK, map[string]any{"user": u})
}

func (s *Server) handleLogout(c echo.Context) error {
	if cookie, err := c.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		_, _ = s.Auth.DB.ExecContext(c.Request().Context(), `DELETE FROM sessions WHERE id = ?`, cookie.Value)
	}
	c.SetCookie(&http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleOIDCStart(c echo.Context) error {
	if s.Cfg.AuthMode != "oidc" || s.Auth.OIDC == nil {
		return errorJSON(c, http.StatusNotFound, "NOT_AVAILABLE", "oidc not configured")
	}
	u, err := s.Auth.OIDC.AuthURL(c.Request().Context(), "state-cookie-protected")
	if err != nil {
		return errorJSON(c, http.StatusBadGateway, "OIDC_UNAVAILABLE", err.Error())
	}
	return c.Redirect(http.StatusFound, u)
}

func (s *Server) handleOIDCCallback(c echo.Context) error {
	if s.Cfg.AuthMode != "oidc" || s.Auth.OIDC == nil {
		return errorJSON(c, http.StatusNotFound, "NOT_AVAILABLE", "oidc not configured")
	}
	code := c.QueryParam("code")
	if code == "" {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "missing code")
	}
	email, name, err := s.Auth.OIDC.Exchange(c.Request().Context(), code)
	if err != nil {
		return errorJSON(c, http.StatusBadGateway, "OIDC_EXCHANGE_FAILED", err.Error())
	}
	u, err := s.Auth.UpsertUser(c.Request().Context(), email, name)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not upsert user")
	}
	sid, _, err := s.Auth.Login(c.Request().Context(), u)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not create session")
	}
	c.SetCookie(&http.Cookie{
		Name: sessionCookie, Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: s.Cfg.SessionTTL,
	})
	return c.Redirect(http.StatusFound, "/")
}
