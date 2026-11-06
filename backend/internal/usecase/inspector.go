package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"legos-factory/backend/internal/model"
)

// Inspector: deterministic verification and specialist review findings.

func (f *Factory) handleVerify(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	profile, err := f.profile(ctx, wo)
	if err != nil {
		return err
	}
	f.event(wo.ID, "inspector", "verification_started",
		fmt.Sprintf("Running %d deterministic checks (revision %d)", len(profile.Checks), wo.Revision), nil)
	mu := f.wsLock(wo.ID)
	mu.Lock()
	f.runSetup(ctx, wo, profile, f.workspaceDir(wo.ID), "inspector")
	mu.Unlock()
	failed := ""
	var failedResults []model.CheckResult
	for _, c := range profile.Checks {
		r := f.Sandbox.RunCheck(ctx, f.workspaceDir(wo.ID), c, f.QualTimeout)
		if err := f.saveCheck(ctx, wo, c, r); err != nil {
			return err
		}
		f.event(wo.ID, "inspector", "check_"+strings.ToLower(string(r.Outcome)),
			fmt.Sprintf("%s → %s", c.Name, r.Outcome), map[string]any{"command": c.Command})
		if r.Outcome != model.Pass && failed == "" {
			failed = c.Name
		}
		if r.Outcome != model.Pass {
			failedResults = append(failedResults, r)
		}
	}
	if failed != "" {
		if wo.VerifyFailures >= MaxRevisionCycles {
			f.event(wo.ID, "factory", "escalated", "Verification failed after max revision cycles; blocking", nil)
			if err := f.step(ctx, wo, model.StateVerifying, model.StateSpecialistReview, "factory", "Final evaluation after exhausted retries"); err != nil {
				return err
			}
			if err := f.step(ctx, wo, model.StateSpecialistReview, model.StatePRPreparation, "factory", "Preparing change summary"); err != nil {
				return err
			}
			if err := f.step(ctx, wo, model.StatePRPreparation, model.StateMergeReadiness, "factory", "Evaluating merge readiness"); err != nil {
				return err
			}
			return f.evaluateReadiness(ctx, wo, true, nil)
		}
		newRev, failures := wo.Revision+1, wo.VerifyFailures+1
		if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"revision": newRev, "commit_sha": "", "verify_failures": failures}); err != nil {
			return err
		}
		wo.Revision, wo.CommitSHA, wo.VerifyFailures = newRev, "", failures
		if err := f.step(ctx, wo, model.StateVerifying, model.StateImplementing, "factory",
			fmt.Sprintf("Check %q failed; returning to builder (attempt %d of %d)", failed, failures, MaxRevisionCycles)); err != nil {
			return err
		}
		_, err := f.Sched.Enqueue(ctx, JobImplement, wo.ID, "", map[string]any{
			"reason": "fix_failed_verification", "failed_checks": failedChecksSection(failedResults),
		})
		return err
	}
	if len(profile.Checks) == 0 {
		f.event(wo.ID, "factory", "verification_skipped", "No checks detected in the repository; add .legos/checks.json to define them", nil)
	} else {
		f.event(wo.ID, "inspector", "verification_passed", "All deterministic checks passed", nil)
	}
	if err := f.commitVerified(ctx, wo); err != nil {
		return err
	}
	if err := f.step(ctx, wo, model.StateVerifying, model.StateSpecialistReview, "factory", "Verification passed; specialist review"); err != nil {
		if wo.State != model.StateSpecialistReview {
			return err
		}
	}
	return f.enqueue(ctx, JobInspect, wo.ID, "")
}

