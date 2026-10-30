// Package api is the owner-facing JSON HTTP API over profiles, projects and work orders.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"legos-factory/factory/internal/engine"
	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/harness"
	"legos-factory/factory/internal/profile"
	"legos-factory/factory/internal/workflow"
)

const (
	maxBody  = 1 << 20
	pageSize = 200
)

type Server struct {
	Token        string
	Profiles     *profile.Store
	Engine       *engine.Engine
	ProjectsDir  string
	SmokeTimeout time.Duration   // default 10m
	BaseCtx      context.Context // lifetime of background runs; default Background
	Log          *slog.Logger

	mu      sync.Mutex
	running map[string]bool
	smoking map[string]bool
}

var (
	idRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	envRef  = regexp.MustCompile(`^\$\{?[A-Za-z_][A-Za-z0-9_]*\}?$`)
	roles   = map[harness.Role]bool{harness.RoleForeman: true, harness.RoleCataloger: true, harness.RoleArchitect: true, harness.RoleBuilder: true, harness.RoleInspector: true}
	errBody = errors.New("invalid request body")
)

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /credentials", s.createCredential)
	mux.HandleFunc("GET /credentials", s.listCredentials)
	mux.HandleFunc("GET /credentials/{id}", s.getCredential)
	mux.HandleFunc("DELETE /credentials/{id}", s.deleteCredential)
	mux.HandleFunc("GET /credentials/{id}/models", s.credentialModels)
	mux.HandleFunc("GET /credentials/{id}/agents", s.credentialAgents)

	mux.HandleFunc("POST /profiles", s.createProfile)
	mux.HandleFunc("GET /profiles", s.listProfiles)
	mux.HandleFunc("GET /profiles/{id}", s.getProfile)
	mux.HandleFunc("DELETE /profiles/{id}", s.deleteProfile)
	mux.HandleFunc("POST /profiles/{id}/smoke", s.smoke)

	mux.HandleFunc("PUT /bindings/{role}", s.bind)
	mux.HandleFunc("GET /bindings/{role}", s.resolve)
	mux.HandleFunc("PUT /projects/{project}/bindings/{role}", s.bind)
	mux.HandleFunc("GET /projects/{project}/bindings/{role}", s.resolve)

	mux.HandleFunc("POST /projects", s.createProject)
	mux.HandleFunc("GET /projects", s.listProjects)
	mux.HandleFunc("GET /projects/{id}", s.getProject)

	mux.HandleFunc("POST /work-orders", s.createWorkOrder)
	mux.HandleFunc("GET /work-orders", s.listWorkOrders)
	mux.HandleFunc("GET /work-orders/{id}", s.getWorkOrder)
	mux.HandleFunc("POST /work-orders/{id}/run", s.runWorkOrder)
	mux.HandleFunc("POST /work-orders/{id}/approve", s.approveWorkOrder)
	return s.auth(mux)
}

func (s *Server) auth(next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(s.Token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, tok, found := strings.Cut(r.Header.Get("Authorization"), " ")
		ok := found && strings.EqualFold(scheme, "Bearer")
		got := sha256.Sum256([]byte(tok))
		if s.Token == "" || !ok || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(v); err != nil || dec.More() {
		writeErr(w, http.StatusBadRequest, errBody.Error())
		return false
	}
	return true
}

// fail maps store errors to statuses; messages of unclassified errors are redacted of the credential's secret.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error, credID string) {
	switch {
	case errors.Is(err, profile.ErrNotFound), errors.Is(err, profile.ErrNoBinding):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, profile.ErrUnverified):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, profile.ErrSingleOwnerOnly):
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
	default:
		msg := s.redact(r.Context(), err.Error(), credID)
		s.log().Error("request failed", "path", r.URL.Path, "err", msg)
		writeErr(w, http.StatusBadGateway, msg)
	}
}

