package catalog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestStoreRoundTripAndCache(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok, err := s.Get(ctx, "r", "c1"); err != nil || ok {
		t.Fatalf("miss expected: ok=%v err=%v", ok, err)
	}
	p := Pack{Repo: "r", Commit: "c1", Summary: "s", Files: []PackFile{{"a.go", "x"}}, Commands: map[string]string{"test": "go test ./..."}, CreatedAt: time.Now().UTC().Truncate(time.Second)}
	if err := s.Put(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, "r", "c1")
	if err != nil || !ok || got.Summary != "s" || got.Files[0].Path != "a.go" || !got.CreatedAt.Equal(p.CreatedAt) {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, _ := s.Get(ctx, "r", "c2"); ok {
		t.Fatal("different commit must miss")
	}
	if _, ok, _ := s.Get(ctx, "other", "c1"); ok {
		t.Fatal("different repo must miss")
	}
	p.Summary = "s2"
	if err := s.Put(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Get(ctx, "r", "c1"); got.Summary != "s2" {
		t.Fatalf("upsert failed: %+v", got)
	}
}

func TestParse(t *testing.T) {
	good := `{"summary":"S","files":[{"path":"a/b.go","why":"w"}],"conventions":["c"],"commands":{"test":"go test ./..."}}`
	tests := []struct {
		name, reply string
		wantErr     bool
	}{
		{"fenced", "thinking\n```json\n" + good + "\n```\ndone", false},
		{"last fence wins", "```json\n{\"files\":[]}\n```\n```json\n" + good + "\n```", false},
		{"bare object", "here: " + good + " bye", false},
		{"bare last wins", `{"files":[]} then ` + good, false},
		{"no json", "nothing here", true},
		{"empty files", "```json\n{\"files\":[]}\n```", true},
		{"absolute path", "```json\n{\"files\":[{\"path\":\"/etc/passwd\",\"why\":\"\"}]}\n```", true},
		{"dotdot", "```json\n{\"files\":[{\"path\":\"../x\",\"why\":\"\"}]}\n```", true},
		{"unclean", "```json\n{\"files\":[{\"path\":\"a//b\",\"why\":\"\"}]}\n```", true},
		{"non-string command", "```json\n{\"files\":[{\"path\":\"a\",\"why\":\"\"}],\"commands\":{\"test\":5}}\n```", true},
		{"malformed", "```json\n{\"files\": [\n```", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse(tt.reply)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if err == nil && (p.Summary != "S" || p.Commands["test"] == "") {
				t.Fatalf("bad pack %+v", p)
			}
		})
	}
}

func lsRunner(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDeterministic(t *testing.T) {
	ctx := context.Background()
	t.Run("go", func(t *testing.T) {
		d := t.TempDir()
		write(t, d, "go.mod", "module example.com/x\n\ngo 1.22\n")
		write(t, d, "README.md", "# X\n\nX does things.\nSecond line.\n\nOther para.\n")
		if err := os.Mkdir(filepath.Join(d, "internal"), 0o755); err != nil {
			t.Fatal(err)
		}
		p, err := Deterministic(ctx, lsRunner, d)
		if err != nil {
			t.Fatal(err)
		}
		if p.Commands["test_pkg"] != "go test ./{pkg}" || p.Commands["test"] != "go test ./..." {
			t.Fatalf("%+v", p.Commands)
		}
		if !strings.Contains(p.Summary, "X does things. Second line.") || !strings.Contains(p.Summary, "example.com/x") || !strings.Contains(p.Summary, "internal") || strings.Contains(p.Summary, "Other") {
			t.Fatalf("summary %q", p.Summary)
		}
	})
	t.Run("node", func(t *testing.T) {
		d := t.TempDir()
		write(t, d, "package.json", `{"name":"web","scripts":{"build":"vite build","test":"vitest","start":"vite"}}`)
		p, err := Deterministic(ctx, nil, d)
		if err != nil {
			t.Fatal(err)
		}
		if p.Commands["test"] != "npm test" || p.Commands["build"] != "npm run build" || p.Commands["run"] != "npm run start" {
			t.Fatalf("%+v", p.Commands)
		}
	})
	t.Run("makefile", func(t *testing.T) {
		d := t.TempDir()
		write(t, d, "Makefile", ".PHONY: build test\nbuild:\n\tcc\ntest:\n\t./t\nvet:\n\techo\nclean:\n\trm\n")
		p, err := Deterministic(ctx, nil, d)
		if err != nil {
			t.Fatal(err)
		}
		if p.Commands["build"] != "make build" || p.Commands["test"] != "make test" || p.Commands["lint"] != "make vet" || p.Commands["run"] != "" {
			t.Fatalf("%+v", p.Commands)
		}
	})
	t.Run("empty", func(t *testing.T) {
		if _, err := Deterministic(ctx, nil, t.TempDir()); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestMerge(t *testing.T) {
	base := Pack{Repo: "r", Commit: "c", Summary: "base", Files: []PackFile{{"go.mod", "mod"}, {"a.go", "base-why"}},
		Conventions: []string{"x"}, Commands: map[string]string{"test": "go test ./...", "build": "go build ./..."}}
	over := Pack{Summary: "llm", Files: []PackFile{{"a.go", "llm-why"}, {"b.go", "new"}},
		Conventions: []string{"y", "x"}, Commands: map[string]string{"test": "make test"}}
	m := Merge(base, over)
	if m.Summary != "llm" || m.Repo != "r" || m.Commands["test"] != "make test" || m.Commands["build"] != "go build ./..." {
		t.Fatalf("%+v", m)
	}
	if len(m.Files) != 3 || m.Files[0].Why != "llm-why" || m.Files[2].Path != "go.mod" {
		t.Fatalf("files %+v", m.Files)
	}
	if len(m.Conventions) != 2 {
		t.Fatalf("conventions %+v", m.Conventions)
	}
	if base.Commands["test"] != "go test ./..." {
		t.Fatal("base mutated")
	}
	if Merge(Pack{}, Pack{}).Commands == nil {
		t.Fatal("nil commands map")
	}
}

func TestRenderTruncation(t *testing.T) {
	p := Pack{Repo: "r", Commit: "c", Summary: "héllo wörld", Commands: map[string]string{"test": "go test"}}
	for i := 0; i < 50; i++ {
		p.Files = append(p.Files, PackFile{"dir/ü.go", "ü why"})
	}
	full := Render(p, 0)
	if !strings.Contains(full, "### Commands") || !strings.Contains(full, "test: `go test`") {
		t.Fatalf("%s", full)
	}
	for _, n := range []int{5, 10, 100, 333, 500} {
		s := Render(p, n)
		if len(s) > n {
			t.Fatalf("n=%d len=%d", n, len(s))
		}
		if !utf8.ValidString(s) {
			t.Fatalf("invalid utf8 at n=%d", n)
		}
	}
	if !strings.HasSuffix(Render(p, 200), "…(truncated)\n") {
		t.Fatal("missing truncation marker")
	}
}
