package usecase

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"legos-factory/backend/internal/model"
)

// Gate wording, thresholds and truth tables for the Jev plan and done gates
// (docs/foreman-flow/SPEC.md sections 4 and 7). Everything here is pure.

const (
	yesAbove = 0.7
	noBelow  = 0.3

	maxRequestTokens  = 64000
	maxQuestionTokens = 32000
	limitMarginPct    = 90    // the token estimate is rough: stay under 90% of each limit
	diffHunkBudget    = 48000 // bytes of hunk text per done-gate request

	maxPlanRevisions = 2
)

const (
	GateEscalate = "escalate"
	GateRevise   = "revise"
	GateApprove  = "approve"
	GateFix      = "fix"
	GateDone     = "done"
	GateLoop     = "loop" // checks failed or blockers open: the existing loops own it
)

// errNoJudge is the judgeErr callers pass when Factory.Judge is nil, so the
// audit row says why the gate escalated.
var errNoJudge = errors.New("jev is not configured")

type verdict int

const (
	vNo verdict = iota
	vUnsettled
	vYes
)

// judge maps a probability to a verdict: exactly 0.7 and exactly 0.3 are unsettled.
func judge(p float64) verdict {
	switch {
	case p > yesAbove:
		return vYes
	case p < noBelow:
		return vNo
	}
	return vUnsettled
}

type GateOutcome struct {
	Outcome  string
	Row      int
	Note     string   // code-built revise/fix/escalate reason
	Issues   []string // the done gate's reasons one by one; Note joins them
	Builder  []string // plan gate: unsettled answers that go to the Builder as notes, not to a revision
	InReport []string // the Issues that restate the Architect's report, which the fix brief carries in full
}

type Criterion struct{ ID, Text string }

type PlanTask struct {
	ID, Title, Objective string
	Scope, Deps          []string
}

type PlanGateInput struct {
	Request       string // request title and objective
	Criteria      []Criterion
	Brief, Spec   string
	Tasks         []PlanTask
	Findings      []reviewFinding // the Foreman's analysis: unresolved criticals plus high and low notes
	Coverage      []reviewCoverage
	PlanRevisions int
}

type FileDiff = model.FileDiff

type GateCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
}

