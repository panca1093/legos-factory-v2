package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"legos-factory/factory/internal/engine"
	"legos-factory/factory/internal/fact"
	"legos-factory/factory/internal/profile"
	"legos-factory/factory/internal/workflow"
)

const (
	token  = "owner-token"
	secret = "sk-super-secret-value"
)

type client struct {
	t      *testing.T
	srv    *httptest.Server
	bodies []string
}

func (c *client) do(method, path, auth string, body any) (int, []byte) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, rd)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	c.bodies = append(c.bodies, method+" "+path+" -> "+string(out))
	return resp.StatusCode, out
}

func (c *client) call(method, path string, body any) (int, []byte) {
	return c.do(method, path, "Bearer "+token, body)
}

func (c *client) mustCall(method, path string, body any, want int) []byte {
	c.t.Helper()
	code, out := c.call(method, path, body)
	if code != want {
		c.t.Fatalf("%s %s = %d, want %d: %s", method, path, code, want, out)
	}
	return out
}

func newClient(t *testing.T) (*client, *Server) {
	t.Helper()
	dir := t.TempDir()
	ps, err := profile.Open(profile.Config{DB: filepath.Join(dir, "p.db"), Key: bytes.Repeat([]byte{1}, 32),
		PlansFile: filepath.Join(dir, "plans.json")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ps.Close() })
	fs, err := fact.Open(filepath.Join(dir, "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fs.Close() })
	s := &Server{Token: token, Profiles: ps, Engine: &engine.Engine{Facts: fs}, ProjectsDir: filepath.Join(dir, "projects")}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &client{t: t, srv: ts}, s
}

func (c *client) assertNoSecret() {
	c.t.Helper()
	for _, b := range c.bodies {
		if strings.Contains(b, secret) {
			c.t.Errorf("secret leaked in %s", b)
		}
	}
}

func TestAuthRequired(t *testing.T) {
	c, _ := newClient(t)
	for _, auth := range []string{"", "Bearer wrong", "Basic " + token, token} {
		if code, _ := c.do("GET", "/credentials", auth, nil); code != http.StatusUnauthorized {
			t.Errorf("auth %q = %d, want 401", auth, code)
		}
	}
	c.mustCall("GET", "/credentials", nil, http.StatusOK)
}

func TestBearerSchemeCaseInsensitive(t *testing.T) {
	c, _ := newClient(t)
	for _, scheme := range []string{"bearer", "BEARER", "BeArEr"} {
		if code, _ := c.do("GET", "/credentials", scheme+" "+token, nil); code != http.StatusOK {
			t.Errorf("scheme %q = %d, want 200", scheme, code)
		}
	}
}

func TestEmptyTokenRejectsEverything(t *testing.T) {
	c, s := newClient(t)
	s.Token = ""
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/credentials", nil)
	req.Header.Set("Authorization", "Bearer ")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	_ = c
}

func TestCredentialSecretNeverReturned(t *testing.T) {
	c, _ := newClient(t)
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"data":[{"id":"m1"}]}`)
	}))
	defer models.Close()

	var cred profile.Credential
	out := c.mustCall("POST", "/credentials", map[string]any{"name": "k", "kind": "api_key", "provider": "custom",
		"base_url": models.URL, "secret": secret}, http.StatusCreated)
	json.Unmarshal(out, &cred)

	c.mustCall("GET", "/credentials", nil, http.StatusOK)
	c.mustCall("GET", "/credentials/"+cred.ID, nil, http.StatusOK)
	c.mustCall("GET", "/credentials/"+cred.ID+"/agents", nil, http.StatusOK)
	c.mustCall("GET", "/credentials/"+cred.ID+"/models", nil, http.StatusOK)
	c.mustCall("GET", "/credentials/nope", nil, http.StatusNotFound)
	c.mustCall("POST", "/credentials", map[string]any{"name": "", "kind": "api_key", "provider": "x", "secret": secret}, http.StatusBadRequest)
	c.mustCall("POST", "/credentials", map[string]any{"name": "n", "kind": "subscription", "provider": "claude", "secret": secret}, http.StatusUnprocessableEntity)
	c.mustCall("POST", "/credentials", "not an object", http.StatusBadRequest)

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected "+r.Header.Get("Authorization"), http.StatusUnauthorized)
	}))
	defer down.Close()
	var badCred profile.Credential
	json.Unmarshal(c.mustCall("POST", "/credentials", map[string]any{"name": "bad", "kind": "api_key", "provider": "custom",
		"base_url": down.URL, "secret": secret}, http.StatusCreated), &badCred)
	c.mustCall("GET", "/credentials/"+badCred.ID+"/models", nil, http.StatusBadGateway)

	c.mustCall("DELETE", "/credentials/"+cred.ID, nil, http.StatusNoContent)
	c.mustCall("GET", "/credentials/"+cred.ID, nil, http.StatusNotFound)
	c.assertNoSecret()
}

func TestBindUnverifiedBuilderConflicts(t *testing.T) {
	c, _ := newClient(t)
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"m1"}]}`)
	}))
	defer models.Close()
	var cred profile.Credential
	json.Unmarshal(c.mustCall("POST", "/credentials", map[string]any{"name": "k", "kind": "api_key", "provider": "custom",
		"base_url": models.URL, "secret": secret}, http.StatusCreated), &cred)
	var prof profile.Profile
	json.Unmarshal(c.mustCall("POST", "/profiles", map[string]any{"name": "p", "agent": "opencode",
		"credential_id": cred.ID, "model": "m1"}, http.StatusCreated), &prof)
	c.mustCall("GET", "/profiles/"+prof.ID, nil, http.StatusOK)
	c.mustCall("GET", "/profiles", nil, http.StatusOK)

	c.mustCall("PUT", "/bindings/builder", map[string]string{"profile_id": prof.ID}, http.StatusConflict)
	c.mustCall("PUT", "/projects/p1/bindings/builder", map[string]string{"profile_id": prof.ID}, http.StatusConflict)
	c.mustCall("PUT", "/bindings/nonsense", map[string]string{"profile_id": prof.ID}, http.StatusBadRequest)
	c.mustCall("PUT", "/bindings/builder", map[string]string{"profile_id": "prof-missing"}, http.StatusNotFound)
	c.mustCall("GET", "/bindings/builder", nil, http.StatusNotFound)

	c.mustCall("PUT", "/bindings/architect", map[string]string{"profile_id": prof.ID}, http.StatusOK)
	c.mustCall("GET", "/projects/p1/bindings/architect", nil, http.StatusOK)

	c.mustCall("DELETE", "/profiles/"+prof.ID, nil, http.StatusNoContent)
	c.assertNoSecret()
}

