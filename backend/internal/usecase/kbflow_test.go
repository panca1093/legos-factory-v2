package usecase

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

const (
	planJSON     = `{"decision":"APPROVE","summary":"s","spec":"sp","tasks":[{"id":"t1","title":"Round money","objective":"o","role":"builder"}]}`
	reviewJSON   = `{"notes":"ok","findings":[],"coverage":[]}`
	convergeJSON = `{"gaps":[],"converged":true}`
	noFindings   = `{"findings":[]}`
	moneyDoc     = "# Money\n\nMoney is integer rupiah; never use floats for rounding.\n"
	routingBody  = "---\ntype: Reviewer Routing\nroutes:\n  - paths: [\"src/**/*.ts\", \"!src/**/*.test.ts\"]\n    reviewers: [backend]\n  - paths: [\"**/*.test.ts\"]\n    reviewers: [testing]\n---\n\n# Routing\n"
)

type replyHarness struct {
	mu       sync.Mutex
	prompts  map[string][]string // by role
	specials []string
	reqs     map[string]model.AgentRequest // by session id
	converge string                        // overrides the Architect converge reply
	inspect  string                        // overrides the Inspector reply
	foreman  string                        // overrides the Foreman reply
	archErr  error                         // fails the Architect converge run
	archBoom bool                          // panics in the Architect converge run
}

func (*replyHarness) Name() string { return "fake" }
func (h *replyHarness) Start(_ context.Context, req model.AgentRequest) (model.AgentSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.prompts == nil {
		h.prompts = map[string][]string{}
	}
	h.prompts[string(req.Role)] = append(h.prompts[string(req.Role)], req.Prompt)
	if req.Role == model.AgentInspector {
		h.specials = append(h.specials, req.Context["specialty"])
	}
	if h.reqs == nil {
		h.reqs = map[string]model.AgentRequest{}
	}
	id := fmt.Sprintf("r%d", len(h.reqs)+1)
	h.reqs[id] = req
	return idSession(id), nil
}
func (*replyHarness) Cancel(context.Context, string) error { return nil }
func (h *replyHarness) Result(_ context.Context, id string) (model.AgentResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	req := h.reqs[id]
	text := map[model.AgentRole]string{model.AgentArchitect: planJSON, model.AgentForeman: reviewJSON, model.AgentInspector: noFindings}[req.Role]
	if h.inspect != "" && req.Role == model.AgentInspector {
		text = h.inspect
	}
	if h.foreman != "" && req.Role == model.AgentForeman {
		text = h.foreman
	}
	if req.Role == model.AgentArchitect && req.Context["stage"] == "converge" {
		if h.archBoom {
			panic("scripted converge crash")
		}
		if h.archErr != nil {
			return model.AgentResult{}, h.archErr
		}
		text = convergeJSON
		if h.converge != "" {
			text = h.converge
		}
	}
	return model.AgentResult{Text: text}, nil
}

type filesSandbox struct {
	dirSandbox
	files []string
}

func (s filesSandbox) ChangedFiles(context.Context, string) ([]string, error) { return s.files, nil }

type rig struct {
	f    *Factory
	wo   *model.WorkOrder
	wos  *store.WorkOrderRepo
	db   *sql.DB
	know *KB
	h    *replyHarness
	q    *fakeQueue
}

