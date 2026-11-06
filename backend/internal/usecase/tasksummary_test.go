package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
	"legos-factory/backend/internal/store"
)

type summaryEnv struct {
	f       *Factory
	wos     *store.WorkOrderRepo
	quality *store.QualityRepo
	finds   *store.FindingRepo
	bundle  *kb.Bundle
	runDB   *sql.DB
}

func newSummaryEnv(t *testing.T, files []string, withCatalog bool) summaryEnv {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := (&store.UserRepo{DB: db}).Create(ctx, model.User{ID: "u1", Email: "u1@t", Name: "U", Role: model.RoleEngineer}); err != nil {
		t.Fatal(err)
	}
	proj := &store.ProjectRepo{DB: db}
	if err := proj.Create(ctx, &model.Project{ID: "p1", Name: "p"}); err != nil {
		t.Fatal(err)
	}
	bundle, err := kb.OpenBundle(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	env := summaryEnv{
		wos: &store.WorkOrderRepo{DB: db}, quality: &store.QualityRepo{DB: db},
		finds: &store.FindingRepo{DB: db}, bundle: bundle, runDB: db,
	}
	env.f = &Factory{
		WO: env.wos, Findings: env.finds, Quality: env.quality, Proj: proj,
		Sched: &fakeQueue{}, Bus: nopBus{}, Log: slog.Default(),
		Sandbox: filesSandbox{files: files}, DataDir: t.TempDir(),
	}
	if withCatalog {
		env.f.Catalog = &Cataloger{Bundle: bundle, Log: slog.Default()}
		env.f.KB = &KB{Bundle: bundle, Idx: &store.KBRepo{DB: db}, Emb: kb.HashEmbedder{}, Log: slog.Default()}
	}
	return env
}

func (e summaryEnv) newWO(t *testing.T, id, title, objective, acceptance string, state model.State) *model.WorkOrder {
	t.Helper()
	wo := &model.WorkOrder{ID: id, ProjectID: "p1", Title: title, Objective: objective, Acceptance: acceptance, State: state, CreatedBy: "u1"}
	if err := e.wos.Create(context.Background(), wo); err != nil {
		t.Fatal(err)
	}
	return wo
}

func (e summaryEnv) check(t *testing.T, woID, name string, rev int64, outcome, sha string) {
	t.Helper()
	err := e.quality.Save(context.Background(), model.QualityCheck{
		ID: fmt.Sprintf("q_%s_%s_%d_%s", woID, name, rev, outcome), WOID: woID, Name: name, Command: "run " + name,
		Outcome: outcome, Revision: rev, CommitSHA: sha,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e summaryEnv) completed(t *testing.T, woID string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.bundle.Root(), "projects", "p1", "log.md"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.Contains(l, "`"+woID+"` **"+model.KnowledgeWorkOrderCompleted+"**") {
			out = append(out, l)
		}
	}
	return out
}

func (e summaryEnv) summary(t *testing.T, woID, outcome string) string {
	t.Helper()
	return e.f.taskSummary(context.Background(), mustGet(t, e, woID), outcome)
}

func (e summaryEnv) event(t *testing.T, woID, actor, kind, message string) {
	t.Helper()
	if _, err := e.wos.AppendEvent(context.Background(), woID, actor, kind, message, nil); err != nil {
		t.Fatal(err)
	}
}

var summaryHeaders = []string{"## Goal", "## Final verification", "## Open items and follow-up", "## Key decisions and rationale", "## Main changes"}

func requireHeadersInOrder(t *testing.T, body string) {
	t.Helper()
	at := 0
	for _, h := range summaryHeaders {
		i := strings.Index(body[at:], h)
		if i < 0 {
			t.Fatalf("missing or out-of-order header %q in:\n%s", h, body)
		}
		at += i + len(h)
	}
}

func TestReadyAfterFailThenPassWritesOneSummary(t *testing.T) {
	env := newSummaryEnv(t, []string{"a.go", "b.go"}, true)
	ctx := context.Background()
	wo := env.newWO(t, "wo_ready", "Ship it", "Do the thing", "It works", model.StateNeedsHumanReview)
	env.check(t, wo.ID, "typecheck", 0, "FAIL", "")
	env.check(t, wo.ID, "typecheck", 1, "PASS", "deadbeef1234")
	env.check(t, wo.ID, "test", 1, "PASS", "deadbeef1234")
	if err := env.wos.UpdateFields(ctx, wo.ID, map[string]any{"revision": 1}); err != nil {
		t.Fatal(err)
	}
	env.event(t, wo.ID, "human", "finding_disputed", "nil check: false positive because the caller guards it")

	env.f.completeWO(ctx, wo, "READY")

	got := env.completed(t, wo.ID)
	if len(got) != 1 {
		t.Fatalf("want exactly one work_order_completed log line, got %d", len(got))
	}
	if !strings.Contains(got[0], "(cataloger) — Task summary: Ship it (READY) — summary document: projects/p1/task/") {
		t.Fatalf("log line: %q", got[0])
	}
	body := env.summary(t, wo.ID, "READY")
	requireHeadersInOrder(t, body)
	if !strings.Contains(body, "Revision 1, commit deadbeef1234") {
		t.Fatalf("missing revision-1 commit:\n%s", body)
	}
	dec := body[strings.Index(body, "## Key decisions and rationale"):strings.Index(body, "## Main changes")]
	if !strings.Contains(dec, "- finding_disputed: nil check: false positive") {
		t.Fatalf("seeded decision missing under Key decisions:\n%s", body)
	}
	if !strings.Contains(body, "- a.go\n- b.go") {
		t.Fatalf("changed files missing:\n%s", body)
	}
}

func TestCancelAfterFailedVerificationShowsEarlierFailAsUncommitted(t *testing.T) {
	env := newSummaryEnv(t, nil, true)
	ctx := context.Background()
	wo := env.newWO(t, "wo_cancel", "Cancel me", "obj", "acc", model.StateImplementing)
	env.check(t, wo.ID, "test", 0, "FAIL", "")
	if err := env.wos.UpdateFields(ctx, wo.ID, map[string]any{"revision": 1}); err != nil {
		t.Fatal(err)
	}

	if err := env.f.Cancel(ctx, wo.ID); err != nil {
		t.Fatal(err)
	}

	if got := env.completed(t, wo.ID); len(got) != 1 {
		t.Fatalf("want one summary line, got %d", len(got))
	}
	body := env.summary(t, wo.ID, "CANCELLED")
	requireHeadersInOrder(t, body)
	if !strings.Contains(body, "Revision 0, uncommitted / not verified on this revision (revision 1 has no checks recorded)") || !strings.Contains(body, "- test → FAIL") {
		t.Fatalf("earlier FAIL not shown as uncommitted:\n%s", body)
	}
	open := body[strings.Index(body, "## Open items and follow-up"):strings.Index(body, "## Key decisions")]
	if !strings.Contains(open, "Outcome: CANCELLED") || !strings.Contains(open, "- check test FAIL") {
		t.Fatalf("open items: %s", open)
	}
}

func TestBlockedWithNothingRecordedRendersFallbacks(t *testing.T) {
	env := newSummaryEnv(t, nil, true)
	wo := env.newWO(t, "wo_blocked", "Blocked", "obj", "acc", model.StateBlocked)

	env.f.completeWO(context.Background(), wo, "BLOCKED")

	body := env.summary(t, wo.ID, "BLOCKED")
	requireHeadersInOrder(t, body)
	for _, want := range []string{"No checks recorded", "Outcome: BLOCKED", "none recorded", "none (no changes in workspace)"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing fallback %q:\n%s", want, body)
		}
	}
}

func TestOversizedWorkOrderStaysWithinBudget(t *testing.T) {
	var files []string
	for i := 0; i < 50; i++ {
		files = append(files, fmt.Sprintf("internal/some/deep/path/file_%02d.go", i))
	}
	env := newSummaryEnv(t, files, true)
	ctx := context.Background()
	long := strings.Repeat("x", 600)
	wo := env.newWO(t, "wo_big", long, long, long, model.StateBlocked)
	for i := 0; i < 10; i++ {
		env.check(t, wo.ID, fmt.Sprintf("check%d", i), 0, "FAIL", "")
	}
	env.seedFindings(t, wo.ID, 20, long)
	devs := make([]string, 20)
	for i := range devs {
		devs[i] = long
	}
	env.setReport(t, wo.ID, 0, devs)
	for i := 0; i < 10; i++ {
		env.event(t, wo.ID, "human", "finding_disputed", long)
	}

	env.f.completeWO(ctx, wo, "BLOCKED")

	body := env.summary(t, wo.ID, "BLOCKED")
	if len(body) > summaryBudget {
		t.Fatalf("body %d bytes exceeds budget", len(body))
	}
	requireHeadersInOrder(t, body)
	if head := body[:strings.Index(body, "## Main changes")]; strings.Contains(head, "[truncated]") {
		t.Fatalf("leading sections truncated:\n%s", body)
	}
}

func (e summaryEnv) seedFindings(t *testing.T, woID string, n int, title string) {
	t.Helper()
	ctx := context.Background()
	if err := (&store.RunRepo{DB: e.runDB}).Start(ctx, "run_"+woID, woID, "", "inspector", "mock"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		err := e.finds.Create(ctx, model.Finding{ID: fmt.Sprintf("f%s%02d", woID, i), RunID: "run_" + woID, WOID: woID,
			Severity: "major", Title: title, Location: "x.go", Status: model.FindingOpen, Specialty: "testing"})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func (e summaryEnv) setReport(t *testing.T, woID string, rev int, fixes []string) {
	t.Helper()
	var gaps []convergeGap
	for _, f := range fixes {
		gaps = append(gaps, convergeGap{Type: "partial", Layer: "code", Cites: "spec", Evidence: "x.go", Fix: f})
	}
	raw, _ := json.Marshal(architectReport{Revision: rev, Gaps: gaps})
	if err := e.wos.UpdateFields(context.Background(), woID, map[string]any{"architect_report": string(raw)}); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyCurrentRevisionDeviationsListed(t *testing.T) {
	env := newSummaryEnv(t, nil, true)
	ctx := context.Background()
	wo := env.newWO(t, "wo_dev", "Dev", "obj", "acc", model.StateBlocked)
	if err := env.wos.UpdateFields(ctx, wo.ID, map[string]any{"revision": 2}); err != nil {
		t.Fatal(err)
	}
	env.setReport(t, wo.ID, 1, []string{"stale deviation"})
	env.f.completeWO(ctx, wo, "BLOCKED")
	if body := env.summary(t, wo.ID, "BLOCKED"); strings.Contains(body, "stale deviation") {
		t.Fatalf("older-revision deviation leaked:\n%s", body)
	}

	env.setReport(t, wo.ID, 2, []string{"fresh deviation"})
	if got := env.f.currentArchitectReport(mustGet(t, env, wo.ID)); got == nil || got.Gaps[0].Fix != "fresh deviation" {
		t.Fatalf("current report not visible: %+v", got)
	}
}

func mustGet(t *testing.T, e summaryEnv, id string) *model.WorkOrder {
	t.Helper()
	wo, err := e.wos.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return wo
}

func TestCancelWithNilCatalogDoesNotPanic(t *testing.T) {
	env := newSummaryEnv(t, nil, false)
	wo := env.newWO(t, "wo_nilcat", "t", "o", "a", model.StateImplementing)
	if err := env.f.Cancel(context.Background(), wo.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, env, wo.ID); got.State != model.StateCancelled {
		t.Fatalf("state %s", got.State)
	}
}

func TestFormerDecisionKindsAreCoveredByEvents(t *testing.T) {
	env := newSummaryEnv(t, nil, true)
	ctx := context.Background()
	wo := env.newWO(t, "wo_dec", "Decide", "obj", "acc", model.StateBlocked)

	// former journal kind -> the wof_events kind that now feeds Key decisions
	covers := []struct{ formerKind, event string }{
		{model.KnowledgePlanApproved, "plan_approved"},
		{model.KnowledgePlanRevision, "plan_revision_human"},
		{model.KnowledgeClarification, "clarification_answered"},
		{model.KnowledgeFindingDecision, "finding_disputed"},
		{model.KnowledgePRDecision, "pr_approved"},
	}
	for _, c := range covers {
		env.event(t, wo.ID, "human", c.event, "msg for "+c.formerKind)
	}
	for _, extra := range []struct{ actor, kind string }{
		{"factory", "plan_revision_requested"}, {"human", "revision_requested"}, {"human", "pr_declined"},
		{"jev", "plan_gate"}, {"human", "finding_confirmed"}, {"human", "finding_resolved"}, {"human", "finding_escalated"}, {"foreman", "clarification_answered"},
	} {
		env.event(t, wo.ID, extra.actor, extra.kind, "msg for "+extra.kind)
	}
	// architect_review is covered by plan_ready, an AI interpretation kept out of the decisions
	for _, noise := range []string{"plan_ready", "finding", "state_changed", "execution_queued", "plan_reviewed"} {
		env.event(t, wo.ID, "architect", noise, "noise "+noise)
	}

	lines := env.f.decisionLines(ctx, wo.ID)
	joined := strings.Join(lines, "\n")
	for _, c := range covers {
		if !strings.Contains(joined, "- "+c.event+": msg for "+c.formerKind) {
			t.Fatalf("former %s not covered by %s:\n%s", c.formerKind, c.event, joined)
		}
	}
	if len(lines) != len(covers)+8 || strings.Contains(joined, "noise") {
		t.Fatalf("want %d decision lines and no noise, got:\n%s", len(covers)+8, joined)
	}
	if lines[0] != "- clarification_answered: msg for clarification_answered" || !strings.HasPrefix(lines[len(lines)-1], "- plan_approved") {
		t.Fatalf("newest decision must come first:\n%s", joined)
	}
	for _, kind := range decisionEventKinds {
		if !strings.Contains(joined, "- "+kind+":") {
			t.Fatalf("decision kind %s missing:\n%s", kind, joined)
		}
	}
}
