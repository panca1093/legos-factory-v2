package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"legos-factory/backend/internal/model"
)

// Cataloger: durable engineering knowledge. Recording is deterministic and
// never needs an LLM; the optional summary is one agent run per completed
// work order and is stored as a non-authoritative interpretation.

const (
	JobCatalogSummary = "catalog_summary"

	maxKnowledgeBody = 2000
	retrievalLimit   = 5
)

type Cataloger struct {
	Knowledge KnowledgeStore
	Proj      ProjectStore
	Settings  SettingsReader
	Log       *slog.Logger
}

// Record stores one knowledge entry for wo. Failures are logged, never
// propagated: knowledge capture must not break the workflow.
func (c *Cataloger) Record(ctx context.Context, wo *model.WorkOrder, kind string, trust model.Trust, actor, title, body string, refs ...string) string {
	if c == nil {
		return ""
	}
	proj, err := c.Proj.Get(ctx, wo.ProjectID)
	if err != nil {
		c.Log.Error("cataloger: project lookup failed", "wo", wo.ID, "err", err)
		return ""
	}
	k := &model.KnowledgeRecord{
		WorkspaceID: proj.WorkspaceID, ProjectID: proj.ID, WOID: wo.ID,
		RepoURL: proj.RepoURL, CommitSHA: wo.CommitSHA,
		Kind: kind, Trust: trust, Actor: actor,
		Title: sanitize(title, 200), Body: sanitize(body, maxKnowledgeBody), SourceRefs: refs,
	}
	if err := c.Knowledge.Insert(ctx, k); err != nil {
		c.Log.Error("cataloger: record failed", "wo", wo.ID, "kind", kind, "err", err)
		return ""
	}
	return k.ID
}

// Relevant returns prior knowledge from wo's own project, never another's.
func (c *Cataloger) Relevant(ctx context.Context, wo *model.WorkOrder) []model.KnowledgeRecord {
	if c == nil {
		return nil
	}
	found, err := c.Knowledge.Search(ctx, model.KnowledgeQuery{
		ProjectIDs: []string{wo.ProjectID},
		Text:       wo.Title + " " + wo.Objective + " " + wo.Acceptance,
		Limit:      retrievalLimit + 20,
	})
	if err != nil {
		c.Log.Error("cataloger: retrieval failed", "wo", wo.ID, "err", err)
		return nil
	}
	out := make([]model.KnowledgeRecord, 0, retrievalLimit)
	for _, k := range found {
		if k.WOID == wo.ID {
			continue
		}
		out = append(out, k)
		if len(out) == retrievalLimit {
			break
		}
	}
	return out
}

// Completed records the task summary as the work order's terminal entry,
// linking every record of the work order, and returns that record's id.
func (c *Cataloger) Completed(ctx context.Context, wo *model.WorkOrder, outcome, body string) string {
	if c == nil {
		return ""
	}
	prior, err := c.Knowledge.Search(ctx, model.KnowledgeQuery{WOID: wo.ID, Limit: 200})
	if err != nil {
		c.Log.Error("cataloger: completion lookup failed", "wo", wo.ID, "err", err)
	}
	refs := make([]string, 0, len(prior))
	for _, k := range prior {
		refs = append(refs, "knowledge:"+k.ID)
	}
	return c.Record(ctx, wo, model.KnowledgeWorkOrderCompleted, model.TrustVerified, "cataloger",
		fmt.Sprintf("Task summary: %s (%s)", clip(oneLine(wo.Title), 120), outcome), body, refs...)
}

// completeWO records the terminal task summary from the order's current state.
func (f *Factory) completeWO(ctx context.Context, wo *model.WorkOrder, outcome string) {
	if f.Catalog == nil {
		return
	}
	if cur, err := f.WO.Get(ctx, wo.ID); err == nil {
		wo = cur
	}
	f.Catalog.Completed(ctx, wo, outcome, f.taskSummary(ctx, wo, outcome))
}

const (
	summaryBudget      = 1800
	summaryMaxChecks   = 8
	summaryMaxItems    = 5
	summaryMainChanges = "Main changes:\n"
)

// summaryLevels shrink the per-item and goal caps until the sections ahead of
// Main changes leave room for it within summaryBudget.
var summaryLevels = []struct{ item, goal int }{{160, 300}, {100, 200}, {60, 100}, {30, 50}}

