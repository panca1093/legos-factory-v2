package http

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

func (s *Server) handleListKnowledge(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	q := model.KnowledgeQuery{
		WOID:  c.QueryParam("work_order_id"),
		Trust: model.Trust(c.QueryParam("trust")),
		Text:  c.QueryParam("q"),
		Limit: limit,
	}
	switch q.Trust {
	case "", model.TrustVerified, model.TrustHumanDecision, model.TrustAIInterpretation:
	default:
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", "trust must be verified, human_decision or ai_interpretation")
	}
	records, err := s.Catalog.Find(c.Request().Context(), s.Projects, currentUser(c), c.QueryParam("project_id"), q)
	if err != nil {
		if usecase.IsNotFound(err) {
			return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "project not found")
		}
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not search knowledge")
	}
	return c.JSON(http.StatusOK, map[string]any{"records": records})
}

func (s *Server) handleGetKnowledge(c echo.Context) error {
	k, err := s.Catalog.Inspect(c.Request().Context(), s.Projects, currentUser(c), c.Param("id"))
	if err != nil {
		if usecase.IsNotFound(err) {
			return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "knowledge record not found")
		}
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load knowledge record")
	}
	return c.JSON(http.StatusOK, k)
}

// ---- project membership ----

func (s *Server) handleListMembers(c echo.Context) error {
	ctx := c.Request().Context()
	if err := s.Projects.Authorize(ctx, currentUser(c), c.Param("id")); err != nil {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "project not found")
	}
	members, err := s.Projects.ListMembers(ctx, c.Param("id"))
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not list members")
	}
	return c.JSON(http.StatusOK, map[string]any{"members": members})
}

func (s *Server) handleAddMember(c echo.Context) error {
	var req model.AddMemberRequest
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" {
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", "email is required")
	}
	if err := s.Projects.AddMember(c.Request().Context(), c.Param("id"), req.Email); err != nil {
		if usecase.IsNotFound(err) {
			return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "project or user not found")
		}
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not add member")
	}
	return s.handleListMembers(c)
}

func (s *Server) handleRemoveMember(c echo.Context) error {
	if err := s.Projects.RemoveMember(c.Request().Context(), c.Param("id"), c.Param("user_id")); err != nil {
		if usecase.IsNotFound(err) {
			return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "project not found")
		}
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not remove member")
	}
	return c.NoContent(http.StatusNoContent)
}
