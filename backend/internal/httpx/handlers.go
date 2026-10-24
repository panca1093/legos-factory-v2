package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/execenv"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/usecase"
)

func shortUUID() string { return uuid.NewString()[:12] }

func (s *Server) handleListProjects(c echo.Context) error {
	projects, err := s.Projects.List(c.Request().Context(), currentUser(c))
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not list projects")
	}
	out := make([]model.Project, 0, len(projects))
	for _, p := range projects {
		p.QualityProfile = "" // not exposed in list view
		out = append(out, p)
	}
	return c.JSON(http.StatusOK, map[string]any{"projects": out})
}

func (s *Server) handleCreateProject(c echo.Context) error {
	var req model.CreateProjectRequest
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	if err := req.Validate(); err != nil {
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
	}
	p := &model.Project{
		Name: req.Name, Description: req.Description, RepoURL: req.RepoURL,
		Language: req.Language,
	}
	if p.Language == "" {
		p.Language = "go"
	}
	if err := s.Projects.Create(c.Request().Context(), currentUser(c), p); err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not create project")
	}
	return c.JSON(http.StatusCreated, p)
}

func (s *Server) handleGetProject(c echo.Context) error {
	p, err := s.Projects.Get(c.Request().Context(), currentUser(c), c.Param("id"))
	if err != nil {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "project not found")
	}
	return c.JSON(http.StatusOK, p)
}

// ---- work orders ----

func (s *Server) handleCreateWorkOrder(c echo.Context) error {
	var req model.CreateWorkOrderRequest
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	if err := req.Validate(); err != nil {
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
	}
	u := currentUser(c)
	// unknown and inaccessible projects are indistinguishable to the caller
	proj, err := s.Projects.Get(c.Request().Context(), u, req.ProjectID)
	if err != nil {
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", "unknown project_id")
	}
	wo := &model.WorkOrder{
		ID:             "wo_" + shortUUID(),
		ProjectID:      req.ProjectID,
		Title:          req.Title,
		Objective:      req.Objective,
		Acceptance:     req.Acceptance,
		State:          model.StateCreated,
		Autonomy:       req.Autonomy,
		BuilderModel:   req.BuilderModel,
		InspectorModel: req.InspectorModel,
		CreatedBy:      u.ID,
	}
	if err := s.WORepo.Create(c.Request().Context(), wo); err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not create work order")
	}
	// isolated workspace: builders never touch the project source dir
	if _, err := execenv.Prepare(c.Request().Context(), s.Cfg.DataDir, wo.ID, proj.RepoURL); err != nil {
		return errorJSON(c, http.StatusInternalServerError, "WORKSPACE_FAILED", fmt.Sprintf("could not prepare workspace: %v", err))
	}
	return c.JSON(http.StatusCreated, wo)
}

func (s *Server) handleListWorkOrders(c echo.Context) error {
	wos, err := s.visibleWorkOrders(c)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not list work orders")
	}
	// server-side filtering
	if state := c.QueryParam("state"); state != "" {
		filtered := make([]model.WorkOrder, 0, len(wos))
		for _, wo := range wos {
			if string(wo.State) == strings.ToUpper(state) {
				filtered = append(filtered, wo)
			}
		}
		wos = filtered
	}
	if proj := c.QueryParam("project_id"); proj != "" {
		filtered := make([]model.WorkOrder, 0, len(wos))
		for _, wo := range wos {
			if wo.ProjectID == proj {
				filtered = append(filtered, wo)
			}
		}
		wos = filtered
	}
	out := make([]model.WorkOrder, 0, len(wos))
	out = append(out, wos...)
	if out == nil {
		out = []model.WorkOrder{}
	}
	return c.JSON(http.StatusOK, map[string]any{"work_orders": out})
}

func (s *Server) handleGetWorkOrder(c echo.Context) error {
	wo := woFrom(c)
	tasks, _ := s.WORepo.Plan(c.Request().Context(), wo.ID)
	runs, _ := s.Runs.List(c.Request().Context(), wo.ID)
	return c.JSON(http.StatusOK, map[string]any{
		"work_order": wo, "plan": tasksOrEmpty(tasks), "runs": runs,
	})
}

func tasksOrEmpty(t []model.Task) []model.Task {
	if t == nil {
		return []model.Task{}
	}
	return t
}

