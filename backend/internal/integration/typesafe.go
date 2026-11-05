package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
)

const typeSafeURL = "https://api.typesafe.ai"

// TypeSafe is a client for TypeSafe's System One API (Jev judgments).
type TypeSafe struct {
	model   string
	a       api
	backoff time.Duration
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
	Model          string // model that answered
	RequestedModel string
	Request        json.RawMessage // exact request body; set even on error
	Answers        map[string]float64
	Usage          Usage
}

// NewTypeSafe builds a client; baseURL "" means the production API.
func NewTypeSafe(key, model, baseURL string) *TypeSafe {
	if baseURL == "" {
		baseURL = typeSafeURL
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	return &TypeSafe{model: model, a: newAPI("typesafe", baseURL, hc, bearer(key)), backoff: 2 * time.Second}
}

type wireNoul struct {
	Type string `json:"type"`
	Noul
}

// Noul asks every question and returns one probability per id. It retries
// once on a transient failure (429, 5xx, timeout or network error).
func (t *TypeSafe) Noul(ctx context.Context, state any, questions map[string]Noul) (SystemOneResult, error) {
	qs := make(map[string]wireNoul, len(questions))
	for id, q := range questions {
		qs[id] = wireNoul{Type: "noul", Noul: q}
	}
	raw, err := json.Marshal(map[string]any{"model": t.model, "state": state, "questions": qs})
	if err != nil {
		return SystemOneResult{}, err
	}
	out := SystemOneResult{RequestedModel: t.model, Request: raw}
	var resp struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Type string   `json:"type"`
			Noul *float64 `json:"noul"`
		} `json:"answers"`
		Usage Usage `json:"usage"`
	}
	err = t.a.do(ctx, http.MethodPost, "/v1/systemone", json.RawMessage(raw), &resp)
	if err != nil && transient(ctx, err) {
		select {
		case <-time.After(t.backoff):
			err = t.a.do(ctx, http.MethodPost, "/v1/systemone", json.RawMessage(raw), &resp)
		case <-ctx.Done():
		}
	}
	if err != nil {
		return out, err
	}
	out.Model, out.Usage = resp.Model, resp.Usage
	out.Answers = make(map[string]float64, len(questions))
	var missing []string
	for id := range questions {
		a, ok := resp.Answers[id]
		if !ok || a.Type != "noul" || a.Noul == nil || !(*a.Noul >= 0 && *a.Noul <= 1) {
			missing = append(missing, id)
			continue
		}
		out.Answers[id] = *a.Noul
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		out.Answers = nil
		return out, fmt.Errorf("typesafe: no answer for %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func transient(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var pe *model.ProviderError
	if errors.As(err, &pe) {
		return pe.Status == http.StatusTooManyRequests || pe.Status >= 500
	}
	var ne net.Error
	return errors.As(err, &ne)
}
