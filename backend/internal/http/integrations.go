package http

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

// integrationError maps connection and provider failures; provider messages
// are passed through because they explain scope or credential problems.
func integrationError(c echo.Context, err error) error {
	var pe *model.ProviderError
	switch {
	case errors.Is(err, usecase.ErrForbidden):
		return errorJSON(c, http.StatusForbidden, "FORBIDDEN", "only admins manage shared connections")
	case errors.Is(err, usecase.ErrInvalid):
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
	case errors.Is(err, model.ErrSecretsUnavailable):
		return errorJSON(c, http.StatusServiceUnavailable, "SECRETS_UNAVAILABLE", "credential storage is not configured; set FACTORY_SECRET_KEY")
	case errors.Is(err, model.ErrConnectionDisabled):
		return errorJSON(c, http.StatusConflict, "CONNECTION_DISABLED", "connection is disabled")
	case usecase.IsNotFound(err):
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "not found")
	case errors.As(err, &pe):
		return errorJSON(c, http.StatusBadGateway, "PROVIDER_ERROR", pe.Error())
	default:
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}

func (s *Server) handleListConnections(c echo.Context) error {
	conns, err := s.Integrations.List(c.Request().Context(), currentUser(c))
	if err != nil {
		return integrationError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"connections": conns, "credentials_configured": s.Integrations.Secrets != nil})
}

func (s *Server) handleCreateConnection(c echo.Context) error {
	var req model.CreateConnectionRequest
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	if err := req.Validate(); err != nil {
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
	}
	conn, err := s.Integrations.Create(c.Request().Context(), currentUser(c), req)
	if err != nil {
		return integrationError(c, err)
	}
	return c.JSON(http.StatusCreated, conn)
}

func (s *Server) handleUpdateConnection(c echo.Context) error {
	var req model.UpdateConnectionRequest
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	conn, err := s.Integrations.Update(c.Request().Context(), currentUser(c), c.Param("id"), req)
	if err != nil {
		return integrationError(c, err)
	}
	return c.JSON(http.StatusOK, conn)
}

func (s *Server) handleDeleteConnection(c echo.Context) error {
	if err := s.Integrations.Delete(c.Request().Context(), currentUser(c), c.Param("id")); err != nil {
		return integrationError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleTestConnection(c echo.Context) error {
	conn, err := s.Integrations.Test(c.Request().Context(), currentUser(c), c.Param("id"))
	if err != nil {
		return integrationError(c, err)
	}
	return c.JSON(http.StatusOK, conn)
}

func (s *Server) handleBindProject(c echo.Context) error {
	var req model.ProjectBindingRequest
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	p, err := s.Integrations.Bind(c.Request().Context(), currentUser(c), c.Param("id"), req)
	if err != nil {
		return integrationError(c, err)
	}
	return c.JSON(http.StatusOK, p)
}

func (s *Server) handleJiraLookup(c echo.Context) error {
	var req model.IssueLookupRequest
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	draft, err := s.Integrations.LookupIssue(c.Request().Context(), currentUser(c), req)
	if err != nil {
		return integrationError(c, err)
	}
	return c.JSON(http.StatusOK, draft)
}
