package usecase

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"legos-factory/backend/internal/model"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRepoProfile(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  model.Profile
	}{
		{"go module", map[string]string{"go.mod": "module x"}, model.Profile{
			Language: "go",
			Checks:   []model.Check{{Name: "go-test", Command: "go test ./..."}, {Name: "go-vet", Command: "go vet ./..."}},
		}},
		{"npm runs only the scripts that exist", map[string]string{
			"package.json":      `{"scripts":{"typecheck":"tsc --noEmit","test":"vitest","dev":"vite"}}`,
			"package-lock.json": "{}",
			"tsconfig.json":     "{}",
		}, model.Profile{
			Language: "typescript",
			Setup:    "npm ci --no-audit --no-fund",
			Checks:   []model.Check{{Name: "typecheck", Command: "npm run -s typecheck"}, {Name: "test", Command: "npm test"}},
		}},
		{"npm without a lockfile installs", map[string]string{
			"package.json": `{"scripts":{"test":"node --test"}}`,
		}, model.Profile{
			Language: "node",
			Setup:    "npm install --no-audit --no-fund",
			Checks:   []model.Check{{Name: "test", Command: "npm test"}},
		}},
		{"monorepo runs every detected toolchain", map[string]string{
			"go.mod": "module x", "Cargo.toml": "[package]",
		}, model.Profile{
			Language: "go+rust",
			Checks: []model.Check{
				{Name: "go-test", Command: "go test ./..."}, {Name: "go-vet", Command: "go vet ./..."},
				{Name: "cargo-check", Command: "cargo check"}, {Name: "cargo-test", Command: "cargo test"},
			},
		}},
		{"repo override wins over detection", map[string]string{
			"go.mod":             "module x",
			".legos/checks.json": `{"setup":"make deps","checks":[{"name":"ci","command":"make ci"}]}`,
		}, model.Profile{
			Language: "go", Setup: "make deps",
			Checks: []model.Check{{Name: "ci", Command: "make ci"}},
		}},
		{"nothing detected", map[string]string{"README.md": "hi"}, model.Profile{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := repoProfile(writeFiles(t, c.files))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestRepoProfileRejectsInvalidOverride(t *testing.T) {
	if _, err := repoProfile(writeFiles(t, map[string]string{".legos/checks.json": "{"})); err == nil {
		t.Fatal("an unreadable override must not silently fall back to detection")
	}
}
