package kb

import (
	"strings"
	"testing"
	"time"
)

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"docs/**", "docs/a.md", true},
		{"docs/**", "docs/x/y/a.md", true},
		{"docs/**", "other/docs/a.md", false},
		{"**/a.md", "a.md", true},
		{"**/a.md", "x/y/a.md", true},
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/x/a.md", false},
		{"README.md", "README.md", true},
		{"README.md", "sub/README.md", false},
		{"README.md", "READMExmd", false},
		{"backend/internal/kb/defaults/**", "backend/internal/kb/defaults/playbooks/b.md", true},
		{"a?.md", "ab.md", true},
		{"a?.md", "a/.md", false},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"./docs/**", "docs/a.md", true},
		{"docs/", "docs/a/b.md", true},
		{"docs/", "docs", false},
		{"Docs/**", "docs/a.md", false},
		{"[ab].md", "[ab].md", true},
		{"[ab].md", "a.md", false},
	}
	for _, c := range cases {
		if got := GlobMatch(c.pattern, c.path); got != c.want {
			t.Errorf("GlobMatch(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestGlobMatchLongPatternIsFast(t *testing.T) {
	pattern := strings.Repeat("**/a", 50)[:200]
	path := strings.Repeat("a/", 2000) + "b"
	start := time.Now()
	GlobMatch(pattern, path)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v", d)
	}
}