func (s *Server) redact(ctx context.Context, msg, credID string) string {
	if credID == "" {
		return msg
	}
	if secret, err := s.Profiles.Secret(ctx, credID); err == nil && secret != "" {
		return strings.ReplaceAll(msg, secret, "[redacted]")
	}
	return msg
}

// --- credentials ---

func (s *Server) createCredential(w http.ResponseWriter, r *http.Request) {
	var in struct {
		profile.Credential
		Secret string `json:"secret"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Name == "" || in.Provider == "" || in.Secret == "" {
		writeErr(w, http.StatusBadRequest, "name, provider and secret are required")
		return
	}
	if in.Kind != "api_key" && in.Kind != "subscription" {
		writeErr(w, http.StatusBadRequest, "kind must be api_key or subscription")
		return
	}
	c, err := s.Profiles.CreateCredential(r.Context(), in.Credential, in.Secret)
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	list, err := s.Profiles.ListCredentials(r.Context())
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	if list == nil {
		list = []profile.Credential{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getCredential(w http.ResponseWriter, r *http.Request) {
	c, err := s.Profiles.GetCredential(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	if err := s.Profiles.DeleteCredential(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) credentialModels(w http.ResponseWriter, r *http.Request) {
	c, err := s.Profiles.GetCredential(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	models, err := s.Profiles.Models(r.Context(), c)
	if err != nil {
		s.fail(w, r, err, c.ID)
		return
	}
	if models == nil {
		models = []profile.Model{}
	}
	writeJSON(w, http.StatusOK, models)
}

func (s *Server) credentialAgents(w http.ResponseWriter, r *http.Request) {
	c, err := s.Profiles.GetCredential(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	agents := profile.Agents(c)
	if agents == nil {
		agents = []string{}
	}
	writeJSON(w, http.StatusOK, agents)
}

// --- profiles ---

func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	var in profile.Profile
	if !decode(w, r, &in) {
		return
	}
	if in.Name == "" || in.Model == "" || in.Agent == "" || in.CredentialID == "" {
		writeErr(w, http.StatusBadRequest, "name, agent, credential_id and model are required")
		return
	}
	p, err := s.Profiles.CreateProfile(r.Context(), in)
	if err != nil {
		if errors.Is(err, profile.ErrNotFound) {
			s.fail(w, r, err, "")
		} else {
			writeErr(w, http.StatusUnprocessableEntity, s.redact(r.Context(), err.Error(), in.CredentialID))
		}
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	list, err := s.Profiles.ListProfiles(r.Context())
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	if list == nil {
		list = []profile.Profile{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.Profiles.GetProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.Profiles.DeleteProfile(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// smoke runs in the background; clients poll GET /profiles/{id} for verified_at, smoke_error and smoke_at.
func (s *Server) smoke(w http.ResponseWriter, r *http.Request) {
	p, err := s.Profiles.GetProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	s.mu.Lock()
	if s.smoking[p.ID] {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "smoke already running for this profile")
		return
	}
	if s.smoking == nil {
		s.smoking = map[string]bool{}
	}
	s.smoking[p.ID] = true
	s.mu.Unlock()

	d := s.SmokeTimeout
	if d == 0 {
		d = 10 * time.Minute
	}
	base := s.BaseCtx
	if base == nil {
		base = context.Background()
	}
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.smoking, p.ID)
			s.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(base, d)
		defer cancel()
		msg := ""
		if err := s.Profiles.Smoke(ctx, s.Engine.Harness, s.Engine.Runtime, p); err != nil {
			msg = s.redact(context.WithoutCancel(ctx), err.Error(), p.CredentialID)
		}
		if err := s.Profiles.RecordSmoke(context.WithoutCancel(ctx), p.ID, msg); err != nil {
			s.log().Warn("record smoke", "profile", p.ID, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "running"})
}

// --- bindings ---

func (s *Server) bind(w http.ResponseWriter, r *http.Request) {
	role := harness.Role(r.PathValue("role"))
	if !roles[role] {
		writeErr(w, http.StatusBadRequest, "unknown role")
		return
	}
	var in struct {
		ProfileID string `json:"profile_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	project := r.PathValue("project")
	if project != "" && !idRe.MatchString(project) {
		writeErr(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if err := s.Profiles.Bind(r.Context(), project, role, in.ProfileID); err != nil {
		s.fail(w, r, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"project": project, "role": string(role), "profile_id": in.ProfileID})
}

func (s *Server) resolve(w http.ResponseWriter, r *http.Request) {
	role := harness.Role(r.PathValue("role"))
	if !roles[role] {
		writeErr(w, http.StatusBadRequest, "unknown role")
		return
	}
	p, err := s.Profiles.Resolve(r.Context(), r.PathValue("project"), role)
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// --- projects ---

func (s *Server) projectPath(id string) string { return filepath.Join(s.ProjectsDir, id+".json") }

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeErr(w, http.StatusBadRequest, errBody.Error())
		return
	}
	var f struct {
		ID      string `json:"id"`
		RepoURL string `json:"repo_url"`
		Repo    string `json:"repo"`
		Builder struct {
			Env map[string]string `json:"env"`
		} `json:"builder"`
		Roles map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"roles"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &f) != nil || json.Unmarshal(raw, &fields) != nil {
		writeErr(w, http.StatusBadRequest, errBody.Error())
		return
	}
	if _, ok := fields["scm_token"]; ok {
		writeErr(w, http.StatusBadRequest, "raw tokens are not accepted; use scm_token_env to name an environment variable")
		return
	}
	envs := map[string]map[string]string{"builder": f.Builder.Env}
	for role, rc := range f.Roles {
		envs["roles."+role] = rc.Env
	}
	for where, env := range envs {
		for k, v := range env {
			if !envRef.MatchString(v) {
				writeErr(w, http.StatusBadRequest, where+".env."+k+" must reference an environment variable like ${NAME}")
				return
			}
		}
	}
	if !idRe.MatchString(f.ID) || f.RepoURL == "" || f.Repo == "" {
		writeErr(w, http.StatusBadRequest, "a valid id, repo_url and repo are required")
		return
	}
	if err := os.MkdirAll(s.ProjectsDir, 0o755); err != nil {
		s.fail(w, r, err, "")
		return
	}
	file, err := os.OpenFile(s.projectPath(f.ID), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		writeErr(w, http.StatusConflict, "project already exists")
		return
	}
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	_, werr := file.Write(raw)
	if cerr := file.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(s.projectPath(f.ID))
		s.fail(w, r, werr, "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(raw)
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	files, err := filepath.Glob(filepath.Join(s.ProjectsDir, "*.json"))
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	sort.Strings(files)
	out := []json.RawMessage{}
	for _, f := range files {
		if raw, err := os.ReadFile(f); err == nil && json.Valid(raw) {
			out = append(out, raw)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	raw, err := os.ReadFile(s.projectPath(id))
	if errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

// loadProject resolves env-var secrets at call time, so a project can be stored before its env is set.
func (s *Server) loadProject(id string) (engine.Project, int, error) {
	if !idRe.MatchString(id) {
		return engine.Project{}, http.StatusNotFound, errors.New("project not found")
	}
	if _, err := os.Stat(s.projectPath(id)); err != nil {
		return engine.Project{}, http.StatusNotFound, errors.New("project not found")
	}
	p, err := engine.LoadProject(s.projectPath(id))
	if err != nil {
		return engine.Project{}, http.StatusUnprocessableEntity, err
	}
	return p, 0, nil
}

// --- work orders ---

func (s *Server) createWorkOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project   string `json:"project"`
		Objective string `json:"objective"`
		Level     *int   `json:"level"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Project == "" || strings.TrimSpace(in.Objective) == "" {
		writeErr(w, http.StatusBadRequest, "project and objective are required")
		return
	}
	level := workflow.DefaultLevelCap
	if in.Level != nil {
		if level = *in.Level; level < 0 || level > workflow.DefaultLevelCap {
			writeErr(w, http.StatusBadRequest, "level must be between 0 and 3")
			return
		}
	}
	p, code, err := s.loadProject(in.Project)
	if err != nil {
		writeErr(w, code, err.Error())
		return
	}
	id, err := s.Engine.CreateAt(r.Context(), p, in.Objective, level)
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "project": p.ID, "status": string(fact.StatusQueued)})
}

type woSummary struct {
	ID      string      `json:"id"`
	Status  fact.Status `json:"status"`
	Stalled bool        `json:"stalled"`
}

func (s *Server) listWorkOrders(w http.ResponseWriter, r *http.Request) {
	subjects, err := s.Engine.Facts.Subjects(r.Context())
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	out := []woSummary{}
	for _, id := range subjects {
		st, stalled, err := s.Engine.Status(r.Context(), id)
		if err != nil {
			s.fail(w, r, err, "")
			return
		}
		out = append(out, woSummary{id, st, stalled})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getWorkOrder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	facts, err := s.Engine.Facts.List(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	if len(facts) == 0 {
		writeErr(w, http.StatusNotFound, "work order not found")
		return
	}
	var after int64
	if q := r.URL.Query().Get("after"); q != "" {
		if after, err = strconv.ParseInt(q, 10, 64); err != nil {
			writeErr(w, http.StatusBadRequest, "after must be a fact id")
			return
		}
	}
	status, stalled, _ := s.Engine.Status(r.Context(), id)
	page := []fact.Fact{}
	more := false
	for _, f := range facts {
		if f.ID <= after {
			continue
		}
		if len(page) == pageSize {
			more = true
			break
		}
		page = append(page, f)
	}
	created, _ := fact.Decode[fact.WOCreated](facts[0])
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "project": created.Project, "objective": created.Objective,
		"status": status, "stalled": stalled, "facts": page, "more": more, "running": s.isRunning(id)})
}

// approveWorkOrder records the human approval a level gate (gate "plan", the default) or an escalation (gate "escalation") waits for; the caller then starts a run to continue.
func (s *Server) approveWorkOrder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Gate string `json:"gate"`
		Note string `json:"note"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	if in.Gate == "" {
		in.Gate = workflow.GatePlan
	}
	facts, err := s.Engine.Facts.List(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	if len(facts) == 0 {
		writeErr(w, http.StatusNotFound, "work order not found")
		return
	}
	if st := workflow.Fold(facts); st.Phase != workflow.PhaseAwaitingHuman || st.Gate != in.Gate {
		writeErr(w, http.StatusConflict, "work order is not waiting for approval of the "+in.Gate+" gate")
		return
	}
	if _, err := s.Engine.Facts.Append(r.Context(), id, workflow.KindHumanApproved, workflow.HumanApproved{Gate: in.Gate, Note: in.Note}); err != nil {
		s.fail(w, r, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "gate": in.Gate, "state": "approved"})
}

func (s *Server) isRunning(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[id]
}

func (s *Server) runWorkOrder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	facts, err := s.Engine.Facts.List(r.Context(), id)
	if err != nil {
		s.fail(w, r, err, "")
		return
	}
	if len(facts) == 0 {
		writeErr(w, http.StatusNotFound, "work order not found")
		return
	}
	created, err := fact.Decode[fact.WOCreated](facts[0])
	if err != nil || created.Project == "" {
		writeErr(w, http.StatusUnprocessableEntity, "not a work order")
		return
	}
	p, code, err := s.loadProject(created.Project)
	if err != nil {
		writeErr(w, code, err.Error())
		return
	}
	s.mu.Lock()
	if s.running == nil {
		s.running = map[string]bool{}
	}
	if s.running[id] {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "work order is already running")
		return
	}
	s.running[id] = true
	s.mu.Unlock()

	base := s.BaseCtx
	if base == nil {
		base = context.Background()
	}
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.running, id)
			s.mu.Unlock()
		}()
		if err := s.Engine.Run(base, p, id); err != nil {
			s.log().Error("work order run failed", "wo", id, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id, "state": "running"})
}
