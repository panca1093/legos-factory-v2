package usecase

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

// Agent replies are prose plus JSON. Only a reply that decodes AND passes its
// schema may drive a workflow transition (INSTRUCTION §7.2).

type reply interface{ validate() error }

var errNoJSON = errors.New("no JSON object found in agent reply")

// decodeReply fills v from the agent's answer: the whole fence-stripped text
// if it is JSON, otherwise the last balanced object that decodes and
// validates. It returns the validation error of the best candidate.
func decodeReply(text string, v reply) error {
	text = StripFences(text)
	candidates := append([]string{strings.TrimSpace(text)}, jsonObjects(text)...)
	var lastErr error = errNoJSON
	for i := len(candidates) - 1; i >= 0; i-- {
		c := candidates[i]
		if i == 0 && !strings.HasPrefix(c, "{") {
			continue
		}
		reset(v)
		if err := json.Unmarshal([]byte(c), v); err != nil {
			continue
		}
		if err := v.validate(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	reset(v)
	return lastErr
}

// jsonObjects returns every top-level balanced {...} segment, ignoring
// braces inside JSON strings.
func jsonObjects(s string) []string {
	var out []string
	depth, start := 0, -1
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case esc:
			esc = false
		case inStr && ch == '\\':
			esc = true
		case ch == '"' && depth > 0:
			inStr = !inStr
		case inStr:
		case ch == '{':
			if depth == 0 {
				start = i
			}
			depth++
		case ch == '}' && depth > 0:
			depth--
			if depth == 0 {
				out = append(out, s[start:i+1])
			}
		}
	}
	return out
}

func reset(v any) {
	e := reflect.ValueOf(v).Elem()
	e.Set(reflect.Zero(e.Type()))
}

func oneOf(field, got string, allowed ...string) error {
	for _, a := range allowed {
		if got == a {
			return nil
		}
	}
	return fmt.Errorf("%s must be one of %s, got %q", field, strings.Join(allowed, "|"), got)
}

var taskIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type planTask struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Objective string   `json:"objective"`
	Role      string   `json:"role"`
	Deps      []string `json:"deps"`
	Scope     []string `json:"scope"`
}

// planReply is the Architect's draft.
type planReply struct {
	Decision     string     `json:"decision"`
	Question     string     `json:"question"`
	QuestionText string     `json:"question_text"`
	Summary      string     `json:"summary"`
	Spec         string     `json:"spec"`
	Tasks        []planTask `json:"tasks"`
	CodeFacts    []string   `json:"code_facts"`
}

func (p *planReply) validate() error {
	if err := oneOf("decision", p.Decision, "APPROVE", "CLARIFY"); err != nil {
		return err
	}
	if p.Decision == "CLARIFY" {
		if strings.TrimSpace(p.QuestionText+p.Question) == "" {
			return errors.New("CLARIFY requires question_text")
		}
		return nil
	}
	if strings.TrimSpace(p.Spec) == "" {
		return errors.New("APPROVE requires spec")
	}
	if len(p.Tasks) == 0 {
		return errors.New("APPROVE requires at least one task")
	}
	ids := map[string]bool{}
	for i, t := range p.Tasks {
		if strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.Objective) == "" {
			return fmt.Errorf("task %d needs a title and an objective", i+1)
		}
		if t.Role != "" && t.Role != "builder" {
			return fmt.Errorf("task %d role must be builder, got %q", i+1, t.Role)
		}
		if t.ID != "" && !taskIDPattern.MatchString(t.ID) {
			return fmt.Errorf("task id %q may only contain letters, digits, '-' and '_'", t.ID)
		}
		if t.ID != "" && ids[t.ID] {
			return fmt.Errorf("duplicate task id %q", t.ID)
		}
		ids[t.ID] = true
	}
	return nil
}

// reviewFinding is one problem the Foreman found in the contract. Question is
// set only on a requirement-layer finding that needs a human answer.
type reviewFinding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Layer    string `json:"layer"`
	Cites    string `json:"cites"`
	Issue    string `json:"issue"`
	Fix      string `json:"fix"`
	Question string `json:"question,omitempty"`
}

type reviewCoverage struct {
	Criterion    string `json:"criterion"`
	Verification string `json:"verification"`
}

type findingStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

