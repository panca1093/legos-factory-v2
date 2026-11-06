package kb

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Violation struct {
	Gate    string `json:"gate"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

var (
	logHeading   = regexp.MustCompile(`^## \d{4}-\d{2}-\d{2}\s*$`)
	lintWiki     = regexp.MustCompile(`\[\[[^\]]+\]\]`)
	lintInline   = regexp.MustCompile(`\[[^\]\n]*\]\(([^)\n]*)\)`)
	lintRefLink  = regexp.MustCompile(`(?m)^\[[^\]\n]*\]:\s*(\S+)`)
	lintAutoLink = regexp.MustCompile(`<([^>\n]+)>`)
	externalRe   = regexp.MustCompile(`^(https?://|mailto:|tel:|#)`)
)

func localPath(l string) bool {
	return strings.Contains(l, "file:///") || strings.HasPrefix(l, "/Users/") || strings.HasPrefix(l, "/home/")
}

// Lint checks the bundle at root against the OKF v0.1 gates [A]-[G] of loan-platform's wiki-lint.
func Lint(root string) ([]Violation, error) {
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("bundle root %q is not a readable directory", root)
	}
	var vs []Violation
	add := func(gate, rel, format string, a ...any) {
		vs = append(vs, Violation{gate, rel, fmt.Sprintf(format, a...)})
	}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".md") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(p)
		if err != nil {
			add("A", rel, "unreadable: %v", err)
			return nil
		}
		fr, body := splitFront(data)
		hasFront := fr != nil
		var meta map[string]any
		var yamlErr error
		if hasFront {
			yamlErr = yaml.Unmarshal(fr, &meta)
		}
		switch base := path.Base(rel); {
		case rel == "index.md":
			if yamlErr != nil || !hasFront || len(meta) != 1 || meta["okf_version"] == nil {
				add("B", rel, "frontmatter must be exactly okf_version")
			}
		case base == "index.md":
			if hasFront {
				add("C", rel, "sub-index must not have frontmatter")
			}
		case base == "log.md":
			for _, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(line, "## ") && !logHeading.MatchString(line) {
					add("D", rel, "non-ISO heading %q, want ## YYYY-MM-DD", line)
				}
			}
		default:
			// stricter than the reference script, which accepts a bare null type
			typ, _ := meta["type"].(string)
			switch {
			case !hasFront:
				add("A", rel, "missing frontmatter")
			case yamlErr != nil:
				add("A", rel, "unparseable frontmatter: %v", yamlErr)
			case strings.TrimSpace(typ) == "":
				add("A", rel, "missing or empty type")
			}
		}
		text := string(data)
		if n := len(lintWiki.FindAllString(text, -1)); n > 0 {
			add("E", rel, "%d [[wikilink]] left", n)
		}
		var links []string
		for _, m := range lintInline.FindAllStringSubmatch(text, -1) {
			links = append(links, strings.TrimSpace(m[1]))
		}
		for _, m := range lintRefLink.FindAllStringSubmatch(text, -1) {
			links = append(links, strings.TrimSpace(m[1]))
		}
		for _, m := range lintAutoLink.FindAllStringSubmatch(text, -1) {
			if localPath(m[1]) {
				add("F", rel, "absolute or local link %s", m[1])
			}
		}
		for _, l := range links {
			switch {
			case l == "" || externalRe.MatchString(l):
			case localPath(l):
				add("F", rel, "absolute or local link %s", l)
			case strings.HasPrefix(l, "/"):
				add("G", rel, "leading-slash link %s, use a relative one", l)
			default:
				target := strings.SplitN(strings.SplitN(l, "#", 2)[0], "?", 2)[0]
				if target == "" {
					continue
				}
				if _, err := os.Stat(filepath.Join(filepath.Dir(p), filepath.FromSlash(target))); err != nil {
					add("G", rel, "broken link %s", l)
				}
			}
		}
		return nil
	})
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].Path < vs[j].Path })
	return vs, nil
}
