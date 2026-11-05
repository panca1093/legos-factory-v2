package usecase

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"legos-factory/backend/internal/model"
)

// overrideFile lets a repository declare its own verification commands.
const overrideFile = ".legos/checks.json"

// repoProfile derives the deterministic checks from the repository itself:
// its override file when present, otherwise the toolchains its manifests
// reveal. Agents never choose the checks that gate their own work.
func repoProfile(dir string) (model.Profile, error) {
	detected := detectProfile(dir)
	b, err := os.ReadFile(filepath.Join(dir, overrideFile))
	if os.IsNotExist(err) {
		return detected, nil
	}
	if err != nil {
		return model.Profile{}, err
	}
	var p model.Profile
	if err := json.Unmarshal(b, &p); err != nil {
		return model.Profile{}, fmt.Errorf("invalid %s: %w", overrideFile, err)
	}
	if p.Language == "" {
		p.Language = detected.Language
	}
	return p, nil
}

func detectProfile(dir string) model.Profile {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	var p model.Profile
	var langs []string
	if exists("go.mod") {
		langs = append(langs, "go")
		p.Checks = append(p.Checks,
			model.Check{Name: "go-test", Command: "go test ./..."},
			model.Check{Name: "go-vet", Command: "go vet ./..."})
	}
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		lang := "node"
		if exists("tsconfig.json") {
			lang = "typescript"
		}
		langs = append(langs, lang)
		p.Setup = "npm install --no-audit --no-fund"
		if exists("package-lock.json") {
			p.Setup = "npm ci --no-audit --no-fund"
		}
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		_ = json.Unmarshal(b, &pkg)
		if pkg.Scripts["typecheck"] != "" {
			p.Checks = append(p.Checks, model.Check{Name: "typecheck", Command: "npm run -s typecheck"})
		}
		if pkg.Scripts["test"] != "" {
			p.Checks = append(p.Checks, model.Check{Name: "test", Command: "npm test"})
		}
	}
	if exists("Cargo.toml") {
		langs = append(langs, "rust")
		p.Checks = append(p.Checks,
			model.Check{Name: "cargo-check", Command: "cargo check"},
			model.Check{Name: "cargo-test", Command: "cargo test"})
	}
	p.Language = strings.Join(langs, "+")
	return p
}
