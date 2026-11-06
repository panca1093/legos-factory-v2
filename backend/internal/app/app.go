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
	"legos-factory/backend/internal/kb"
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
	bundle, err := kb.OpenBundle(filepath.Join(cfg.DataDir, "knowledge"), true)
	if err != nil {
		return nil, err
	}
	emb := kb.HashEmbedder{}
	know := &usecase.KB{Bundle: bundle, Idx: &store.KBRepo{DB: db, Embedder: emb.Name()}, Emb: emb, Log: log}
	if _, err := know.SyncFactory(context.Background()); err != nil {
		log.Warn("factory knowledge indexing incomplete", "err", err)
	}
	catalog := &usecase.Cataloger{Bundle: bundle, Log: log}
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
	var localProjects *usecase.Local
	if cfg.AuthMode == "dev" { // gh and the folder endpoints act as the machine's user
		ghFor := func(repo, remote string) usecase.GitProvider { return integration.NewGHCLI(repo, remote, nil) }
		integrations.Host, integrations.GH = sandbox.Host{}, ghFor
		localProjects = &usecase.Local{Host: sandbox.Host{}, GH: ghFor}
	}
	box := sandbox.Local{DataDir: cfg.DataDir}
	workOrders := &usecase.WorkOrders{Store: woRepo, Projects: projects, Sandbox: box, Integrations: integrations, Settings: settingsRepo, Chats: &store.ChatRepo{DB: db}}

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
	var realH usecase.Harness
	var lister modelLister
	switch {
	case cfg.Harness == "acp":
		acp := agent.NewACPHarness(cfg.ACPCommand, filepath.Join(cfg.DataDir, "agent-probe"))
		realH, lister = acp, acp
	case cfg.OpenCodeURL != "":
		client := agent.NewOpenCodeClient(cfg.OpenCodeURL, cfg.OpenCodeUser, cfg.OpenCodePass)
		realH, lister = agent.NewOpenCodeHarness(client), client
	}

	resolve := func(role model.AgentRole, wo *model.WorkOrder) usecase.Harness {
		if realH != nil {
			return realH
		}
		return mockH
	}
	var modelDefaults map[string]string
	if lister != nil {
		modelDefaults = discoverDefaults(lister)
	}

	modelFor := func(role model.AgentRole, wo *model.WorkOrder) (string, string) {
		if realH == nil {
			return "mock", "mock-local"
		}
		s, _ := settingsRepo.Get(context.Background())
		return usecase.SplitModelRef(usecase.ResolveModel(role, wo, s, modelDefaults))
	}

	factory := &usecase.Factory{
		WO: woRepo, Runs: runRepo, Findings: findingRepo, Quality: qualityRepo,
		Proj:    projRepo,
		Resolve: resolve, Model: modelFor,
		Bus: bus, Usage: usageTracker,
		Catalog: catalog, KB: know, Sandbox: box, Integrations: integrations,
		DataDir:     cfg.DataDir,
		QualTimeout: 5 * time.Minute,
		Log:         log,
	}

	if cfg.TypeSafeKey != "" {
		factory.Judge = jevJudge{integration.NewTypeSafe(cfg.TypeSafeKey, cfg.TypeSafeModel, "")}
	}

	sched := scheduler.New(jobRepo, log, cfg.MaxConcurrentRuns)
	factory.Sched = sched
	for kind, h := range factory.Handlers() {
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
	srv.Local = localProjects
	srv.Catalog = catalog
	srv.KBDocs = &usecase.KBDocs{KB: know, Projects: projects, Proj: projRepo, WO: woRepo, DataDir: cfg.DataDir}
	srv.Factory = factory
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
	listModels := func(ctx context.Context) ([]model.ModelInfo, error) {
		// mock harness exposes a single scriptable pseudo-model
		return []model.ModelInfo{{
			ProviderID: "mock", ModelID: "mock-local", Name: "Scripted Mock (no real model)", Harness: "mock",
		}}, nil
	}
	if lister != nil {
		harnessName := realH.Name()
		listModels = func(ctx context.Context) ([]model.ModelInfo, error) {
			models, err := lister.Models(ctx)
			if err != nil {
				return nil, err
			}
			out := make([]model.ModelInfo, 0, len(models))
			for _, m := range models {
				out = append(out, model.ModelInfo{
					ProviderID: m.ProviderID, ModelID: m.ModelID, Name: m.Name, Harness: harnessName,
				})
			}
			return out, nil
		}
	}
	srv.Models = func(c echo.Context) ([]model.ModelInfo, error) { return listModels(c.Request().Context()) }

	// Chat needs a harness that can hold an agent read-only; the OpenCode HTTP
	// harness cannot restrict a session, so chats are off there.
	var chatH usecase.Harness
	switch {
	case cfg.Harness == "acp":
		chatH = realH
	case realH == nil:
		chatH = mockH
	}
	srv.Chat = &usecase.Chat{
		Store: &store.ChatRepo{DB: db}, Docs: srv.KBDocs, Related: srv.KBDocs, Projects: projects,
		Settings: settingsRepo, Usage: usageTracker, Harness: chatH,
		Models: listModels,
		Default: func(ctx context.Context) string {
			p, m := modelFor(model.AgentArchitect, &model.WorkOrder{})
			if m == "" {
				return ""
			}
			return p + "/" + m
		},
		Dir: filepath.Join(cfg.DataDir, "chat"),
	}

	return &App{Cfg: cfg, DBCloser: db.Close, Server: srv, Sched: sched, settings: settingsRepo, log: log}, nil
}

// Start launches the scheduler and prepares shutdown hooks.
func (a *App) Start(ctx context.Context) error {
	if _, err := a.Server.Auth.EnsureDevUser(ctx); err != nil {
		return err
	}
	a.Sched.Start(ctx)
	if err := a.Server.Factory.ResumeOnStart(ctx); err != nil {
		return err
	}
	a.Server.Factory.WatchPRs(ctx)
	// apply persisted budget limit to the tracker
	if s, err := a.settings.Get(ctx); err == nil {
		a.Server.Usage.SetBudgetLimit(s.BudgetLimitMicros)
	}
	return nil
}

type modelLister interface {
	Models(ctx context.Context) ([]agent.Model, error)
}

// discoverDefaults picks the first available real model as the workspace
// default for all roles (admins refine via settings; preferences come per
// work order).
func discoverDefaults(c modelLister) map[string]string {
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

type jevJudge struct{ c *integration.TypeSafe }

func (j jevJudge) Ask(ctx context.Context, state any, questions map[string]usecase.Noul) (usecase.JudgeResult, error) {
	qs := make(map[string]integration.Noul, len(questions))
	for id, q := range questions {
		n := integration.Noul{Instructions: q.Instructions}
		if q.Criteria != nil {
			n.Criteria = &integration.NoulCriteria{True: q.Criteria.True, False: q.Criteria.False}
		}
		qs[id] = n
	}
	r, err := j.c.Noul(ctx, state, qs)
	return usecase.JudgeResult{
		Answers: r.Answers, Model: r.Model, RequestedModel: r.RequestedModel, Request: r.Request,
		Usage: usecase.Usage{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens},
	}, err
}
