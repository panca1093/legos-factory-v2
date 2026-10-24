// Package app wires the application together and owns lifecycle (start,
// graceful shutdown, background workers).
package app

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"log/slog"

	"legos-factory/backend/internal/agents"
	"legos-factory/backend/internal/agents/mock"
	"legos-factory/backend/internal/agents/opencode"
	"legos-factory/backend/internal/config"
	"legos-factory/backend/internal/eventbus"
	"legos-factory/backend/internal/httpx"
	"legos-factory/backend/internal/identity"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/orchestration"
	"legos-factory/backend/internal/scheduler"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usage"
)

type App struct {
	Cfg      *config.Config
	DBCloser func() error
	Server   *httpx.Server
	Sched    *scheduler.Scheduler
	settings *store.SettingsRepo
	log      *slog.Logger
}

// Build resolves configuration, opens the database, wires domains and
// constructs the HTTP server. It does not start background work.
func Build(cfg *config.Config, log *slog.Logger) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(cfg.DataDir, "factory.db")
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}

	bus := eventbus.New()
	woRepo := &store.WorkOrderRepo{DB: db}
	projRepo := &store.ProjectRepo{DB: db}
	runRepo := &store.RunRepo{DB: db}
	findingRepo := &store.FindingRepo{DB: db}
	qualityRepo := &store.QualityRepo{DB: db}
	jobRepo := &store.JobRepo{DB: db}
	settingsRepo := &store.SettingsRepo{DB: db}
	usageTracker := usage.NewTracker(&store.UsageRepo{DB: db})

	auth := &identity.Service{
		Users: &store.UserRepo{DB: db},
		Mode:  cfg.AuthMode,
		DevUser: model.User{
			ID:    cfg.DevUserID,
			Email: "dev@localhost", Name: cfg.DevUserName, Role: model.RoleOwner,
		},
		Log: log,
	}
	if cfg.AuthMode == "oidc" {
		auth.OIDC = &identity.OIDCClient{
			Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret,
			RedirectURL: cfg.FrontendOrigin + "/auth/oidc/callback",
			HTTP:        &http.Client{Timeout: 45 * time.Second},
		}
	}

	mockH := mock.New()
	var ocHarness *opencode.Harness
	var ocClient *opencode.Client
	if cfg.OpenCodeURL != "" {
		ocClient = opencode.NewClient(cfg.OpenCodeURL, cfg.OpenCodeUser, cfg.OpenCodePass)
		ocHarness = opencode.NewHarness(ocClient)
	}

	resolve := func(role model.AgentRole, wo *model.WorkOrder) agents.Harness {
		if ocHarness != nil {
			return ocHarness
		}
		return mockH
	}
	var modelDefaults map[string]string
	if ocHarness != nil {
		modelDefaults = discoverDefaults(ocClient)
	}

	modelFor := func(role model.AgentRole, wo *model.WorkOrder) (string, string) {
		if ocHarness == nil {
			return "mock", "mock-local"
		}
		switch role {
		case model.AgentBuilder:
			if wo.BuilderModel != "" && wo.BuilderModel != "mock-local" {
				return splitModelRef(wo.BuilderModel)
			}
		case model.AgentInspector:
			if wo.InspectorModel != "" && wo.InspectorModel != "mock-local" {
				return splitModelRef(wo.InspectorModel)
			}
		}
		def := modelDefaults[string(role)]
		if def == "" {
			def = modelDefaults["architect"]
		}
		return splitModelRef(def)
	}

	foreman := &orchestration.Foreman{
		WO: woRepo, Runs: runRepo, Findings: findingRepo, Quality: qualityRepo,
		Proj:    projectReader{repo: projRepo},
		Resolve: resolve, Model: modelFor,
		Bus: bus, Usage: usageTracker,
		DataDir:     cfg.DataDir,
		QualTimeout: 5 * time.Minute,
		Log:         log,
	}

	sched := scheduler.New(jobRepo, log, cfg.MaxConcurrentRuns)
	foreman.Sched = sched
	foreman.Register(sched)

	srv := httpx.New(cfg, log)
	srv.WORepo = woRepo
	srv.ProjRepo = projRepo
	srv.Runs = runRepo
	srv.Findings = findingRepo
	srv.Quality = qualityRepo
	srv.Jobs = jobRepo
	srv.Settings = settingsRepo
	srv.Foreman = foreman
	srv.Bus = bus
	srv.Auth = auth
	srv.Usage = usageTracker
	if ocClient != nil {
		oc := ocClient
		srv.Models = func(c echo.Context) ([]model.ModelInfo, error) {
			models, err := oc.Models(c.Request().Context())
			if err != nil {
				return nil, err
			}
			out := make([]model.ModelInfo, 0, len(models))
			for _, m := range models {
				out = append(out, model.ModelInfo{
					ProviderID: m.ProviderID, ModelID: m.ModelID, Name: m.Name, Harness: "opencode",
				})
			}
			return out, nil
		}
	} else {
		// mock harness exposes a single scriptable pseudo-model
		srv.Models = func(c echo.Context) ([]model.ModelInfo, error) {
			return []model.ModelInfo{{
				ProviderID: "mock", ModelID: "mock-local", Name: "Scripted Mock (no real model)", Harness: "mock",
			}}, nil
		}
	}

	return &App{Cfg: cfg, DBCloser: db.Close, Server: srv, Sched: sched, settings: settingsRepo, log: log}, nil
}

// Start launches the scheduler and prepares shutdown hooks.
func (a *App) Start(ctx context.Context) error {
	if _, err := a.Server.Auth.EnsureDevUser(ctx); err != nil {
		return err
	}
	a.Sched.Start(ctx)
	if err := a.Server.Foreman.ResumeOnStart(ctx); err != nil {
		return err
	}
	// apply persisted budget limit to the tracker
	if s, err := a.settings.Get(ctx); err == nil {
		a.Server.Usage.SetBudgetLimit(s.BudgetLimitMicros)
	}
	return nil
}

// projectReader adapts store.ProjectRepo to orchestration.ProjectReader.
type projectReader struct{ repo *store.ProjectRepo }

func (r projectReader) Get(ctx context.Context, id string) (*orchestration.ProjectView, error) {
	p, err := r.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &orchestration.ProjectView{
		ID: p.ID, RepoURL: p.RepoURL, Language: p.Language, QualityProfile: p.QualityProfile,
	}, nil
}

func splitModelRef(ref string) (string, string) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ref
}

// discoverDefaults picks the first available real model as the workspace
// default for all roles (admins refine via settings; preferences come per
// work order).
func discoverDefaults(c *opencode.Client) map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	models, err := c.Models(ctx)
	if err != nil || len(models) == 0 {
		return map[string]string{}
	}
	ref := models[0].ProviderID + "/" + models[0].ModelID
	return map[string]string{
		"foreman": ref, "architect": ref, "builder": ref, "inspector": ref,
	}
}