// taskSummary renders the deterministic five-section summary of wo's task.
func (f *Factory) taskSummary(ctx context.Context, wo *model.WorkOrder, outcome string) string {
	var decisions []model.KnowledgeRecord
	if recs, err := f.Catalog.Knowledge.Search(ctx, model.KnowledgeQuery{WOID: wo.ID, Limit: 200}); err == nil {
		for _, k := range recs {
			switch k.Kind {
			case model.KnowledgeFindingDecision, model.KnowledgeClarification, model.KnowledgePRDecision, model.KnowledgePlanApproved:
				decisions = append(decisions, k)
			}
		}
	}
	sort.SliceStable(decisions, func(i, j int) bool {
		if !decisions[i].CreatedAt.Equal(decisions[j].CreatedAt) {
			return decisions[i].CreatedAt.After(decisions[j].CreatedAt)
		}
		return decisions[i].ID < decisions[j].ID
	})

	verLines, failing, verNote := f.finalVerification(ctx, wo)
	var findings []string
	if all, err := f.Findings.List(ctx, wo.ID); err == nil {
		for _, fd := range all {
			switch fd.Status {
			case model.FindingOpen, model.FindingConfirmed, model.FindingEscalated:
				findings = append(findings, fmt.Sprintf("[%s/%s] %s (%s)", fd.Severity, fd.Status, oneLine(fd.Title), oneLine(fd.Location)))
			}
		}
	}
	var deviations []string
	if r := f.currentArchitectReport(wo); r != nil {
		deviations = r.Deviations
	}
	changes := f.changedFilesText(ctx, wo)

	var pre string
	for _, lv := range summaryLevels {
		var b strings.Builder
		fmt.Fprintf(&b, "Goal: %s\nObjective: %s\nAcceptance: %s\n\n",
			clip(oneLine(wo.Title), 120), clip(oneLine(wo.Objective), lv.goal), clip(oneLine(wo.Acceptance), lv.goal))
		fmt.Fprintf(&b, "Final verification: %s\n", verNote)
		if len(verLines) == 0 {
			b.WriteString("- no verification recorded\n")
		}
		for i, l := range verLines {
			if i == summaryMaxChecks {
				break
			}
			b.WriteString("- " + clip(l, lv.item) + "\n")
		}
		b.WriteString("\nOpen items / follow-up:\n")
		var open []string
		if outcome != "READY" {
			open = append(open, "Outcome: "+outcome)
		}
		for _, c := range capped(failing) {
			open = append(open, "Failing check: "+c)
		}
		for _, x := range capped(findings) {
			open = append(open, "Open finding: "+x)
		}
		for _, x := range capped(deviations) {
			open = append(open, "Deviation: "+oneLine(x))
		}
		if len(open) == 0 {
			b.WriteString("- none recorded\n")
		}
		for _, o := range open {
			b.WriteString("- " + clip(o, lv.item) + "\n")
		}
		b.WriteString("\nKey decisions and rationale:\n")
		if len(decisions) == 0 {
			b.WriteString("- none recorded\n")
		}
		for i, k := range decisions {
			if i == summaryMaxItems {
				break
			}
			b.WriteString("- " + clip(oneLine(k.Title+": "+k.Body), lv.item) + "\n")
		}
		b.WriteString("\n")
		pre = b.String()
		if len(pre)+len(summaryMainChanges)+60 <= summaryBudget {
			break
		}
	}
	room := summaryBudget - len(pre) - len(summaryMainChanges)
	if room < 0 {
		room = 0
	}
	return pre + summaryMainChanges + clip(changes, room)
}

func capped(s []string) []string {
	if len(s) > summaryMaxItems {
		return s[:summaryMaxItems]
	}
	return s
}

// finalVerification walks revisions down from wo.Revision to the newest one
// that has checks, returning its check lines, its failing checks and a label.
func (f *Factory) finalVerification(ctx context.Context, wo *model.WorkOrder) (lines, failing []string, label string) {
	for rev := wo.Revision; rev >= 0; rev-- {
		stored, err := f.Quality.ForRevision(ctx, wo.ID, rev)
		if err != nil || len(stored) == 0 {
			continue
		}
		checks := latestPerName(stored)
		sha := ""
		for _, c := range checks {
			if c.CommitSHA != "" {
				sha = c.CommitSHA
			}
		}
		where := "uncommitted / not verified on this revision"
		if sha != "" {
			where = "commit " + sha
		}
		label = fmt.Sprintf("revision %d, %s", rev, where)
		if rev != wo.Revision {
			label = fmt.Sprintf("current revision %d has no checks; last verified at %s", wo.Revision, label)
		}
		for _, c := range checks {
			lines = append(lines, fmt.Sprintf("%s: %s", c.Name, c.Outcome))
			if c.Outcome != string(model.Pass) {
				failing = append(failing, fmt.Sprintf("%s (%s) → %s", c.Name, oneLine(c.Command), c.Outcome))
			}
		}
		return lines, failing, label
	}
	return nil, nil, fmt.Sprintf("revision %d, no checks recorded", wo.Revision)
}

