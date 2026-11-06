package kb

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed defaults
var defaultsFS embed.FS

var (
	frontOrder = []string{"type", "title", "description", "status"}
	jakarta    = time.FixedZone("WIB", 7*3600)
	now        = time.Now
)

// Clock is the time the logs use: it is now() in Asia/Jakarta.
func Clock() time.Time { return now().In(jakarta) }

// Bundle is the factory's own knowledge bundle: an OKF-format git repo with
// one commit per write.
type Bundle struct {
	root string
	mu   sync.Mutex
	head string
}

func OpenBundle(root string, seed bool) (*Bundle, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	b := &Bundle{root: root}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		if out, err := b.git("init", "-q"); err != nil {
			return nil, fmt.Errorf("git init: %v: %s", err, out)
		}
	}
	b.head = b.revParse()
	if !seed {
		return b, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var added []string
	err := fs.WalkDir(defaultsFS, "defaults", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, "defaults/")
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			return nil
		}
		data, err := defaultsFS.ReadFile(p)
		if err != nil {
			return err
		}
		added = append(added, rel)
		return b.putFile(rel, data)
	})
	if err != nil {
		return nil, err
	}
	if len(added) == 0 {
		return b, nil
	}
	if err := b.logEntry("Seeded default playbooks"); err != nil {
		return nil, err
	}
	if _, err := b.commit("Seed default playbooks"); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Bundle) Root() string { return b.root }

func (b *Bundle) Head() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.head
}

func (b *Bundle) HasDefault(p string) bool {
	_, err := fs.Stat(defaultsFS, "defaults/"+path.Clean(p))
	return err == nil
}

func (b *Bundle) Write(p string, front map[string]any, body, message string) (string, error) {
	rel, err := cleanPath(p)
	if err != nil {
		return "", err
	}
	data, err := render(front, body)
	if err != nil {
		return "", err
	}
	return b.writeRaw(rel, data, message)
}

func (b *Bundle) ResetDefault(p, message string) (string, error) {
	rel, err := cleanPath(p)
	if err != nil {
		return "", err
	}
	data, err := defaultsFS.ReadFile("defaults/" + rel)
	if err != nil {
		return "", fmt.Errorf("no default for %s", rel)
	}
	return b.writeRaw(rel, data, message)
}

func (b *Bundle) Read(p string) (map[string]any, string, error) {
	rel, err := cleanPath(p)
	if err != nil {
		return nil, "", err
	}
	return ReadMarkdown(b.root, rel)
}

// ReadMarkdown reads one markdown file under root, splitting frontmatter from the body.
func ReadMarkdown(root, rel string) (map[string]any, string, error) {
	data, err := readInRoot(root, rel)
	if err != nil {
		return nil, "", err
	}
	return parseMarkdown(data)
}

// readInRoot reads rel under root, refusing paths that escape it, symlinks included.
func readInRoot(root, rel string) ([]byte, error) {
	rel = path.Clean(filepath.ToSlash(rel))
	if rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return nil, fmt.Errorf("path %q escapes the root", rel)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	if r, err := filepath.Rel(realRoot, real); err != nil || r == ".." || strings.HasPrefix(r, "../") {
		return nil, fmt.Errorf("path %q escapes the root", rel)
	}
	return os.ReadFile(real)
}

var commitRef = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// ReadGitMarkdown reads rel as committed at sha in the repository at root.
// Reading from the object store never follows a working-tree symlink.
func ReadGitMarkdown(root, sha, rel string) (map[string]any, string, error) {
	rel = path.Clean(filepath.ToSlash(rel))
	if !commitRef.MatchString(sha) || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return nil, "", fmt.Errorf("invalid commit or path %q", rel)
	}
	out, err := exec.Command("git", "-C", root, "show", sha+":"+rel).Output()
	if err != nil {
		return nil, "", err
	}
	return parseMarkdown(out)
}