func (s *Server) handlePlanGenerate(c echo.Context) error {
	wo := woFrom(c)
	resumable := map[model.State]bool{
		model.StateCreated: true, model.StateRequirement: true,
		model.StateDecomposition: true, model.StatePlanGeneration: true,
		model.StatePlanning: true,
	}
	if !resumable[wo.State] {
		return errorJSON(c, http.StatusConflict, "INVALID_STATE_TRANSITION",
			fmt.Sprintf("plan generation requires a pre-execution state, WO is %s", wo.State))
	}
	if _, err := s.Foreman.Sched.Enqueue(c.Request().Context(), usecase.JobPlan, wo.ID, "", map[string]any{}); err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not enqueue planning job")
	}
	return c.JSON(http.StatusAccepted, map[string]string{"status": "queued"})
}

func (s *Server) handleGetPlan(c echo.Context) error {
	tasks, err := s.WORepo.Plan(c.Request().Context(), c.Param("id"))
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load plan")
	}
	return c.JSON(http.StatusOK, map[string]any{"tasks": tasksOrEmpty(tasks)})
}

func (s *Server) handlePlanDecision(c echo.Context) error {
	var req model.PlanDecisionRequest
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	woID := c.Param("id")
	var err error
	switch strings.ToLower(req.Decision) {
	case "approve":
		err = s.Foreman.ApprovePlan(c.Request().Context(), woID)
	case "revise":
		err = s.Foreman.RevisePlan(c.Request().Context(), woID, req.Notes)
	default:
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", "decision must be approve or revise")
	}
	if err != nil {
		return mapDomainError(c, err)
	}
	return c.JSON(http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) handleClarification(c echo.Context) error {
	var req model.ClarificationRequest
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	req.Answer = strings.TrimSpace(req.Answer)
	if req.Answer == "" {
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", "answer is required")
	}
	// only the owner may answer a clarification for their work order
	wo := woFrom(c)
	u := currentUser(c)
	if wo.CreatedBy != u.ID && !u.Role.IsAdmin() {
		return errorJSON(c, http.StatusForbidden, "FORBIDDEN", "only the work order owner or an admin may clarify")
	}
	if err := s.Foreman.AnswerClarification(c.Request().Context(), wo.ID, req.Answer); err != nil {
		return mapDomainError(c, err)
	}
	return c.JSON(http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) handlePRDecision(c echo.Context) error {
	var req model.PRDecisionRequest
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	switch strings.ToLower(req.Decision) {
	case "approve", "request_changes", "decline":
	default:
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", "decision must be approve, request_changes or decline")
	}
	if err := s.Foreman.HumanPRDecision(c.Request().Context(), c.Param("id"), strings.ToLower(req.Decision), req.Reason); err != nil {
		return mapDomainError(c, err)
	}
	return c.JSON(http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) handleCancel(c echo.Context) error {
	if err := s.Foreman.Cancel(c.Request().Context(), c.Param("id")); err != nil {
		return mapDomainError(c, err)
	}
	return c.JSON(http.StatusAccepted, map[string]string{"status": "cancelled"})
}

// visibleWorkOrders lists work orders in projects the caller may access.
func (s *Server) visibleWorkOrders(c echo.Context) ([]model.WorkOrder, error) {
	ctx := c.Request().Context()
	wos, err := s.WORepo.List(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := s.Projects.Scope(ctx, currentUser(c))
	if err != nil || scope == nil {
		return wos, err
	}
	allowed := make(map[string]bool, len(scope))
	for _, id := range scope {
		allowed[id] = true
	}
	out := make([]model.WorkOrder, 0, len(wos))
	for _, wo := range wos {
		if allowed[wo.ProjectID] {
			out = append(out, wo)
		}
	}
	return out, nil
}

func mapDomainError(c echo.Context, err error) error {
	if errors.Is(err, model.ErrWorkOrderNotFound) {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "work order not found")
	}
	if errors.Is(err, model.ErrStateConflict) {
		return errorJSON(c, http.StatusConflict, "INVALID_STATE_TRANSITION", err.Error())
	}
	msg := err.Error()
	if strings.Contains(msg, "INVALID_STATE_TRANSITION") {
		return errorJSON(c, http.StatusConflict, "INVALID_STATE_TRANSITION", msg)
	}
	return errorJSON(c, http.StatusInternalServerError, "INTERNAL", msg)
}

// ---- runs / quality / findings ----

func (s *Server) handleListRuns(c echo.Context) error {
	runs, err := s.Runs.List(c.Request().Context(), c.Param("id"))
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load runs")
	}
	return c.JSON(http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleListEvents(c echo.Context) error {
	events, err := s.WORepo.Events(c.Request().Context(), c.Param("id"), 0, 200)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load events")
	}
	if events == nil {
		events = []model.Event{}
	}
	return c.JSON(http.StatusOK, map[string]any{"events": events})
}

func (s *Server) handleListQuality(c echo.Context) error {
	wo := woFrom(c)
	checks, err := s.Quality.List(c.Request().Context(), wo.ID)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load quality checks")
	}
	return c.JSON(http.StatusOK, map[string]any{"checks": checks, "revision": wo.Revision})
}

func (s *Server) handleListFindings(c echo.Context) error {
	findings, err := s.Findings.List(c.Request().Context(), c.Param("id"))
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load findings")
	}
	return c.JSON(http.StatusOK, map[string]any{"findings": findings})
}

// ---- models / usage / overview / settings ----

func (s *Server) handleModels(c echo.Context) error {
	if s.Models == nil {
		return errorJSON(c, http.StatusServiceUnavailable, "MODELS_UNAVAILABLE", "no model provider configured")
	}
	models, err := s.Models(c)
	if err != nil {
		return errorJSON(c, http.StatusBadGateway, "MODELS_UNAVAILABLE", err.Error())
	}
	return c.JSON(http.StatusOK, map[string]any{"models": models})
}

func (s *Server) handleUsage(c echo.Context) error {
	summary, err := s.Usage.Summary(c.Request().Context())
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load usage")
	}
	limit, spent, reserved, available := s.Usage.BudgetStatus()
	return c.JSON(http.StatusOK, map[string]any{
		"usage": summary,
		"budget": map[string]int64{
			"limit_micros": limit, "spent_micros": spent, "reserved_micros": reserved, "available_micros": available,
		},
	})
}

