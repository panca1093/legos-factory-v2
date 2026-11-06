package http

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestKBScopeGlobValidationCaps(t *testing.T) {
	r := bootKB(t)
	put := func(body string) int {
		code, _ := r.do(t, "eng", "PUT", "/api/v1/projects/p1/kb-scope", body)
		return code
	}
	var many []string
	for i := 0; i < 51; i++ {
		many = append(many, fmt.Sprintf(`"g%d"`, i))
	}
	cases := map[string]int{
		`{"kb_exclude":["` + strings.Repeat("a", 200) + `"]}`:   200,
		`{"kb_exclude":["` + strings.Repeat("a", 201) + `"]}`:   400,
		`{"kb_exclude":[` + strings.Join(many, ",") + `]}`:      400,
		`{"kb_exclude":[` + strings.Join(many[:50], ",") + `]}`: 200,
		`{"kb_exclude":["notes..md"]}`:                          200,
		`{"kb_exclude":["a/../b"]}`:                             400,
		`{"kb_exclude":[".."]}`:                                 400,
	}
	for body, want := range cases {
		if got := put(body); got != want {
			t.Errorf("%.60s: got %d want %d", body, got, want)
		}
	}
}

func TestKBScopeOverFetchesPastTheListLimit(t *testing.T) {
	r := bootKB(t)
	ctx := context.Background()
	gen := filepath.Join(r.repo, "docs", "gen")
	if err := os.MkdirAll(gen, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 600; i++ {
		if err := os.WriteFile(filepath.Join(gen, fmt.Sprintf("n%03d.md", i)), []byte(fmt.Sprintf("# Gen %d\n\nbulk\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "gen"}} {
		if out, err := exec.Command("git", append([]string{"-C", r.repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if _, _, _, err := r.know.SyncRepo(ctx, "p1", r.repo, true); err != nil {
		t.Fatal(err)
	}
	_, body := r.do(t, "eng", "GET", "/api/v1/kb/docs?project_id=p1", "")
	if ids := docIDs(t, body); has(ids, repoID) {
		t.Fatal("fixture must push money.md past the unscoped limit")
	}
	r.do(t, "eng", "PUT", "/api/v1/projects/p1/kb-scope", `{"kb_exclude":["./docs/gen/"]}`)
	_, body = r.do(t, "eng", "GET", "/api/v1/kb/docs?project_id=p1", "")
	if ids := docIDs(t, body); !has(ids, repoID) || len(ids) > 10 {
		t.Fatalf("excluded docs must not take slots from valid ones: %d ids, money=%v", len(ids), has(ids, repoID))
	}
}
