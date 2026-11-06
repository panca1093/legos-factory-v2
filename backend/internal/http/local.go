package http

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

// local gates a handler to Engineers in dev auth mode: these endpoints expose
// the host filesystem and the user's own git login, which is only safe when
// the backend runs on the user's own machine.
func (s *Server) local(h echo.HandlerFunc) echo.HandlerFunc {
	return s.requireRole(model.RoleEngineer, func(c echo.Context) error {
		if s.Cfg.AuthMode != "dev" || s.Local == nil {
			return errorJSON(c, http.StatusNotFound, "NOT_AVAILABLE", "local folder projects need FACTORY_AUTH_MODE=dev")
		}
		return h(c)
	})
}

func localError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, usecase.ErrInvalid):
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
	case errors.Is(err, model.ErrBusy):
		return errorJSON(c, http.StatusConflict, "BUSY", err.Error())
	case errors.Is(err, model.ErrNotSupported):
		return errorJSON(c, http.StatusNotImplemented, "NOT_SUPPORTED", "the folder chooser is only available on macOS; type the path instead")
	}
	return errorJSON(c, http.StatusInternalServerError, "INTERNAL", err.Error())
}

func (s *Server) handleLocalPickFolder(c echo.Context) error {
	path, err := s.Local.PickFolder(c.Request().Context())
	switch {
	case errors.Is(err, model.ErrCancelled):
		return c.JSON(http.StatusOK, map[string]any{"cancelled": true})
	case err != nil:
		return localError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"path": path})
}

func (s *Server) handleLocalPickCancel(c echo.Context) error {
	s.Local.CancelPick()
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleLocalInspect(c echo.Context) error {
	r, err := s.Local.Inspect(c.Request().Context(), c.QueryParam("path"))
	if err != nil {
		return localError(c, err)
	}
	return c.JSON(http.StatusOK, r)
}

func (s *Server) handleLocalClone(c echo.Context) error {
	var req struct {
		URL       string `json:"url"`
		ParentDir string `json:"parent_dir"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	r, err := s.Local.Clone(c.Request().Context(), req.URL, req.ParentDir)
	if err != nil {
		return localError(c, err)
	}
	return c.JSON(http.StatusOK, r)
}

func (s *Server) handleLocalInit(c echo.Context) error {
	var req struct {
		Name      string `json:"name"`
		ParentDir string `json:"parent_dir"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	r, err := s.Local.Init(c.Request().Context(), req.Name, req.ParentDir)
	if err != nil {
		return localError(c, err)
	}
	return c.JSON(http.StatusOK, r)
}

func (s *Server) handleLocalGH(c echo.Context) error {
	installed, authenticated, account, problem := s.Local.GHStatus(c.Request().Context())
	out := map[string]any{"installed": installed, "authenticated": authenticated, "account": account}
	if problem != "" {
		out["error"] = problem
	}
	return c.JSON(http.StatusOK, out)
}