func TestWorkOrderCreateAndGet(t *testing.T) {
	c, s := newClient(t)
	c.mustCall("POST", "/projects", map[string]any{"id": "p1", "repo_url": "file:///x", "repo": "o/r"}, http.StatusCreated)
	c.mustCall("POST", "/projects", map[string]any{"id": "p1", "repo_url": "file:///x", "repo": "o/r"}, http.StatusConflict)
	c.mustCall("GET", "/projects/p1", nil, http.StatusOK)
	c.mustCall("GET", "/projects/missing", nil, http.StatusNotFound)
	c.mustCall("GET", "/projects/..%2Fetc", nil, http.StatusNotFound)

	c.mustCall("POST", "/work-orders", map[string]string{"project": "nope", "objective": "x"}, http.StatusNotFound)
	c.mustCall("POST", "/work-orders", map[string]string{"project": "p1"}, http.StatusBadRequest)

	var wo struct{ ID, Status string }
	json.Unmarshal(c.mustCall("POST", "/work-orders", map[string]string{"project": "p1", "objective": "add x"}, http.StatusCreated), &wo)
	if wo.ID == "" || wo.Status != "queued" {
		t.Fatalf("created = %+v", wo)
	}

	var got struct {
		Status  string
		Stalled bool
		Facts   []fact.Fact
		More    bool
	}
	json.Unmarshal(c.mustCall("GET", "/work-orders/"+wo.ID, nil, http.StatusOK), &got)
	if got.Status != "queued" || got.Stalled || len(got.Facts) != 1 || got.More {
		t.Fatalf("got = %+v", got)
	}
	s.Engine.Facts.Append(context.Background(), wo.ID, fact.KindRunStep, fact.RunStep{})
	json.Unmarshal(c.mustCall("GET", "/work-orders/"+wo.ID+"?after="+itoa(got.Facts[0].ID), nil, http.StatusOK), &got)
	if len(got.Facts) != 1 || got.Facts[0].Kind != fact.KindRunStep {
		t.Fatalf("paged = %+v", got.Facts)
	}
	c.mustCall("GET", "/work-orders/"+wo.ID+"?after=abc", nil, http.StatusBadRequest)
	c.mustCall("GET", "/work-orders/wo-missing", nil, http.StatusNotFound)

	var list []woSummary
	json.Unmarshal(c.mustCall("GET", "/work-orders", nil, http.StatusOK), &list)
	if len(list) != 1 || list[0].ID != wo.ID {
		t.Fatalf("list = %+v", list)
	}
	c.mustCall("POST", "/work-orders/wo-missing/run", nil, http.StatusNotFound)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestApproveResumesLevelGate(t *testing.T) {
	ctx := context.Background()
	c, s := newClient(t)
	c.mustCall("POST", "/projects", map[string]any{"id": "p1", "repo_url": "file:///x", "repo": "o/r"}, http.StatusCreated)
	c.mustCall("POST", "/work-orders", map[string]any{"project": "p1", "objective": "x", "level": 4}, http.StatusBadRequest)
	var wo struct{ ID string }
	json.Unmarshal(c.mustCall("POST", "/work-orders", map[string]any{"project": "p1", "objective": "x", "level": 0}, http.StatusCreated), &wo)
	c.mustCall("POST", "/work-orders/"+wo.ID+"/approve", nil, http.StatusConflict)
	c.mustCall("POST", "/work-orders/wo-missing/approve", nil, http.StatusNotFound)

	for _, f := range []struct {
		kind string
		v    any
	}{
		{fact.KindRunStarted, fact.RunStarted{Run: "r1", Role: workflow.RoleArchitect}},
		{workflow.KindSpecReady, workflow.SpecReady{Spec: "s"}},
		{workflow.KindForemanDecision, workflow.ForemanDecision{Tool: workflow.ToolApproveSpec, Args: workflow.ApproveSpec("").Args(), Accepted: true}},
		{workflow.KindPlanReady, workflow.PlanReady{Tasks: []workflow.PlanTask{{ID: "a", Paths: []string{"a/"}}}}},
		{workflow.KindForemanDecision, workflow.ForemanDecision{Tool: workflow.ToolApprovePlan, Args: workflow.ApprovePlan("").Args(), Accepted: true}},
	} {
		if _, err := s.Engine.Facts.Append(ctx, wo.ID, f.kind, f.v); err != nil {
			t.Fatal(err)
		}
	}
	var got struct{ Status string }
	json.Unmarshal(c.mustCall("GET", "/work-orders/"+wo.ID, nil, http.StatusOK), &got)
	if got.Status != "awaiting_human" {
		t.Fatalf("status = %s", got.Status)
	}
	c.mustCall("POST", "/work-orders/"+wo.ID+"/approve", map[string]string{"gate": "spec"}, http.StatusConflict)
	c.mustCall("POST", "/work-orders/"+wo.ID+"/approve", nil, http.StatusOK)
	json.Unmarshal(c.mustCall("GET", "/work-orders/"+wo.ID, nil, http.StatusOK), &got)
	if got.Status == "awaiting_human" {
		t.Fatalf("still waiting after approval: %s", got.Status)
	}
	c.mustCall("POST", "/work-orders/"+wo.ID+"/approve", nil, http.StatusConflict)
}

func TestApproveEscalationReturnsToEscalatedPhase(t *testing.T) {
	ctx := context.Background()
	c, s := newClient(t)
	c.mustCall("POST", "/projects", map[string]any{"id": "p1", "repo_url": "file:///x", "repo": "o/r"}, http.StatusCreated)
	var wo struct{ ID string }
	json.Unmarshal(c.mustCall("POST", "/work-orders", map[string]any{"project": "p1", "objective": "x"}, http.StatusCreated), &wo)
	for _, f := range []struct {
		kind string
		v    any
	}{
		{fact.KindRunStarted, fact.RunStarted{Run: "r1", Role: workflow.RoleArchitect}},
		{workflow.KindSpecReady, workflow.SpecReady{Spec: "s"}},
		{workflow.KindEscalated, workflow.Escalated{Reason: "3 consecutive rejected Foreman calls"}},
	} {
		if _, err := s.Engine.Facts.Append(ctx, wo.ID, f.kind, f.v); err != nil {
			t.Fatal(err)
		}
	}
	var got struct{ Status string }
	json.Unmarshal(c.mustCall("GET", "/work-orders/"+wo.ID, nil, http.StatusOK), &got)
	if got.Status != "awaiting_human" {
		t.Fatalf("status = %s", got.Status)
	}
	c.mustCall("POST", "/work-orders/"+wo.ID+"/approve", nil, http.StatusConflict)
	c.mustCall("POST", "/work-orders/"+wo.ID+"/approve", map[string]string{"gate": "escalation", "note": "carry on"}, http.StatusOK)
	json.Unmarshal(c.mustCall("GET", "/work-orders/"+wo.ID, nil, http.StatusOK), &got)
	facts, _ := s.Engine.Facts.List(ctx, wo.ID)
	if st := workflow.Fold(facts); got.Status == "awaiting_human" || st.Phase != workflow.PhaseSpecReady || st.ConsecutiveRejections != 0 {
		t.Fatalf("status %s, state %+v", got.Status, st)
	}
	if last := facts[len(facts)-1]; last.Kind != workflow.KindHumanApproved || !strings.Contains(string(last.Payload), "carry on") {
		t.Errorf("last fact = %s %s", last.Kind, last.Payload)
	}
	c.mustCall("POST", "/work-orders/"+wo.ID+"/approve", map[string]string{"gate": "escalation"}, http.StatusConflict)
}

func TestRedactReplacesSecret(t *testing.T) {
	c, s := newClient(t)
	var cred profile.Credential
	json.Unmarshal(c.mustCall("POST", "/credentials", map[string]any{"name": "k", "kind": "api_key", "provider": "anthropic", "secret": secret}, http.StatusCreated), &cred)
	got := s.redact(context.Background(), "boom "+secret+" boom", cred.ID)
	if strings.Contains(got, secret) || !strings.Contains(got, "[redacted]") {
		t.Fatalf("redact = %q", got)
	}
}

func TestProjectRejectsRawSecrets(t *testing.T) {
	c, _ := newClient(t)
	c.mustCall("POST", "/projects", map[string]any{"id": "p", "repo_url": "u", "repo": "o/r", "scm_token": secret}, http.StatusBadRequest)
	c.mustCall("POST", "/projects", map[string]any{"id": "p", "repo_url": "u", "repo": "o/r",
		"builder": map[string]any{"env": map[string]string{"K": secret}}}, http.StatusBadRequest)
	c.mustCall("POST", "/projects", map[string]any{"id": "p", "repo_url": "u", "repo": "o/r", "scm_token_env": "GIT_TOKEN",
		"builder": map[string]any{"env": map[string]string{"K": "${SOME_KEY}"}}}, http.StatusCreated)
	c.mustCall("POST", "/projects", map[string]any{"id": "../x", "repo_url": "u", "repo": "o/r"}, http.StatusBadRequest)
	if code, out := c.call("GET", "/projects", nil); code != 200 || bytes.Contains(out, []byte(secret)) {
		t.Fatalf("list: %d %s", code, out)
	}
	c.assertNoSecret()
}

func TestRunGuardOnePerWorkOrder(t *testing.T) {
	c, s := newClient(t)
	c.mustCall("POST", "/projects", map[string]any{"id": "p1", "repo_url": "file:///x", "repo": "o/r"}, http.StatusCreated)
	var wo struct{ ID string }
	json.Unmarshal(c.mustCall("POST", "/work-orders", map[string]string{"project": "p1", "objective": "x"}, http.StatusCreated), &wo)
	s.mu.Lock()
	s.running = map[string]bool{wo.ID: true}
	s.mu.Unlock()
	c.mustCall("POST", "/work-orders/"+wo.ID+"/run", nil, http.StatusConflict)
}

func newWO(t *testing.T, c *client, s *Server) string {
	t.Helper()
	c.mustCall("POST", "/projects", map[string]any{"id": "p1", "repo_url": "file:///x", "repo": "o/r"}, http.StatusCreated)
	var wo struct{ ID string }
	json.Unmarshal(c.mustCall("POST", "/work-orders", map[string]string{"project": "p1", "objective": "x"}, http.StatusCreated), &wo)
	return wo.ID
}

func TestRunNudgesRunnerWhenPresent(t *testing.T) {
	c, s := newClient(t)
	id := newWO(t, c, s)
	s.Runner = &engine.Runner{Engine: s.Engine, Projects: s.LoadProject}
	c.mustCall("POST", "/work-orders/"+id+"/run", nil, http.StatusAccepted)
	c.mustCall("POST", "/work-orders/"+id+"/run", nil, http.StatusAccepted)
	c.mustCall("POST", "/work-orders/wo-missing/run", nil, http.StatusNotFound)
}

func TestRetryOnlyWhenFailed(t *testing.T) {
	ctx := context.Background()
	c, s := newClient(t)
	id := newWO(t, c, s)
	c.mustCall("POST", "/work-orders/"+id+"/retry", nil, http.StatusConflict)
	c.mustCall("POST", "/work-orders/wo-missing/retry", nil, http.StatusNotFound)
	s.Engine.Facts.Append(ctx, id, fact.KindWOFailed, fact.WOFailed{Class: "provider", Message: "boom"})
	c.mustCall("POST", "/work-orders/"+id+"/retry", map[string]string{"note": "credit topped up"}, http.StatusAccepted)

	facts, _ := s.Engine.Facts.List(ctx, id)
	last := facts[len(facts)-1]
	r, _ := fact.Decode[fact.WORetried](last)
	if last.Kind != fact.KindWORetried || r.By != "human" || r.Note != "credit topped up" {
		t.Fatalf("last fact = %s %+v", last.Kind, r)
	}
	if st, _, _ := s.Engine.Status(ctx, id); st == fact.StatusFailed {
		t.Fatalf("status still %s", st)
	}
	c.mustCall("POST", "/work-orders/"+id+"/retry", nil, http.StatusConflict)
}

func TestFollowUpCreatesChildOnPRBranch(t *testing.T) {
	ctx := context.Background()
	c, s := newClient(t)
	id := newWO(t, c, s)
	c.mustCall("POST", "/work-orders/"+id+"/follow-up", map[string]string{"objective": "more"}, http.StatusConflict)
	s.Engine.Facts.Append(ctx, id, fact.KindPROpened, fact.PROpened{Number: 3, Branch: "lf/" + id, HeadSHA: "aaa"})
	c.mustCall("POST", "/work-orders/"+id+"/follow-up", map[string]string{"objective": " "}, http.StatusBadRequest)
	c.mustCall("POST", "/work-orders/wo-missing/follow-up", map[string]string{"objective": "more"}, http.StatusNotFound)

	var child struct{ ID, Parent, Project, Status string }
	json.Unmarshal(c.mustCall("POST", "/work-orders/"+id+"/follow-up", map[string]string{"objective": "address review"}, http.StatusCreated), &child)
	if child.Parent != id || child.Project != "p1" || child.Status != string(fact.StatusQueued) {
		t.Fatalf("child = %+v", child)
	}
	facts, _ := s.Engine.Facts.List(ctx, child.ID)
	created, _ := fact.Decode[fact.WOCreated](facts[0])
	if created.ParentWO != id || created.BaseBranch != "lf/"+id || created.Objective != "address review" {
		t.Fatalf("created = %+v", created)
	}
}
