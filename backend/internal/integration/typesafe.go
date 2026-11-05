package integration

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

const typeSafeURL = "https://api.typesafe.ai"

// TypeSafe is a client for TypeSafe's System One API (Jev judgments).
type TypeSafe struct {
	model string
	a     api
}

type Noul struct {
	Instructions string        `json:"instructions"`
	Criteria     *NoulCriteria `json:"criteria,omitempty"`
}

type NoulCriteria struct {
	True  string `json:"true"`
	False string `json:"false"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type SystemOneResult struct {
	Model   string
	Answers map[string]float64
	Usage   Usage
}

// NewTypeSafe builds a client; baseURL "" means the production API.
func NewTypeSafe(key, model, baseURL string) *TypeSafe {
	if baseURL == "" {
		baseURL = typeSafeURL
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	return &TypeSafe{model: model, a: newAPI("typesafe", baseURL, hc, bearer(key))}
}

// Noul asks every question and returns one probability per id.
func (t *TypeSafe) Noul(ctx context.Context, state any, questions map[string]Noul) (SystemOneResult, error) {
	qs := make(map[string]map[string]any, len(questions))
	for id, q := range questions {
		m := map[string]any{"type": "noul", "instructions": q.Instructions}
		if q.Criteria != nil {
			m["criteria"] = q.Criteria
		}
		qs[id] = m
	}
	var resp struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Type string   `json:"type"`
			Noul *float64 `json:"noul"`
		} `json:"answers"`
		Usage Usage `json:"usage"`
	}
	body := map[string]any{"model": t.model, "state": state, "questions": qs}
	if err := t.a.do(ctx, http.MethodPost, "/v1/systemone", body, &resp); err != nil {
		return SystemOneResult{}, err
	}
	out := SystemOneResult{Model: resp.Model, Answers: make(map[string]float64, len(questions)), Usage: resp.Usage}
	var missing []string
	for id := range questions {
		a, ok := resp.Answers[id]
		if !ok || a.Noul == nil {
			missing = append(missing, id)
			continue
		}
		out.Answers[id] = *a.Noul
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return SystemOneResult{}, fmt.Errorf("typesafe: no answer for %s", strings.Join(missing, ", "))
	}
	return out, nil
}
