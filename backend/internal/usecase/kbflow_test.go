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
	planJSON    = `{"decision":"APPROVE","summary":"s","tasks":[{"id":"t1","title":"Round money","objective":"o","role":"builder"}]}`
	reviewJSON  = `{"decision":"APPROVE","notes":"ok"}`
	noFindings  = `{"findings":[]}`
	moneyDoc    = "# Money\n\nMoney is integer rupiah; never use floats for rounding.\n"
	routingBody = "---\ntype: Reviewer Routing\nroutes:\n  - paths: [\"src/**/*.ts\", \"!src/**/*.test.ts\"]\n    reviewers: [backend]\n  - paths: [\"**/*.test.ts\"]\n    reviewers: [testing]\n---\n\n# Routing\n"
)

type replyHarness struct {
	mu       sync.Mutex
	prompts  map[string][]string // by role
	specials []string
	last     model.AgentRequest
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
	h.last = req
	return fakeSession{}, nil
}
func (*replyHarness) Cancel(context.Context, string) error { return nil }
func (h *replyHarness) Result(context.Context, string) (model.AgentResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	text := map[model.AgentRole]string{model.AgentArchitect: planJSON, model.AgentForeman: reviewJSON, model.AgentInspector: noFindings}[h.last.Role]
	return model.AgentResult{Text: text}, nil
}

type filesSandbox struct {
	dirSandbox
	files []string
}

func (s filesSandbox) ChangedFiles(context.Context, string) ([]string, error) { return s.files, nil }

type rig struct {
	f    *Foreman
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
	f, wo, wos := monitorForeman(t, h)
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
	f.Proj, f.KB, f.Sched, f.Findings = projects, know, q, &store.FindingRepo{DB: db}
	f.Catalog = &Cataloger{Knowledge: &JournalStore{KB: know, Proj: projects}, Proj: projects, Settings: &store.SettingsRepo{DB: db}, Log: slog.Default()}
	f.Model = func(model.AgentRole, *model.WorkOrder) (string, string) { return "p", "m" }
	f.DataDir = t.TempDir()
	f.Sandbox = filesSandbox{}
	wo.Title, wo.Objective, wo.Acceptance = "Round money", "Make money rounding use integer rupiah", "no floats"
	return &rig{f, wo, wos, db, know, h, q}
}

func (r *rig) workspace(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := r.f.workspaceDir(r.wo.ID)
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
	for _, want := range []string{"Factory playbooks for your role", "When the Foreman escalates to a human", "Escalate when", "docs/money.md", kbPrecedence} {
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
	if !contains(docs, "factory::playbooks/foreman/escalation.md") || !contains(docs, "repo:p1:docs/money.md") {
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
	if text, _ := r.know.Context(ctx, "p1", "inspector", "backend", "anything"); strings.Contains(text, "Missing nil check") {
		t.Fatal("a draft must never reach an agent")
	}

	docs := &KBDocs{KB: r.know, Projects: &Projects{Store: r.f.Proj, Members: &store.MemberRepo{DB: r.db}, Users: &store.UserRepo{DB: r.db}},
		Proj: r.f.Proj, WO: r.wos, DataDir: r.f.DataDir}
	u := &model.User{ID: "u1", Role: model.RoleOwner}
	approved, err := docs.Approve(ctx, u, d.ID, "good rule")
	if err != nil || approved.Status != model.KBStatusStable || approved.Trust != model.KBTrustHuman {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	text, audit := r.know.Context(ctx, "p1", "inspector", "backend", "anything")
	if !strings.Contains(text, "Missing nil check f1") || !strings.Contains(text, kbPrecedence) || !strings.Contains(fmt.Sprint(audit["kb_docs"]), d.ID) {
		t.Fatalf("approved playbook must be inlined for the backend inspector:\n%s", text)
	}
	if other, _ := r.know.Context(ctx, "p1", "inspector", "frontend", "anything"); strings.Contains(other, "Missing nil check") {
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
	r.f.Catalog.Record(ctx, prev, model.KnowledgeClarification, model.TrustHumanDecision, "human",
		"Clarification: Should discount rounding go up or down?", "Question: Should discount rounding go up or down?\nAnswer: Always round down.")
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
	if !strings.Contains(body, "The Foreman asked a similar question before") || !strings.Contains(body, "Always round down.") || !strings.Contains(body, "Round down as well.") {
		t.Fatalf("body: %s", body)
	}
}

func TestRepoPlaybooksAreInlinedBeforeFactoryOnes(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	pb := func(title, role, specialty, rule string) string {
		s := "---\ntype: Playbook\ntitle: " + title + "\nrole: " + role + "\n"
		if specialty != "" {
			s += "specialty: " + specialty + "\n"
		}
		return s + "---\n\n" + rule + "\n"
	}
	r.workspace(t, map[string]string{
		"docs/review-backend.md": pb("Backend review rules", "inspector", "backend", "Business logic lives in usecase/."),
		"docs/review-all.md":     pb("Review rules for every inspector", "inspector", "", "Money is int64 rupiah."),
		"docs/foreman.md":        pb("Foreman rules", "foreman", "", "Never escalate naming."),
	})
	r.f.syncRepo(ctx, r.wo, true)
	// the builder may edit the file after the base commit; the playbook is read at the indexed commit
	if err := os.WriteFile(filepath.Join(r.f.workspaceDir(r.wo.ID), "docs/review-backend.md"), []byte(pb("Backend review rules", "inspector", "backend", "Anything goes.")), 0o644); err != nil {
		t.Fatal(err)
	}

	backend, audit := r.know.Context(ctx, r.wo.ProjectID, string(model.AgentInspector), "backend", "money")
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
	if !contains(toAny(audit["kb_docs"]), "repo:"+r.wo.ProjectID+":docs/review-backend.md") {
		t.Fatalf("audit lacks the repo playbook: %v", audit["kb_docs"])
	}

	tester, _ := r.know.Context(ctx, r.wo.ProjectID, string(model.AgentInspector), "testing", "money")
	if !strings.Contains(tester, "Money is int64 rupiah.") || strings.Contains(tester, "Business logic lives in usecase/.") {
		t.Fatalf("testing inspector must get the generic repo playbook only:\n%s", tester)
	}
	if strings.Contains(backend, "- docs/review-backend.md") {
		t.Fatal("playbooks must not also be listed as pointers")
	}
	foreman, _ := r.know.Context(ctx, r.wo.ProjectID, string(model.AgentForeman), "", "money")
	if !strings.Contains(foreman, "Never escalate naming.") || strings.Contains(foreman, "Money is int64 rupiah.") {
		t.Fatalf("foreman must get only foreman playbooks:\n%s", foreman)
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
