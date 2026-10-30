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
