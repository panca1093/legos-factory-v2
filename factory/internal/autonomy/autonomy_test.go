package autonomy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const base = "package a\n\n// F does x.\nfunc F(a int) int {\n\tb := a + 1\n\treturn b\n}\n"

func mod(p, o, n string) FileChange {
	return FileChange{Path: p, Status: "modified", Old: []byte(o), New: []byte(n)}
}

func TestDiffLevel(t *testing.T) {
	prot := []string{"migrations/**", "deploy", "*.sql", "infra/prod.yaml"}
	tests := []struct {
		name string
		in   []FileChange
		want int
	}{
		{"docs only", []FileChange{mod("README.md", "a", "b"), {Path: "docs/x.rst", Status: "added"}}, 3},
		{"docs dir non-doc ext", []FileChange{{Path: "docs/x.png", Status: "added"}}, 2},
		{"txt is not doc", []FileChange{mod("notes.txt", "a", "b")}, 2},
		{"embedded doc", []FileChange{{Path: "tmpl/a.md", Status: "modified", Embedded: true}}, 2},
		{"deleted test file", []FileChange{{Path: "a_test.go", Status: "deleted", Old: []byte("package a")}}, 2},
		{"testdata edit", []FileChange{mod("pkg/testdata/golden.json", "a", "b")}, 2},
		{"testdata go test file", []FileChange{mod("testdata/x_test.go", "a", "b")}, 2},
		{"removed go:build ignore", []FileChange{mod("a.go", "//go:build ignore\n\npackage a\n", "package a\n")}, 2},
		{"go:embed change", []FileChange{mod("a.go", "package a\nimport _ \"embed\"\n//go:embed x.txt\nvar X string\n", "package a\nimport _ \"embed\"\n//go:embed y.txt\nvar X string\n")}, 2},
		{"go:linkname change", []FileChange{mod("a.go", "package a\nimport _ \"unsafe\"\n//go:linkname f runtime.a\nfunc f()\n", "package a\nimport _ \"unsafe\"\n//go:linkname f runtime.b\nfunc f()\n")}, 2},
		{"cgo preamble edit", []FileChange{mod("a.go", "package a\n/*\nint f(){return 1;}\n*/\nimport \"C\"\n", "package a\n/*\nint f(){return 2;}\n*/\nimport \"C\"\n")}, 2},
		{"cgo comment-only still 2", []FileChange{mod("a.go", "package a\nimport \"C\"\n", "package a\n\n// x\nimport \"C\"\n")}, 2},
		{"go:noinline unchanged", []FileChange{mod("a.go", "package a\n//go:noinline\nfunc f() {}\n", "package a\n\n//go:noinline\nfunc f() {}\n")}, 3},
		{"line directive change", []FileChange{mod("a.go", "package a\n//line x.go:1\nfunc f() {}\n", "package a\n//line y.go:1\nfunc f() {}\n")}, 2},

		{"tests only", []FileChange{mod("a_test.go", "x", "y"), {Path: "b_test.go", Status: "added", New: []byte("package a")}}, 3},
		{"gofmt only", []FileChange{mod("a.go", base, "package a\nfunc F(a int) int {\nb:=a+1\nreturn b}\n")}, 3},
		{"comment only", []FileChange{mod("a.go", base, "package a\n\n// changed\nfunc F(a int) int {\n\tb := a + 1 // hi\n\treturn b\n}\n")}, 3},
		{"renamed var", []FileChange{mod("a.go", base, "package a\n\nfunc F(a int) int {\n\tc := a + 1\n\treturn c\n}\n")}, 2},
		{"new func", []FileChange{mod("a.go", base, base+"func G() {}\n")}, 2},
		{"mix docs tests ast-equal", []FileChange{mod("README.md", "a", "b"), mod("a_test.go", "x", "y"), mod("a.go", base, "package a\n\nfunc F(a int) int {\n\tb := a + 1\n\treturn b\n}\n")}, 3},
		{"docs + logic", []FileChange{mod("README.md", "a", "b"), mod("a.go", base, base+"var X = 1\n")}, 2},
		{"unparseable", []FileChange{mod("a.go", base, "package a\nfunc {")}, 2},
		{"added non-test go", []FileChange{{Path: "n.go", Status: "added", New: []byte("package a")}}, 2},
		{"deleted go", []FileChange{{Path: "n.go", Status: "deleted", Old: []byte("package a")}}, 2},
		{"renamed go", []FileChange{{Path: "b.go", OldPath: "a.go", Status: "renamed", Old: []byte(base), New: []byte(base)}}, 2},
		{"renamed doc", []FileChange{{Path: "docs/b.md", OldPath: "a.md", Status: "renamed"}}, 3},
		{"protected glob", []FileChange{mod("x.sql", "a", "b")}, 0},
		{"protected dir/**", []FileChange{mod("migrations/001/up.go", "a", "b")}, 0},
		{"protected dir prefix", []FileChange{mod("deploy/k8s/a.yaml", "a", "b")}, 0},
		{"protected exact", []FileChange{mod("infra/prod.yaml", "a", "b")}, 0},
		{"protected via oldpath", []FileChange{{Path: "docs/m.md", OldPath: "deploy/m.md", Status: "renamed"}}, 0},
		{"protected beats docs", []FileChange{mod("migrations/README.md", "a", "b")}, 0},
		{"sibling prefix not protected", []FileChange{mod("deployment/README.md", "a", "b")}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reasons := DiffLevel(tt.in, prot)
			if got != tt.want {
				t.Fatalf("level = %d, want %d (%v)", got, tt.want, reasons)
			}
			if len(reasons) == 0 {
				t.Fatal("no reasons")
			}
		})
	}
}

