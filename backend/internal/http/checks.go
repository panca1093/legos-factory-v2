package http

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

func checksError(c echo.Context, err error) error {
	switch {
	case usecase.IsNotFound(err):
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "project not found")
	case errors.Is(err, usecase.ErrCheckSuggestBusy):
		return errorJSON(c, http.StatusConflict, "CHECK_SUGGEST_RUNNING", err.Error())
	case errors.Is(err, usecase.ErrCheckSuggestUnavailable):
		return errorJSON(c, http.StatusServiceUnavailable, "CHECK_SUGGEST_UNAVAILABLE", err.Error())
	case errors.Is(err, usecase.ErrInvalid):
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
	}
	return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not complete the request")
}

func (s *Server) checksReady(c echo.Context) bool {
	if s.Checks == nil {
		_ = errorJSON(c, http.StatusServiceUnavailable, "CHECK_SUGGEST_UNAVAILABLE", usecase.ErrCheckSuggestUnavailable.Error())
		return false
	}
	return true
}

func (s *Server) handleSuggestChecks(c echo.Context) error {
	if !s.checksReady(c) {
		return nil
	}
	if err := s.Checks.Start(c.Request().Context(), currentUser(c), c.Param("id")); err != nil {
		return checksError(c, err)
	}
	return c.JSON(http.StatusAccepted, map[string]any{"run": usecase.CheckRunStatus{Status: "running"}})
}

func (s *Server) handleGetChecks(c echo.Context) error {
	if !s.checksReady(c) {
		return nil
	}
	st, err := s.Checks.Status(c.Request().Context(), currentUser(c), c.Param("id"))
	if err != nil {
		return checksError(c, err)
	}
	return c.JSON(http.StatusOK, st)
}

func (s *Server) handleApproveChecks(c echo.Context) error {
	if !s.checksReady(c) {
		return nil
	}
	var req struct {
		Profile model.Profile `json:"profile"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	p, err := s.Checks.Approve(c.Request().Context(), currentUser(c), c.Param("id"), req.Profile)
	if err != nil {
		return checksError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"profile": p})
}

func (s *Server) handleStopChecks(c echo.Context) error {
	if !s.checksReady(c) {
		return nil
	}
	if err := s.Checks.Stop(c.Request().Context(), currentUser(c), c.Param("id")); err != nil {
		return checksError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleDiscardChecks(c echo.Context) error {
	if !s.checksReady(c) {
		return nil
	}
	if err := s.Checks.Discard(c.Request().Context(), currentUser(c), c.Param("id")); err != nil {
		return checksError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
