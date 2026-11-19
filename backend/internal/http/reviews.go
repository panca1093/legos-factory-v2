package http

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

func (s *Server) needReviews(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if s.Reviews == nil {
			return errorJSON(c, http.StatusServiceUnavailable, "NOT_CONFIGURED", "pull request review is not configured")
		}
		return next(c)
	}
}

// reviewError maps review failures to the error envelope; provider failures keep their reason.
func (s *Server) reviewError(c echo.Context, err error) error {
	var conflict *usecase.PostConflict
	switch {
	case errors.As(err, &conflict):
		return c.JSON(http.StatusConflict, map[string]any{
			"error":   model.APIError{Code: "CONFLICT", Message: conflict.Reason, RequestID: c.Response().Header().Get(echo.HeaderXRequestID)},
			"current": conflict.Current,
		})
	case usecase.IsNotFound(err) || errors.Is(err, usecase.ErrReviewNotFound):
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "not found")
	case errors.Is(err, usecase.ErrInvalid):
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
	case errors.Is(err, sql.ErrNoRows):
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "not found")
	case errors.Is(err, usecase.ErrPostInFlight):
		return errorJSON(c, http.StatusConflict, "POST_IN_PROGRESS", err.Error())
	case errors.Is(err, usecase.ErrReviewRunning):
		return errorJSON(c, http.StatusConflict, "RUN_IN_PROGRESS", err.Error())
	case errors.Is(err, usecase.ErrAIUnavailable):
		return errorJSON(c, http.StatusUnprocessableEntity, "AI_UNAVAILABLE", err.Error())
	case errors.Is(err, usecase.ErrCLIUnavailable):
		return errorJSON(c, http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, usecase.ErrReadOnlyToken):
		return errorJSON(c, http.StatusForbidden, "READ_ONLY_TOKEN", err.Error())
	case errors.Is(err, usecase.ErrIdentityChanged):
		return errorJSON(c, http.StatusConflict, "IDENTITY_CHANGED", err.Error())
	case errors.Is(err, usecase.ErrIdentityAmbiguous):
		return errorJSON(c, http.StatusConflict, "IDENTITY_AMBIGUOUS", err.Error())
	case errors.Is(err, usecase.ErrNoReviewIdentity):
		return errorJSON(c, http.StatusConflict, "NO_IDENTITY", err.Error())
	case errors.Is(err, model.ErrConnectionDisabled):
		return errorJSON(c, http.StatusConflict, "CONNECTION_DISABLED", "connection is disabled")
	}
	pe := s.Reviews.ProviderErrOf(err)
	switch pe.Code {
	case model.ErrCodeRateLimit:
		return errorJSON(c, http.StatusTooManyRequests, "RATE_LIMITED", pe.Message)
	case model.ErrCodeNotFound:
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", pe.Message)
	case model.ErrCodeForbidden:
		return errorJSON(c, http.StatusForbidden, "FORBIDDEN", pe.Message)
	case model.ErrCodeNoIdentity:
		return errorJSON(c, http.StatusConflict, "NO_IDENTITY", pe.Message)
	}
	return errorJSON(c, http.StatusBadGateway, "PROVIDER_ERROR", pe.Message)
}

func prNumber(c echo.Context) (int, error) {
	n, err := strconv.Atoi(c.Param("number"))
	if err != nil || n <= 0 {
		return 0, errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", "pull request number must be a positive integer")
	}
	return n, nil
}

// pr wraps a handler that works on one pull request of a project.
func (s *Server) pr(h func(c echo.Context, number int) error) echo.HandlerFunc {
	return s.needReviews(func(c echo.Context) error {
		n, err := prNumber(c)
		if err != nil {
			return err
		}
		return h(c, n)
	})
}

func (s *Server) reply(c echo.Context, status int, v any, err error) error {
	if err != nil {
		return s.reviewError(c, err)
	}
	return c.JSON(status, v)
}

func (s *Server) handlePullRequests(c echo.Context) error {
	v, err := s.Reviews.PullRequests(c.Request().Context(), currentUser(c), c.Param("id"), c.QueryParam("filter"))
	return s.reply(c, http.StatusOK, v, err)
}

func (s *Server) handleAllReviews(c echo.Context) error {
	v, err := s.Reviews.AllReviews(c.Request().Context(), currentUser(c), c.QueryParam("filter"))
	return s.reply(c, http.StatusOK, v, err)
}

func (s *Server) handleProjectsSummary(c echo.Context) error {
	v, err := s.Reviews.Summary(c.Request().Context(), currentUser(c))
	return s.reply(c, http.StatusOK, v, err)
}

