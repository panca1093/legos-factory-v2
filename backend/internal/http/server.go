// Package http wires the Echo HTTP server: middleware, routes, handlers
// and SSE streaming. Handlers stay thin; logic lives in domain packages.
package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

type Server struct {
	Echo     *echo.Echo
	Cfg      *model.Config
	WORepo   *store.WorkOrderRepo
	ProjRepo *store.ProjectRepo
	Runs     *store.RunRepo
	Findings *store.FindingRepo
	Quality  *store.QualityRepo
	Jobs     *store.JobRepo
	Settings *store.SettingsRepo
	Foreman  *usecase.Foreman
	Projects *usecase.Projects
	Catalog  *usecase.Cataloger
	Bus      *Bus
	Auth     *usecase.Identity
	OIDC     *OIDCClient
	Usage    *usecase.UsageTracker
	Models   func(ctx echo.Context) ([]model.ModelInfo, error)
	log      *slog.Logger
}

func New(cfg *model.Config, log *slog.Logger) *Server {
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
	s.mountStatic()
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

func currentUser(c echo.Context) *model.User {
	u, _ := c.Get("user").(*model.User)
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
func (s *Server) requireRole(min model.Role, next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		u := currentUser(c)
		if u == nil {
			return errorJSON(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Sign in required")
		}
		switch min {
		case model.RoleAdmin:
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

// requireWOAccess loads the :id work order and rejects callers outside its
// project with the same 404 as a missing order.
func (s *Server) requireWOAccess(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := c.Request().Context()
		wo, err := s.WORepo.Get(ctx, c.Param("id"))
		if err != nil {
			return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "work order not found")
		}
		if err := s.Projects.Authorize(ctx, currentUser(c), wo.ProjectID); err != nil {
			if usecase.IsNotFound(err) {
				return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "work order not found")
			}
			return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not check access")
		}
		c.Set("wo", wo)
		return next(c)
	}
}

func woFrom(c echo.Context) *model.WorkOrder {
	wo, _ := c.Get("wo").(*model.WorkOrder)
	return wo
}

// ---- error envelope ----

func errorJSON(c echo.Context, status int, code, message string) error {
	return c.JSON(status, map[string]any{
		"error":      model.APIError{Code: code, Message: message, RequestID: c.Response().Header().Get(echo.HeaderXRequestID)},
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
	api.POST("/projects", s.requireRole(model.RoleEngineer, s.handleCreateProject))
	api.GET("/projects/:id", s.handleGetProject)
	api.GET("/projects/:id/members", s.handleListMembers)
	api.POST("/projects/:id/members", s.requireRole(model.RoleAdmin, s.handleAddMember))
	api.DELETE("/projects/:id/members/:user_id", s.requireRole(model.RoleAdmin, s.handleRemoveMember))

	api.GET("/work-orders", s.handleListWorkOrders)
	api.POST("/work-orders", s.requireRole(model.RoleEngineer, s.handleCreateWorkOrder))
	wo := api.Group("/work-orders/:id", s.requireWOAccess)
	wo.GET("", s.handleGetWorkOrder)
	wo.POST("/plan/generate", s.requireRole(model.RoleEngineer, s.handlePlanGenerate))
	wo.GET("/plan", s.handleGetPlan)
	wo.POST("/plan/decisions", s.requireRole(model.RoleEngineer, s.handlePlanDecision))
	wo.POST("/clarifications", s.requireRole(model.RoleEngineer, s.handleClarification))
	wo.POST("/pr-decision", s.requireRole(model.RoleEngineer, s.handlePRDecision))
	wo.POST("/cancel", s.requireRole(model.RoleEngineer, s.handleCancel))
	wo.GET("/runs", s.handleListRuns)
	wo.GET("/activity", s.handleListEvents)
	wo.GET("/quality", s.handleListQuality)
	wo.GET("/findings", s.handleListFindings)
	wo.GET("/events", s.handleWOEvents)
	api.GET("/events", s.handleGlobalEvents)

	api.GET("/knowledge", s.handleListKnowledge)
	api.GET("/knowledge/:id", s.handleGetKnowledge)

	api.GET("/models", s.handleModels)
	api.GET("/usage", s.handleUsage)
	api.GET("/overview", s.handleOverview)

	settings := api.Group("/settings")
	settings.GET("", s.handleGetSettings)
	settings.PUT("", s.requireRole(model.RoleAdmin, s.handlePutSettings))
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
		_ = s.Auth.Logout(c.Request().Context(), cookie.Value)
	}
	c.SetCookie(&http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleOIDCStart(c echo.Context) error {
	if s.Cfg.AuthMode != "oidc" || s.OIDC == nil {
		return errorJSON(c, http.StatusNotFound, "NOT_AVAILABLE", "oidc not configured")
	}
	u, err := s.OIDC.AuthURL(c.Request().Context(), "state-cookie-protected")
	if err != nil {
		return errorJSON(c, http.StatusBadGateway, "OIDC_UNAVAILABLE", err.Error())
	}
	return c.Redirect(http.StatusFound, u)
}

func (s *Server) handleOIDCCallback(c echo.Context) error {
	if s.Cfg.AuthMode != "oidc" || s.OIDC == nil {
		return errorJSON(c, http.StatusNotFound, "NOT_AVAILABLE", "oidc not configured")
	}
	code := c.QueryParam("code")
	if code == "" {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "missing code")
	}
	email, name, err := s.OIDC.Exchange(c.Request().Context(), code)
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
