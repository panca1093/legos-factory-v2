package kb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"legos-factory/backend/internal/model"
)

var (
	mdLink    = regexp.MustCompile(`\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	wikiLink  = regexp.MustCompile(`\[\[([^\]|#]+)(?:#[^\]|]*)?(?:\|[^\]]*)?\]\]`)
	headingRe = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*#*\s*$`)
)

// DocID is the index id of a document: source:projectID:relPath.
func DocID(source, projectID, relPath string) string {
	return source + ":" + projectID + ":" + relPath
}

// splitFront separates leading `---` delimited frontmatter from the body.
func splitFront(content []byte) (front []byte, body string) {
	s := string(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n")))
	if !strings.HasPrefix(s, "---\n") {
		return nil, s
	}
	pos := 4
	for pos <= len(s) {
		end := strings.IndexByte(s[pos:], '\n')
		line, next := s[pos:], len(s)
		if end >= 0 {
			line, next = s[pos:pos+end], pos+end+1
		}
		if strings.TrimRight(line, " \t") == "---" {
			return []byte(s[4:pos]), s[next:]
		}
		if end < 0 {
			break
		}
		pos = next
	}
	return nil, s
}

// Parse reads one markdown document. resolve maps a bare [[wiki name]] to a
// path relative to the source root; nil drops bare wiki links.
func Parse(source, projectID, relPath string, content []byte, resolve func(wikiName string) (string, bool)) (model.KBParsed, error) {
	sum := sha256.Sum256(content)
	frontRaw, body := splitFront(content)
	meta := map[string]any{}
	if len(frontRaw) > 0 {
		if err := yaml.Unmarshal(frontRaw, &meta); err != nil {
			return model.KBParsed{}, err
		}
		if meta == nil {
			meta = map[string]any{}
		}
	}
	str := func(k string) string {
		s, _ := meta[k].(string)
		delete(meta, k)
		return strings.TrimSpace(s)
	}
	typ, title, desc, status := str("type"), str("title"), str("description"), str("status")

	headings, h1, prose, mdTargets, wikiNames := scan(body)
	if title == "" {
		title = h1
	}
	if title == "" {
		title = strings.TrimSuffix(path.Base(relPath), ".md")
	}
	if desc == "" {
		desc = prose
	}
	if status == "" {
		status = model.KBStatusStable
	}

	id := DocID(source, projectID, relPath)
	seen := map[string]bool{id: true}
	var links []string
	add := func(rel string) {
		lid := DocID(source, projectID, rel)
		if !seen[lid] {
			seen[lid] = true
			links = append(links, lid)
		}
	}
	dir := path.Dir(relPath)
	for _, t := range mdTargets {
		if rel, ok := resolveLink(dir, t); ok {
			add(rel)
		}
	}
	for _, n := range wikiNames {
		if strings.Contains(n, "/") {
			if p := path.Clean(strings.TrimSuffix(n, ".md") + ".md"); !strings.HasPrefix(p, "..") && !path.IsAbs(p) {
				add(p)
			}
		} else if resolve != nil {
			if p, ok := resolve(n); ok {
				add(p)
			}
		}
	}

	return model.KBParsed{
		KBDoc: model.KBDoc{
			ID: id, Source: source, ProjectID: projectID, Path: relPath,
			ContentHash: hex.EncodeToString(sum[:]),
			Type:        typ, Status: status, Trust: trustOf(meta["verified"]),
			Title: title, Description: desc, Meta: meta, Links: links,
		},
		Headings: headings, Body: body,
	}, nil
}

func resolveLink(dir, target string) (string, bool) {
	if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") {
		return "", false
	}
	if i := strings.IndexAny(target, "#?"); i >= 0 {
		target = target[:i]
	}
	if u, err := url.PathUnescape(target); err == nil {
		target = u
	}
	if !strings.HasSuffix(target, ".md") {
		return "", false
	}
	var p string
	if strings.HasPrefix(target, "/") {
		p = path.Clean(strings.TrimPrefix(target, "/"))
	} else {
		p = path.Join(dir, target)
	}
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// scan walks the body outside fenced code blocks.
func scan(body string) (headings, h1, prose string, mdTargets, wikiNames []string) {
	var hs []string
	var para []string
	proseDone := false
	fence := false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if m := headingRe.FindStringSubmatch(t); m != nil {
			hs = append(hs, m[1])
			if h1 == "" && strings.HasPrefix(t, "# ") {
				h1 = m[1]
			}
			proseDone = proseDone || len(para) > 0
			continue
		}
		for _, m := range mdLink.FindAllStringSubmatch(line, -1) {
			mdTargets = append(mdTargets, m[1])
		}
		for _, m := range wikiLink.FindAllStringSubmatch(line, -1) {
			wikiNames = append(wikiNames, strings.TrimSpace(m[1]))
		}
		if proseDone {
			continue
		}
		if t == "" || strings.ContainsAny(t[:1], "-*>|<!+") || t == "---" {
			proseDone = len(para) > 0
			continue
		}
		para = append(para, t)
	}
	prose = strings.Join(para, " ")
	if utf8.RuneCountInString(prose) > 200 {
		prose = string([]rune(prose)[:200])
	}
	return strings.Join(hs, "\n"), h1, prose, mdTargets, wikiNames
}

func trustOf(verified any) string {
	actors := verifiers(verified)
	for _, a := range actors {
		if strings.HasPrefix(a, "human:") {
			return model.KBTrustHuman
		}
	}
	if len(actors) > 0 {
		return model.KBTrustMachine
	}
	return model.KBTrustUnverified
}

// CompileGlob turns a glob into an anchored regexp, nil if invalid. Case matters and "[...]" is literal;
// "*" and "?" stay within a segment, "**" crosses segments, a leading "./" is dropped and a trailing "/" means "/**".
func CompileGlob(pattern string) *regexp.Regexp {
	pattern = strings.TrimPrefix(pattern, "./")
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; {
		case strings.HasPrefix(pattern[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil
	}
	return re
}

func GlobMatch(pattern, p string) bool {
	re := CompileGlob(pattern)
	return re != nil && re.MatchString(p)
}

func verifiers(v any) []string {
	switch x := v.(type) {
	case string:
		if x = strings.TrimSpace(x); x != "" {
			return []string{x}
		}
	case map[string]any:
		return verifiers(x["by"])
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, verifiers(e)...)
		}
		return out
	}
	return nil
}
