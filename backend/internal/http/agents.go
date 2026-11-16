package http

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

func (s *Server) agentsUnavailable(c echo.Context) error {
	return errorJSON(c, http.StatusServiceUnavailable, "AGENTS_UNAVAILABLE", "agent accounts are not configured")
}

func (s *Server) handleListAgents(c echo.Context) error {
	if s.Agents == nil {
		return s.agentsUnavailable(c)
	}
	out, err := s.Agents.List(c.Request().Context(), currentUser(c))
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load agents")
	}
	return c.JSON(http.StatusOK, out)
}

func (s *Server) handleSetAgentEnabled(c echo.Context) error {
	if s.Agents == nil {
		return s.agentsUnavailable(c)
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.Bind(&req); err != nil || req.Enabled == nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "enabled is required")
	}
	if err := s.Agents.SetEnabled(c.Request().Context(), c.Param("agent"), *req.Enabled); err != nil {
		return mapDomainError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]bool{"enabled": *req.Enabled})
}

func (s *Server) handleCreateAgentAccount(c echo.Context) error {
	if s.Agents == nil {
		return s.agentsUnavailable(c)
	}
	var req struct {
		Label string `json:"label"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	out, err := s.Agents.CreateAccount(c.Request().Context(), currentUser(c), c.Param("agent"), req.Label)
	if err != nil {
		return mapAgentError(c, err)
	}
	return c.JSON(http.StatusCreated, out)
}

func (s *Server) handleActivateAgentAccount(c echo.Context) error {
	if s.Agents == nil {
		return s.agentsUnavailable(c)
	}
	if err := s.Agents.Activate(c.Request().Context(), currentUser(c), c.Param("agent"), c.Param("aid")); err != nil {
		return mapAgentError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRefreshAgentAccount(c echo.Context) error {
	if s.Agents == nil {
		return s.agentsUnavailable(c)
	}
	acc, err := s.Agents.Refresh(c.Request().Context(), currentUser(c), c.Param("agent"), c.Param("aid"))
	if err != nil {
		return mapAgentError(c, err)
	}
	return c.JSON(http.StatusOK, acc)
}

func (s *Server) handleDeleteAgentAccount(c echo.Context) error {
	if s.Agents == nil {
		return s.agentsUnavailable(c)
	}
	if err := s.Agents.DeleteAccount(c.Request().Context(), currentUser(c), c.Param("agent"), c.Param("aid")); err != nil {
		return mapAgentError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handlePatchPreferences(c echo.Context) error {
	if s.Agents == nil {
		return s.agentsUnavailable(c)
	}
	var req struct {
		ClaudeUsageOptIn *bool `json:"claude_usage_opt_in"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	ctx := c.Request().Context()
	if req.ClaudeUsageOptIn != nil {
		if err := s.Agents.SetClaudeUsageOptIn(ctx, currentUser(c), *req.ClaudeUsageOptIn); err != nil {
			return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not save preferences")
		}
	}
	on, err := s.Agents.Store.ClaudeUsageOptIn(ctx, currentUser(c).ID)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load preferences")
	}
	return c.JSON(http.StatusOK, map[string]bool{"claude_usage_opt_in": on})
}

func mapAgentError(c echo.Context, err error) error {
	if errors.Is(err, model.ErrAgentAccountNotFound) || errors.Is(err, model.ErrAgentUnknown) {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	}
	if errors.Is(err, usecase.ErrInvalid) {
		return mapDomainError(c, err)
	}
	return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "agent account operation failed")
}