const (
	maxFailedChecks    = 3
	maxFailedOutputLen = 3000
)

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// failedChecksSection pre-renders to a string because job payloads round-trip through JSON.
func failedChecksSection(failures []model.CheckResult) string {
	var b strings.Builder
	for i, r := range failures {
		if i == maxFailedChecks {
			fmt.Fprintf(&b, "(%d more failing checks omitted)\n", len(failures)-i)
			break
		}
		exit := "n/a"
		if r.ExitCode != nil {
			exit = fmt.Sprint(*r.ExitCode)
		}
		note := ""
		if r.Outcome != model.Fail {
			// timeouts and exec errors are reported on the first output line, which the tail cut can drop
			note = " - " + strings.TrimSpace(strings.SplitN(strings.TrimSpace(ansiEscape.ReplaceAllString(r.Output, "")), "\n", 2)[0])
		}
		fmt.Fprintf(&b, "Check %q: %s%s (exit code %s)\nCommand: %s\nOutput (tail):\n%s\n\n",
			r.Name, r.Outcome, note, exit, r.Command, outputTail(r.Output))
	}
	return strings.TrimSpace(b.String())
}

// retryPayload keeps the failing checks in front of the Builder when a human retries after a failed verification.
func (f *Factory) retryPayload(ctx context.Context, wo *model.WorkOrder) map[string]any {
	brief := f.storedBrief(ctx, wo)
	payload := map[string]any{"reason": "human_retry"}
	if brief != "" {
		payload["brief"] = brief
	} else {
		payload = f.withBrief(ctx, wo, wo.Revision, payload, "")
	}
	if wo.VerifyFailures == 0 || wo.Revision < 1 {
		return payload
	}
	stored, err := f.Quality.ForRevision(ctx, wo.ID, wo.Revision-1)
	if err != nil {
		return payload
	}
	// the revision also holds the pre-change baseline and re-run duplicates; only each check's latest run counts
	latest := map[string]int{}
	for i, c := range stored {
		latest[c.Name] = i
	}
	var failed []model.CheckResult
	for i, c := range stored {
		if latest[c.Name] != i || c.Outcome == string(model.Pass) {
			continue
		}
		r := model.CheckResult{Name: c.Name, Command: c.Command, Outcome: model.Outcome(c.Outcome), Output: c.Output}
		if c.ExitCode >= 0 {
			r.ExitCode = &c.ExitCode
		}
		failed = append(failed, r)
	}
	if len(failed) == 0 {
		return payload
	}
	payload["reason"], payload["failed_checks"] = "fix_failed_verification", failedChecksSection(failed)
	return payload
}

// storedBrief is the brief of the remediation that opened the order's current revision, for a retry after that remediation failed.
func (f *Factory) storedBrief(ctx context.Context, wo *model.WorkOrder) string {
	var best *model.Event
	for _, kind := range []string{"fix_requested", "revision_requested"} {
		e, err := f.WO.LatestEvent(ctx, wo.ID, kind)
		if err != nil || e == nil {
			continue
		}
		if rev, _ := e.Payload["revision"].(float64); int(rev) == wo.Revision && (best == nil || e.ID > best.ID) {
			best = e
		}
	}
	if best == nil {
		return ""
	}
	b, _ := best.Payload["brief"].(string)
	return b
}

func outputTail(out string) string {
	out = strings.TrimSpace(ansiEscape.ReplaceAllString(out, ""))
	if len(out) <= maxFailedOutputLen {
		return out
	}
	out = out[len(out)-maxFailedOutputLen:]
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[i+1:]
	}
	return "…\n" + out
}