// GitPlaybookPaths lists the markdown files at sha whose front matter may
// declare type Playbook, sorted. Callers still check the parsed front matter.
func GitPlaybookPaths(root, sha string) ([]string, error) {
	if !commitRef.MatchString(sha) {
		return nil, fmt.Errorf("invalid commit %q", sha)
	}
	// loose pattern: quoted or tab-separated values still count; callers check the parsed type
	out, err := exec.Command("git", "-C", root, "grep", "-l", "-z", "-I", "-e", "^type:.*Playbook", sha, "--", "*.md").Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for _, entry := range strings.Split(string(out), "\x00") {
		if p := strings.TrimPrefix(entry, sha+":"); p != "" && !skipped(p, false) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func parseMarkdown(data []byte) (map[string]any, string, error) {
	fr, body := splitFront(data)
	front := map[string]any{}
	if len(fr) > 0 {
		if err := yaml.Unmarshal(fr, &front); err != nil {
			return nil, "", err
		}
	}
	return front, strings.TrimPrefix(body, "\n"), nil
}

func (b *Bundle) Remove(p, message string) (string, error) {
	rel, err := cleanPath(p)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := os.Remove(filepath.Join(b.root, filepath.FromSlash(rel))); err != nil {
		return "", err
	}
	dir := path.Dir(rel)
	for dir != "." {
		abs := filepath.Join(b.root, filepath.FromSlash(dir))
		if ents, err := os.ReadDir(abs); err == nil && len(ents) == 1 && ents[0].Name() == "index.md" {
			os.Remove(filepath.Join(abs, "index.md"))
		}
		if os.Remove(abs) != nil {
			break
		}
		dir = path.Dir(dir)
	}
	for d := dir; ; d = path.Dir(d) {
		if err := b.regenIndex(d); err != nil {
			return "", err
		}
		if d == "." {
			break
		}
	}
	if err := b.logEntry(message); err != nil {
		return "", err
	}
	return b.commit(message)
}

func (b *Bundle) writeRaw(rel string, data []byte, message string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.putFile(rel, data); err != nil {
		return "", err
	}
	if err := b.logEntry(message); err != nil {
		return "", err
	}
	return b.commit(message)
}

// File is one document for WriteMany.
type File struct {
	Path  string
	Front map[string]any
	Body  string
}

// WriteMany writes every file and commits once, regenerating each affected index.md once.
func (b *Bundle) WriteMany(files []File, message string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	dirs := map[string]bool{}
	for _, f := range files {
		rel, err := cleanPath(f.Path)
		if err != nil {
			return "", err
		}
		data, err := render(f.Front, f.Body)
		if err != nil {
			return "", err
		}
		if err := b.putRaw(rel, data); err != nil {
			return "", err
		}
		for d := path.Dir(rel); ; d = path.Dir(d) {
			dirs[d] = true
			if d == "." {
				break
			}
		}
	}
	for d := range dirs {
		if err := b.regenIndex(d); err != nil {
			return "", err
		}
	}
	if err := b.logEntry(message); err != nil {
		return "", err
	}
	return b.commit(message)
}

func (b *Bundle) putRaw(rel string, data []byte) error {
	abs := filepath.Join(b.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, data, 0o644)
}

func (b *Bundle) putFile(rel string, data []byte) error {
	if err := b.putRaw(rel, data); err != nil {
		return err
	}
	for d := path.Dir(rel); ; d = path.Dir(d) {
		if err := b.regenIndex(d); err != nil {
			return err
		}
		if d == "." {
			return nil
		}
	}
}

func cleanPath(p string) (string, error) {
	rel := path.Clean(filepath.ToSlash(p))
	switch {
	case rel == "." || path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../"):
		return "", fmt.Errorf("path %q escapes the bundle", p)
	case rel == ".git" || strings.HasPrefix(rel, ".git/"):
		return "", fmt.Errorf("path %q is inside .git", p)
	case !strings.HasSuffix(rel, ".md"):
		return "", fmt.Errorf("path %q is not a markdown file", p)
	}
	if base := path.Base(rel); base == "index.md" || base == "log.md" {
		return "", fmt.Errorf("%s is generated", base)
	}
	return rel, nil
}

func render(front map[string]any, body string) ([]byte, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	add := func(k string) error {
		v, ok := front[k]
		if !ok {
			return nil
		}
		var vn yaml.Node
		if err := vn.Encode(v); err != nil {
			return err
		}
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &vn)
		return nil
	}
	rest := make([]string, 0, len(front))
	for k := range front {
		if !contains(frontOrder, k) {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range append(append([]string{}, frontOrder...), rest...) {
		if err := add(k); err != nil {
			return nil, err
		}
	}
	y, err := yaml.Marshal(node)
	if err != nil {
		return nil, err
	}
	if len(node.Content) == 0 {
		y = nil
	}
	return []byte("---\n" + string(y) + "---\n\n" + strings.TrimLeft(body, "\n")), nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func (b *Bundle) regenIndex(dir string) error {
	abs := filepath.Join(b.root, filepath.FromSlash(dir))
	ents, err := os.ReadDir(abs)
	if err != nil {
		return err
	}
	heading := path.Base(dir)
	if dir == "." {
		heading = "Knowledge"
	}
	var sb strings.Builder
	if dir == "." {
		sb.WriteString("---\nokf_version: \"0.1\"\n---\n\n")
	}
	sb.WriteString("# " + heading + "\n\n")
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() {
			if name != ".git" {
				fmt.Fprintf(&sb, "* [%s/](%s/) - %d documents\n", name, name, countMD(filepath.Join(abs, name)))
			}
			continue
		}
		if !strings.HasSuffix(name, ".md") || name == "index.md" || name == "log.md" {
			continue
		}
		title, desc := strings.TrimSuffix(name, ".md"), ""
		if data, err := os.ReadFile(filepath.Join(abs, name)); err == nil {
			if p, err := Parse("factory", "", name, data, nil); err == nil {
				title, desc = p.Title, p.Description
			}
		}
		fmt.Fprintf(&sb, "* [%s](%s) - %s\n", title, name, desc)
	}
	return os.WriteFile(filepath.Join(abs, "index.md"), []byte(sb.String()), 0o644)
}

func countMD(dir string) int {
	n := 0
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") && d.Name() != "index.md" && d.Name() != "log.md" {
			n++
		}
		return nil
	})
	return n
}