func (f *Factory) changedFilesText(ctx context.Context, wo *model.WorkOrder) string {
	if f.Sandbox == nil {
		return "changed files unavailable"
	}
	mu := f.wsLock(wo.ID)
	if !mu.TryLock() {
		return "workspace busy; changed files not listed"
	}
	files, err := f.Sandbox.ChangedFiles(ctx, f.workspaceDir(wo.ID))
	mu.Unlock()
	switch {
	case err != nil:
		return "changed files unavailable"
	case len(files) == 0:
		return "no changed files recorded"
	}
	return strings.Join(files, ", ")
}

// clip cuts s to at most max bytes on a rune boundary, marking the cut with an ellipsis.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 0 {
		return ""
	}
	cut := max - len("…")
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// Find searches knowledge visible to u. A requested project is checked
// against the caller's scope; no request means every accessible project.
func (c *Cataloger) Find(ctx context.Context, p *Projects, u *model.User, projectID string, q model.KnowledgeQuery) ([]model.KnowledgeRecord, error) {
	if projectID != "" {
		if err := p.Authorize(ctx, u, projectID); err != nil {
			return nil, err
		}
		q.ProjectIDs = []string{projectID}
	} else {
		scope, err := p.Scope(ctx, u)
		if err != nil {
			return nil, err
		}
		q.ProjectIDs = scope
	}
	return c.Knowledge.Search(ctx, q)
}

// Inspect returns one record, or not-found when it lies outside u's scope.
func (c *Cataloger) Inspect(ctx context.Context, p *Projects, u *model.User, id string) (*model.KnowledgeRecord, error) {
	k, err := c.Knowledge.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	ok, err := p.CanAccess(ctx, u, k.ProjectID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, model.ErrKnowledgeNotFound
	}
	return k, nil
}

func (c *Cataloger) summariesEnabled(ctx context.Context) bool {
	if c == nil || c.Settings == nil {
		return false
	}
	s, err := c.Settings.Get(ctx)
	return err == nil && s.CatalogerSummaries
}

// knowledgePrompt renders prior records for an agent prompt, labelled by trust.
func knowledgePrompt(records []model.KnowledgeRecord) string {
	if len(records) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nPrior knowledge from this project (trust in brackets; ai_interpretation is NOT authoritative):\n")
	for _, k := range records {
		fmt.Fprintf(&b, "- [%s] %s: %s (ref knowledge:%s)\n", k.Trust, k.Title, truncate(oneLine(k.Body), 300), k.ID)
	}
	return b.String()
}

// handleCatalogSummary runs the Cataloger model once over a completed work
// order's records and stores the result as an AI interpretation citing them.
func (f *Factory) handleCatalogSummary(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	records, err := f.Catalog.Knowledge.Search(ctx, model.KnowledgeQuery{WOID: wo.ID, Limit: 200})
	if err != nil {
		return err
	}
	refs := make([]string, 0, len(records)+1)
	var src strings.Builder
	for _, k := range records {
		refs = append(refs, "knowledge:"+k.ID)
		fmt.Fprintf(&src, "- [%s] %s: %s\n", k.Trust, k.Title, truncate(oneLine(k.Body), 400))
	}
	provider, modelID := f.Model(model.AgentCataloger, wo)
	res, runID, err := f.runAgent(ctx, wo, model.AgentCataloger, provider, modelID, catalogSummaryPrompt(wo, src.String()),
		map[string]string{"record_count": fmt.Sprintf("%d", len(records))}, "", runOpts{})
	if err != nil {
		return err
	}
	refs = append(refs, "run:"+runID)
	f.Catalog.Record(ctx, wo, model.KnowledgeSummary, model.TrustAIInterpretation, "cataloger",
		fmt.Sprintf("Summary of %q (AI-generated)", wo.Title), res.Text, refs...)
	f.event(wo.ID, "cataloger", "knowledge_summarized", "Cataloger summary recorded (AI interpretation)", nil)
	return nil
}

func catalogSummaryPrompt(wo *model.WorkOrder, records string) string {
	return fmt.Sprintf(`You are the Cataloger. Summarize the engineering knowledge produced by work order %q
for future work in the same project. Use only the records below; do not invent decisions.
Keep it under 150 words and plain text.

Records:
%s`, wo.Title, records)
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|secret|password|passwd|authorization)\b(\s*[:=]\s*)("[^"]*"|'[^']*'|\S+)`),
	regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16})\b`),
}

// sanitize redacts likely credentials and bounds size so the knowledge
// store never holds secrets or unrestricted logs.
func sanitize(s string, max int) string {
	s = secretPatterns[0].ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	s = secretPatterns[1].ReplaceAllString(s, "$1$2[REDACTED]")
	s = secretPatterns[2].ReplaceAllString(s, "[REDACTED]")
	return truncate(strings.TrimSpace(s), max)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max] + "… [truncated]"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func orUnversioned(sha string) string {
	if sha == "" {
		return "unversioned"
	}
	return sha
}