const maxReviewFindings = 10

// reviewReply is the Foreman's read-only analysis of the contract. It carries
// no decision: the contract gate decides in code (gates.go). Resolved is set
// only on a scoped re-review, where it marks each previous critical finding.
type reviewReply struct {
	Findings []reviewFinding  `json:"findings"`
	Coverage []reviewCoverage `json:"coverage"`
	Resolved []findingStatus  `json:"resolved,omitempty"`
}

func (r *reviewReply) validate() error {
	if r.Findings == nil {
		return errors.New("findings is required (use [] when there are none)")
	}
	if len(r.Findings) > maxReviewFindings {
		return fmt.Errorf("at most %d findings, got %d", maxReviewFindings, len(r.Findings))
	}
	for i, f := range r.Findings {
		if strings.TrimSpace(f.ID) == "" || strings.TrimSpace(f.Issue) == "" {
			return fmt.Errorf("findings[%d] needs an id and an issue", i)
		}
		if err := oneOf(fmt.Sprintf("findings[%d].severity", i), f.Severity, "critical", "high", "low"); err != nil {
			return err
		}
		if err := oneOf(fmt.Sprintf("findings[%d].layer", i), f.Layer, "contract", "requirement"); err != nil {
			return err
		}
		if f.Severity == "critical" && strings.TrimSpace(f.Cites) == "" {
			return fmt.Errorf("findings[%d] is critical, so it must cite the contract", i)
		}
	}
	for i, c := range r.Coverage {
		if strings.TrimSpace(c.Criterion) == "" {
			return fmt.Errorf("coverage[%d] needs a criterion", i)
		}
		if err := oneOf(fmt.Sprintf("coverage[%d].verification", i), c.Verification, "test", "check", "missing"); err != nil {
			return err
		}
	}
	for i, s := range r.Resolved {
		if err := oneOf(fmt.Sprintf("resolved[%d].status", i), s.Status, "resolved", "unresolved"); err != nil {
			return err
		}
	}
	return nil
}

func findingLine(f reviewFinding) string {
	return fmt.Sprintf("%s [%s, %s] %s: %s Fix: %s", f.ID, f.Severity, f.Layer, strings.TrimSpace(f.Cites), strings.TrimSpace(f.Issue), strings.TrimSpace(f.Fix))
}

// text renders the analysis for the activity trail and the knowledge record.
func (r *reviewReply) text() string {
	var b strings.Builder
	for _, f := range r.Findings {
		b.WriteString("- " + findingLine(f) + "\n")
	}
	if b.Len() == 0 {
		b.WriteString("No findings.\n")
	}
	if len(r.Coverage) > 0 {
		b.WriteString("Coverage:")
		for _, c := range r.Coverage {
			b.WriteString(" " + strings.TrimSpace(c.Criterion) + "=" + c.Verification)
		}
	}
	return strings.TrimSpace(b.String())
}

// validationReply is the Foreman's notes on whether the finished work is done.
type validationReply struct {
	Notes string `json:"notes"`
}

func (r *validationReply) validate() error {
	if strings.TrimSpace(r.Notes) == "" {
		return errors.New("notes is required")
	}
	return nil
}

// verifyReply is the Architect's check of the built result against its spec
// and plan.
type verifyReply struct {
	Verdicts   []ArchitectVerdict `json:"verdicts"`
	Deviations []string           `json:"deviations"`
}

func (r *verifyReply) validate() error {
	if r.Verdicts == nil {
		return errors.New("verdicts is required (one entry per acceptance criterion)")
	}
	for i := range r.Verdicts {
		v := &r.Verdicts[i]
		v.CriterionID = strings.TrimSpace(v.CriterionID)
		v.Evidence, v.Gap, v.Fix = strings.TrimSpace(v.Evidence), strings.TrimSpace(v.Gap), strings.TrimSpace(v.Fix)
		v.Status = strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(v.Status)), "-", "_"), " ", "_")
		if v.CriterionID == "" {
			return fmt.Errorf("verdicts[%d] needs a criterion_id", i)
		}
		if err := oneOf(fmt.Sprintf("verdicts[%d].status", i), v.Status, "met", "not_met", "unclear"); err != nil {
			return err
		}
	}
	var deviations []string
	for _, d := range r.Deviations {
		if d = strings.TrimSpace(d); d != "" {
			deviations = append(deviations, d)
		}
	}
	r.Deviations = deviations
	return nil
}