func (f *Factory) handleInspect(ctx context.Context, job model.Job) error {
	wo, err := f.WO.Get(ctx, job.WOID)
	if err != nil {
		return err
	}
	files, err := f.Sandbox.ChangedFiles(ctx, f.workspaceDir(wo.ID))
	if err != nil {
		f.event(wo.ID, "inspector", "changed_files_unavailable", err.Error(), nil)
	}
	picks, routing := f.reviewers(ctx, wo, files)
	names := make([]string, 0, len(picks))
	for _, p := range picks {
		names = append(names, p.Specialty)
	}
	f.event(wo.ID, "factory", "inspection_specialists",
		fmt.Sprintf("Specialist review by %s (%d changed files)", strings.Join(names, ", "), len(files)),
		map[string]any{"specialists": picks, "routing": routing})

	// With Jev on, the Architect compares the result with the contract while the
	// specialists review the diff. Without Jev nothing reads the gaps.
	var wg sync.WaitGroup
	var archErr error
	if f.Judge != nil {
		actx, cancel := context.WithCancel(ctx)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					f.event(wo.ID, "architect", "converge_failed", fmt.Sprintf("converge panicked: %v", r), nil)
				}
			}()
			archErr = f.converge(actx, *wo, files)
		}()
		// an early return must neither leave the verification running nor wait for it to finish
		defer func() {
			cancel()
			wg.Wait()
		}()
	}

	var runIDs, notes []string
	blockers, total := 0, 0
	criteria := splitCriteria(wo.Acceptance)
	for _, p := range picks {
		var out findingsReply
		kbText, audit := f.KB.Context(ctx, wo.ProjectID, string(model.AgentInspector), p.Specialty,
			woQuery(wo)+" "+strings.Join(files, " ")+" "+p.Specialty, f.workspaceDir(wo.ID), wo.KBBaseSHA)
		_, runID, err := f.runStructured(ctx, wo, model.AgentInspector, inspectPrompt(wo, p, files)+kbText,
			map[string]string{"specialty": p.Specialty}, "", &out, runOpts{Audit: audit})
		if err != nil {
			return err
		}
		runIDs = append(runIDs, runID)
		var summary strings.Builder
		specBlockers := 0
		for _, fd := range out.Findings {
			if fd.Severity != model.SeverityBlocker && citesCriterion(fd.Cites, criteria) {
				fd.Severity, fd.Detail = model.SeverityBlocker, fd.Detail+" (cites "+fd.Cites+")"
			}
			id := fmt.Sprintf("fnd_%d", time.Now().UnixNano())
			if err := f.Findings.Create(ctx, model.Finding{
				ID: id, RunID: runID, WOID: wo.ID, Severity: fd.Severity, Title: fd.Title,
				Detail: fd.Detail, Location: fd.Location, Status: model.FindingOpen, Specialty: p.Specialty,
			}); err != nil {
				return err
			}
			fmt.Fprintf(&summary, "[%s] %s — %s (%s)\n", fd.Severity, fd.Title, fd.Detail, fd.Location)
			f.event(wo.ID, "inspector", "finding", fmt.Sprintf("[%s/%s] %s", p.Specialty, fd.Severity, fd.Title), map[string]any{
				"finding_id": id, "severity": fd.Severity, "location": fd.Location, "specialty": p.Specialty,
			})
			if fd.Severity == model.SeverityBlocker {
				specBlockers++
			} else {
				notes = append(notes, fmt.Sprintf("[%s] %s (%s): %s", fd.Severity, fd.Title, fd.Location, fd.Detail))
			}
		}
		blockers += specBlockers
		total += len(out.Findings)
		title := fmt.Sprintf("%s inspection of revision %d: %d finding(s), %d blocker(s)", p.Specialty, wo.Revision, len(out.Findings), specBlockers)
		f.Catalog.Record(ctx, wo, model.KnowledgeFindings, model.TrustAIInterpretation, "inspector", title, summary.String(), "run:"+runID)
	}
	wg.Wait()
	if archErr != nil && blockers == 0 {
		// retry only converge: requeueing the job would re-run every specialist
		archErr = f.retryConverge(ctx, wo, files, archErr)
	}
	if archErr != nil {
		f.event(wo.ID, "architect", "converge_failed", archErr.Error(), nil)
	}
	f.event(wo.ID, "inspector", "inspection_finished", fmt.Sprintf("%d finding(s), %d blocker(s)", total, blockers), nil)
	if err := f.setBuilderNotes(ctx, wo.ID, "inspector", notes); err != nil {
		return err
	}

	if blockers == 0 {
		// a later clean inspection supersedes earlier findings; this inspection's own
		// stay open for the converge gate, which resolves them once it has decided
		if err := f.Findings.ResolveOpenExcept(ctx, wo.ID, runIDs); err != nil {
			return err
		}
		return f.enqueue(ctx, JobEvaluate, wo.ID, "")
	}
	if wo.ReviewFixes >= MaxRevisionCycles {
		f.event(wo.ID, "factory", "escalated", "Blocker findings remain after max revision cycles; escalating to human", nil)
		if err := f.step(ctx, wo, model.StateSpecialistReview, model.StateWaitingForHuman, "factory", "Escalated: blocker findings unresolved"); err != nil {
			return err
		}
		return nil
	}
	var all []model.Finding
	if f.Findings != nil {
		all, _ = f.Findings.List(ctx, wo.ID)
	}
	payload := map[string]any{"reason": "fix_findings"}
	evPayload := map[string]any{}
	newRev, fixes := wo.Revision+1, wo.ReviewFixes+1
	if items := fixItems(fromRuns(openFindings(all), runIDs), ""); len(items) > 0 {
		b := f.withEarlierFixes(ctx, wo.ID, newRev, items)
		payload["brief"] = b
		evPayload["brief"], evPayload["items"] = b, items
	}
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"revision": newRev, "commit_sha": "", "review_fixes": fixes}); err != nil {
		return err
	}
	wo.Revision, wo.CommitSHA, wo.ReviewFixes = newRev, "", fixes
	evPayload["revision"], evPayload["reason"] = newRev, "fix_findings"
	f.event(wo.ID, "factory", "fix_requested", fmt.Sprintf("Revision %d: requesting bounded remediation", newRev), evPayload)
	if err := f.step(ctx, wo, model.StateSpecialistReview, model.StateImplementing, "factory", "Fix required; returning to builder"); err != nil {
		return err
	}
	// the fix runs as a builder job; its completion drives → VERIFYING again
	return f.enqueuePayload(ctx, JobImplement, wo.ID, payload)
}

