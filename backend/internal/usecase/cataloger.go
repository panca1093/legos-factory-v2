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
		fmt.Sprintf("Task summary: %s (%s)", wo.Title, outcome), body, refs...)
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
	summaryBudget   = 1800
	summaryMaxLines = 8
	summaryMaxItems = 5
	summaryGoalCap  = 150
	summaryMaxFiles = 10

	summaryGoalBudget     = 480
	summaryVerifyBudget   = 330
	summaryOpenBudget     = 400
	summaryDecisionBudget = 290
)

// taskSummary renders the deterministic five-section summary of wo's task.
func (f *Factory) taskSummary(ctx context.Context, wo *model.WorkOrder, outcome string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\nObjective: %s\nAcceptance: %s\n\n",
		orNone(clip(oneLine(wo.Title), 120)), orNone(clip(oneLine(wo.Objective), summaryGoalCap)), orNone(clip(oneLine(wo.Acceptance), summaryGoalCap)))

	fitSection(&b, 0, summaryGoalBudget)
	start := b.Len()
	b.WriteString("Final verification:\n")
	header, checks := f.finalVerification(ctx, wo)
	b.WriteString(header + "\n")
	var failing []model.QualityCheck
	var lines []string
	for _, c := range checks {
		lines = append(lines, clip(fmt.Sprintf("- %s → %s", oneLine(c.Name), c.Outcome), 120))
		if c.Outcome != string(model.Pass) {
			failing = append(failing, c)
		}
	}
	writeCapped(&b, summaryMaxLines, lines)

	fitSection(&b, start, summaryVerifyBudget)
	start = b.Len()
	b.WriteString("\nOpen items / follow-up:\n")
	empty := true
	if outcome != "READY" {
		b.WriteString("Outcome: " + outcome + "\n")
		empty = false
	}
	var failLines, findLines, devLines []string
	for _, c := range failing {
		failLines = append(failLines, fmt.Sprintf("- check %s %s", oneLine(c.Name), c.Outcome))
	}
	if all, err := f.Findings.List(ctx, wo.ID); err == nil {
		for _, fd := range all {
			switch fd.Status {
			case model.FindingOpen, model.FindingConfirmed, model.FindingEscalated:
				findLines = append(findLines, clip(fmt.Sprintf("- [%s] %s (%s)", fd.Severity, oneLine(fd.Title), oneLine(fd.Location)), 160))
			}
		}
	}
	if r := f.currentArchitectReport(wo); r != nil {
		for _, d := range r.Deviations {
			devLines = append(devLines, clip("- deviation: "+oneLine(d), 160))
		}
	}
	for _, l := range [][]string{failLines, findLines, devLines} {
		if len(l) > 0 {
			empty = false
			writeCapped(&b, summaryMaxItems, l)
		}
	}
	if empty {
		b.WriteString("none\n")
	}

	fitSection(&b, start, summaryOpenBudget)
	start = b.Len()
	b.WriteString("\nKey decisions and rationale:\n")
	var decisions []model.KnowledgeRecord
	if recs, err := f.Catalog.Knowledge.Search(ctx, model.KnowledgeQuery{WOID: wo.ID, Limit: 200}); err == nil {
		for _, k := range recs {
			switch k.Kind {
			case model.KnowledgePlanApproved, model.KnowledgePlanRevision, model.KnowledgeClarification,
				model.KnowledgeArchitectReview, model.KnowledgeFindingDecision, model.KnowledgePRDecision:
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
	if len(decisions) > summaryMaxItems {
		decisions = decisions[:summaryMaxItems]
	}
	if len(decisions) == 0 {
		b.WriteString("none recorded\n")
	}
	for _, k := range decisions {
		b.WriteString(clip(fmt.Sprintf("- %s: %s", k.Kind, oneLine(k.Title+" — "+k.Body)), 160) + "\n")
	}
	fitSection(&b, start, summaryDecisionBudget)

	b.WriteString("\nMain changes:\n")
	body := b.String()
	files, note := f.changedFiles(ctx, wo)
	if note != "" {
		body += note + "\n"
	} else {
		for i, line := range changeLines(files) {
			if len(body)+len(line)+1 > summaryBudget {
				body += fmt.Sprintf("(+%d more)\n", len(changeLines(files))-i)
				break
			}
			body += line + "\n"
		}
	}
	return truncate(body, summaryBudget)
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max] + "…"
}

func fitSection(b *strings.Builder, start, budget int) {
	all := b.String()
	sec := all[start:]
	for len(sec) > budget {
		trimmed := strings.TrimRight(sec, "\n")
		i := strings.LastIndex(trimmed, "\n")
		if i < 0 {
			break
		}
		sec = trimmed[:i+1]
	}
	b.Reset()
	b.WriteString(all[:start] + sec)
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func writeCapped(b *strings.Builder, max int, lines []string) {
	for i, l := range lines {
		if i == max {
			fmt.Fprintf(b, "(+%d more)\n", len(lines)-max)
			return
		}
		b.WriteString(l + "\n")
	}
}

func changeLines(files []string) []string {
	if len(files) <= summaryMaxFiles {
		out := make([]string, len(files))
		for i, p := range files {
			out[i] = "- " + p
		}
		return out
	}
	counts := map[string]int{}
	for _, p := range files {
		dir, _, _ := strings.Cut(p, "/")
		if !strings.Contains(p, "/") {
			dir = "."
		}
		counts[dir]++
	}
	dirs := make([]string, 0, len(counts))
	for d := range counts {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	out := make([]string, len(dirs))
	for i, d := range dirs {
		out[i] = fmt.Sprintf("- %s/ (%d files)", d, counts[d])
	}
	return out
}

// finalVerification walks revisions down from wo.Revision to the newest one
// that has checks, returning its header line and latest check per name.
func (f *Factory) finalVerification(ctx context.Context, wo *model.WorkOrder) (string, []model.QualityCheck) {
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
		header := fmt.Sprintf("Revision %d, uncommitted / not verified on this revision", rev)
		if sha != "" {
			header = fmt.Sprintf("Revision %d, commit %s", rev, sha)
		}
		if rev < wo.Revision {
			header += fmt.Sprintf(" (revision %d has no checks recorded)", wo.Revision)
		}
		return header, checks
	}
	return "No checks recorded", nil
}

// changedFiles returns the workspace diff, or a note when it cannot be read.
func (f *Factory) changedFiles(ctx context.Context, wo *model.WorkOrder) ([]string, string) {
	mu := f.wsLock(wo.ID)
	if !mu.TryLock() {
		return nil, "unavailable (workspace busy)"
	}
	files, err := f.Sandbox.ChangedFiles(ctx, f.workspaceDir(wo.ID))
	mu.Unlock()
	if err != nil {
		return nil, fmt.Sprintf("unavailable (%v)", err)
	}
	if len(files) == 0 {
		return nil, "none (no changes in workspace)"
	}
	return files, ""
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
