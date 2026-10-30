package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type PackFile struct {
	Path string `json:"path"`
	Why  string `json:"why"`
}

type Pack struct {
	Repo        string            `json:"repo"`
	Commit      string            `json:"commit"`
	Summary     string            `json:"summary"`
	Files       []PackFile        `json:"files"`
	Conventions []string          `json:"conventions"`
	Commands    map[string]string `json:"commands"` // build, test, test_pkg (with {pkg}), lint, run
	CreatedAt   time.Time         `json:"created_at"`
}

// The pack is repo-level: keyed by (repo, commit) and reused across objectives, so
// objective_key is "" for it; the column leaves room for per-objective packs later.
type Store struct{ db *sql.DB }

func Open(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS packs(
		repo TEXT NOT NULL, commit_sha TEXT NOT NULL, objective_key TEXT NOT NULL DEFAULT '',
		pack_json TEXT NOT NULL, created_at TEXT NOT NULL,
		PRIMARY KEY(repo, commit_sha, objective_key))`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Get returns (zero, false, nil) on a cache miss.
func (s *Store) Get(ctx context.Context, repo, commit string) (Pack, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT pack_json FROM packs WHERE repo=? AND commit_sha=? AND objective_key=''`, repo, commit).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Pack{}, false, nil
	}
	if err != nil {
		return Pack{}, false, err
	}
	var p Pack
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Pack{}, false, err
	}
	return p, true, nil
}

func (s *Store) Put(ctx context.Context, p Pack) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO packs(repo, commit_sha, objective_key, pack_json, created_at) VALUES(?,?,'',?,?)
		 ON CONFLICT(repo, commit_sha, objective_key) DO UPDATE SET pack_json=excluded.pack_json, created_at=excluded.created_at`,
		p.Repo, p.Commit, string(raw), p.CreatedAt.Format(time.RFC3339))
	return err
}

func Prompt(objective, repoTree string) string {
	return fmt.Sprintf(`You are the Cataloger. Explore this repository (read-only, at the base commit) and produce a context pack for the agents who will plan and build the objective below. Do not modify anything.

Objective:
%s

Repository tree:
%s

Output exactly ONE JSON object in a final `+"```json"+` fenced block, with this shape:
{
  "summary": "2-4 sentences: what the repo is, main languages, layout",
  "files": [{"path": "relative/path", "why": "why it matters"}],
  "conventions": ["short, concrete rules: naming, error handling, test style, layering"],
  "commands": {"build": "...", "test": "...", "test_pkg": "command with {pkg} placeholder", "lint": "...", "run": "..."}
}

