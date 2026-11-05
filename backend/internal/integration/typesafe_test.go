package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(url, model string) *TypeSafe {
	c := NewTypeSafe("k", model, url)
	c.backoff = 0
	return c
}

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
	res, err := testClient(srv.URL, "jev-1.14.0").Noul(context.Background(), map[string]any{"x": 1}, map[string]Noul{
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
		"non-2xx":    {500, strings.Repeat("x", 500), "500"},
		"missing":    {200, `{"answers":{"a":{"type":"noul","noul":0.5}}}`, "no answer for b"},
		"no value":   {200, `{"answers":{"a":{"type":"noul","noul":0.5},"b":{"type":"noul"}}}`, "no answer for b"},
		"malformed":  {200, `{not json`, "unexpected JSON"},
		"wrong type": {200, `{"answers":{"a":{"type":"noul","noul":0.5},"b":{"type":"other","noul":0.5}}}`, "no answer for b"},
		"above one":  {200, `{"answers":{"a":{"type":"noul","noul":1.2},"b":{"type":"noul","noul":0.5}}}`, "no answer for a"},
		"below zero": {200, `{"answers":{"a":{"type":"noul","noul":0.5},"b":{"type":"noul","noul":-0.1}}}`, "no answer for b"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				w.Write([]byte(c.body))
			}))
			defer srv.Close()
			_, err := testClient(srv.URL, "m").Noul(context.Background(), map[string]any{}, map[string]Noul{"a": {Instructions: "q"}, "b": {Instructions: "q"}})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want containing %q", err, c.want)
			}
			if len(err.Error()) > 400 {
				t.Fatalf("error body not trimmed: %d bytes", len(err.Error()))
			}
		})
	}
}

func TestTypeSafeRequestBytes(t *testing.T) {
	var wire []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"answers":{"a":{"type":"noul","noul":0.5}}}`))
	}))
	defer srv.Close()
	res, err := testClient(srv.URL, "jev-1.14.0").Noul(context.Background(), map[string]any{"x": "<&>"}, map[string]Noul{"a": {Instructions: "q"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Request) != string(wire) || res.RequestedModel != "jev-1.14.0" {
		t.Fatalf("recorded %s, wire %s, model %q", res.Request, wire, res.RequestedModel)
	}
	var body struct {
		Model     string
		Questions map[string]map[string]any
	}
	if err := json.Unmarshal(wire, &body); err != nil || body.Model != "jev-1.14.0" || body.Questions["a"]["type"] != "noul" {
		t.Fatalf("wire body: %s (%v)", wire, err)
	}
}

func TestTypeSafeRetry(t *testing.T) {
	ok := `{"answers":{"a":{"type":"noul","noul":0.5}}}`
	cases := []struct {
		name     string
		first    func(w http.ResponseWriter)
		wantErr  bool
		wantHits int32
	}{
		{"429 then ok", func(w http.ResponseWriter) { w.WriteHeader(429) }, false, 2},
		{"503 then ok", func(w http.ResponseWriter) { w.WriteHeader(503) }, false, 2},
		{"400 not retried", func(w http.ResponseWriter) { w.WriteHeader(400) }, true, 1},
		{"401 not retried", func(w http.ResponseWriter) { w.WriteHeader(401) }, true, 1},
		{"404 not retried", func(w http.ResponseWriter) { w.WriteHeader(404) }, true, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var hits int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if atomic.AddInt32(&hits, 1) == 1 {
					c.first(w)
					return
				}
				w.Write([]byte(ok))
			}))
			defer srv.Close()
			_, err := testClient(srv.URL, "m").Noul(context.Background(), map[string]any{}, map[string]Noul{"a": {Instructions: "q"}})
			if (err != nil) != c.wantErr || atomic.LoadInt32(&hits) != c.wantHits {
				t.Fatalf("err=%v hits=%d, want err=%v hits=%d", err, hits, c.wantErr, c.wantHits)
			}
		})
	}
	t.Run("only one retry", func(t *testing.T) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(500)
		}))
		defer srv.Close()
		res, err := testClient(srv.URL, "m").Noul(context.Background(), map[string]any{}, map[string]Noul{"a": {Instructions: "q"}})
		if err == nil || hits != 2 || len(res.Request) == 0 {
			t.Fatalf("err=%v hits=%d request=%s", err, hits, res.Request)
		}
	})
	t.Run("network error then ok", func(t *testing.T) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if atomic.AddInt32(&hits, 1) == 1 {
				hj, _ := w.(http.Hijacker)
				conn, _, _ := hj.Hijack()
				conn.Close()
				return
			}
			w.Write([]byte(ok))
		}))
		defer srv.Close()
		if _, err := testClient(srv.URL, "m").Noul(context.Background(), map[string]any{}, map[string]Noul{"a": {Instructions: "q"}}); err != nil || hits != 2 {
			t.Fatalf("err=%v hits=%d", err, hits)
		}
	})
	t.Run("timeout then ok", func(t *testing.T) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if atomic.AddInt32(&hits, 1) == 1 {
				time.Sleep(200 * time.Millisecond)
				return
			}
			w.Write([]byte(ok))
		}))
		defer srv.Close()
		c := testClient(srv.URL, "m")
		c.a.hc = &http.Client{Timeout: 50 * time.Millisecond}
		if _, err := c.Noul(context.Background(), map[string]any{}, map[string]Noul{"a": {Instructions: "q"}}); err != nil || hits != 2 {
			t.Fatalf("err=%v hits=%d", err, hits)
		}
	})
	t.Run("cancelled context not retried", func(t *testing.T) {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(500)
		}))
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := testClient(srv.URL, "m").Noul(ctx, map[string]any{}, map[string]Noul{"a": {Instructions: "q"}}); err == nil || hits > 0 {
			t.Fatalf("err=%v hits=%d", err, hits)
		}
	})
}