type GateFinding struct {
	Severity string `json:"severity"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
}

type DoneGateInput struct {
	Criteria          []Criterion
	Spec              string
	Diff              []FileDiff
	Checks            []GateCheck
	OpenFindings      []GateFinding
	ArchitectVerdicts []ArchitectVerdict
	Deviations        []string // work the Architect found outside the spec or plan
	ForemanNotes      string
}

// ArchitectVerdict is the Architect's per-criterion result; Status is
// "met", "not_met" or "unclear".
type ArchitectVerdict struct {
	CriterionID string `json:"criterion_id"`
	Status      string `json:"status"`
	Evidence    string `json:"evidence"`
	Gap         string `json:"gap,omitempty"`
	Fix         string `json:"fix,omitempty"`
}

type orderedCriteria []Criterion

func (o orderedCriteria) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, c := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(c.ID)
		v, _ := json.Marshal(c.Text)
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

type orderedTasks []PlanTask

func (o orderedTasks) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, t := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(t.ID)
		v, err := json.Marshal(map[string]any{
			"title": t.Title, "objective": t.Objective,
			"scope": nonNil(t.Scope), "deps": nonNil(t.Deps),
		})
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

var bulletRe = regexp.MustCompile(`^\s*(?:[-*]|\d+[.)])\s+(.*)$`)

// splitCriteria turns the free-text acceptance field into c1..cn. Lines that
// are not bullets continue the previous bullet; text before the first bullet
// is dropped. Without any bullet the whole text is one criterion.
func splitCriteria(acceptance string) []Criterion {
	var items []string
	for _, line := range strings.Split(acceptance, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if m := bulletRe.FindStringSubmatch(line); m != nil {
			items = append(items, strings.TrimSpace(m[1]))
		} else if len(items) > 0 {
			items[len(items)-1] += " " + strings.TrimSpace(line)
		}
	}
	if len(items) == 0 {
		if t := strings.TrimSpace(acceptance); t != "" {
			items = []string{t}
		}
	}
	out := make([]Criterion, len(items))
	for i, t := range items {
		out[i] = Criterion{ID: fmt.Sprintf("c%d", i+1), Text: t}
	}
	return out
}

func yn(t, f string) *NoulCriteria { return &NoulCriteria{True: t, False: f} }

// Plan questions that hold one proposition each. The phrase is the note text
// when the answer is yes; "it is unclear whether <phrase>" when unsettled.
var planPhrases = map[string]string{
	"scope_creep":            "the plan includes work the request does not ask for",
	"open_questions":         "the spec still contains an open question or a guessed behaviour",
	"touches_schema":         "a task changes a database schema or migration",
	"touches_authentication": "a task changes authentication",
	"touches_authorization":  "a task changes authorization or permission checks",
	"touches_payments":       "a task changes payment handling",
	"touches_secrets":        "a task changes how secrets are stored or read",
}

var (
	planFixedIDs = []string{"critical_resolved", "open_questions", "scope_creep"}
	planRiskIDs  = []string{"touches_schema", "touches_authentication", "touches_authorization", "touches_payments", "touches_secrets"}
)

var doneHardPhrases = map[string]string{
	"deviates":         "the diff changes behaviour the spec does not describe",
	"notes_unresolved": "the Foreman notes report a problem the diff does not fix",
}

var doneFixedIDs = []string{"deviates", "notes_unresolved"}

func planGateState(in PlanGateInput) (map[string]any, map[string]Noul) {
	state := map[string]any{
		"request":             in.Request,
		"acceptance_criteria": orderedCriteria(in.Criteria),
		"brief":               in.Brief,
		"spec":                in.Spec,
		"plan":                orderedTasks(in.Tasks),
		"analysis":            map[string]any{"findings": nonNilSlice(in.Findings), "coverage": nonNilSlice(in.Coverage)},
	}
	qs := map[string]Noul{}
	for _, c := range in.Criteria {
		qs["covers_"+c.ID] = Noul{
			Instructions: fmt.Sprintf("Does at least one task in plan implement the behaviour that acceptance_criteria.%s describes?", c.ID),
			Criteria: yn(
				fmt.Sprintf("A task's objective or scope names the behaviour in acceptance_criteria.%s, so building that task would make the criterion true.", c.ID),
				fmt.Sprintf("No task's objective or scope mentions the behaviour in acceptance_criteria.%s, or the criterion is only touched by a task about something else.", c.ID)),
		}
	}
	for _, c := range in.Criteria {
		qs["verified_"+c.ID] = Noul{
			Instructions: fmt.Sprintf("Does the acceptance-criteria verification table in spec give acceptance_criteria.%s a concrete test or check?", c.ID),
			Criteria: yn(
				fmt.Sprintf("The verification table in spec has a row for acceptance_criteria.%s that names a specific automated test, or a specific check a reviewer can run on the diff.", c.ID),
				fmt.Sprintf("The verification table in spec has no row for acceptance_criteria.%s, or its row only says something vague such as \"works correctly\".", c.ID)),
		}
	}
	qs["scope_creep"] = Noul{
		Instructions: "Does plan include a task that implements behaviour which neither request nor acceptance_criteria asks for?",
		Criteria: yn(
			"A task adds a feature, refactor or cleanup that appears nowhere in request or acceptance_criteria, such as a new endpoint nobody asked for.",
			"Every task in plan traces back to something request or acceptance_criteria asks for; supporting work like tests for requested behaviour counts as asked for."),
	}
	qs["touches_schema"] = Noul{
		Instructions: "Does any task in plan create or change a database schema or write a migration?",
		Criteria: yn(
			"A task adds, drops or alters a table, column, index or constraint, or adds a migration file.",
			"No task changes how data is stored; reading or writing existing columns does not count."),
	}
	qs["touches_authentication"] = Noul{
		Instructions: "Does any task in plan change how users or services prove who they are, such as login, sessions or token verification?",
		Criteria: yn(
			"A task adds or changes login, sign-up, session handling, or password or token verification.",
			"No task touches how identity is proven; code that only reads the current user's id does not count."),
	}
	qs["touches_authorization"] = Noul{
		Instructions: "Does any task in plan change who is allowed to do what, such as permission checks, roles or ownership rules?",
		Criteria: yn(
			"A task adds, removes or changes a permission check, role, ownership rule or access policy.",
			"No task changes what any user or service is allowed to do; calling an existing permission check unchanged does not count."),
	}
	qs["touches_payments"] = Noul{
		Instructions: "Does any task in plan change payment, billing or money-movement code?",
		Criteria: yn(
			"A task changes charging, refunds, invoices, prices, billing plans or calls to a payment provider.",
			"No task touches payment or billing code, even if it sits in the same file or package as such code."),
	}
	qs["touches_secrets"] = Noul{
		Instructions: "Does any task in plan change how secrets are stored, read or passed around?",
		Criteria: yn(
			"A task changes how API keys, tokens, passwords or encryption keys are stored, loaded, passed or logged.",
			"No task changes secret handling; using an already configured secret unchanged does not count."),
	}
	qs["critical_resolved"] = Noul{
		Instructions: "Is every critical finding in analysis addressed by spec or by a task in plan?",
		Criteria: yn(
			"analysis has no critical finding, or each critical finding is answered by a rule in spec or by a task in plan.",
			"analysis has a critical finding that neither spec nor any task in plan addresses."),
	}
	qs["open_questions"] = Noul{
		Instructions: "Does spec still contain an open question or a behaviour that the Architect guessed?",
		Criteria: yn(
			"spec lists an unanswered question, says a behaviour is assumed or unknown, or leaves a behaviour undecided that a Builder would have to guess.",
			"spec states every behaviour it relies on, and its open-questions list is empty or absent."),
	}
	return state, qs
}

func doneGateState(in DoneGateInput) (map[string]any, map[string]Noul) {
	diff, trimmed := trimHunks(in.Diff, diffHunkBudget)
	state := map[string]any{
		"acceptance_criteria": orderedCriteria(in.Criteria),
		"spec":                in.Spec,
		"diff":                diff,
		"diff_trimmed":        trimmed,
		"checks":              nonNilSlice(in.Checks),
		"open_findings":       nonNilSlice(in.OpenFindings),
		"architect_verdicts":  nonNilSlice(in.ArchitectVerdicts),
		"deviations":          nonNilSlice(in.Deviations),
		"foreman_notes":       in.ForemanNotes,
	}
	qs := map[string]Noul{}
	for _, c := range in.Criteria {
		qs["met_"+c.ID] = Noul{
			Instructions: fmt.Sprintf("Does diff implement the behaviour that acceptance_criteria.%s describes?", c.ID),
			Criteria: yn(
				fmt.Sprintf("The diff contains code that implements the behaviour acceptance_criteria.%s describes.", c.ID),
				fmt.Sprintf("The diff contains no code for the behaviour acceptance_criteria.%s describes, or only code for something else.", c.ID)),
		}
	}
	qs["deviates"] = Noul{
		Instructions: "Does diff change behaviour that spec does not describe?",
		Criteria: yn(
			"The diff adds or alters user-visible behaviour, an interface or a data format that spec never mentions.",
			"Every behaviour change in the diff is described by spec; formatting, tests and internal renames do not count."),
	}
	qs["notes_unresolved"] = Noul{
		Instructions: "Do foreman_notes report a problem that the diff does not fix?",
		Criteria: yn(
			"foreman_notes name a defect or missing piece and the diff contains no change that resolves it.",
			"foreman_notes report no problem, or the diff contains a change that resolves each problem they report."),
	}
	return state, qs
}

func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

var generatedFiles = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true, "go.sum": true, "Cargo.lock": true,
}

func isGenerated(p string) bool {
	if generatedFiles[path.Base(p)] {
		return true
	}
	for _, seg := range strings.Split(path.Dir(p), "/") {
		if seg == "vendor" || seg == "dist" {
			return true
		}
	}
	return false
}

// trimHunks keeps hunk text up to budget bytes across all files, in order.
// Lock and generated files lose their hunks entirely. A first pass lets one
// file keep at most budget/4 so it cannot starve the rest; a second pass hands
// whatever is left to the files that were cut, in order. Cuts land on a line
// boundary; paths whose hunks were cut are returned.
func trimHunks(files []FileDiff, budget int) ([]FileDiff, []string) {
	out := make([]FileDiff, len(files))
	left := budget
	perFile := budget / 4
	for i, f := range files {
		out[i] = f
		if isGenerated(f.Path) {
			out[i].Hunks = ""
			continue
		}
		out[i].Hunks = cutAtLine(f.Hunks, min(left, perFile))
		left -= len(out[i].Hunks)
	}
	for i, f := range files {
		if left <= 0 {
			break
		}
		if isGenerated(f.Path) || len(out[i].Hunks) == len(f.Hunks) {
			continue
		}
		out[i].Hunks = cutAtLine(f.Hunks, len(out[i].Hunks)+left)
		left = budget - usedHunks(out)
	}
	trimmed := []string{}
	for i, f := range files {
		if len(out[i].Hunks) < len(f.Hunks) {
			trimmed = append(trimmed, f.Path)
		}
	}
	return out, trimmed
}

func usedHunks(files []FileDiff) int {
	n := 0
	for _, f := range files {
		n += len(f.Hunks)
	}
	return n
}

// cutAtLine returns s when it fits in n bytes, else the longest prefix of
// whole lines (valid UTF-8) within n.
func cutAtLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:max(n, 0)]
	j := strings.LastIndexByte(cut, '\n')
	if j < 0 {
		return ""
	}
	return cut[:j+1]
}

// estimateTokens is len/4 of the unescaped JSON: HTML escaping would inflate
// code-heavy diffs well past what the model actually counts.
func estimateTokens(v any) int {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return (b.Len() - 1) / 4
}

// overLimit reports whether a request would exceed 90% of 64k tokens, or any
// single question plus the state 90% of 32k.
func overLimit(state any, questions map[string]Noul) bool {
	s := estimateTokens(state)
	if s+estimateTokens(questions) > maxRequestTokens*limitMarginPct/100 {
		return true
	}
	for _, q := range questions {
		if s+estimateTokens(q) > maxQuestionTokens*limitMarginPct/100 {
			return true
		}
	}
	return false
}

// missingAnswers lists the required or asked question ids that have no answer.
// Answers to questions that were not asked are ignored.
func missingAnswers(required []string, asked map[string]Noul, answers map[string]float64) []string {
	ids := map[string]bool{}
	for _, id := range required {
		if _, ok := asked[id]; !ok {
			ids[id] = true
		}
	}
	for id := range asked {
		if _, ok := answers[id]; !ok {
			ids[id] = true
		}
	}
	var missing []string
	for id := range ids {
		missing = append(missing, id)
	}
	sort.Strings(missing)
	return missing
}

func joinNotes(notes []string) string { return strings.Join(notes, "; ") }

func truncateText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func criterionLabels(cs []Criterion) map[string]string {
	m := make(map[string]string, len(cs))
	for _, c := range cs {
		m[c.ID] = fmt.Sprintf("%s (%s)", c.ID, truncateText(c.Text, 80))
	}
	return m
}

func numberNotes(notes []string) string {
	lines := make([]string, len(notes))
	for i, n := range notes {
		lines[i] = fmt.Sprintf("%d. %s", i+1, n)
	}
	return strings.Join(lines, "\n")
}

func gateFailure(noCriteria bool, judgeErr error, overLimit bool, missing []string) (string, bool) {
	switch {
	case noCriteria:
		return "no acceptance criteria to judge against", true
	case judgeErr != nil:
		return "judgment failed: " + judgeErr.Error(), true
	case overLimit:
		return "state is over the Jev context limit", true
	case len(missing) > 0:
		return "judgment is missing answers for " + strings.Join(missing, ", "), true
	}
	return "", false
}

func planTaskTitles(tasks []PlanTask) string {
	parts := make([]string, len(tasks))
	for i, t := range tasks {
		parts[i] = fmt.Sprintf("%q (%s)", t.Title, strings.Join(t.Scope, ", "))
	}
	return strings.Join(parts, ", ")
}

// decidePlanGate applies the contract gate truth table (contract-flow SPEC
// section 3), first match wins:
//
//	1 escalate: no acceptance criteria, Jev failed or not configured, state over
//	  the limit, or any asked question has no answer
//	2 escalate: touches_schema/authentication/authorization/payments/secrets is yes
//	3 revise:   a covers_* or verified_* is no, critical_resolved is no,
//	  open_questions is yes, or scope_creep is yes
//	4 revise:   covers_*, verified_* or critical_resolved is unsettled
//	5 approve
//
// Rows 3 and 4 become escalate once in.PlanRevisions reaches maxPlanRevisions.
// Unsettled scope_creep, open_questions and touches_* never revise: they come
// go to the Builder as notes. The revise note holds only the
// triggering answers and the critical findings in in.Findings. Only the
// questions in asked are read; answers to other ids are ignored.
func decidePlanGate(in PlanGateInput, asked map[string]Noul, answers map[string]float64, judgeErr error, overLimit bool) GateOutcome {
	required := append(append([]string{}, planFixedIDs...), planRiskIDs...)
	for _, c := range in.Criteria {
		required = append(required, "covers_"+c.ID, "verified_"+c.ID)
	}
	if note, bad := gateFailure(len(in.Criteria) == 0, judgeErr, overLimit, missingAnswers(required, asked, answers)); bad {
		return GateOutcome{Outcome: GateEscalate, Row: 1, Note: note}
	}
	var hard, soft, risky, builder []string
	labels := make(map[string]string, len(in.Criteria))
	for _, c := range in.Criteria {
		labels[c.ID] = fmt.Sprintf("%s (%s)", c.ID, c.Text)
	}
	for _, c := range in.Criteria {
		switch judge(answers["covers_"+c.ID]) {
		case vNo:
			hard = append(hard, fmt.Sprintf("acceptance criterion %s is not covered by any task", labels[c.ID]))
		case vUnsettled:
			soft = append(soft, fmt.Sprintf("it is unclear whether acceptance criterion %s is covered by a task", labels[c.ID]))
		}
		switch judge(answers["verified_"+c.ID]) {
		case vNo:
			hard = append(hard, fmt.Sprintf("acceptance criterion %s has no concrete test or check in the spec's verification table", labels[c.ID]))
		case vUnsettled:
			soft = append(soft, fmt.Sprintf("it is unclear whether acceptance criterion %s has a concrete test or check in the spec's verification table", labels[c.ID]))
		}
	}
	var criticals []string
	for _, f := range in.Findings {
		if f.Severity == "critical" {
			criticals = append(criticals, findingLine(f))
		}
	}
	criticalNote := "critical findings the spec or a task must address: " + strings.Join(criticals, " | ")
	switch judge(answers["critical_resolved"]) {
	case vNo:
		hard = append(hard, "a critical finding is not addressed by the spec or any task; "+criticalNote)
	case vUnsettled:
		soft = append(soft, "it is unclear whether every critical finding is addressed; "+criticalNote)
	}
	if judge(answers["open_questions"]) == vYes {
		hard = append(hard, planPhrases["open_questions"])
	} else if judge(answers["open_questions"]) == vUnsettled {
		builder = append(builder, "it is unclear whether "+planPhrases["open_questions"])
	}
	switch judge(answers["scope_creep"]) {
	case vYes:
		hard = append(hard, planPhrases["scope_creep"]+"; the plan's tasks are "+planTaskTitles(in.Tasks))
	case vUnsettled:
		builder = append(builder, "it is unclear whether "+planPhrases["scope_creep"]+"; stay within the request and acceptance criteria")
	}
	for _, id := range planRiskIDs {
		switch judge(answers[id]) {
		case vYes:
			risky = append(risky, planPhrases[id])
		case vUnsettled:
			builder = append(builder, "it is unclear whether "+planPhrases[id])
		}
	}
	if len(risky) > 0 {
		return GateOutcome{Outcome: GateEscalate, Row: 2, Note: joinNotes(risky), Builder: builder}
	}
	row, note := 0, ""
	switch {
	case len(hard) > 0:
		row, note = 3, numberNotes(append(hard, soft...))
	case len(soft) > 0:
		row, note = 4, numberNotes(soft)
	default:
		return GateOutcome{Outcome: GateApprove, Row: 5, Builder: builder}
	}
	if in.PlanRevisions >= maxPlanRevisions {
		return GateOutcome{Outcome: GateEscalate, Row: row, Note: note, Builder: builder}
	}
	return GateOutcome{Outcome: GateRevise, Row: row, Note: note, Builder: builder}
}

// architectStatus reduces the Architect's verdicts to one status per
// criterion: not_met beats unclear beats met; an unknown status or a missing
// verdict is unclear.
func architectStatus(verdicts []ArchitectVerdict, id string) (status string, given bool) {
	rank := map[string]int{"met": 0, "unclear": 1, "not_met": 2}
	status = "met"
	for _, v := range verdicts {
		if v.CriterionID != id {
			continue
		}
		given = true
		s := strings.ToLower(strings.TrimSpace(v.Status))
		if _, ok := rank[s]; !ok {
			s = "unclear"
		}
		if rank[s] > rank[status] {
			status = s
		}
	}
	if !given {
		return "unclear", false
	}
	return status, true
}

// decideDoneGate applies the done truth table (SPEC section 7), first match wins:
//
//	1 loop:     a check failed or a blocker finding is open (existing loops own it)
//	2 escalate: no acceptance criteria, Jev failed or not configured, state over
//	  the limit, or any asked question has no answer
//	3 fix:      a met_* is no, deviates/notes_unresolved is yes, the Architect
//	  marks a criterion not_met, or the Architect reports deviations
//	4 fix:      any answer is unsettled, or the Architect is unclear about a
//	  criterion or gave no verdict for it
//	5 done
//
// Rows 3 and 4 become escalate when no fix budget is left. Only the
// questions in asked are read; answers to other ids are ignored.
func decideDoneGate(in DoneGateInput, asked map[string]Noul, answers map[string]float64, judgeErr error, overLimit, checksFailed bool, openBlockers int, fixBudgetLeft bool) GateOutcome {
	if checksFailed || openBlockers > 0 {
		return GateOutcome{Outcome: GateLoop, Row: 1, Note: "a check failed or a blocker finding is open"}
	}
	required := append([]string{}, doneFixedIDs...)
	for _, c := range in.Criteria {
		required = append(required, "met_"+c.ID)
	}
	if note, bad := gateFailure(len(in.Criteria) == 0, judgeErr, overLimit, missingAnswers(required, asked, answers)); bad {
		return GateOutcome{Outcome: GateEscalate, Row: 2, Note: note}
	}
	var hard, soft, inReport []string
	labels := criterionLabels(in.Criteria)
	for _, c := range in.Criteria {
		switch judge(answers["met_"+c.ID]) {
		case vNo:
			hard = append(hard, fmt.Sprintf("the diff does not implement acceptance criterion %s", labels[c.ID]))
		case vUnsettled:
			soft = append(soft, fmt.Sprintf("it is unclear whether the diff implements acceptance criterion %s", labels[c.ID]))
		}
		switch status, given := architectStatus(in.ArchitectVerdicts, c.ID); {
		case status == "not_met":
			hard = append(hard, fmt.Sprintf("the Architect reports acceptance criterion %s as not met", labels[c.ID]))
			inReport = append(inReport, hard[len(hard)-1])
		case status == "unclear" && given:
			soft = append(soft, fmt.Sprintf("the Architect is unsure whether acceptance criterion %s is met", labels[c.ID]))
			inReport = append(inReport, soft[len(soft)-1])
		case !given:
			soft = append(soft, fmt.Sprintf("the Architect gave no verdict for acceptance criterion %s", labels[c.ID]))
		}
	}
	for _, id := range doneFixedIDs {
		switch judge(answers[id]) {
		case vYes:
			hard = append(hard, doneHardPhrases[id])
		case vUnsettled:
			soft = append(soft, "it is unclear whether "+doneHardPhrases[id])
		}
	}
	for _, d := range in.Deviations {
		if strings.TrimSpace(d) != "" {
			hard = append(hard, fmt.Sprintf("the Architect reports work outside the spec or plan: %s", truncateText(d, 80)))
			inReport = append(inReport, hard[len(hard)-1])
		}
	}
	row, issues := 0, []string(nil)
	switch {
	case len(hard) > 0:
		row, issues = 3, append(hard, soft...)
	case len(soft) > 0:
		row, issues = 4, soft
	default:
		return GateOutcome{Outcome: GateDone, Row: 5, Note: ""}
	}
	outcome := GateFix
	if !fixBudgetLeft {
		outcome = GateEscalate
	}
	return GateOutcome{Outcome: outcome, Row: row, Note: joinNotes(issues), Issues: issues, InReport: inReport}
}

// gateEvent builds the audit payload for an actor "jev" event: the exact
// request body that went on the wire, the answers and the table row, enough
// to replay the decision. When nothing was sent (over the limit, no key) the
// would-be state and questions are recorded as unsent_request.
func gateEvent(gate string, state any, questions map[string]Noul, res JudgeResult, out GateOutcome, judgeErr error) map[string]any {
	p := map[string]any{
		"gate":            gate,
		"answers":         res.Answers,
		"requested_model": res.RequestedModel,
		"model":           res.Model,
		"usage":           res.Usage,
		"row":             out.Row,
		"outcome":         out.Outcome,
		"note":            out.Note,
		"issues":          out.Issues,
		"yes_over":        yesAbove,
		"no_under":        noBelow,
	}
	if len(res.Request) > 0 {
		p["request"] = res.Request
	} else {
		p["unsent_request"] = map[string]any{"state": state, "questions": questions}
	}
	if judgeErr != nil {
		p["error"] = judgeErr.Error()
	}
	return p
}