func newRig(t *testing.T) *rig {
	t.Helper()
	h := &replyHarness{}
	f, wo, wos := monitorFactory(t, h)
	db := f.Runs.(*store.RunRepo).DB
	bundle, err := kb.OpenBundle(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	know := &KB{Bundle: bundle, Idx: &store.KBRepo{DB: db}, Emb: kb.HashEmbedder{}, Log: slog.Default()}
	if _, err := know.SyncFactory(context.Background()); err != nil {
		t.Fatal(err)
	}
	projects := &store.ProjectRepo{DB: db}
	q := &fakeQueue{}
	f.Proj, f.KB, f.Sched, f.Findings, f.Quality = projects, know, q, &store.FindingRepo{DB: db}, &store.QualityRepo{DB: db}
	f.Catalog = &Cataloger{Bundle: bundle, Log: slog.Default()}
	f.Model = func(model.AgentRole, *model.WorkOrder) (string, string) { return "p", "m" }
	f.DataDir = t.TempDir()
	f.Sandbox = filesSandbox{}
	wo.Title, wo.Objective, wo.Acceptance = "Round money", "Make money rounding use integer rupiah", "no floats"
	return &rig{f, wo, wos, db, know, h, q}
}

func (r *rig) workspace(t *testing.T, files map[string]string) string {
	t.Helper()
	return r.workspaceFor(t, r.wo.ID, files)
}

func (r *rig) workspaceFor(t *testing.T, woID string, files map[string]string) string {
	t.Helper()
	dir := r.f.workspaceDir(woID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "--quiet")
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "--quiet", "-m", "base")
	return strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
}

