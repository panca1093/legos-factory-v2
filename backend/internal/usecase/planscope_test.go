package usecase

import (
	"strings"
	"testing"
)

func TestPlanScopeRules(t *testing.T) {
	plan := func(tasks string) string {
		return `{"decision":"APPROVE","spec":"s","tasks":[` + tasks + `]}`
	}
	task := func(id, deps, scope string) string {
		return `{"id":"` + id + `","title":"t","objective":"o","deps":[` + deps + `],"scope":[` + scope + `]}`
	}
	cases := []struct {
		name, tasks, wantErr string
	}{
		{"five entries ok", task("a", "", `"1","2","3","4","5"`), ""},
		{"six entries", task("a", "", `"1","2","3","4","5","6"`), `task "a" has 6 scope entries`},
		{"disjoint parallel", task("a", "", `"api/x.go"`) + "," + task("b", "", `"api/y.go"`), ""},
		{"same file unordered", task("a", "", `"api/x.go"`) + "," + task("b", "", `"api/x.go"`), `tasks "a" and "b" both touch "api/x.go"`},
		{"prefix unordered", task("a", "", `"api/internal"`) + "," + task("b", "", `"api/internal/x.go"`), `"api/internal"`},
		{"sibling name not a prefix", task("a", "", `"api/int"`) + "," + task("b", "", `"api/internal/x.go"`), ""},
		{"same file ordered", task("a", "", `"x.go"`) + "," + task("b", `"a"`, `"x.go"`), ""},
		{"transitively ordered", task("a", "", `"x.go"`) + "," + task("b", `"a"`, `"y.go"`) + "," + task("c", `"b"`, `"x.go"`), ""},
	}
	for _, c := range cases {
		var p planReply
		err := decodeReply(plan(c.tasks), &p)
		if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
	}
}

func TestPlanScopeReportsEveryViolation(t *testing.T) {
	err := checkScopes([]planTask{
		{ID: "a", Scope: []string{"1", "2", "3", "4", "5", "6"}},
		{ID: "b", Scope: []string{"x.go"}}, {ID: "c", Scope: []string{"x.go"}},
		{ID: "d", Scope: []string{"y.go"}}, {ID: "e", Scope: []string{"y.go"}},
	})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{`task "a" has 6`, `tasks "b" and "c"`, `tasks "d" and "e"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error lacks %q: %v", want, err)
		}
	}
}
