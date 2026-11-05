package usecase

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestRepoFactsListsChecksTreeAndSkipsVendorDirs(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"go.mod":                    "module x\n",
		"cmd/app/main.go":           "package main\n",
		"cmd/app/deep/too/far.go":   "package deep\n",
		"node_modules/pkg/index.js": "x",
		".git/config":               "x",
		"vendor/v/v.go":             "x",
		"internal/store/store.go":   "package store\n",
		"README.md":                 "# x\n",
	})
	got := repoFacts(context.Background(), dir)
	for _, want := range []string{"Toolchains: go", "Check go-test: go test ./...", "cmd/app/", "internal/store/", "README.md"} {
		if !strings.Contains(got, want) {
			t.Fatalf("facts lack %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"node_modules", ".git", "vendor", "far.go"} {
		if strings.Contains(got, bad) {
			t.Fatalf("facts must not list %q:\n%s", bad, got)
		}
	}
}

func TestRepoFactsReportsProfileErrorAndCapsTree(t *testing.T) {
	files := map[string]string{".legos/checks.json": "{not json"}
	for i := 0; i < factsMaxTree+20; i++ {
		files[fmt.Sprintf("f%03d.txt", i)] = "x"
	}
	got := repoFacts(context.Background(), writeFiles(t, files))
	if !strings.Contains(got, "unavailable: invalid .legos/checks.json") {
		t.Fatalf("profile error missing:\n%s", got)
	}
	if !strings.Contains(got, "listing capped at 200 entries") {
		t.Fatalf("tree must be capped:\n%s", got)
	}
	if strings.Contains(got, "f219.txt") {
		t.Fatal("entries past the cap must not be listed")
	}
}