func (r *rig) events(t *testing.T, kind string) []model.Event {
	t.Helper()
	evs, err := r.wos.Events(context.Background(), r.wo.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func TestPlanStartSyncsRepoAndPromptsCarryPointersAndPlaybook(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	sha := r.workspace(t, map[string]string{"docs/money.md": moneyDoc, "README.md": "# Readme\n\nSee [money](docs/money.md).\n"})

	head0 := r.know.Bundle.Head()
	if err := r.f.handlePlan(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	idx := r.events(t, "knowledge_indexed")
	if len(idx) != 1 || idx[0].Payload["commit_sha"] != sha || idx[0].Payload["source"] != "repo" || idx[0].Payload["docs"] != float64(2) || idx[0].Payload["changed"] != float64(2) {
		t.Fatalf("first sync: %+v", idx)
	}
	r.f.syncRepo(ctx, r.wo, true)
	if idx = r.events(t, "knowledge_indexed"); len(idx) != 2 || idx[1].Payload["changed"] != float64(0) || idx[1].Payload["docs"] != float64(2) {
		t.Fatalf("second sync must skip unchanged files: %+v", idx)
	}
	r.f.syncRepo(ctx, r.wo, false)
	if len(r.events(t, "knowledge_indexed")) != 2 {
		t.Fatal("an unmoved HEAD must not re-sync when not forced")
	}

	arch := r.h.prompts["architect"][0]
	for _, want := range []string{
		"Repository documents that may matter for this work. They are in your workspace; read the ones you need:",
		"- docs/money.md — Money — Money is integer rupiah", kbPrecedence,
		"--- How we work ---", "## Simplest change",
	} {
		if !strings.Contains(arch, want) {
			t.Fatalf("architect prompt lacks %q:\n%s", want, arch)
		}
	}
	if strings.Contains(arch, "never use floats for rounding.\n\n") && strings.Contains(arch, "# Money") {
		t.Fatal("repository documents must be pointers, not inlined")
	}

	if err := r.f.handleReview(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	rev := r.h.prompts["foreman"][0]
	for _, want := range []string{"Factory playbooks for your role", "When the Foreman escalates to a human", "Escalate when", "--- How we work ---", "docs/money.md", kbPrecedence} {
		if !strings.Contains(rev, want) {
			t.Fatalf("foreman prompt lacks %q:\n%s", want, rev)
		}
	}

	var started []model.Event
	for _, e := range r.events(t, "agent_started") {
		started = append(started, e)
	}
	if len(started) != 2 {
		t.Fatalf("agent_started events: %d", len(started))
	}
	for _, e := range started {
		docs, _ := e.Payload["kb_docs"].([]any)
		if e.Payload["kb_repo_commit"] != sha || len(fmt.Sprint(e.Payload["kb_factory_commit"])) != 40 || e.Payload["run_id"] == nil || len(docs) == 0 {
			t.Fatalf("audit payload: %+v", e.Payload)
		}
	}
	if started[0].Payload["kb_factory_commit"] != head0 {
		t.Fatalf("architect saw bundle %v, want %s", started[0].Payload["kb_factory_commit"], head0)
	}
	docs, _ := started[1].Payload["kb_docs"].([]any)
	if !contains(docs, "factory::playbooks/foreman/escalation.md") || !contains(docs, "factory::playbooks/all/how-we-work.md") || !contains(docs, "repo:p1:docs/money.md") {
		t.Fatalf("foreman audit docs: %v", docs)
	}
}

func contains(list []any, want string) bool {
	for _, x := range list {
		if x == want {
			return true
		}
	}
	return false
}

func TestImplementationDoesNotReindexBuilderOutput(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.workspace(t, map[string]string{"docs/money.md": moneyDoc})
	if err := r.f.handlePlan(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	dir := r.f.workspaceDir(r.wo.ID)
	if err := os.WriteFile(filepath.Join(dir, "routing.md"), []byte(routingBody), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "--quiet", "-m", "builder doc")
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateQueued); err != nil {
		t.Fatal(err)
	}
	r.f.Quality, r.f.Sandbox = &store.QualityRepo{DB: r.db}, filesSandbox{files: []string{"x.go"}}
	if err := r.f.handleImplement(ctx, model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	if n := len(r.events(t, "knowledge_indexed")); n != 1 {
		t.Fatalf("knowledge_indexed events = %d; only planning start may index the repository", n)
	}
	if d, _ := r.know.Idx.Get(ctx, "repo:p1:routing.md"); d != nil {
		t.Fatal("a document the builder wrote reached the index")
	}
}

func TestRepoSyncFailureNeverFailsTheJob(t *testing.T) {
	r := newRig(t)
	r.f.syncRepo(context.Background(), r.wo, true)
	if len(r.events(t, "knowledge_index_failed")) != 1 || len(r.events(t, "knowledge_indexed")) != 0 {
		t.Fatal("a missing workspace must be reported as knowledge_index_failed")
	}
}

func inspect(t *testing.T, r *rig, files ...string) model.Event {
	t.Helper()
	r.f.Sandbox = filesSandbox{files: files}
	if err := r.f.handleInspect(context.Background(), model.Job{WOID: r.wo.ID}); err != nil {
		t.Fatal(err)
	}
	return r.events(t, "inspection_specialists")[0]
}

func TestRoutingResolutionOrder(t *testing.T) {
	ctx := context.Background()
	files := []string{"src/pricing.ts", "src/pricing.test.ts"}

	t.Run("repo doc beats factory doc and the POS case picks backend not frontend", func(t *testing.T) {
		r := newRig(t)
		repo := t.TempDir()
		if err := os.WriteFile(filepath.Join(repo, "routing.md"), []byte(routingBody), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := r.know.SyncRepo(ctx, "p1", repo, true); err != nil {
			t.Fatal(err)
		}
		front := map[string]any{"type": model.KBTypeReviewerRoute, "routes": []model.ReviewerRoute{{Paths: []string{"**"}, Reviewers: []string{"frontend"}}}}
		if err := r.know.WriteFactory(ctx, "projects/p1/reviewer-routing.md", front, "x", "factory routing"); err != nil {
			t.Fatal(err)
		}
		ev := inspect(t, r, files...)
		if fmt.Sprint(r.h.specials) != "[testing backend]" {
			t.Fatalf("specialists %v, heuristic would have said frontend", r.h.specials)
		}
		routing, _ := ev.Payload["routing"].(map[string]any)
		if routing["source"] != "repo" || routing["id"] != "repo:p1:routing.md" || routing["status"] != "stable" {
			t.Fatalf("routing audit: %v", routing)
		}
		if len(r.events(t, "routing_proposed")) != 0 {
			t.Fatal("no proposal when a routing doc exists")
		}
	})

	t.Run("factory doc is used when the repo has none", func(t *testing.T) {
		r := newRig(t)
		front := map[string]any{"type": model.KBTypeReviewerRoute, "routes": []model.ReviewerRoute{{Paths: []string{"src/**"}, Reviewers: []string{"security"}}}}
		if err := r.know.WriteFactory(ctx, "projects/p1/reviewer-routing.md", front, "x", "factory routing"); err != nil {
			t.Fatal(err)
		}
		ev := inspect(t, r, files...)
		routing, _ := ev.Payload["routing"].(map[string]any)
		if fmt.Sprint(r.h.specials) != "[testing security]" || routing["source"] != "factory" {
			t.Fatalf("%v %v", r.h.specials, routing)
		}
	})

	t.Run("neither: a draft is proposed once, used, and a human is asked", func(t *testing.T) {
		r := newRig(t)
		ev := inspect(t, r, "internal/pay/pay.go", "internal/pay/pay_test.go")
		routing, _ := ev.Payload["routing"].(map[string]any)
		if routing["status"] != "draft" || routing["id"] != "factory:p1:projects/p1/reviewer-routing.md" {
			t.Fatalf("routing audit: %v", routing)
		}
		prop := r.events(t, "routing_proposed")
		if len(prop) != 1 || !strings.Contains(prop[0].Message, "Knowledge") {
			t.Fatalf("proposal event: %+v", prop)
		}
		d, _ := r.know.Idx.Get(ctx, routing["id"].(string))
		if d == nil || d.Status != model.KBStatusDraft || d.Type != model.KBTypeReviewerRoute || len(routesOf(d.Meta)) == 0 {
			t.Fatalf("draft doc: %+v", d)
		}
		if fmt.Sprint(r.h.specials) != "[testing backend]" {
			t.Fatalf("specialists %v", r.h.specials)
		}
		r.h.specials = nil
		inspect(t, r, "internal/pay/pay.go")
		if len(r.events(t, "routing_proposed")) != 1 || fmt.Sprint(r.h.specials) != "[testing backend]" {
			t.Fatal("the draft must be reused, not proposed again")
		}
	})
}

func TestDisputeDraftIsInertUntilApprovedAndDiscardRemovesIt(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.f.Runs.Start(ctx, "run1", r.wo.ID, "", "inspector", "fake"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"f1", "f2"} {
		if err := r.f.Findings.Create(ctx, model.Finding{ID: id, RunID: "run1", WOID: r.wo.ID, Severity: "blocker", Title: "Missing nil check " + id, Status: model.FindingOpen, Specialty: "backend"}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.f.DecideFinding(ctx, r.wo.ID, id, "u1", model.FindingDecisionRequest{Action: "dispute", Note: "handled by the caller"}); err != nil {
			t.Fatal(err)
		}
	}
	d, _ := r.know.Idx.Get(ctx, "factory:p1:projects/p1/drafts/dispute-f1.md")
	if d == nil || d.Status != model.KBStatusDraft || d.Type != model.KBTypePlaybook || d.Meta["role"] != "inspector" || d.Meta["specialty"] != "backend" {
		t.Fatalf("draft: %+v", d)
	}
	_, body, _ := r.know.Bundle.Read(d.Path)
	if !strings.Contains(body, "reported \"Missing nil check f1\" (blocker) and a human disputed it: handled by the caller") || !strings.Contains(body, "Proposed rule: do not report this kind of finding as blocker unless") {
		t.Fatalf("draft body: %s", body)
	}
	if text, _ := r.know.Context(ctx, "p1", "inspector", "backend", "anything", "", ""); strings.Contains(text, "Missing nil check") {
		t.Fatal("a draft must never reach an agent")
	}

	docs := &KBDocs{KB: r.know, Projects: &Projects{Store: r.f.Proj, Members: &store.MemberRepo{DB: r.db}, Users: &store.UserRepo{DB: r.db}},
		Proj: r.f.Proj, WO: r.wos, DataDir: r.f.DataDir}
	u := &model.User{ID: "u1", Role: model.RoleOwner}
	approved, err := docs.Approve(ctx, u, d.ID, "good rule")
	if err != nil || approved.Status != model.KBStatusStable || approved.Trust != model.KBTrustHuman {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	text, audit := r.know.Context(ctx, "p1", "inspector", "backend", "anything", "", "")
	if !strings.Contains(text, "Missing nil check f1") || !strings.Contains(text, kbPrecedence) || !strings.Contains(fmt.Sprint(audit["kb_docs"]), d.ID) {
		t.Fatalf("approved playbook must be inlined for the backend inspector:\n%s", text)
	}
	if other, _ := r.know.Context(ctx, "p1", "inspector", "frontend", "anything", "", ""); strings.Contains(other, "Missing nil check") {
		t.Fatal("the playbook is for the backend specialty only")
	}

	if err := docs.Discard(ctx, u, "factory:p1:projects/p1/drafts/dispute-f2.md"); err != nil {
		t.Fatal(err)
	}
	if gone, _ := r.know.Idx.Get(ctx, "factory:p1:projects/p1/drafts/dispute-f2.md"); gone != nil {
		t.Fatal("discarded draft must leave the index")
	}
	if _, _, err := r.know.Bundle.Read("projects/p1/drafts/dispute-f2.md"); err == nil {
		t.Fatal("discarded draft must leave the bundle")
	}
	if err := docs.Discard(ctx, u, d.ID); err == nil {
		t.Fatal("a stable document cannot be discarded")
	}
}

func TestSimilarClarificationProposesDraft(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	prev := &model.WorkOrder{ID: "wo_prev", ProjectID: "p1", Title: "Earlier"}
	prev.CreatedBy = r.wo.CreatedBy
	if err := r.wos.Create(ctx, prev); err != nil {
		t.Fatal(err)
	}
	if _, err := r.wos.AppendEvent(ctx, prev.ID, "human", "clarification_answered", "Always round down.",
		map[string]any{"question": "Should discount rounding go up or down?"}); err != nil {
		t.Fatal(err)
	}
	// newer than the human answer, so it is met first and must be skipped
	if _, err := r.wos.AppendEvent(ctx, prev.ID, "foreman", "clarification_answered", "Always round UP.",
		map[string]any{"question": "Should discount rounding go up or down?"}); err != nil {
		t.Fatal(err)
	}
	if err := r.wos.UpdateState(ctx, r.wo.ID, model.StateWaitingForHuman); err != nil {
		t.Fatal(err)
	}
	if err := r.wos.UpdateFields(ctx, r.wo.ID, map[string]any{"clarif_question": "Should tax rounding go up or down?"}); err != nil {
		t.Fatal(err)
	}
	if err := r.f.AnswerClarification(ctx, r.wo.ID, "Round down as well."); err != nil {
		t.Fatal(err)
	}
	d, _ := r.know.Idx.Get(ctx, "factory:p1:projects/p1/drafts/clarification-wo1.md")
	if d == nil || d.Status != model.KBStatusDraft || d.Meta["role"] != "foreman" {
		t.Fatalf("draft: %+v", d)
	}
	_, body, _ := r.know.Bundle.Read(d.Path)
	if !strings.Contains(body, "The Foreman asked a similar question before") || !strings.Contains(body, "Always round down.") || !strings.Contains(body, "Round down as well.") || strings.Contains(body, "round UP") {
		t.Fatalf("body: %s", body)
	}
}

func playbookDoc(title, role, specialty, rule string) string {
	s := "---\ntype: Playbook\ntitle: " + title + "\nrole: " + role + "\n"
	if specialty != "" {
		s += "specialty: " + specialty + "\n"
	}
	return s + "---\n\n" + rule + "\n"
}

func (r *rig) ctxFor(wo *model.WorkOrder, role, specialty, query string) (string, map[string]any) {
	return r.know.Context(context.Background(), wo.ProjectID, role, specialty, query, r.f.workspaceDir(wo.ID), wo.KBBaseSHA)
}

func TestRepoPlaybooksAreInlinedBeforeFactoryOnes(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	pb := playbookDoc
	r.workspace(t, map[string]string{
		"docs/review-backend.md": pb("Backend review rules", "inspector", "backend", "Business logic lives in usecase/."),
		"docs/review-all.md":     pb("Review rules for every inspector", "inspector", "", "Money is int64 rupiah."),
		"docs/foreman.md":        pb("Foreman rules", "foreman", "", "Never escalate naming."),
		"docs/odd-role.md":       pb("Odd role rules", "security-bot", "", "Money must be audited."),
	})
	r.f.syncRepo(ctx, r.wo, true)
	if err := os.WriteFile(filepath.Join(r.f.workspaceDir(r.wo.ID), "docs/review-backend.md"), []byte(pb("Backend review rules", "inspector", "backend", "Anything goes.")), 0o644); err != nil {
		t.Fatal(err)
	}

	backend, audit := r.ctxFor(r.wo, string(model.AgentInspector), "backend", "money")
	repoAt := strings.Index(backend, "Repository playbooks for your role")
	factoryAt := strings.Index(backend, "Factory playbooks for your role")
	if repoAt < 0 || factoryAt < 0 || repoAt > factoryAt {
		t.Fatalf("repo playbooks must come first:\n%s", backend)
	}
	for _, want := range []string{"Business logic lives in usecase/.", "Money is int64 rupiah.", kbPrecedence} {
		if !strings.Contains(backend, want) {
			t.Fatalf("backend inspector prompt lacks %q:\n%s", want, backend)
		}
	}
	if strings.Contains(backend, "Anything goes.") {
		t.Fatal("repo playbooks must be read at the indexed commit, not the working tree")
	}
	if !contains(toAny(audit["kb_docs"]), "repo:"+r.wo.ProjectID+":docs/review-backend.md") || audit["kb_repo_commit"] != r.wo.KBBaseSHA || r.wo.KBBaseSHA == "" {
		t.Fatalf("audit: %v", audit)
	}
	for _, hidden := range []string{"- docs/review-all.md", "- docs/review-backend.md", "- docs/foreman.md"} {
		if strings.Contains(backend, hidden) {
			t.Fatalf("role playbook %q must not be listed as a pointer:\n%s", hidden, backend)
		}
	}
	if !strings.Contains(backend, "- docs/odd-role.md") {
		t.Fatalf("a playbook with an unknown role must stay visible as a pointer:\n%s", backend)
	}

	tester, _ := r.ctxFor(r.wo, string(model.AgentInspector), "testing", "money")
	if !strings.Contains(tester, "Money is int64 rupiah.") || strings.Contains(tester, "Business logic lives in usecase/.") {
		t.Fatalf("testing inspector must get the generic repo playbook only:\n%s", tester)
	}
	foreman, _ := r.ctxFor(r.wo, string(model.AgentForeman), "", "money")
	if !strings.Contains(foreman, "Never escalate naming.") || strings.Contains(foreman, "Money is int64 rupiah.") || strings.Contains(foreman, "docs/review-backend.md") {
		t.Fatalf("foreman must get only foreman playbooks:\n%s", foreman)
	}
}

func TestRepoPlaybooksSurviveARestart(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.workspace(t, map[string]string{"docs/review-all.md": playbookDoc("Review rules", "inspector", "", "Money is int64 rupiah.")})
	r.f.syncRepo(ctx, r.wo, true)

	fresh := &KB{Bundle: r.know.Bundle, Idx: r.know.Idx, Emb: r.know.Emb, Log: slog.Default()}
	wo, err := r.wos.Get(ctx, r.wo.ID)
	if err != nil || wo.KBBaseSHA == "" {
		t.Fatalf("planning base not persisted: %+v %v", wo, err)
	}
	text, audit := fresh.Context(ctx, wo.ProjectID, string(model.AgentInspector), "backend", "money", r.f.workspaceDir(wo.ID), wo.KBBaseSHA)
	if !strings.Contains(text, "Money is int64 rupiah.") || audit["kb_repo_commit"] != wo.KBBaseSHA {
		t.Fatalf("repo playbook lost after restart (%v):\n%s", audit, text)
	}
}

func TestRepoPlaybooksAreReadAtEachWorkOrdersOwnBase(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.workspace(t, map[string]string{"docs/review-all.md": playbookDoc("Review rules", "inspector", "", "Rule from A.")})
	r.f.syncRepo(ctx, r.wo, true)

	b := *r.wo
	b.ID = "wo_b"
	if err := r.wos.Create(ctx, &b); err != nil {
		t.Fatal(err)
	}
	r.workspaceFor(t, b.ID, map[string]string{"docs/review-all.md": playbookDoc("Review rules", "inspector", "", "Rule from B.")})
	r.f.syncRepo(ctx, &b, true)

	a, _ := r.wos.Get(ctx, r.wo.ID)
	bb, _ := r.wos.Get(ctx, b.ID)
	if a.KBBaseSHA == bb.KBBaseSHA {
		t.Fatal("work orders must record their own base commits")
	}
	textA, auditA := r.ctxFor(a, string(model.AgentInspector), "backend", "x")
	textB, auditB := r.ctxFor(bb, string(model.AgentInspector), "backend", "x")
	if !strings.Contains(textA, "Rule from A.") || strings.Contains(textA, "Rule from B.") || auditA["kb_repo_commit"] != a.KBBaseSHA {
		t.Fatalf("work order A read another base:\n%s", textA)
	}
	if !strings.Contains(textB, "Rule from B.") || strings.Contains(textB, "Rule from A.") || auditB["kb_repo_commit"] != bb.KBBaseSHA {
		t.Fatalf("work order B read another base:\n%s", textB)
	}
}

func toAny(v any) []any {
	switch s := v.(type) {
	case []any:
		return s
	case []string:
		out := make([]any, len(s))
		for i, x := range s {
			out[i] = x
		}
		return out
	}
	return nil
}

func TestRepoPlaybooksSurviveAnotherWorkOrdersNewerBase(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.workspace(t, map[string]string{
		"docs/review.md": playbookDoc("Review rules", "inspector", "", "Review rule."),
		"docs/gone.md":   playbookDoc("Gone rules", "inspector", "", "Gone rule."),
	})
	r.f.syncRepo(ctx, r.wo, true)

	b := *r.wo
	b.ID = "wo_b"
	if err := r.wos.Create(ctx, &b); err != nil {
		t.Fatal(err)
	}
	r.workspaceFor(t, b.ID, map[string]string{"docs/review.md": playbookDoc("Review rules", "foreman", "", "Review rule.")})
	r.f.syncRepo(ctx, &b, true)

	a, _ := r.wos.Get(ctx, r.wo.ID)
	bb, _ := r.wos.Get(ctx, b.ID)
	inspA, _ := r.ctxFor(a, string(model.AgentInspector), "backend", "x")
	foreA, _ := r.ctxFor(a, string(model.AgentForeman), "", "x")
	if !strings.Contains(inspA, "Review rule.") || !strings.Contains(inspA, "Gone rule.") || strings.Contains(foreA, "Review rule.") {
		t.Fatalf("work order A lost its playbooks:\ninspector:\n%s\nforeman:\n%s", inspA, foreA)
	}
	inspB, _ := r.ctxFor(bb, string(model.AgentInspector), "backend", "x")
	foreB, _ := r.ctxFor(bb, string(model.AgentForeman), "", "x")
	if strings.Contains(inspB, "Review rule.") || strings.Contains(inspB, "Gone rule.") || !strings.Contains(foreB, "Review rule.") {
		t.Fatalf("work order B read the wrong playbooks:\ninspector:\n%s\nforeman:\n%s", inspB, foreB)
	}
}