// openQuestion is a real unknown the Foreman cannot settle from the request or the repository.
type openQuestion struct {
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	Recommended string   `json:"recommended"`
}

// ask renders the question as the human sees it.
func (q openQuestion) ask() string {
	s := strings.TrimSpace(q.Question)
	if len(q.Options) > 0 {
		s += " Options: " + strings.Join(q.Options, " / ") + "."
	}
	return s + " Recommended: " + strings.TrimSpace(q.Recommended) + "."
}

const maxOpenQuestions = 3

// openQuestionsHeader marks a brief that still waits on a human answer.
const openQuestionsHeader = "Open questions:"

// intakeReply is the Foreman's brief for the Architect: what and why, never how.
type intakeReply struct {
	Outcome          string         `json:"outcome"`
	Why              string         `json:"why"`
	DomainInvariants []string       `json:"domain_invariants"`
	SurfacesTouched  []string       `json:"surfaces_touched"`
	Constraints      []string       `json:"constraints"`
	OutOfScope       []string       `json:"out_of_scope"`
	Assumptions      []string       `json:"assumptions"`
	OpenQuestions    []openQuestion `json:"open_questions"`
	CodeFacts        []string       `json:"code_facts"`
}

func (r *intakeReply) validate() error {
	if strings.TrimSpace(r.Outcome) == "" {
		return errors.New("outcome is required")
	}
	var qs []openQuestion
	for i, q := range r.OpenQuestions {
		if strings.TrimSpace(q.Question) == "" {
			continue
		}
		if strings.TrimSpace(q.Recommended) == "" {
			return fmt.Errorf("open_questions[%d] needs a recommended answer", i)
		}
		qs = append(qs, q)
	}
	if len(qs) > maxOpenQuestions {
		qs = qs[:maxOpenQuestions] // ranked, so the tail is the least important
	}
	r.OpenQuestions = qs
	return nil
}

// text renders the reply as the stored brief the Architect reads.
func (r *intakeReply) text() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(r.Outcome))
	if why := strings.TrimSpace(r.Why); why != "" {
		b.WriteString("\n\nWhy: " + why)
	}
	for _, s := range []struct {
		title string
		items []string
	}{
		{"Domain invariants", r.DomainInvariants}, {"Surfaces touched", r.SurfacesTouched},
		{"Constraints", r.Constraints}, {"Out of scope", r.OutOfScope}, {"Assumptions", r.Assumptions},
	} {
		if len(s.items) == 0 {
			continue
		}
		b.WriteString("\n\n" + s.title + ":")
		for _, it := range s.items {
			b.WriteString("\n- " + strings.TrimSpace(it))
		}
	}
	if len(r.OpenQuestions) > 0 {
		b.WriteString("\n\n" + openQuestionsHeader)
		for _, q := range r.OpenQuestions {
			b.WriteString("\n- " + q.ask())
		}
	}
	return b.String()
}

// foremanAnswerReply is the Foreman's response to an Architect question.
type foremanAnswerReply struct {
	Decision string `json:"decision"`
	Answer   string `json:"answer"`
	Reason   string `json:"reason"`
}

func (r *foremanAnswerReply) validate() error {
	if err := oneOf("decision", r.Decision, "ANSWER", "ESCALATE"); err != nil {
		return err
	}
	if r.Decision == "ANSWER" && strings.TrimSpace(r.Answer) == "" {
		return errors.New("ANSWER requires answer")
	}
	return nil
}

type findingItem struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Location string `json:"location"`
}

// findingsReply is an Inspector's review.
type findingsReply struct {
	Findings []findingItem `json:"findings"`
}

func (f *findingsReply) validate() error {
	if f.Findings == nil {
		return errors.New(`findings is required (use [] when there are none)`)
	}
	for i, fd := range f.Findings {
		if err := oneOf(fmt.Sprintf("findings[%d].severity", i), fd.Severity, "blocker", "major", "minor", "info"); err != nil {
			return err
		}
		if strings.TrimSpace(fd.Title) == "" {
			return fmt.Errorf("findings[%d] needs a title", i)
		}
	}
	return nil
}
