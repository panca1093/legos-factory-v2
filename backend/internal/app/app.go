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
	warm     func() // fills the model catalog of the default agent in the background
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
	know := &usecase.KB{Bundle: bundle, Idx: &store.KBRepo{DB: db, Embedder: emb.Name()}, Emb: emb, Log: log, Projects: projRepo}
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
	mcpServers := &usecase.MCPServers{Store: &store.MCPRepo{DB: db}, Lister: agent.ListMCPTools,
		CallbackURL: cfg.FrontendOrigin + "/api/v1/mcp/oauth/callback"}
	if secrets != nil {
		mcpServers.Secrets = secrets
	}
	var localProjects *usecase.Local
	if cfg.AuthMode == "dev" { // gh and the folder endpoints act as the machine's user
		ghFor := func(repo, remote string) usecase.GitProvider { return integration.NewGHCLI(repo, remote, nil) }
		integrations.Host, integrations.GH = sandbox.Host{}, ghFor
		localProjects = &usecase.Local{Host: sandbox.Host{}, GH: ghFor}
	}
	box := sandbox.Local{DataDir: cfg.DataDir}

	acpMode := cfg.Harness == "acp"
	agents := usecase.NewAgents(&store.AgentAccountRepo{DB: db}, cfg.DataDir)
	defaultAgent := "claude"
	if acpMode {
		defaultAgent = providerAgent(cfg.ACPCommand)
		if defaultAgent == "" {
			defaultAgent = "opencode" // a custom command stands in for the agent most setups ran before
		}
		agents.Commands = map[string][]string{defaultAgent: cfg.ACPCommand}
	}
	hs := &harnessSet{agents: agents, probe: filepath.Join(cfg.DataDir, "agent-probe")}
	cats := &catalogs{hs: hs}
	ws := &usecase.WorkspaceSettings{Store: settingsRepo, EnvJevKey: cfg.TypeSafeKey, DefaultAgent: defaultAgent, DefaultWorkers: cfg.MaxConcurrentRuns}
	if secrets != nil {
		ws.Secrets = secrets
	}
	workOrders := &usecase.WorkOrders{Store: woRepo, Projects: projects, Sandbox: box, Integrations: integrations, Settings: ws, Chats: &store.ChatRepo{DB: db}}

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
	if !acpMode && cfg.OpenCodeURL != "" {
		client := agent.NewOpenCodeClient(cfg.OpenCodeURL, cfg.OpenCodeUser, cfg.OpenCodePass)
		realH, lister = agent.NewOpenCodeHarness(client), client
	}

	roleAgent := func(role model.AgentRole, wo *model.WorkOrder) string {
		s, _ := ws.Get(context.Background())
		return usecase.RoleAgent(role, wo, s, defaultAgent)
	}
	resolve := func(role model.AgentRole, wo *model.WorkOrder) usecase.Harness {
		if !acpMode {
			if realH != nil {
				return realH
			}
			return mockH
		}
		id := roleAgent(role, wo)
		spec, ok := usecase.AgentByID(id)
		if !ok {
			return unavailable{id, fmt.Errorf("%w: unknown agent %q for the %s role", model.ErrPermanent, id, role)}
		}
		if err := agents.Ready(context.Background(), id); err != nil {
			return unavailable{id, err}
		}
		if !spec.Allows(role) {
			return unavailable{id, fmt.Errorf("%w: %s cannot run the %s role", model.ErrPermanent, spec.Name, role)}
		}
		h, err := hs.get(id)
		if err != nil {
			return unavailable{id, fmt.Errorf("%w: %v", model.ErrPermanent, err)}
		}
		return h
	}
	var modelDefaults map[string]string
	if lister != nil {
		modelDefaults = discoverDefaults(lister)
	}

	// catalogFor is the role agent's model list under the work order creator's account; empty when it cannot be read.
	catalogFor := func(role model.AgentRole, wo *model.WorkOrder) []string {
		id := roleAgent(role, wo)
		if agents.Ready(context.Background(), id) != nil {
			return nil
		}
		ms, _ := cats.list(context.Background(), agents, id, wo.CreatedBy, false)
		return refs(ms)
	}
	modelFor := func(role model.AgentRole, wo *model.WorkOrder) (string, string) {
		if acpMode {
			s, _ := ws.Get(context.Background())
			return usecase.SplitModelRef(usecase.ResolveAgentModel(role, wo, s, catalogFor(role, wo)))
		}
		if realH == nil {
			return "mock", "mock-local"
		}
		s, _ := settingsRepo.Get(context.Background())
		return usecase.SplitModelRef(usecase.ResolveModel(role, wo, s, modelDefaults))
	}

	workOrders.ResolveDefault = func(role model.AgentRole, s *model.Settings) string {
		if acpMode {
			id := usecase.RoleAgent(role, nil, s, defaultAgent)
			return usecase.ResolveAgentModel(role, &model.WorkOrder{}, s, refs(cats.peek(id, "")))
		}
		return usecase.ResolveModel(role, &model.WorkOrder{}, s, modelDefaults)
	}

	jv := &jev{model: cfg.TypeSafeModel}
	factory := &usecase.Factory{
		WO: woRepo, Runs: runRepo, Findings: findingRepo, CodeFacts: &store.CodeFactRepo{DB: db}, Quality: qualityRepo,
		Proj:    projRepo,
		Resolve: resolve, Model: modelFor,
		Bus: bus, Usage: usageTracker,
		Catalog: catalog, KB: know, Sandbox: box, Integrations: integrations, MCP: mcpServers,
		DataDir:     cfg.DataDir,
		QualTimeout: 5 * time.Minute,
		Log:         log,
		JudgeFn:     jv.get,
	}
	if acpMode {
		factory.RunEnv = func(ctx context.Context, role model.AgentRole, wo *model.WorkOrder) ([]string, []string, error) {
			return agents.RunEnv(ctx, roleAgent(role, wo), wo.CreatedBy, role)
		}
	}

	sched := scheduler.New(jobRepo, log, cfg.MaxConcurrentRuns)
	ws.OnChange = func(st *model.Settings) {
		sched.SetWorkers(st.MaxConcurrentRuns)
		jv.set(ws.JevKey(context.Background()))
	}
	if st, err := ws.Get(context.Background()); err == nil {
		sched.SetWorkers(st.MaxConcurrentRuns)
	}
	jv.set(ws.JevKey(context.Background()))
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
	srv.Rail = &usecase.Rail{Projects: projects, WO: woRepo, Integrations: integrations}
	srv.Integrations = integrations
	srv.MCP = mcpServers
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
	srv.Agents = agents
	srv.WS = ws
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
	chatAgent := func() string { return roleAgent(model.AgentChat, nil) }
	if acpMode {
		listModels = func(ctx context.Context) ([]model.ModelInfo, error) {
			return cats.list(ctx, agents, chatAgent(), usecase.ChatUser(ctx), false)
		}
		srv.Models = func(c echo.Context) ([]model.ModelInfo, error) {
			id := c.QueryParam("agent")
			if id == "" {
				id = defaultAgent
			}
			uid := ""
			if u, ok := c.Get("user").(*model.User); ok {
				uid = u.ID
			}
			if err := agents.Ready(c.Request().Context(), id); err != nil {
				return nil, err
			}
			return cats.list(c.Request().Context(), agents, id, uid, false)
		}
	} else {
		srv.Models = func(c echo.Context) ([]model.ModelInfo, error) { return listModels(c.Request().Context()) }
	}

	// Chat needs a harness that can hold an agent read-only; the OpenCode HTTP
	// harness cannot restrict a session, so chats are off there.
	var chatH usecase.Harness
	var chatRunner func(ctx context.Context, userID string) (usecase.Harness, []string, []string, error)
	switch {
	case acpMode:
		chatH, _ = hs.get(defaultAgent)
		chatRunner = func(ctx context.Context, userID string) (usecase.Harness, []string, []string, error) {
			h := resolve(model.AgentChat, nil)
			env, args, err := agents.RunEnv(ctx, chatAgent(), userID, model.AgentChat)
			return h, env, args, err
		}
	case realH == nil:
		chatH = mockH
	}
	chatModel := func(ctx context.Context, fallbackRole model.AgentRole) string {
		if !acpMode {
			p, m := modelFor(fallbackRole, &model.WorkOrder{})
			fallback := ""
			if m != "" {
				fallback = p + "/" + m
			}
			s, _ := settingsRepo.Get(ctx)
			return usecase.ChatAuto(s, fallback)
		}
		s, _ := ws.Get(ctx)
		id := chatAgent()
		var cat []string
		if agents.Ready(ctx, id) == nil {
			ms, _ := cats.list(ctx, agents, id, usecase.ChatUser(ctx), false)
			cat = refs(ms)
		}
		return usecase.ResolveAgentModel(model.AgentChat, &model.WorkOrder{}, s, cat)
	}
	srv.Chat = &usecase.Chat{
		Store: &store.ChatRepo{DB: db}, Docs: srv.KBDocs, Related: srv.KBDocs, Projects: projects,
		Settings: settingsRepo, Usage: usageTracker, Harness: chatH, MCP: mcpServers,
		Runs: runRepo, MCPServers: mcpServers.Store, DataDir: cfg.DataDir,
		Models: listModels,
		Default:     func(ctx context.Context) string { return chatModel(ctx, model.AgentArchitect) },
		RepoDefault: func(ctx context.Context) string { return chatModel(ctx, model.AgentBuilder) },
		Runner:      chatRunner,
		Dir: filepath.Join(cfg.DataDir, "chat"),
		Repo: func(ctx context.Context, projectID string, refresh bool) (dir, sha string, err error) {
			defer func() {
				if err != nil {
					log.Warn("chat project folder unusable", "project", projectID, "err", err)
				}
			}()
			p, err := projRepo.Get(ctx, projectID)
			if err != nil {
				return "", "", err
			}
			return usecase.ProjectFolder(ctx, p.RepoURL)
		},
		RepoFacts: factory.RepoFacts,
	}

	srv.Checks = &usecase.CheckSuggester{
		Projects: projects, Proj: projRepo, Store: &store.CheckProposalRepo{DB: db}, Usage: usageTracker, KB: know, Log: log,
		DryRun: factory.DryRunChecks, StopStack: factory.StopStack,
		Checkout: func(ctx context.Context, projectID string) (dir, sha string, cleanup func(), err error) {
			p, err := projRepo.Get(ctx, projectID)
			if err != nil {
				return "", "", nil, err
			}
			auth, err := integrations.GitAuth(ctx, p)
			if err != nil {
				return "", "", nil, err
			}
			id := "suggest-" + projectID
			remove := func() { _ = box.Destroy(context.Background(), id) }
			remove()
			if dir, sha, err = box.Checkout(ctx, id, p.RepoURL, auth, false); err != nil {
				remove()
				return "", "", nil, err
			}
			return dir, sha, remove, nil
		},
		SourceDir: func(ctx context.Context, projectID string) (string, error) {
			p, err := projRepo.Get(ctx, projectID)
			if err != nil {
				return "", err
			}
			dir, _, err := usecase.ProjectFolder(ctx, p.RepoURL)
			return dir, err
		},
		Agent: func(ctx context.Context, userID, dir, prompt string) (string, model.AgentResult, error) {
			ctx = usecase.WithChatUser(ctx, userID)
			h, env, args := chatH, []string(nil), []string(nil)
			if chatRunner != nil {
				var err error
				if h, env, args, err = chatRunner(ctx, userID); err != nil {
					return "", model.AgentResult{}, err
				}
			}
			if h == nil {
				return "", model.AgentResult{}, usecase.ErrCheckSuggestUnavailable
			}
			var ref string
			if acpMode {
				s, _ := ws.Get(ctx)
				var cat []string
				if agents.Ready(ctx, chatAgent()) == nil {
					ms, _ := cats.list(ctx, agents, chatAgent(), userID, false)
					cat = refs(ms)
				}
				ref = usecase.ResolveAgentModel(model.AgentBuilder, &model.WorkOrder{}, s, cat)
			} else if p, m := modelFor(model.AgentBuilder, &model.WorkOrder{}); m != "" {
				ref = p + "/" + m
			}
			provider, modelID := usecase.SplitModelRef(ref)
			ses, err := h.Start(ctx, model.AgentRequest{Role: model.AgentChat, ProviderID: provider, ModelID: modelID, WorkspaceDir: dir,
				RepoAccess: true, Prompt: prompt, OnText: func(string) {}, Env: env, Args: args})
			if err != nil {
				return ref, model.AgentResult{}, err
			}
			res, err := h.Result(ctx, ses.ID())
			return ref, res, err
		},
	}

	warm := func() {
		if acpMode && agents.Ready(context.Background(), defaultAgent) == nil {
			_, _ = cats.list(context.Background(), agents, defaultAgent, "", false)
		}
	}
	return &App{warm: warm, Cfg: cfg, DBCloser: db.Close, Server: srv, Sched: sched, settings: settingsRepo, log: log}, nil
}

// Start launches the scheduler and prepares shutdown hooks.
func (a *App) Start(ctx context.Context) error {
	if _, err := a.Server.Auth.EnsureDevUser(ctx); err != nil {
		return err
	}
	if n, err := a.Server.Runs.FailOrphans(ctx, "interrupted by restart"); err != nil {
		return err
	} else if n > 0 {
		a.log.Warn("failed runs left running by a dead process", "count", n)
	}
	if a.Server.Checks != nil {
		if err := a.Server.Checks.Store.FailRunning(ctx, "interrupted by restart"); err != nil {
			return err
		}
	}
	if a.Server.Chat != nil {
		if err := a.Server.Chat.CollectFiles(ctx); err != nil {
			a.log.Warn("chat file cleanup failed", "err", err)
		}
	}
	a.Sched.Start(ctx)
	go a.warm()
	a.Server.Factory.RemoveLegacyWorkspaces(ctx)
	if err := a.Server.Factory.ResumeOnStart(ctx); err != nil {
		return err
	}
	a.Server.Factory.WatchPRs(ctx)
	a.Server.Factory.WatchStacks(ctx)
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
