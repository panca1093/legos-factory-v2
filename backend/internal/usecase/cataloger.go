package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
)

// Cataloger: the work-order journal is one log line per record in the
// project's log.md. Recording is deterministic and never needs an LLM.

const (
	maxKnowledgeBody = 2000
	maxLogBody       = 300
)

type Cataloger struct {
	Bundle *kb.Bundle
	Log    *slog.Logger
}

// Record appends one line to wo's project log and returns "". Failures are
// logged, never propagated: knowledge capture must not break the workflow.
func (c *Cataloger) Record(ctx context.Context, wo *model.WorkOrder, kind string, trust model.Trust, actor, title, body string, refs ...string) string {
	if c == nil {
		return ""
	}
	line := fmt.Sprintf("* %s `%s` **%s** (%s) — %s", kb.Clock().Format("15:04"), wo.ID, kind, actor, oneLine(sanitize(title, 200)))
	if b := clip(oneLine(sanitize(body, maxKnowledgeBody)), maxLogBody); b != "" {
		line += " — " + b
	}
	if _, err := c.Bundle.Append("projects/"+wo.ProjectID+"/log.md", line, "Log: "+oneLine(kind+" "+wo.ID)); err != nil {
		c.Log.Error("cataloger: log append failed", "wo", wo.ID, "kind", kind, "err", err)
	}
	return ""
}

// Relevant returns prior knowledge for the Architect; it is empty until retrieval reads documents.
func (c *Cataloger) Relevant(ctx context.Context, wo *model.WorkOrder) []model.KnowledgeRecord {
	return nil
}

