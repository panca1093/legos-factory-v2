package usecase

import (
	"strings"
	"testing"
)

func specialties(picks []SpecialistPick) string {
	var s []string
	for _, p := range picks {
		s = append(s, p.Specialty)
	}
	return strings.Join(s, ",")
}

func TestSelectSpecialists(t *testing.T) {
	cases := []struct {
		files []string
		want  string
	}{
		{nil, "testing"},
		{[]string{"greeter/greeter.go"}, "testing,backend"},
		{[]string{"internal/http/auth.go"}, "testing,security,backend"},
		{[]string{"src/features/Order.vue"}, "testing,frontend,ui_ux"},
		{[]string{"README.md"}, "testing"},
		{[]string{"a/x.md", "b/y.md", "c/z.md"}, "testing,architecture"},
		{[]string{"store/migrations/006.sql"}, "testing,backend,architecture"},
	}
	for _, c := range cases {
		if got := specialties(SelectSpecialists(c.files)); got != c.want {
			t.Errorf("%v: got %s want %s", c.files, got, c.want)
		}
	}
	many := SelectSpecialists([]string{"api/session.go", "web/src/App.vue", "db/migrations/1.sql"})
	if len(many) != maxSpecialists || many[0].Specialty != "testing" || many[1].Specialty != "security" {
		t.Fatalf("cap and priority: %+v", many)
	}
	for _, p := range many {
		if p.Reason == "" {
			t.Fatalf("every pick needs a reason: %+v", p)
		}
	}
}