Rules:
- files: 5-25 repo-relative paths, most relevant to the objective first; no absolute paths, no "..".
- summary, conventions and commands stay general to the repo, not tied to this objective.
- commands must include how to run tests for a single package or directory via test_pkg; omit keys that do not apply.
- Prefer targeted reading over exhaustive exploration; do not run package installs or builds that change tracked files.
`, objective, repoTree)
}

var fenceRe = regexp.MustCompile("(?s)```(?:json)?[ \t]*\n(.*?)```")

func Parse(reply string) (Pack, error) {
	var cand string
	for _, m := range fenceRe.FindAllStringSubmatch(reply, -1) {
		if s := strings.TrimSpace(m[1]); strings.HasPrefix(s, "{") {
			cand = s
		}
	}
	if cand == "" {
		cand = lastObject(reply)
	}
	if cand == "" {
		return Pack{}, errors.New("catalog: no JSON object in reply")
	}
	var p Pack
	if err := json.Unmarshal([]byte(cand), &p); err != nil {
		return Pack{}, fmt.Errorf("catalog: invalid JSON: %w", err)
	}
	if err := p.validate(); err != nil {
		return Pack{}, err
	}
	return p, nil
}

// lastObject returns the last balanced top-level {...} in s that is valid JSON.
func lastObject(s string) string {
	last := ""
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == nil {
			last = string(raw)
			i += len(raw) - 1
		}
	}
	return last
}

func (p *Pack) validate() error {
	if len(p.Files) == 0 {
		return errors.New("catalog: files must be non-empty")
	}
	for i, f := range p.Files {
		c := path.Clean(f.Path)
		if f.Path == "" || path.IsAbs(c) || c == ".." || strings.HasPrefix(c, "../") || c != f.Path {
			return fmt.Errorf("catalog: file %d path %q must be relative and clean", i, f.Path)
		}
	}
	for k, v := range p.Commands {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("catalog: command %q is empty", k)
		}
	}
	return nil
}

// Merge returns base with over applied: non-empty over fields win; files and conventions are unioned, over first.
func Merge(base, over Pack) Pack {
	out := base
	if over.Repo != "" {
		out.Repo = over.Repo
	}
	if over.Commit != "" {
		out.Commit = over.Commit
	}
	if over.Summary != "" {
		out.Summary = over.Summary
	}
	if !over.CreatedAt.IsZero() {
		out.CreatedAt = over.CreatedAt
	}
	out.Files = nil
	seen := map[string]bool{}
	for _, f := range append(append([]PackFile{}, over.Files...), base.Files...) {
		if !seen[f.Path] {
			seen[f.Path] = true
			out.Files = append(out.Files, f)
		}
	}
	out.Conventions = nil
	seenC := map[string]bool{}
	for _, c := range append(append([]string{}, over.Conventions...), base.Conventions...) {
		if !seenC[c] {
			seenC[c] = true
			out.Conventions = append(out.Conventions, c)
		}
	}
	out.Commands = map[string]string{}
	for k, v := range base.Commands {
		out.Commands[k] = v
	}
	for k, v := range over.Commands {
		if v != "" {
			out.Commands[k] = v
		}
	}
	return out
}

var targetRe = regexp.MustCompile(`(?m)^([A-Za-z0-9_.-]+):`)

// Deterministic builds a pack from manifest files in dir; run (optional) lists top-level dirs via ls.
func Deterministic(ctx context.Context, run func(ctx context.Context, dir string, args ...string) (string, error), dir string) (Pack, error) {
	p := Pack{Commands: map[string]string{}, CreatedAt: time.Now().UTC()}
	has := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(dir, name)); return string(b) }
	setDefault := func(k, v string) {
		if _, ok := p.Commands[k]; !ok {
			p.Commands[k] = v
		}
	}
	var layout []string

	if has("go.mod") {
		mod := ""
		for _, l := range strings.Split(read("go.mod"), "\n") {
			if strings.HasPrefix(l, "module ") {
				mod = strings.TrimSpace(strings.TrimPrefix(l, "module "))
				break
			}
		}
		layout = append(layout, "Go module "+mod)
		p.Files = append(p.Files, PackFile{"go.mod", "Go module definition"})
		p.Commands["build"] = "go build ./..."
		p.Commands["test"] = "go test ./..."
		p.Commands["test_pkg"] = "go test ./{pkg}"
		p.Commands["lint"] = "go vet ./..."
	}
	if has("package.json") {
		var pj struct {
			Name    string            `json:"name"`
			Scripts map[string]string `json:"scripts"`
		}
		_ = json.Unmarshal([]byte(read("package.json")), &pj)
		layout = append(layout, "Node package "+pj.Name)
		p.Files = append(p.Files, PackFile{"package.json", "Node scripts and dependencies"})
		for _, k := range []string{"build", "test", "lint", "start"} {
			if _, ok := pj.Scripts[k]; !ok {
				continue
			}
			switch k {
			case "test":
				setDefault("test", "npm test")
			case "start":
				setDefault("run", "npm run start")
			default:
				setDefault(k, "npm run "+k)
			}
		}
	}
	if has("Makefile") {
		p.Files = append(p.Files, PackFile{"Makefile", "Build and test targets"})
		targets := map[string]bool{}
		for _, m := range targetRe.FindAllStringSubmatch(read("Makefile"), -1) {
			targets[m[1]] = true
		}
		for _, c := range []struct{ key, target string }{
			{"build", "build"}, {"test", "test"}, {"test", "check"}, {"lint", "lint"}, {"lint", "vet"}, {"run", "run"},
		} {
			if targets[c.target] {
				setDefault(c.key, "make "+c.target)
			}
		}
	}
	for _, n := range []string{"README.md", "README"} {
		if has(n) {
			p.Files = append(p.Files, PackFile{n, "Project overview"})
			p.Summary = firstParagraph(read(n))
			break
		}
	}
	if run != nil {
		if out, err := run(ctx, dir, "ls", "-1p"); err == nil {
			var dirs []string
			for _, l := range strings.Split(out, "\n") {
				l = strings.TrimSpace(l)
				if strings.HasSuffix(l, "/") && !strings.HasPrefix(l, ".") {
					dirs = append(dirs, strings.TrimSuffix(l, "/"))
				}
			}
			sort.Strings(dirs)
			if len(dirs) > 0 {
				layout = append(layout, "top-level dirs: "+strings.Join(dirs, ", "))
			}
		}
	}
	if len(p.Files) == 0 {
		return Pack{}, errors.New("catalog: no recognizable manifest in " + dir)
	}
	if len(layout) > 0 {
		l := strings.Join(layout, "; ")
		if p.Summary == "" {
			p.Summary = l
		} else {
			p.Summary += " (" + l + ")"
		}
	}
	return p, nil
}

func firstParagraph(md string) string {
	var para []string
	for _, l := range strings.Split(md, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "![") || strings.HasPrefix(t, "[!") {
			if len(para) > 0 {
				break
			}
			continue
		}
		para = append(para, t)
	}
	s := strings.Join(para, " ")
	if len(s) > 400 {
		s = strings.ToValidUTF8(s[:400], "")
	}
	return s
}

// Render emits compact markdown, cut at a line boundary within maxBytes (0 = unlimited).
func Render(p Pack, maxBytes int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Context pack: %s @ %s\n\n%s\n", p.Repo, p.Commit, p.Summary)
	if len(p.Commands) > 0 {
		b.WriteString("\n### Commands\n")
		keys := make([]string, 0, len(p.Commands))
		for k := range p.Commands {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "- %s: `%s`\n", k, p.Commands[k])
		}
	}
	if len(p.Conventions) > 0 {
		b.WriteString("\n### Conventions\n")
		for _, c := range p.Conventions {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	if len(p.Files) > 0 {
		b.WriteString("\n### Files\n")
		for _, f := range p.Files {
			fmt.Fprintf(&b, "- `%s` — %s\n", f.Path, f.Why)
		}
	}
	s := b.String()
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	const mark = "…(truncated)\n"
	if maxBytes <= len(mark) {
		return ""
	}
	cut := maxBytes - len(mark)
	if i := strings.LastIndex(s[:cut], "\n"); i >= 0 {
		cut = i + 1
	} else {
		cut = len(strings.ToValidUTF8(s[:cut], ""))
	}
	return s[:cut] + mark
}
