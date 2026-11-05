package usecase

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Gate wording, thresholds and truth tables for the Jev plan and done gates
// (docs/foreman-flow/SPEC.md sections 4 and 7). Everything here is pure.

const (
	yesAbove = 0.7
	noBelow  = 0.3

	maxRequestTokens  = 64000
	maxQuestionTokens = 32000
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
	Outcome string
	Row     int
	Note    string // code-built revise/fix/escalate reason
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
	ForemanReview string
	PlanRevisions int
}

type FileDiff struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Hunks   string `json:"hunks"`
}

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
	Criteria        []Criterion
	Spec            string
	Diff            []FileDiff
	Checks          []GateCheck
	OpenFindings    []GateFinding
	ArchitectReport string
	ForemanNotes    string
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

func planGateState(in PlanGateInput) (map[string]any, map[string]Noul) {
	state := map[string]any{
		"request":             in.Request,
		"acceptance_criteria": orderedCriteria(in.Criteria),
		"brief":               in.Brief,
		"spec":                in.Spec,
		"plan":                orderedTasks(in.Tasks),
		"foreman_review":      in.ForemanReview,
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
	qs["touches_sensitive"] = Noul{
		Instructions: "Does any task in plan change authentication, authorization, payment handling or how secrets are stored or read?",
		Criteria: yn(
			"A task changes login, sessions, permission checks, payment or billing code, API keys, tokens or credential storage.",
			"No task touches login, permissions, payments or secrets, even if it sits in the same file or package as such code."),
	}
	qs["task_overlap"] = Noul{
		Instructions: "Do two different tasks in plan implement the same behaviour?",
		Criteria: yn(
			"Two tasks both build the same function, endpoint or screen, so one would redo or conflict with the other.",
			"Each behaviour is built by exactly one task; tasks that depend on each other but build different parts do not overlap."),
	}
	qs["review_unresolved"] = Noul{
		Instructions: "Does foreman_review report a problem that plan does not address?",
		Criteria: yn(
			"foreman_review names a gap, risk or defect and no task in plan fixes or accounts for it.",
			"foreman_review reports no problem, or every problem it reports is addressed by a task in plan."),
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
		"architect_report":    in.ArchitectReport,
		"foreman_notes":       in.ForemanNotes,
	}
	qs := map[string]Noul{}
	for _, c := range in.Criteria {
		qs["met_"+c.ID] = Noul{
			Instructions: fmt.Sprintf("Does the evidence in diff, checks and architect_report show that acceptance_criteria.%s is satisfied?", c.ID),
			Criteria: yn(
				fmt.Sprintf("The diff contains code that implements acceptance_criteria.%s, and architect_report does not mark it not_met or unclear.", c.ID),
				fmt.Sprintf("The diff has no code for acceptance_criteria.%s, or architect_report marks it not_met or unclear.", c.ID)),
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

// trimHunks keeps hunk text up to budget bytes across all files, in order,
// cutting at a line boundary, and returns the paths whose hunks were cut.
func trimHunks(files []FileDiff, budget int) ([]FileDiff, []string) {
	out := make([]FileDiff, len(files))
	trimmed := []string{}
	left := budget
	for i, f := range files {
		out[i] = f
		if len(f.Hunks) <= left {
			left -= len(f.Hunks)
			continue
		}
		cut := f.Hunks[:left]
		if j := strings.LastIndexByte(cut, '\n'); j >= 0 {
			cut = cut[:j+1]
		} else {
			cut = ""
		}
		for !utf8.ValidString(cut) {
			cut = cut[:len(cut)-1]
		}
		out[i].Hunks = cut
		trimmed = append(trimmed, f.Path)
		left = 0
	}
	return out, trimmed
}

func estimateTokens(v any) int {
	raw, _ := json.Marshal(v)
	return len(raw) / 4
}

// overLimit reports whether a request would exceed 64k tokens, or any single
// question plus the state would exceed 32k.
func overLimit(state any, questions map[string]Noul) bool {
	s := estimateTokens(state)
	if s+estimateTokens(questions) > maxRequestTokens {
		return true
	}
	for _, q := range questions {
		if s+estimateTokens(q) > maxQuestionTokens {
			return true
		}
	}
	return false
}

func has(answers map[string]float64, ids ...string) bool {
	for _, id := range ids {
		if _, ok := answers[id]; !ok {
			return false
		}
	}
	return true
}

func sortedIDs(answers map[string]float64) []string {
	ids := make([]string, 0, len(answers))
	for id := range answers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func joinNotes(notes []string) string { return strings.Join(notes, "; ") }

func gateFailure(judgeErr error, overLimit bool, missing bool) (string, bool) {
	switch {
	case judgeErr != nil:
		return "judgment failed: " + judgeErr.Error(), true
	case overLimit:
		return "state is over the Jev context limit", true
	case missing:
		return "judgment is missing answers", true
	}
	return "", false
}

func decidePlanGate(answers map[string]float64, judgeErr error, overLimit bool, planRevisions int) GateOutcome {
	fixed := []string{"scope_creep", "touches_schema", "touches_sensitive", "task_overlap", "review_unresolved"}
	if note, bad := gateFailure(judgeErr, overLimit, !has(answers, fixed...)); bad {
		return GateOutcome{GateEscalate, 1, note}
	}
	var hard, soft []string // row 3 triggers, row 4 triggers
	var risky []string
	for _, id := range sortedIDs(answers) {
		v := judge(answers[id])
		switch {
		case id == "touches_schema" && v == vYes:
			risky = append(risky, "a task changes a database schema or migration")
		case id == "touches_sensitive" && v == vYes:
			risky = append(risky, "a task changes authentication, payments or secrets handling")
		case strings.HasPrefix(id, "covers_"):
			c := strings.TrimPrefix(id, "covers_")
			switch v {
			case vNo:
				hard = append(hard, fmt.Sprintf("acceptance criterion %s is not covered by any task", c))
			case vUnsettled:
				soft = append(soft, fmt.Sprintf("it is unclear whether acceptance criterion %s is covered", c))
			}
		case id == "scope_creep", id == "task_overlap", id == "review_unresolved":
			phrase := map[string]string{
				"scope_creep":       "the plan includes work the request does not ask for",
				"task_overlap":      "tasks overlap",
				"review_unresolved": "the Foreman review reports a problem the plan does not address",
			}[id]
			switch v {
			case vYes:
				hard = append(hard, phrase)
			case vUnsettled:
				soft = append(soft, "unsettled: "+phrase)
			}
		case v == vUnsettled: // touches_schema / touches_sensitive
			soft = append(soft, "unsettled: "+id)
		}
	}
	if len(risky) > 0 {
		return GateOutcome{GateEscalate, 2, joinNotes(risky)}
	}
	row, note := 0, ""
	switch {
	case len(hard) > 0:
		row, note = 3, joinNotes(append(hard, soft...))
	case len(soft) > 0:
		row, note = 4, joinNotes(soft)
	default:
		return GateOutcome{GateApprove, 5, ""}
	}
	if planRevisions >= maxPlanRevisions {
		return GateOutcome{GateEscalate, row, note}
	}
	return GateOutcome{GateRevise, row, note}
}

func decideDoneGate(answers map[string]float64, judgeErr error, overLimit, checksFailed bool, openBlockers int, fixBudgetLeft bool) GateOutcome {
	if checksFailed || openBlockers > 0 {
		return GateOutcome{GateLoop, 1, "a check failed or a blocker finding is open"}
	}
	if note, bad := gateFailure(judgeErr, overLimit, !has(answers, "deviates", "notes_unresolved")); bad {
		return GateOutcome{GateEscalate, 2, note}
	}
	var hard, soft []string
	for _, id := range sortedIDs(answers) {
		v := judge(answers[id])
		if v == vNo && strings.HasPrefix(id, "met_") {
			hard = append(hard, fmt.Sprintf("acceptance criterion %s is not shown as satisfied", strings.TrimPrefix(id, "met_")))
			continue
		}
		if v == vYes && (id == "deviates" || id == "notes_unresolved") {
			hard = append(hard, map[string]string{
				"deviates":         "the diff changes behaviour the spec does not describe",
				"notes_unresolved": "the Foreman notes report a problem the diff does not fix",
			}[id])
			continue
		}
		if v == vUnsettled {
			soft = append(soft, "unsettled: "+id)
		}
	}
	row, note := 0, ""
	switch {
	case len(hard) > 0:
		row, note = 3, joinNotes(append(hard, soft...))
	case len(soft) > 0:
		row, note = 4, joinNotes(soft)
	default:
		return GateOutcome{GateDone, 5, ""}
	}
	if !fixBudgetLeft {
		return GateOutcome{GateEscalate, row, note}
	}
	return GateOutcome{GateFix, row, note}
}

// gateEvent builds the audit payload for an actor "jev" event: the exact
// request, the answers and the table row, enough to replay the decision.
func gateEvent(gate string, state any, questions map[string]Noul, answers map[string]float64, model string, out GateOutcome, judgeErr error) map[string]any {
	p := map[string]any{
		"gate":     gate,
		"request":  map[string]any{"state": state, "questions": questions},
		"answers":  answers,
		"model":    model,
		"row":      out.Row,
		"outcome":  out.Outcome,
		"note":     out.Note,
		"yes_over": yesAbove,
		"no_under": noBelow,
	}
	if judgeErr != nil {
		p["error"] = judgeErr.Error()
	}
	return p
}
