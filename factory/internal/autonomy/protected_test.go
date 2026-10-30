package autonomy

import "testing"

func TestProtectedIn(t *testing.T) {
	protected := []string{"db/migrations/**", "internal/money", "*.sql"}
	cases := map[string]struct {
		owned []string
		want  int
	}{
		"inside protected dir":      {[]string{"db/migrations/"}, 1},
		"file inside protected dir": {[]string{"db/migrations/001.go"}, 1},
		"parent contains protected": {[]string{"db/"}, 1},
		"whole repo":                {[]string{"."}, 1},
		"exact dir":                 {[]string{"internal/money"}, 1},
		"sibling only":              {[]string{"internal/moneyx/", "docs/"}, 0},
		"glob owned over protected": {[]string{"db/*/x.go"}, 1},
		"glob pattern file":         {[]string{"schema.sql"}, 1},
		"unrelated":                 {[]string{"pkg/", "cmd/app/"}, 0},
	}
	for name, c := range cases {
		if got := ProtectedIn(c.owned, protected); len(got) != c.want {
			t.Errorf("%s: %v -> %v", name, c.owned, got)
		}
	}
	if got := ProtectedIn([]string{"db/"}, nil); len(got) != 0 {
		t.Errorf("no protected paths: %v", got)
	}
}

func TestMatchesProtected(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"**/billing/**", "billing/x.go", true},
		{"**/billing/**", "services/a/billing/x/y.go", true},
		{"**/billing/**", "services/a/billings/x.go", false},
		{"*.sql", "a/b/c/x.sql", true},
		{"*.sql", "x.sqlx", false},
		{"*.sql", "x.SQL", false},
		{"migrations/**", "migrations/001/up.sql", true},
		{"migrations/**", "app/migrations/001.go", false},
		{"services/*/money", "services/lsm/money/x.go", true},
		{"services/*/money", "services/lsm/x/money/x.go", false},
		{"a/**/z", "a/z/f.go", true},
		{"a/**/z", "a/b/c/z/f.go", true},
		{"deploy", "src/deploy/x.yaml", true},
		{"./deploy/", "deploy/x.yaml", true},
		{"Deploy", "deploy/x.yaml", false},
	}
	for _, c := range cases {
		if got := matchesProtected(c.path, []string{c.pat}); got != c.want {
			t.Errorf("%q vs %q = %v, want %v", c.pat, c.path, got, c.want)
		}
	}
}

func TestValidatePattern(t *testing.T) {
	for _, ok := range []string{"a/**", "**/b/**", "*.sql", "x/[ab]/y", "./deploy/"} {
		if err := ValidatePattern(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/", "/abs", "a/[", "a/**b", "../x", "a//b"} {
		if ValidatePattern(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
