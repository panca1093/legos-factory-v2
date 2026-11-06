package kb

import "testing"

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
	}
	for _, c := range cases {
		if got := GlobMatch(c.pattern, c.path); got != c.want {
			t.Errorf("GlobMatch(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}
