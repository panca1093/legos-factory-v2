// Package app wires the application together and owns lifecycle (start,
// graceful shutdown, background workers).
package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"log/slog"

	"legos-factory/backend/internal/agent"
	httpapi "legos-factory/backend/internal/http"
	"legos-factory/backend/internal/integration"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/sandbox"
	"legos-factory/backend/internal/scheduler"
	"legos-factory/backend/internal/store"
	"legos-factory/backend/internal/usecase"
)

type App struct {
	Cfg      *model.Config
	DBCloser func() error
	Server   *httpapi.Server
	Sched    *scheduler.Scheduler
	settings *store.SettingsRepo
	log      *slog.Logger
}

// Build resolves configuration, opens the database, wires domains and
// constructs the HTTP server. It does not start background work.
func Build(cfg *model.Config, log *slog.Logger) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(cfg.DataDir, "factory.db")
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}

	bus := httpapi.NewBus()
	woRepo := &store.WorkOrderRepo{DB: db}
	projRepo := &store.ProjectRepo{DB: db}
	runRepo := &store.RunRepo{DB: db}
	findingRepo := &store.FindingRepo{DB: db}
	qualityRepo := &store.QualityRepo{DB: db}
	jobRepo := &store.JobRepo{DB: db}
	settingsRepo := &store.SettingsRepo{DB: db}
	userRepo := &store.UserRepo{DB: db}
	usageTracker := usecase.NewUsageTracker(&store.UsageRepo{DB: db})
	projects := &usecase.Projects{Store: projRepo, Members: &store.MemberRepo{DB: db}, Users: userRepo}
	catalog := &usecase.Cataloger{Knowledge: &store.KnowledgeRepo{DB: db}, Proj: projRepo, Settings: settingsRepo, Log: log}
	secrets, err := secretBox(cfg, log)
	if err != nil {
		return nil, err
	}
	integrations := &usecase.Integrations{
		Store: &store.ConnectionRepo{DB: db}, Projects: projects,
		Git: newGitProvider, Issues: newIssueTracker,
	}
	if secrets != nil { // a nil *SecretBox in the interface would not be nil
		integrations.Secrets = secrets
	}
	box := sandbox.Local{DataDir: cfg.DataDir}
	workOrders := &usecase.WorkOrders{Store: woRepo, Projects: projects, Sandbox: box, Integrations: integrations}

	auth := &usecase.Identity{
		Users: userRepo,
		Mode:  cfg.AuthMode,
		DevUser: model.User{
			ID:    cfg.DevUserID,
			Email: "dev@localhost", Name: cfg.DevUserName, Role: model.RoleOwner,
		},
		Log: log,
	}
	mockH := agent.NewMock()
	var ocHarness *agent.OpenCodeHarness
	var ocClient *agent.OpenCodeClient
	if cfg.OpenCodeURL != "" {
		ocClient = agent.NewOpenCodeClient(cfg.OpenCodeURL, cfg.OpenCodeUser, cfg.OpenCodePass)
		ocHarness = agent.NewOpenCodeHarness(ocClient)
	}

	resolve := func(role model.AgentRole, wo *model.WorkOrder) usecase.Harness {
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

	foreman := &usecase.Foreman{
		WO: woRepo, Runs: runRepo, Findings: findingRepo, Quality: qualityRepo,
		Proj:    projRepo,
		Resolve: resolve, Model: modelFor,
		Bus: bus, Usage: usageTracker,
		Catalog: catalog, Sandbox: box, Integrations: integrations,
		DataDir:     cfg.DataDir,
		QualTimeout: 5 * time.Minute,
		Log:         log,
	}

	sched := scheduler.New(jobRepo, log, cfg.MaxConcurrentRuns)
	foreman.Sched = sched
	for kind, h := range foreman.Handlers() {
		sched.Register(kind, h)
	}

	srv := httpapi.New(cfg, log)
	srv.WORepo = woRepo
	srv.ProjRepo = projRepo
	srv.Runs = runRepo
	srv.Findings = findingRepo
	srv.Quality = qualityRepo
	srv.Jobs = jobRepo
	srv.Settings = settingsRepo
	srv.Projects = projects
	srv.WorkOrders = workOrders
	srv.Integrations = integrations
	srv.Catalog = catalog
	srv.Foreman = foreman
	srv.Bus = bus
	srv.Auth = auth
	if cfg.AuthMode == "oidc" {
		srv.OIDC = &httpapi.OIDCClient{
			Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret,
			RedirectURL: cfg.FrontendOrigin + "/auth/oidc/callback",
			HTTP:        &http.Client{Timeout: 45 * time.Second},
		}
	}
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
func discoverDefaults(c *agent.OpenCodeClient) map[string]string {
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

func newGitProvider(c model.Connection, token string) (usecase.GitProvider, error) {
	switch c.Provider {
	case model.ProviderGitHub:
		return integration.NewGitHub(c.BaseURL, token, nil), nil
	case model.ProviderGitLab:
		return integration.NewGitLab(c.BaseURL, token, nil), nil
	case model.ProviderBitbucket:
		return integration.NewBitbucket(c.BaseURL, c.Username, token, nil), nil
	}
	return nil, fmt.Errorf("%s is not a git provider", c.Provider)
}

func newIssueTracker(c model.Connection, token string) (usecase.IssueTracker, error) {
	if c.Provider != model.ProviderJira {
		return nil, fmt.Errorf("%s is not an issue tracker", c.Provider)
	}
	return integration.NewJira(c.BaseURL, c.Username, token, nil), nil
}

// secretBox loads FACTORY_SECRET_KEY. Dev mode without one generates a
// local key file so connections work out of the box; other modes leave
// credential storage disabled rather than invent a key.
func secretBox(cfg *model.Config, log *slog.Logger) (*store.SecretBox, error) {
	raw := cfg.SecretKey
	if raw == "" {
		if cfg.AuthMode != "dev" {
			log.Warn("FACTORY_SECRET_KEY not set; provider connections are disabled")
			return nil, nil
		}
		path := filepath.Join(cfg.DataDir, "secret.key")
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			key := make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				return nil, err
			}
			data = []byte(base64.StdEncoding.EncodeToString(key))
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return nil, err
			}
			log.Warn("generated a development credential key; set FACTORY_SECRET_KEY outside local development", "path", path)
		} else if err != nil {
			return nil, err
		}
		raw = strings.TrimSpace(string(data))
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("FACTORY_SECRET_KEY must be base64: %w", err)
	}
	return store.NewSecretBox(key)
}