// commitVerified records the verified workspace as a local commit (never
// pushed) and stamps the work order and this revision's checks with its SHA.
func (f *Factory) commitVerified(ctx context.Context, wo *model.WorkOrder) error {
	sha, err := f.Sandbox.Commit(ctx, f.workspaceDir(wo.ID), fmt.Sprintf("legos-factory: %s revision %d (%s)", wo.ID, wo.Revision, wo.Title))
	if err != nil {
		return err
	}
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"commit_sha": sha}); err != nil {
		return err
	}
	if err := f.Quality.AttachCommit(ctx, wo.ID, wo.Revision, sha); err != nil {
		return err
	}
	wo.CommitSHA = sha
	f.event(wo.ID, "factory", "commit_recorded", fmt.Sprintf("Verified revision %d committed locally as %s (not pushed)", wo.Revision, orUnversioned(sha)),
		map[string]any{"commit_sha": sha, "revision": wo.Revision})
	return nil
}

const convergeAttempts = 3

var architectRetryDelay = 5 * time.Second

func (f *Factory) retryConverge(ctx context.Context, wo *model.WorkOrder, files []string, err error) error {
	for i := 1; i < convergeAttempts && err != nil; i++ {
		select {
		case <-ctx.Done():
			return err
		case <-time.After(architectRetryDelay):
		}
		err = f.converge(ctx, *wo, files)
	}
	return err
}

// architectReport is the Architect's converge report for one revision; the
// converge gate only trusts it for the revision it was made on.
type architectReport struct {
	Revision  int           `json:"revision"`
	Round     int           `json:"round"`
	Gaps      []convergeGap `json:"gaps"`
	Converged bool          `json:"converged"`
}

// convergeAppends is how many append rounds converge has used on the order.
func (f *Factory) convergeAppends(ctx context.Context, woID string) (int, error) {
	evs, err := f.WO.EventsByKind(ctx, woID, []string{"converge_append"})
	return len(evs), err
}

