package http

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/usecase"
)

func (s *Server) handleKBList(c echo.Context) error {
	docs, err := s.KBDocs.List(c.Request().Context(), currentUser(c), usecase.KBListParams{
		ProjectID: c.QueryParam("project_id"), Source: c.QueryParam("source"), Type: c.QueryParam("type"),
		Status: c.QueryParam("status"), Q: c.QueryParam("q"),
	})
	if err != nil {
		return mapDomainError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"docs": docs})
}

func (s *Server) handleKBGet(c echo.Context) error {
	det, err := s.KBDocs.Detail(c.Request().Context(), currentUser(c), c.QueryParam("id"))
	if err != nil {
		return mapDomainError(c, err)
	}
	return c.JSON(http.StatusOK, det)
}

func (s *Server) handleKBPut(c echo.Context) error {
	var req struct {
		Frontmatter map[string]any `json:"frontmatter"`
		Body        string         `json:"body"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	d, err := s.KBDocs.Update(c.Request().Context(), currentUser(c), c.QueryParam("id"), req.Frontmatter, req.Body)
	if errors.Is(err, usecase.ErrForbidden) {
		return errorJSON(c, http.StatusForbidden, "FORBIDDEN", "only admins edit factory-wide documents")
	}
	if err != nil {
		return mapDomainError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"doc": d})
}

func (s *Server) handleKBApprove(c echo.Context) error {
	var req struct {
		Note string `json:"note"`
	}
	_ = c.Bind(&req)
	d, err := s.KBDocs.Approve(c.Request().Context(), currentUser(c), c.QueryParam("id"), req.Note)
	if err != nil {
		return mapDomainError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"doc": d})
}

func (s *Server) handleKBDiscard(c echo.Context) error {
	if err := s.KBDocs.Discard(c.Request().Context(), currentUser(c), c.QueryParam("id")); err != nil {
		return mapDomainError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (s *Server) handleKBReset(c echo.Context) error {
	d, err := s.KBDocs.Reset(c.Request().Context(), currentUser(c), c.QueryParam("id"))
	if err != nil {
		return mapDomainError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"doc": d})
}

func (s *Server) handleKBDelete(c echo.Context) error {
	err := s.KBDocs.Delete(c.Request().Context(), currentUser(c), c.QueryParam("id"))
	if errors.Is(err, usecase.ErrForbidden) {
		return errorJSON(c, http.StatusForbidden, "FORBIDDEN", "only admins delete factory-wide documents")
	}
	if err != nil {
		return mapDomainError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
