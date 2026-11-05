package usecase

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Repo facts are built by code only, so the Foreman and Architect read them
// instead of rediscovering them (and never take them from an LLM).

const (
	factsMaxTree   = 200
	factsTreeDepth = 2
)

var factsSkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true}

var factsTools = []struct{ name, flag string }{
	{"go", "version"}, {"node", "--version"}, {"npm", "--version"}, {"cargo", "--version"}, {"git", "--version"},
}

func repoFacts(ctx context.Context, dir string) string {
	var b strings.Builder
	b.WriteString("Repository facts (detected by code):\n")
	p, err := repoProfile(dir)
	if err != nil {
		fmt.Fprintf(&b, "Toolchains and checks: unavailable: %v\n", err)
	} else {
		lang := p.Language
		if lang == "" {
			lang = "none detected"
		}
		fmt.Fprintf(&b, "Toolchains: %s\n", lang)
		for _, c := range p.Checks {
			fmt.Fprintf(&b, "Check %s: %s\n", c.Name, c.Command)
		}
	}
	b.WriteString("Directory tree (depth 2):\n")
	b.WriteString(dirTree(dir))
	var tools []string
	for _, t := range factsTools {
		if v := toolVersion(ctx, t.name, t.flag); v != "" {
			tools = append(tools, v)
		}
	}
	if len(tools) > 0 {
		b.WriteString("Host tools: " + strings.Join(tools, "; ") + "\n")
	}
	return b.String()
}

func dirTree(dir string) string {
	var lines []string
	truncated := false
	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		entries, err := os.ReadDir(filepath.Join(dir, rel))
		if err != nil {
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if truncated {
				return
			}
			if e.IsDir() && factsSkipDirs[e.Name()] {
				continue
			}
			if len(lines) >= factsMaxTree {
				truncated = true
				return
			}
			path := filepath.Join(rel, e.Name())
			if e.IsDir() {
				lines = append(lines, "  "+path+"/")
				if depth < factsTreeDepth {
					walk(path, depth+1)
				}
			} else {
				lines = append(lines, "  "+path)
			}
		}
	}
	walk("", 1)
	if len(lines) == 0 {
		return "  (empty)\n"
	}
	out := strings.Join(lines, "\n") + "\n"
	if truncated {
		out += fmt.Sprintf("  … listing capped at %d entries\n", factsMaxTree)
	}
	return out
}

func toolVersion(ctx context.Context, name, flag string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, path, flag).Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if !strings.Contains(line, name) {
		line = name + " " + line
	}
	return line
}