// converge has the Architect compare the built result with the contract and
// stores the gaps for the converge gate. A failure is reported, not fatal: the
// gate then has no report for this revision.
func (f *Factory) converge(ctx context.Context, wo model.WorkOrder, files []string) error {
	dir := f.workspaceDir(wo.ID)
	diff, err := f.Sandbox.Diff(ctx, dir)
	if err != nil {
		f.event(wo.ID, "architect", "diff_unavailable", err.Error(), nil)
	}
	tasks, err := f.WO.Plan(ctx, wo.ID)
	if err != nil {
		return err
	}
	appends, err := f.convergeAppends(ctx, wo.ID)
	if err != nil {
		return err
	}
	checks, _ := f.checksText(ctx, &wo)
	kbText, audit := f.KB.Context(ctx, wo.ProjectID, string(model.AgentArchitect), "",
		woQuery(&wo)+" "+strings.Join(files, " "), dir, wo.KBBaseSHA)
	var r convergeReply
	_, runID, err := f.runStructured(ctx, &wo, model.AgentArchitect, convergePrompt(&wo, tasks, diff, checks)+kbText,
		map[string]string{"stage": "converge"}, "", &r, runOpts{Audit: audit})
	if err != nil {
		if errors.Is(err, model.ErrPermanent) || ctx.Err() != nil {
			f.event(wo.ID, "architect", "converge_failed", err.Error(), nil)
			return nil
		}
		return err
	}
	round := appends + 1
	for i := range r.Gaps {
		r.Gaps[i].ID = fmt.Sprintf("C%d-G%d", round, i+1)
	}
	report := architectReport{Revision: wo.Revision, Round: round, Gaps: r.Gaps, Converged: r.Converged}
	raw, _ := json.Marshal(report)
	if err := f.WO.UpdateFields(ctx, wo.ID, map[string]any{"architect_report": string(raw)}); err != nil {
		return err
	}
	f.event(wo.ID, "architect", "converge_report", fmt.Sprintf("Converge round %d: %d gap(s)", round, len(report.Gaps)),
		map[string]any{"revision": wo.Revision, "round": round, "gaps": report.Gaps, "converged": report.Converged, "run_id": runID})
	f.Catalog.Record(ctx, &wo, model.KnowledgeArchitectVerified, model.TrustAIInterpretation, "architect",
		fmt.Sprintf("Architect converge of revision %d: %d gap(s)", wo.Revision, len(report.Gaps)), reportText(report), "run:"+runID)
	return nil
}

func reportText(r architectReport) string {
	var b strings.Builder
	for _, g := range r.Gaps {
		fmt.Fprintf(&b, "%s\n", gapLine(g))
	}
	if b.Len() == 0 {
		b.WriteString("No gaps.\n")
	}
	return b.String()
}

// fixBrief renders every open finding and the human reason as one numbered
// checklist; it is a string because job payloads round-trip through JSON.
func fixBrief(findings []model.Finding, humanReason string) string {
	return numberedBrief(fixItems(findings, humanReason))
}

func fixItems(findings []model.Finding, humanReason string) []string {
	var items []string
	if strings.TrimSpace(humanReason) != "" {
		items = append(items, "The reviewer asked for changes: "+humanReason)
	}
	for _, fd := range findings {
		items = append(items, fmt.Sprintf("[%s] %s (%s): %s", fd.Severity, fd.Title, fd.Location, fd.Detail))
	}
	return items
}

func numberedBrief(items []string) string {
	var b strings.Builder
	for i, it := range items {
		fmt.Fprintf(&b, "%d. %s\n", i+1, it)
	}
	return strings.TrimSpace(b.String())
}

// briefItems are the fix items of the order's open findings plus the human reason.
func (f *Factory) briefItems(ctx context.Context, wo *model.WorkOrder, humanReason string) []string {
	var all []model.Finding
	if f.Findings != nil {
		all, _ = f.Findings.List(ctx, wo.ID)
	}
	return fixItems(openFindings(all), humanReason)
}

func openFindings(all []model.Finding) []model.Finding {
	var open []model.Finding
	for _, fd := range all {
		if fd.Status == model.FindingOpen || fd.Status == model.FindingConfirmed || fd.Status == model.FindingEscalated {
			open = append(open, fd)
		}
	}
	return open
}

// fromRuns keeps the findings raised by the given inspection runs.
func fromRuns(fs []model.Finding, runIDs []string) []model.Finding {
	var out []model.Finding
	for _, fd := range fs {
		if slices.Contains(runIDs, fd.RunID) {
			out = append(out, fd)
		}
	}
	return out
}