func TestProtectedReason(t *testing.T) {
	_, r := DiffLevel([]FileChange{mod("migrations/001.sql", "a", "b")}, []string{"migrations/**"})
	if len(r) != 1 || r[0] != "protected path: migrations/001.sql" {
		t.Fatalf("reasons = %v", r)
	}
}

func TestEffectiveAndInspector(t *testing.T) {
	if got := Effective(3, InspectorLevel(true), 3); got != 2 {
		t.Fatalf("inspector should lower 3 to 2, got %d", got)
	}
	if got := Effective(2, InspectorLevel(false), 3); got != 2 {
		t.Fatalf("inspector must not raise 2, got %d", got)
	}
	if got := Effective(3, 3, 1); got != 1 {
		t.Fatalf("global cap, got %d", got)
	}
}

func TestDecision(t *testing.T) {
	cases := map[int]Approval{
		3: {AutoMerge: true},
		2: {HumanApprovesPR: true},
		1: {HumanApprovesPR: true, HumanApprovesPlan: true},
		0: {HumanApprovesPR: true, HumanApprovesPlan: true},
	}
	for l, want := range cases {
		if got := Decision(l); got != want {
			t.Errorf("Decision(%d) = %+v, want %+v", l, got, want)
		}
	}
}

func gitRunner(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func TestCollect(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	g := func(args ...string) {
		t.Helper()
		full := append([]string{"git", "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)
		if _, err := gitRunner(ctx, dir, full...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	write := func(p, s string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g("init", "-q", "-b", "main")
	write("a.go", base)
	write("docs/old.md", "# doc\nsome long enough content for rename detection\nline3\nline4\n")
	write("gone.txt", "bye")
	g("add", "-A")
	g("commit", "-qm", "base")
	g("checkout", "-qb", "feat")
	write("a.go", base+"func G() {}\n")
	write("new.go", "package a\n")
	g("rm", "-q", "gone.txt")
	g("mv", "docs/old.md", "docs/new.md")
	g("add", "-A")
	g("commit", "-qm", "work")

	got, err := Collect(ctx, dir, "main", gitRunner)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("changes = %+v", got)
	}
	by := map[string]FileChange{}
	for _, c := range got {
		by[c.Path] = c
	}
	if c := by["a.go"]; c.Status != "modified" || string(c.Old) != base || string(c.New) != base+"func G() {}\n" {
		t.Errorf("a.go = %+v", c)
	}
	if c := by["new.go"]; c.Status != "added" || c.Old != nil || string(c.New) != "package a\n" {
		t.Errorf("new.go = %+v", c)
	}
	if c := by["gone.txt"]; c.Status != "deleted" || string(c.Old) != "bye" || c.New != nil {
		t.Errorf("gone.txt = %+v", c)
	}
	if c := by["docs/new.md"]; c.Status != "renamed" || c.OldPath != "docs/old.md" || c.Old == nil || c.New == nil {
		t.Errorf("rename = %+v", c)
	}
	if l, _ := DiffLevel(got, nil); l != 2 {
		t.Errorf("level = %d, want 2", l)
	}
}

func TestCollectMarksEmbeddedDocs(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	g := func(args ...string) {
		t.Helper()
		full := append([]string{"git", "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)
		if _, err := gitRunner(ctx, dir, full...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	write := func(p, s string) {
		t.Helper()
		full := filepath.Join(dir, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g("init", "-q", "-b", "main")
	write("tmpl/t.go", "package tmpl\nimport _ \"embed\"\n//go:embed sub/*.md\nvar F string\n")
	write("tmpl/sub/a.md", "a")
	write("other/b.md", "b")
	g("add", "-A")
	g("commit", "-qm", "base")
	g("checkout", "-qb", "feat")
	write("tmpl/sub/a.md", "a2")
	write("other/b.md", "b2")
	g("add", "-A")
	g("commit", "-qm", "work")
	got, err := Collect(ctx, dir, "main", gitRunner)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if want := c.Path == "tmpl/sub/a.md"; c.Embedded != want {
			t.Errorf("%s embedded = %v", c.Path, c.Embedded)
		}
	}
	if l, _ := DiffLevel(got, nil); l != 2 {
		t.Errorf("level = %d, want 2", l)
	}
	var plain []FileChange
	for _, c := range got {
		if !c.Embedded {
			plain = append(plain, c)
		}
	}
	if l, _ := DiffLevel(plain, nil); l != 3 {
		t.Errorf("non-embedded doc level = %d, want 3", l)
	}
}