func (s *Server) handleOverview(c echo.Context) error {
	ctx := c.Request().Context()
	wos, err := s.visibleWorkOrders(c)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load work orders")
	}
	projects, err := s.Projects.List(ctx, currentUser(c))
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load projects")
	}
	var active, waiting int
	for _, wo := range wos {
		switch {
		case wo.State == model.StateWaitingForHuman:
			waiting++
		case !wo.State.Terminal():
			active++
		}
	}
	pendingJobs := s.Jobs.CountPendingOrRunning(ctx)
	failedJobs := s.Jobs.CountFailed(ctx)
	summary, _ := s.Usage.Summary(ctx)
	limit, spent, reserved, available := s.Usage.BudgetStatus()
	u := currentUser(c)
	_ = u
	return c.JSON(http.StatusOK, map[string]any{
		"metrics": map[string]int{
			"active_work_orders":   active,
			"waiting_for_human":    waiting,
			"pending_jobs":         pendingJobs,
			"failed_jobs":          failedJobs,
			"projects":             len(projects),
			"total_work_orders":    len(wos),
		},
		"usage": summary,
		"budget": map[string]int64{
			"limit_micros": limit, "spent_micros": spent, "reserved_micros": reserved, "available_micros": available,
		},
		"user": currentUser(c),
	})
}

func (s *Server) handleGetSettings(c echo.Context) error {
	settings, err := s.Settings.Get(c.Request().Context())
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load settings")
	}
	return c.JSON(http.StatusOK, settings)
}

func (s *Server) handlePutSettings(c echo.Context) error {
	var dto model.Settings
	if err := c.Bind(&dto); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	ctx := c.Request().Context()
	if err := s.Settings.Put(ctx, dto); err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not save settings")
	}
	s.Usage.SetBudgetLimit(dto.BudgetLimitMicros)
	if u := currentUser(c); u != nil && !u.Role.IsAdmin() {
		return errorJSON(c, http.StatusForbidden, "FORBIDDEN", "admin role required") // unreachable: route guards
	}
	out, err := s.Settings.Get(ctx)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not reload settings")
	}
	return c.JSON(http.StatusOK, out)
}