// Completed logs the task summary as the work order's terminal line.
func (c *Cataloger) Completed(ctx context.Context, wo *model.WorkOrder, outcome, body string) string {
	return c.Record(ctx, wo, model.KnowledgeWorkOrderCompleted, model.TrustVerified, "cataloger",
		fmt.Sprintf("Task summary: %s (%s)", wo.Title, outcome), body)
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

var decisionEventKinds = []string{
	"plan_approved", "plan_revision_requested", "plan_revision_human", "clarification_answered",
	"revision_requested", "pr_approved", "pr_declined",
	"finding_" + model.FindingConfirmed, "finding_" + model.FindingDisputed,
	"finding_" + model.FindingResolved, "finding_" + model.FindingEscalated,
}

const (
	summaryBudget    = 1800
	summaryMaxItems  = 3
	summaryMaxChecks = 8
)

// taskSummary renders the deterministic five-section summary of wo's task.
func (f *Factory) taskSummary(ctx context.Context, wo *model.WorkOrder, outcome string) string {
	orNone := func(s string) string {
		if s = oneLine(s); s == "" {
			return "(none)"
		}
		return s
	}
	more := func(n int) string { return fmt.Sprintf("(+%d more)", n-summaryMaxItems) }

	goal := fitLines("Goal: "+clip(orNone(wo.Title), 100), []string{
		"Objective: " + clip(orNone(wo.Objective), 150),
		"Acceptance: " + clip(orNone(wo.Acceptance), 150),
	}, 450)

	verLines, failing := f.finalVerification(ctx, wo)
	ver := fitLines("Final verification:", verLines, 300)

	var open []string
	if outcome != "READY" {
		open = append(open, "- Outcome: "+outcome)
	}
	for i, c := range failing {
		if i == summaryMaxItems {
			open = append(open, more(len(failing)))
			break
		}
		open = append(open, "- check "+c)
	}
	if all, err := f.Findings.List(ctx, wo.ID); err == nil {
		var lines []string
		for _, fd := range all {
			switch fd.Status {
			case model.FindingOpen, model.FindingConfirmed, model.FindingEscalated:
				lines = append(lines, clip(fmt.Sprintf("- [%s] %s (%s)", fd.Severity, oneLine(fd.Title), oneLine(fd.Location)), 100))
			}
		}
		for i, l := range lines {
			if i == summaryMaxItems {
				open = append(open, more(len(lines)))
				break
			}
			open = append(open, l)
		}
	}
	if r := f.currentArchitectReport(wo); r != nil {
		for i, d := range r.Deviations {
			if i == summaryMaxItems {
				open = append(open, more(len(r.Deviations)))
				break
			}
			open = append(open, clip("- deviation: "+oneLine(d), 100))
		}
	}
	if len(open) == 0 {
		open = []string{"none"}
	}
	openSection := fitLines("Open items / follow-up:", open, 450)

	decLines := f.decisionLines(ctx, wo.ID)
	if len(decLines) == 0 {
		decLines = []string{"none recorded"}
	}
	dec := fitLines("Key decisions and rationale:", decLines, 350)

	body := strings.Join([]string{goal, ver, openSection, dec}, "\n\n") + "\n\nMain changes:"
	for _, l := range f.changedFileLines(ctx, wo) {
		if len(body)+1+len(l) > summaryBudget {
			body += "\n(+more)"
			break
		}
		body += "\n" + l
	}
	if len(body) > summaryBudget {
		body = truncate(body, summaryBudget-len("… [truncated]"))
	}
	return body
}

// decisionLines lists the work order's decision events, newest first.
func (f *Factory) decisionLines(ctx context.Context, woID string) []string {
	evs, err := f.WO.EventsByKind(ctx, woID, decisionEventKinds)
	if err != nil {
		return nil
	}
	lines := make([]string, 0, len(evs))
	for i := len(evs) - 1; i >= 0; i-- {
		line := "- " + evs[i].Kind
		if m := oneLine(evs[i].Message); m != "" {
			line += ": " + m
		}
		lines = append(lines, clip(line, 160))
	}
	return lines
}

// finalVerification walks revisions down from wo.Revision to the newest one
// that has checks, returning its section lines and its failing checks.
func (f *Factory) finalVerification(ctx context.Context, wo *model.WorkOrder) (lines, failing []string) {
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
		head := fmt.Sprintf("Revision %d, uncommitted / not verified on this revision", rev)
		if sha != "" {
			head = fmt.Sprintf("Revision %d, commit %s", rev, sha)
		}
		if rev != wo.Revision {
			head += fmt.Sprintf(" (revision %d has no checks recorded)", wo.Revision)
		}
		lines = append(lines, head)
		for i, c := range checks {
			if i == summaryMaxChecks {
				lines = append(lines, fmt.Sprintf("(+%d more)", len(checks)-summaryMaxChecks))
				break
			}
			lines = append(lines, clip(fmt.Sprintf("- %s → %s", c.Name, c.Outcome), 120))
		}
		for _, c := range checks {
			if c.Outcome != string(model.Pass) {
				failing = append(failing, fmt.Sprintf("%s %s", c.Name, c.Outcome))
			}
		}
		return lines, failing
	}
	return []string{"No checks recorded"}, nil
}

func (f *Factory) changedFileLines(ctx context.Context, wo *model.WorkOrder) []string {
	if f.Sandbox == nil {
		return []string{"unavailable (no sandbox)"}
	}
	mu := f.wsLock(wo.ID)
	if !mu.TryLock() {
		return []string{"unavailable (workspace busy)"}
	}
	files, err := f.Sandbox.ChangedFiles(ctx, f.workspaceDir(wo.ID))
	mu.Unlock()
	switch {
	case err != nil:
		return []string{"unavailable (" + err.Error() + ")"}
	case len(files) == 0:
		return []string{"none (no changes in workspace)"}
	case len(files) <= 10:
		out := make([]string, len(files))
		for i, p := range files {
			out[i] = "- " + p
		}
		return out
	}
	counts := map[string]int{}
	for _, p := range files {
		dir, _, ok := strings.Cut(p, "/")
		if !ok {
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

// fitLines appends lines under head while the result stays within budget, ending with "(+N more)".
func fitLines(head string, lines []string, budget int) string {
	const reserve = len("\n(+99 more)")
	out := head
	for i, l := range lines {
		room := budget
		if i < len(lines)-1 {
			room -= reserve
		}
		if len(out)+1+len(l) > room {
			return out + fmt.Sprintf("\n(+%d more)", len(lines)-i)
		}
		out += "\n" + l
	}
	return out
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
