package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"legos-factory/backend/internal/execenv"
	"legos-factory/backend/internal/orchestration"
	"legos-factory/backend/internal/project"
	"legos-factory/backend/internal/workorder"
)

func shortUUID() string { return uuid.NewString()[:12] }

// ---- DTOs ----

type createProjectReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	RepoURL     string `json:"repo_url"`
	Language    string `json:"language"`
}

func (r *createProjectReq) validate() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(r.Name) > 200 {
		return fmt.Errorf("name too long")
	}
	return nil
}

func (s *Server) handleListProjects(c echo.Context) error {
	projects, err := s.ProjRepo.List(c.Request().Context())
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not list projects")
	}
	out := make([]project.Project, 0, len(projects))
	for _, p := range projects {
		p.QualityProfile = "" // not exposed in list view
		out = append(out, p)
	}
	return c.JSON(http.StatusOK, map[string]any{"projects": out})
}

func (s *Server) handleCreateProject(c echo.Context) error {
	var req createProjectReq
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	if err := req.validate(); err != nil {
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
	}
	p := &project.Project{
		Name: req.Name, Description: req.Description, RepoURL: req.RepoURL,
		Language: req.Language,
	}
	if p.Language == "" {
		p.Language = "go"
	}
	if err := s.ProjRepo.Create(c.Request().Context(), p); err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not create project")
	}
	return c.JSON(http.StatusCreated, p)
}

func (s *Server) handleGetProject(c echo.Context) error {
	p, err := s.ProjRepo.Get(c.Request().Context(), c.Param("id"))
	if err != nil {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "project not found")
	}
	return c.JSON(http.StatusOK, p)
}

// ---- work orders ----

type createWorkOrderReq struct {
	ProjectID      string `json:"project_id"`
	Title          string `json:"title"`
	Objective      string `json:"objective"`
	Acceptance     string `json:"acceptance"`
	Autonomy       string `json:"autonomy"`
	BuilderModel   string `json:"builder_model"`
	InspectorModel string `json:"inspector_model"`
}

func (r *createWorkOrderReq) validate() error {
	r.Title = strings.TrimSpace(r.Title)
	r.Objective = strings.TrimSpace(r.Objective)
	r.Acceptance = strings.TrimSpace(r.Acceptance)
	switch {
	case r.Title == "":
		return fmt.Errorf("title is required")
	case r.Objective == "":
		return fmt.Errorf("objective is required")
	case r.ProjectID == "":
		return fmt.Errorf("project_id is required")
	}
	switch r.Autonomy {
	case "", "guided", "assisted", "delegated":
	default:
		return fmt.Errorf("autonomy must be guided, assisted or delegated")
	}
	return nil
}

func (s *Server) handleCreateWorkOrder(c echo.Context) error {
	var req createWorkOrderReq
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	if err := req.validate(); err != nil {
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
	}
	u := currentUser(c)
	// verify project exists (authorization enforced at workspace boundary)
	proj, err := s.ProjRepo.Get(c.Request().Context(), req.ProjectID)
	if err != nil {
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", "unknown project_id")
	}
	wo := &workorder.WorkOrder{
		ID:             "wo_" + shortUUID(),
		ProjectID:      req.ProjectID,
		Title:          req.Title,
		Objective:      req.Objective,
		Acceptance:     req.Acceptance,
		State:          workorder.StateCreated,
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
	wos, err := s.WORepo.List(c.Request().Context())
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not list work orders")
	}
	// server-side filtering
	if state := c.QueryParam("state"); state != "" {
		filtered := make([]workorder.WorkOrder, 0, len(wos))
		for _, wo := range wos {
			if string(wo.State) == strings.ToUpper(state) {
				filtered = append(filtered, wo)
			}
		}
		wos = filtered
	}
	if proj := c.QueryParam("project_id"); proj != "" {
		filtered := make([]workorder.WorkOrder, 0, len(wos))
		for _, wo := range wos {
			if wo.ProjectID == proj {
				filtered = append(filtered, wo)
			}
		}
		wos = filtered
	}
	out := make([]workorder.WorkOrder, 0, len(wos))
	out = append(out, wos...)
	if out == nil {
		out = []workorder.WorkOrder{}
	}
	return c.JSON(http.StatusOK, map[string]any{"work_orders": out})
}

func (s *Server) handleGetWorkOrder(c echo.Context) error {
	wo, err := s.WORepo.Get(c.Request().Context(), c.Param("id"))
	if err != nil {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "work order not found")
	}
	tasks, _ := s.WORepo.Plan(c.Request().Context(), wo.ID)
	runs, _ := s.runsFor(c, wo.ID)
	return c.JSON(http.StatusOK, map[string]any{
		"work_order": wo, "plan": tasksOrEmpty(tasks), "runs": runs,
	})
}