func (s *Server) handlePullDetail(c echo.Context, n int) error {
	v, err := s.Reviews.Detail(c.Request().Context(), currentUser(c), c.Param("id"), n)
	return s.reply(c, http.StatusOK, v, err)
}

func (s *Server) handlePullFiles(c echo.Context, n int) error {
	v, err := s.Reviews.Files(c.Request().Context(), currentUser(c), c.Param("id"), n)
	return s.reply(c, http.StatusOK, v, err)
}

func (s *Server) handleStartReviewRun(c echo.Context, n int) error {
	v, err := s.Reviews.StartRun(c.Request().Context(), currentUser(c), c.Param("id"), n)
	return s.reply(c, http.StatusAccepted, v, err)
}

func (s *Server) handleReviewDrafts(c echo.Context, n int) error {
	v, err := s.Reviews.Drafts(c.Request().Context(), currentUser(c), c.Param("id"), n)
	return s.reply(c, http.StatusOK, v, err)
}

func (s *Server) handlePatchDraft(c echo.Context, n int) error {
	var req struct {
		Status *string `json:"status"`
		Body   *string `json:"body"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	v, err := s.Reviews.UpdateDraft(c.Request().Context(), currentUser(c), c.Param("id"), n, c.Param("draft"), req.Status, req.Body)
	return s.reply(c, http.StatusOK, v, err)
}

func (s *Server) handleSetViewed(c echo.Context, n int) error {
	var req struct {
		Path   string `json:"path"`
		Viewed bool   `json:"viewed"`
	}
	if err := c.Bind(&req); err != nil || req.Path == "" {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "path and viewed are required")
	}
	v, err := s.Reviews.SetViewed(c.Request().Context(), currentUser(c), c.Param("id"), n, req.Path, req.Viewed)
	return s.reply(c, http.StatusOK, v, err)
}

func (s *Server) handlePatchReview(c echo.Context, n int) error {
	var req struct {
		Summary *string `json:"summary"`
		Verdict *string `json:"verdict"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	v, err := s.Reviews.UpdateReview(c.Request().Context(), currentUser(c), c.Param("id"), n, req.Summary, req.Verdict)
	return s.reply(c, http.StatusOK, v, err)
}

func (s *Server) handlePostReview(c echo.Context, n int) error {
	var req model.PostRequest
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	v, err := s.Reviews.Post(c.Request().Context(), currentUser(c), c.Param("id"), n, req)
	return s.reply(c, http.StatusOK, v, err)
}

func (s *Server) handleReviewIdentities(c echo.Context) error {
	v, err := s.Reviews.Identities(c.Request().Context(), currentUser(c))
	return s.reply(c, http.StatusOK, v, err)
}

func (s *Server) handlePinReviewIdentity(c echo.Context) error {
	var req model.PinRequest
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	v, err := s.Reviews.Pin(c.Request().Context(), currentUser(c), req)
	return s.reply(c, http.StatusOK, v, err)
}

func (s *Server) reviewRoutes(api *echo.Group) {
	write := func(h echo.HandlerFunc) echo.HandlerFunc { return s.requireRole(model.RoleEngineer, h) }
	api.GET("/projects/summary", s.needReviews(s.handleProjectsSummary))
	api.GET("/reviews", s.needReviews(s.handleAllReviews))
	api.GET("/projects/:id/pull-requests", s.needReviews(s.handlePullRequests))
	const pr = "/projects/:id/pull-requests/:number"
	api.GET(pr, s.pr(s.handlePullDetail))
	api.GET(pr+"/files", s.pr(s.handlePullFiles))
	api.POST(pr+"/review/runs", write(s.pr(s.handleStartReviewRun)))
	api.GET(pr+"/review/drafts", s.pr(s.handleReviewDrafts))
	api.PATCH(pr+"/review/drafts/:draft", write(s.pr(s.handlePatchDraft)))
	api.PUT(pr+"/review/viewed", write(s.pr(s.handleSetViewed)))
	api.PATCH(pr+"/review", write(s.pr(s.handlePatchReview)))
	api.POST(pr+"/review/post", write(s.pr(s.handlePostReview)))
	api.GET("/review-identities", s.needReviews(s.handleReviewIdentities))
	api.PUT("/review-identities", write(s.needReviews(s.handlePinReviewIdentity)))
	api.POST("/review-identities/cli/login", write(s.needReviews(s.handleCLILogin)))
}

func (s *Server) handleCLILogin(c echo.Context) error {
	var req struct {
		Tool string `json:"tool"`
		Host string `json:"host"`
	}
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	id, err := s.Reviews.StartCLILogin(c.Request().Context(), currentUser(c), req.Tool, req.Host)
	return s.reply(c, http.StatusOK, map[string]string{"login_id": id}, err)
}