// withBrief adds the fix brief for revision rev to a Builder payload when there is one.
func (f *Factory) withBrief(ctx context.Context, wo *model.WorkOrder, rev int, payload map[string]any, humanReason string) map[string]any {
	if items := f.briefItems(ctx, wo, humanReason); len(items) > 0 {
		payload["brief"], payload["items"] = f.withEarlierFixes(ctx, wo.ID, rev, items), items
	}
	return payload
}

const earlierFixesHeading = "Already addressed in earlier revisions — keep these fixes intact, do not undo or regress them:"

// earlierFixRevisions caps the carried-forward section so a long fix loop cannot grow the brief without limit.
const earlierFixRevisions = 3

var briefItemStart = regexp.MustCompile(`(?m)^\d+\. `)

const foremanNotesPrefix = "The Foreman's validation notes: "

// storedItems are the fix items a fix_requested / revision_requested event asked for; events from before items were stored fall back to splitting the brief.
func storedItems(p map[string]any) []string {
	if raw, ok := p["items"].([]any); ok {
		var out []string
		for _, v := range raw {
			if s, _ := v.(string); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	b, _ := p["brief"].(string)
	b, _, _ = strings.Cut(b, "\n\n"+earlierFixesHeading)
	return briefItemStart.Split(b, -1)
}

// withEarlierFixes numbers the current items and appends the items earlier revisions asked for, newest revisions capped, to the brief of revision rev.
func (f *Factory) withEarlierFixes(ctx context.Context, woID string, rev int, items []string) string {
	brief := numberedBrief(items)
	byRev := map[int][]string{}
	for after := int64(0); ; {
		evs, err := f.WO.Events(ctx, woID, after, 500)
		if err != nil {
			f.Log.Error("earlier fixes: reading events failed, section may be partial", "wo", woID, "err", err)
			break
		}
		if len(evs) == 0 {
			break
		}
		for _, e := range evs {
			after = e.ID
			if e.Kind != "fix_requested" && e.Kind != "revision_requested" {
				continue
			}
			if r, _ := e.Payload["revision"].(float64); int(r) < rev {
				byRev[int(r)] = append(byRev[int(r)], storedItems(e.Payload)...)
			}
		}
	}
	current := map[string]bool{}
	for _, it := range items {
		current[strings.TrimSpace(it)] = true
	}
	revs := slices.Sorted(maps.Keys(byRev))
	revs = revs[max(0, len(revs)-earlierFixRevisions):]
	var section strings.Builder
	seen := map[string]bool{}
	for _, r := range revs {
		for _, it := range byRev[r] {
			if it = strings.TrimSpace(it); it != "" && !seen[it] && !current[it] && !strings.HasPrefix(it, foremanNotesPrefix) {
				seen[it] = true
				section.WriteString("\n- " + it)
			}
		}
	}
	if section.Len() == 0 {
		return brief
	}
	return brief + "\n\n" + earlierFixesHeading + section.String()
}

// latestChecks is each check's most recent result for the current revision; the
// revision also holds the pre-change baseline and re-run duplicates.
func (f *Factory) latestChecks(ctx context.Context, wo *model.WorkOrder) ([]model.QualityCheck, error) {
	stored, err := f.Quality.ForRevision(ctx, wo.ID, wo.Revision)
	if err != nil {
		return nil, err
	}
	return latestPerName(stored), nil
}

// latestPerName keeps each check name's last entry, in stored order.
func latestPerName(stored []model.QualityCheck) []model.QualityCheck {
	latest := map[string]int{}
	for i, c := range stored {
		latest[c.Name] = i
	}
	var out []model.QualityCheck
	for i, c := range stored {
		if latest[c.Name] == i {
			out = append(out, c)
		}
	}
	return out
}

// checksText renders the current check results for a prompt and reports whether any failed.
func (f *Factory) checksText(ctx context.Context, wo *model.WorkOrder) (string, bool) {
	checks, err := f.latestChecks(ctx, wo)
	if err != nil || len(checks) == 0 {
		return "(no checks recorded)", false
	}
	var b strings.Builder
	failed := false
	for _, c := range checks {
		fmt.Fprintf(&b, "%s (%s) → %s\n", c.Name, c.Command, c.Outcome)
		failed = failed || c.Outcome != string(model.Pass)
	}
	return b.String(), failed
}
