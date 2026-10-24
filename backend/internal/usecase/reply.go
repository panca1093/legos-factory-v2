package usecase

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

type planTask struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Objective string   `json:"objective"`
	Role      string   `json:"role"`
	Deps      []string `json:"deps"`
}

// planReply is the Architect's draft.
type planReply struct {
	Decision     string     `json:"decision"`
	Question     string     `json:"question"`
	QuestionText string     `json:"question_text"`
	Summary      string     `json:"summary"`
	Tasks        []planTask `json:"tasks"`
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
		if t.ID != "" && ids[t.ID] {
			return fmt.Errorf("duplicate task id %q", t.ID)
		}
		ids[t.ID] = true
	}
	return nil
}

// reviewReply is the Foreman's independent plan review.
type reviewReply struct {
	Decision     string `json:"decision"`
	Notes        string `json:"notes"`
	QuestionText string `json:"question_text"`
}

func (r *reviewReply) validate() error {
	if err := oneOf("decision", r.Decision, "APPROVE", "REVISE", "CLARIFY"); err != nil {
		return err
	}
	switch {
	case r.Decision == "REVISE" && strings.TrimSpace(r.Notes) == "":
		return errors.New("REVISE requires notes for the Architect")
	case r.Decision == "CLARIFY" && strings.TrimSpace(r.QuestionText) == "":
		return errors.New("CLARIFY requires question_text for the human")
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