func tasksOrEmpty(t []workorder.Task) []workorder.Task {
	if t == nil {
		return []workorder.Task{}
	}
	return t
}

type decisionReq struct {
	Decision string `json:"decision"`
	Notes    string `json:"notes"`
}

func (s *Server) handlePlanGenerate(c echo.Context) error {
	wo, err := s.WORepo.Get(c.Request().Context(), c.Param("id"))
	if err != nil {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "work order not found")
	}
	if wo.State != workorder.StateCreated {
		return errorJSON(c, http.StatusConflict, "INVALID_STATE_TRANSITION",
			fmt.Sprintf("plan generation requires state CREATED, WO is %s", wo.State))
	}
	if _, err := s.Foreman.Sched.Enqueue(c.Request().Context(), orchestration.JobPlan, wo.ID, "", map[string]any{}); err != nil {
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
	var req decisionReq
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

type clarifyReq struct {
	Answer string `json:"answer"`
}

func (s *Server) handleClarification(c echo.Context) error {
	var req clarifyReq
	if err := c.Bind(&req); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	req.Answer = strings.TrimSpace(req.Answer)
	if req.Answer == "" {
		return errorJSON(c, http.StatusBadRequest, "VALIDATION_FAILED", "answer is required")
	}
	// only the owner may answer a clarification for their work order
	wo, err := s.WORepo.Get(c.Request().Context(), c.Param("id"))
	if err != nil {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "work order not found")
	}
	u := currentUser(c)
	if wo.CreatedBy != u.ID && !u.Role.IsAdmin() {
		return errorJSON(c, http.StatusForbidden, "FORBIDDEN", "only the work order owner or an admin may clarify")
	}
	if err := s.Foreman.AnswerClarification(c.Request().Context(), wo.ID, req.Answer); err != nil {
		return mapDomainError(c, err)
	}
	return c.JSON(http.StatusAccepted, map[string]string{"status": "accepted"})
}

type prDecisionReq struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func (s *Server) handlePRDecision(c echo.Context) error {
	var req prDecisionReq
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

func (s *Server) takeWO(c echo.Context) (*workorder.WorkOrder, error) {
	wo, err := s.WORepo.Get(c.Request().Context(), c.Param("id"))
	if err != nil {
		return nil, err
	}
	return wo, nil
}

func mapDomainError(c echo.Context, err error) error {
	if errors.Is(err, workorder.ErrNotFound) {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "work order not found")
	}
	if errors.Is(err, orchestration.ErrStateConflict) {
		return errorJSON(c, http.StatusConflict, "INVALID_STATE_TRANSITION", err.Error())
	}
	msg := err.Error()
	if strings.Contains(msg, "INVALID_STATE_TRANSITION") {
		return errorJSON(c, http.StatusConflict, "INVALID_STATE_TRANSITION", msg)
	}
	return errorJSON(c, http.StatusInternalServerError, "INTERNAL", msg)
}

// ---- runs / quality / findings ----

