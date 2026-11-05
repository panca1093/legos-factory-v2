package usecase

import (
	"reflect"
	"testing"

	"legos-factory/backend/internal/model"
)

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, file string
		want          bool
	}{
		{"src/**/*.ts", "src/a.ts", true},
		{"src/**/*.ts", "src/a/b/c.ts", true},
		{"src/**/*.ts", "lib/a.ts", false},
		{"src/**", "src/a/b.go", true},
		{"**/x.go", "x.go", true},
		{"**/x.go", "a/b/x.go", true},
		{"src/*.ts", "src/a.ts", true},
		{"src/*.ts", "src/a/b.ts", false},
		{"src/?.ts", "src/a.ts", true},
		{"src/?.ts", "src/ab.ts", false},
		{"*.vue", "a/b/App.vue", true},
		{"*.vue", "App.vue", true},
		{"*.vue", "App.vue.go", false},
		{"Dockerfile", "deploy/Dockerfile", true},
		{"a/**/b/**/c.go", "a/x/b/y/z/c.go", true},
		{"a/**/b/**/c.go", "a/b/c.go", true},
		{"a/**/b/**/c.go", "a/x/c.go", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.file); got != c.want {
			t.Errorf("globMatch(%q,%q)=%v want %v", c.pattern, c.file, got, c.want)
		}
	}
}

func TestRouteMatchesNegation(t *testing.T) {
	pats := []string{"src/**/*.ts", "!src/**/*.test.ts"}
	if !routeMatches(pats, "src/a/b.ts") {
		t.Error("plain ts should match")
	}
	if routeMatches(pats, "src/a/b.test.ts") {
		t.Error("test ts should be excluded")
	}
	if routeMatches([]string{"!src/**"}, "src/a.ts") || routeMatches([]string{"!x/**"}, "src/a.ts") {
		t.Error("negation alone never matches")
	}
	if !routeMatches([]string{"!src/gen/**", "src/**"}, "src/a.ts") {
		t.Error("negation order must not matter")
	}
}

func TestMatchReviewersPOS(t *testing.T) {
	routes := []model.ReviewerRoute{
		{Paths: []string{"src/**/*.ts"}, Reviewers: []string{"backend"}},
		{Paths: []string{"**/*.test.ts"}, Reviewers: []string{"testing"}},
	}
	picks := MatchReviewers(routes, []string{"src/checkout/checkout.test.ts", "src/checkout/checkout.ts"})
	if got := specialties(picks); got != "testing,backend" {
		t.Fatalf("got %s", got)
	}
	if picks[1].Reason != "route src/**/*.ts matched src/checkout/checkout.test.ts" {
		t.Fatalf("reason: %q", picks[1].Reason)
	}
	if picks[0].Reason != "always reviews tests against the acceptance criteria" {
		t.Fatalf("testing reason: %q", picks[0].Reason)
	}
}

func TestMatchReviewers(t *testing.T) {
	routes := []model.ReviewerRoute{
		{Paths: []string{"src/ui/**", "*.vue"}, Reviewers: []string{"frontend", "ui_ux"}},
		{Paths: []string{"*.go"}, Reviewers: []string{"backend", "zeta", "alpha"}},
		{Paths: []string{"auth/**"}, Reviewers: []string{"security"}},
	}
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"no match", []string{"README.md"}, "testing"},
		{"no files", nil, "testing"},
		{"union across routes", []string{"src/ui/x.css"}, "testing,frontend,ui_ux"},
		{"unknown after known, alphabetical, capped", []string{"a.go"}, "testing,backend,alpha"},
		{"canonical order", []string{"auth/x.md", "a.vue", "a.go"}, "testing,security,backend"},
		{"cap drops later", []string{"auth/x.md", "a.vue"}, "testing,security,frontend"},
	}
	for _, c := range cases {
		if got := specialties(MatchReviewers(routes, c.files)); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
	if got := MatchReviewers(routes, []string{"a.go", "auth/x.md", "a.vue"}); len(got) != maxSpecialists {
		t.Fatalf("cap: %d", len(got))
	}
}

func TestMatchReviewersUnknownKept(t *testing.T) {
	routes := []model.ReviewerRoute{{Paths: []string{"db/**"}, Reviewers: []string{"dba"}}}
	if got := specialties(MatchReviewers(routes, []string{"db/m.sql"})); got != "testing,dba" {
		t.Fatalf("got %s", got)
	}
}

func TestMatchReviewersOrderIndependent(t *testing.T) {
	routes := []model.ReviewerRoute{
		{Paths: []string{"**/*.go"}, Reviewers: []string{"backend"}},
		{Paths: []string{"web/**"}, Reviewers: []string{"backend", "frontend"}},
	}
	a := MatchReviewers(routes, []string{"web/a.ts", "x/b.go", "c.go"})
	b := MatchReviewers(routes, []string{"c.go", "x/b.go", "web/a.ts"})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("order dependent:\n%+v\n%+v", a, b)
	}
	if a[1].Reason != "route **/*.go matched c.go" {
		t.Fatalf("reason uses first sorted file: %q", a[1].Reason)
	}
}

func TestProposeRoutes(t *testing.T) {
	files := []string{"internal/a.go", "internal/b.go", "cmd/main.go", "web/src/App.vue", "README.md", "internal/auth/token.go"}
	got := ProposeRoutes(files)
	rev := append([]string(nil), files...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	if !reflect.DeepEqual(got, ProposeRoutes(rev)) {
		t.Fatal("not deterministic across input order")
	}
	want := []model.ReviewerRoute{
		{Paths: []string{"cmd/**/*.go"}, Reviewers: []string{"backend"}},
		{Paths: []string{"internal/**/*.go"}, Reviewers: []string{"security", "backend"}},
		{Paths: []string{"web/**/*.vue"}, Reviewers: []string{"frontend", "ui_ux"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestProposeRoutesMergeAndRoot(t *testing.T) {
	got := ProposeRoutes([]string{"web/a.vue", "web/b.tsx", "main.go", "Dockerfile", "README.md"})
	want := []model.ReviewerRoute{
		{Paths: []string{"*.go"}, Reviewers: []string{"backend"}},
		{Paths: []string{"Dockerfile"}, Reviewers: []string{"security"}},
		{Paths: []string{"web/**/*.tsx", "web/**/*.vue"}, Reviewers: []string{"frontend", "ui_ux"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}
