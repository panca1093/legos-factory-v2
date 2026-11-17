package usecase

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"legos-factory/backend/internal/model"
)

// repoProfile derives the deterministic checks from the toolchains the repository's manifests reveal.
// Callers resolve it once from the fresh clone: reading it again from
// a live workspace would let the builder rewrite the checks that gate its work.
func repoProfile(dir string) (model.Profile, error) {
	return detectProfile(dir), nil
}

// detectProfile reads the manifests at the repository root and one level of subfolders; subfolder checks carry Dir.
func detectProfile(dir string) model.Profile {
	var p model.Profile
	var langs []string
	detectIn(dir, "", &p, &langs)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if n := e.Name(); e.IsDir() && !strings.HasPrefix(n, ".") && n != "node_modules" && n != "vendor" {
			detectIn(filepath.Join(dir, n), n, &p, &langs)
		}
	}
	p.Language = strings.Join(langs, "+")
	return p
}

// detectIn adds the checks of the toolchains found in dir; sub is "" at the repository root, else its subfolder name.
func detectIn(dir, sub string, p *model.Profile, langs *[]string) {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	addLang := func(l string) {
		if !slices.Contains(*langs, l) {
			*langs = append(*langs, l)
		}
	}
	named := func(n string) string {
		if sub == "" {
			return n
		}
		return sub + " " + n
	}
	if exists("go.mod") {
		addLang("go")
		p.Checks = append(p.Checks,
			model.Check{Name: named("go-test"), Command: "go test ./...", Dir: sub},
			model.Check{Name: named("go-vet"), Command: "go vet ./...", Dir: sub})
	}
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		_ = json.Unmarshal(b, &pkg)
		var checks []model.Check
		if pkg.Scripts["typecheck"] != "" {
			checks = append(checks, model.Check{Name: named("typecheck"), Command: "npm run -s typecheck", Dir: sub})
		}
		if pkg.Scripts["test"] != "" {
			checks = append(checks, model.Check{Name: named("test"), Command: "npm test", Dir: sub})
		}
		if sub == "" || len(checks) > 0 {
			lang := "node"
			if exists("tsconfig.json") {
				lang = "typescript"
			}
			addLang(lang)
			install := "npm install --no-audit --no-fund --no-package-lock"
			if exists("package-lock.json") {
				install = "npm ci --no-audit --no-fund"
			}
			p.Setup = append(p.Setup, model.Check{Name: named("setup"), Command: install, Dir: sub})
			p.Checks = append(p.Checks, checks...)
		}
	}
	if exists("Cargo.toml") && sub == "" {
		addLang("rust")
		p.Checks = append(p.Checks,
			model.Check{Name: "cargo-check", Command: "cargo check"},
			model.Check{Name: "cargo-test", Command: "cargo test"})
	}
}