func (s *Server) runsFor(c echo.Context, woID string) ([]map[string]any, error) {
	rows, err := s.Foreman.DB.QueryContext(c.Request().Context(), `SELECT id, task_id, kind, state, harness,
		output, error, tokens_in, tokens_out, cost_micros, created_at, started_at, finished_at
		FROM runs WHERE work_order_id = ? ORDER BY created_at`, woID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, kind, state, harness string
		var taskID, output, runErr *string
		var tokensIn, tokensOut, cost int64
		var createdAt, startedAt, finishedAt *int64
		if err := rows.Scan(&id, &taskID, &kind, &state, &harness, &output, &runErr, &tokensIn, &tokensOut, &cost, &createdAt, &startedAt, &finishedAt); err != nil {
			return nil, err
		}
		entry := map[string]any{
			"id": id, "task_id": taskID, "kind": kind, "state": state, "harness": harness,
			"tokens_in": tokensIn, "tokens_out": tokensOut, "cost_micros": cost,
			"created_at": unixTime(createdAt),
		}
		if output != nil {
			entry["output"] = *output
		}
		if runErr != nil {
			entry["error"] = *runErr
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

func (s *Server) handleListRuns(c echo.Context) error {
	if _, err := s.takeWO(c); err != nil {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "work order not found")
	}
	runs, err := s.runsFor(c, c.Param("id"))
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load runs")
	}
	return c.JSON(http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleListQuality(c echo.Context) error {
	wo, err := s.takeWO(c)
	if err != nil {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "work order not found")
	}
	rows, err := s.Foreman.DB.QueryContext(c.Request().Context(), `SELECT id, name, command, outcome, exit_code, output, revision, created_at
		FROM quality_checks WHERE wo_id = ? ORDER BY created_at`, wo.ID)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load quality checks")
	}
	defer rows.Close()
	checks := []map[string]any{}
	for rows.Next() {
		var id, name, command, outcome, output string
		var exit int
		var revision int64
		var createdAt int64
		if err := rows.Scan(&id, &name, &command, &outcome, &exit, &output, &revision, &createdAt); err != nil {
			return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "quality scan failed")
		}
		checks = append(checks, map[string]any{
			"id": id, "name": name, "command": command, "outcome": outcome,
			"exit_code": exit, "output": output, "revision": revision,
			"created_at": time.Unix(createdAt, 0),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"checks": checks, "revision": wo.Revision})
}

func (s *Server) handleListFindings(c echo.Context) error {
	if _, err := s.takeWO(c); err != nil {
		return errorJSON(c, http.StatusNotFound, "NOT_FOUND", "work order not found")
	}
	rows, err := s.Foreman.DB.QueryContext(c.Request().Context(), `SELECT id, run_id, severity, title, detail, location, status, created_at
		FROM findings WHERE wo_id = ? ORDER BY created_at`, c.Param("id"))
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load findings")
	}
	defer rows.Close()
	findings := []map[string]any{}
	for rows.Next() {
		var id, runID, severity, title, detail, location, status string
		var createdAt int64
		if err := rows.Scan(&id, &runID, &severity, &title, &detail, &location, &status, &createdAt); err != nil {
			return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "findings scan failed")
		}
		findings = append(findings, map[string]any{
			"id": id, "run_id": runID, "severity": severity, "title": title,
			"detail": detail, "location": location, "status": status,
			"created_at": time.Unix(createdAt, 0),
		})
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
	wos, err := s.WORepo.List(ctx)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load work orders")
	}
	projects, err := s.ProjRepo.List(ctx)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load projects")
	}
	var active, waiting int
	for _, wo := range wos {
		switch {
		case wo.State == workorder.StateWaitingForHuman:
			waiting++
		case !wo.State.Terminal():
			active++
		}
	}
	var pendingJobs int
	_ = s.Foreman.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE state IN ('PENDING','RUNNING')`).Scan(&pendingJobs)
	var failedJobs int
	_ = s.Foreman.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE state = 'FAILED'`).Scan(&failedJobs)
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
	settings, err := s.loadSettings(c)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not load settings")
	}
	return c.JSON(http.StatusOK, settings)
}

type settingsDTO struct {
	BudgetLimitMicros int64             `json:"budget_limit_micros"`
	ArchitectModel    string            `json:"architect_model"`
	BuilderModel      string            `json:"builder_model"`
	InspectorModel    string            `json:"inspector_model"`
	AllowedModels     []string          `json:"allowed_models"`
	Extra             map[string]string `json:"extra,omitempty"`
}

func (s *Server) loadSettings(ctx echo.Context) (*settingsDTO, error) {
	rows, err := s.Foreman.DB.QueryContext(ctx.Request().Context(), `SELECT key, value FROM settings_kv`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dto := &settingsDTO{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		switch k {
		case "budget_limit_micros":
			fmt.Sscanf(v, "%d", &dto.BudgetLimitMicros)
		case "architect_model":
			dto.ArchitectModel = v
		case "builder_model":
			dto.BuilderModel = v
		case "inspector_model":
			dto.InspectorModel = v
		case "allowed_models":
			dto.AllowedModels = strings.Split(strings.Trim(v, ","), ",")
		default:
			if dto.Extra == nil {
				dto.Extra = map[string]string{}
			}
			dto.Extra[k] = v
		}
	}
	return dto, rows.Err()
}

func (s *Server) handlePutSettings(c echo.Context) error {
	var dto settingsDTO
	if err := c.Bind(&dto); err != nil {
		return errorJSON(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
	}
	ctx := c.Request().Context()
	writes := map[string]string{
		"budget_limit_micros": fmt.Sprintf("%d", dto.BudgetLimitMicros),
		"architect_model":     dto.ArchitectModel,
		"builder_model":       dto.BuilderModel,
		"inspector_model":     dto.InspectorModel,
	}
	if dto.AllowedModels != nil {
		writes["allowed_models"] = strings.Trim(strings.Join(dto.AllowedModels, ","), ",")
	}
	for k, v := range writes {
		if _, err := s.Foreman.DB.ExecContext(ctx, `INSERT INTO settings_kv (key, value) VALUES (?,?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not save settings")
		}
	}
	s.Usage.SetBudgetLimit(dto.BudgetLimitMicros)
	if u := currentUser(c); u != nil && !u.Role.IsAdmin() {
		return errorJSON(c, http.StatusForbidden, "FORBIDDEN", "admin role required") // unreachable: route guards
	}
	out, err := s.loadSettings(c)
	if err != nil {
		return errorJSON(c, http.StatusInternalServerError, "INTERNAL", "could not reload settings")
	}
	return c.JSON(http.StatusOK, out)
}

func unixTime(p *int64) any {
	if p == nil {
		return nil
	}
	return time.Unix(*p, 0)
}
