package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTypeSafeNoul(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers: %v", r.Header)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"model":"jev-1.14.0","answers":{"a":{"type":"noul","noul":0.93},"b":{"type":"noul","noul":0}},"usage":{"input_tokens":360,"output_tokens":39}}`))
	}))
	defer srv.Close()
	res, err := NewTypeSafe("k", "jev-1.14.0", srv.URL).Noul(context.Background(), map[string]any{"x": 1}, map[string]Noul{
		"a": {Instructions: "q", Criteria: &NoulCriteria{True: "t", False: "f"}},
		"b": {Instructions: "q2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answers["a"] != 0.93 || res.Answers["b"] != 0 || res.Model != "jev-1.14.0" || res.Usage.InputTokens != 360 || res.Usage.OutputTokens != 39 {
		t.Fatalf("result: %+v", res)
	}
	qs := got["questions"].(map[string]any)
	a := qs["a"].(map[string]any)
	if got["model"] != "jev-1.14.0" || a["type"] != "noul" || a["instructions"] != "q" || a["criteria"].(map[string]any)["true"] != "t" {
		t.Fatalf("request: %v", got)
	}
	if _, ok := qs["b"].(map[string]any)["criteria"]; ok {
		t.Fatal("criteria must be omitted when unset")
	}
}

func TestTypeSafeErrors(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"non-2xx":   {500, strings.Repeat("x", 500), "500"},
		"missing":   {200, `{"answers":{"a":{"type":"noul","noul":0.5}}}`, "no answer for b"},
		"no value":  {200, `{"answers":{"a":{"type":"noul","noul":0.5},"b":{"type":"noul"}}}`, "no answer for b"},
		"malformed": {200, `{not json`, "unexpected JSON"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				w.Write([]byte(c.body))
			}))
			defer srv.Close()
			_, err := NewTypeSafe("k", "m", srv.URL).Noul(context.Background(), map[string]any{}, map[string]Noul{"a": {Instructions: "q"}, "b": {Instructions: "q"}})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want containing %q", err, c.want)
			}
			if len(err.Error()) > 400 {
				t.Fatalf("error body not trimmed: %d bytes", len(err.Error()))
			}
		})
	}
}
