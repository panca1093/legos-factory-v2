package http

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/model"
)

func (s *Server) mcpReady(c echo.Context) bool {
	if s.MCP == nil {
		_ = errorJSON(c, http.StatusServiceUnavailable, "UNAVAILABLE", "MCP server management is not configured")
		return false
	}
	return true
}

func (s *Server) handleListMCPServers(c echo.Context) error {
	if !s.mcpReady(c) {
		return nil
	}
	list, err := s.MCP.List(c.Request().Context())
	if err != nil {
		return integrationError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"servers": list, "credentials_configured": s.MCP.Secrets != nil})
}

func (s *Server) handleCreateMCPServer(c echo.Context) error {
	if !s.mcpReady(c) {
		return nil
	}
	var req model.CreateMCPServer
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	v, err := s.MCP.Create(c.Request().Context(), req)
	if err != nil {
		return integrationError(c, err)
	}
	return c.JSON(http.StatusCreated, v)
}

func (s *Server) handleUpdateMCPServer(c echo.Context) error {
	if !s.mcpReady(c) {
		return nil
	}
	var req model.UpdateMCPServer
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	v, err := s.MCP.Update(c.Request().Context(), c.Param("id"), req)
	if err != nil {
		return integrationError(c, err)
	}
	return c.JSON(http.StatusOK, v)
}

func (s *Server) handleDeleteMCPServer(c echo.Context) error {
	if !s.mcpReady(c) {
		return nil
	}
	if err := s.MCP.Delete(c.Request().Context(), c.Param("id")); err != nil {
		return integrationError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleDiscoverMCPServer(c echo.Context) error {
	if !s.mcpReady(c) {
		return nil
	}
	v, err := s.MCP.Discover(c.Request().Context(), c.Param("id"))
	if errors.Is(err, model.ErrStateConflict) {
		return errorJSON(c, http.StatusConflict, "MCP_SERVER_CHANGED", err.Error())
	}
	if err != nil {
		return integrationError(c, err)
	}
	return c.JSON(http.StatusOK, v)
}

func (s *Server) handleSetMCPRoles(c echo.Context) error {
	if !s.mcpReady(c) {
		return nil
	}
	var req struct {
		Roles []string `json:"roles"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	v, err := s.MCP.SetRoles(c.Request().Context(), c.Param("id"), req.Roles)
	if err != nil {
		return integrationError(c, err)
	}
	return c.JSON(http.StatusOK, v)
}

func (s *Server) handleSetMCPTools(c echo.Context) error {
	if !s.mcpReady(c) {
		return nil
	}
	var req []model.MCPToolToggle
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "body must be a JSON array of {name, enabled}")
	}
	v, err := s.MCP.SetTools(c.Request().Context(), c.Param("id"), req)
	if err != nil {
		return integrationError(c, err)
	}
	return c.JSON(http.StatusOK, v)
}