func (b *Bundle) logEntry(message string) error {
	return b.appendLog("log.md", "* **Update**: "+strings.Join(strings.Fields(message), " "))
}

var projectLog = regexp.MustCompile(`^projects/[A-Za-z0-9_-]+/log\.md$`)

// Append adds line to a project log (projects/<pid>/log.md) and commits it, leaving the root log that Write feeds alone.
func (b *Bundle) Append(rel, line, message string) (string, error) {
	rel = path.Clean(filepath.ToSlash(rel))
	if !projectLog.MatchString(rel) {
		return "", fmt.Errorf("path %q is not a project log", rel)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.appendLog(rel, strings.Join(strings.Fields(line), " ")); err != nil {
		return "", err
	}
	return b.commit(message)
}

func (b *Bundle) appendLog(rel, entry string) error {
	file := filepath.Join(b.root, filepath.FromSlash(rel))
	old, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := strings.TrimPrefix(string(old), "# Log\n")
	text = strings.TrimLeft(text, "\n")
	day := "## " + Clock().Format("2006-01-02")
	entry += "\n"
	if strings.HasPrefix(text, day+"\n") {
		text = day + "\n\n" + entry + strings.TrimPrefix(strings.TrimPrefix(text, day+"\n"), "\n")
	} else {
		text = day + "\n\n" + entry + "\n" + text
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, []byte("# Log\n\n"+strings.TrimRight(text, "\n")+"\n"), 0o644)
}

func (b *Bundle) commit(message string) (string, error) {
	if out, err := b.git("add", "-A"); err != nil {
		return "", fmt.Errorf("git add: %v: %s", err, out)
	}
	if out, err := b.git("-c", "user.name=legos-factory", "-c", "user.email=factory@legos.local", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "-c", "gc.autoDetach=false", "commit", "-q", "-m", message); err != nil {
		return "", fmt.Errorf("git commit: %v: %s", err, out)
	}
	b.head = b.revParse()
	return b.head, nil
}

func (b *Bundle) revParse() string {
	out, err := b.git("rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (b *Bundle) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = b.root
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
