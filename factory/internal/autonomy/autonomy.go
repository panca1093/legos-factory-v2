package autonomy

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"reflect"
	"slices"
	"sort"
	"strings"
)

type FileChange struct {
	Path    string
	Status  string // added|modified|deleted|renamed
	OldPath string
	Old     []byte
	New     []byte
	// Embedded marks a doc file that a Go package may embed (//go:embed), which makes it code.
	Embedded bool
}

type Runner func(ctx context.Context, dir string, args ...string) (string, error)

type Approval struct {
	AutoMerge         bool
	HumanApprovesPR   bool
	HumanApprovesPlan bool
}

// DiffLevel returns 0 for protected paths, 3 for docs/test/AST-equal-only diffs, else 2.
func DiffLevel(changes []FileChange, protected []string) (int, []string) {
	var reasons []string
	for _, c := range changes {
		for _, p := range []string{c.Path, c.OldPath} {
			if p != "" && matchesProtected(p, protected) {
				reasons = append(reasons, "protected path: "+p)
			}
		}
	}
	if len(reasons) > 0 {
		return 0, reasons
	}
	if len(changes) == 0 {
		return 2, []string{"no changes to classify"}
	}
	for _, c := range changes {
		if why := nonTrivial(c); why != "" {
			return 2, []string{why}
		}
	}
	return 3, []string{"only docs, tests, or AST-equal Go changes"}
}

func nonTrivial(c FileChange) string {
	if inTestdata(c.Path) || inTestdata(c.OldPath) {
		return "testdata change: " + c.Path
	}
	// Assertion-reduction inside edited tests is out of scope: test-only edits stay level 3 (SPEC §9).
	if c.Status == "deleted" && isTest(c.Path) {
		return "test file deleted: " + c.Path
	}
	if onlyPaths(c, isDoc) && !c.Embedded {
		return ""
	}
	if onlyPaths(c, isTest) {
		return ""
	}
	if c.Status == "modified" && isGo(c.Path) {
		if astEqual(c.Old, c.New) {
			return ""
		}
		return "code change: " + c.Path
	}
	return fmt.Sprintf("%s change outside docs/tests: %s", c.Status, c.Path)
}

func inTestdata(p string) bool {
	return slices.Contains(strings.Split(p, "/"), "testdata")
}

func onlyPaths(c FileChange, f func(string) bool) bool {
	return f(c.Path) && (c.OldPath == "" || f(c.OldPath))
}

func isDoc(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".mdx", ".rst", ".adoc":
		return true
	}
	return false
}

func isGo(p string) bool   { return path.Ext(p) == ".go" }
func isTest(p string) bool { return strings.HasSuffix(p, "_test.go") }

// matchesProtected is case-sensitive. A pattern without "/" matches any path segment at any depth; otherwise it is
// anchored at the repo root, "**" spans zero or more segments, and matching a leading directory protects everything below it.
func matchesProtected(p string, patterns []string) bool {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for _, pat := range patterns {
		pat = strings.Trim(strings.TrimPrefix(strings.TrimSpace(pat), "./"), "/")
		if pat == "" {
			continue
		}
		if !strings.Contains(pat, "/") {
			for _, s := range segs {
				if pat == "**" {
					return true
				}
				if ok, _ := path.Match(pat, s); ok {
					return true
				}
			}
			continue
		}
		if matchSegs(strings.Split(pat, "/"), segs) {
			return true
		}
	}
	return false
}

func matchSegs(pat, segs []string) bool {
	if len(pat) == 0 {
		return true
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegs(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	if ok, _ := path.Match(pat[0], segs[0]); !ok {
		return false
	}
	return matchSegs(pat[1:], segs[1:])
}

// ValidatePattern rejects protected patterns that could never match as intended.
func ValidatePattern(pat string) error {
	t := strings.Trim(strings.TrimPrefix(strings.TrimSpace(pat), "./"), "/")
	if t == "" || strings.HasPrefix(strings.TrimSpace(pat), "/") {
		return fmt.Errorf("protected pattern %q: must be a non-empty repo-relative path", pat)
	}
	for _, seg := range strings.Split(t, "/") {
		switch {
		case seg == "" || seg == "." || seg == "..":
			return fmt.Errorf("protected pattern %q: invalid segment %q", pat, seg)
		case strings.Contains(seg, "**") && seg != "**":
			return fmt.Errorf("protected pattern %q: ** must be a whole path segment", pat)
		}
		if _, err := path.Match(seg, ""); err != nil {
			return fmt.Errorf("protected pattern %q: %w", pat, err)
		}
	}
	return nil
}

// ProtectedIn returns the owned paths (files, directories or globs) that overlap a protected pattern in either direction.
func ProtectedIn(owned, protected []string) []string {
	var hit []string
	for _, o := range owned {
		o = strings.Trim(strings.TrimPrefix(strings.TrimSpace(o), "./"), "/")
		o = globStatic(o)
		if overlaps(o, protected) {
			hit = append(hit, o)
		}
	}
	return hit
}

func globStatic(p string) string {
	if i := strings.IndexAny(p, "*?["); i >= 0 {
		return strings.Trim(p[:strings.LastIndex(p[:i], "/")+1], "/")
	}
	return p
}

func overlaps(owned string, protected []string) bool {
	if owned == "" || owned == "." || matchesProtected(owned, protected) {
		return true
	}
	for _, pat := range protected {
		pat = strings.Trim(globStatic(strings.TrimPrefix(strings.TrimSpace(pat), "./")), "/")
		if pat != "" && (pat == owned || strings.HasPrefix(pat, owned+"/")) {
			return true
		}
	}
	return false
}

func astEqual(a, b []byte) bool {
	fset := token.NewFileSet()
	fa, err := parser.ParseFile(fset, "a.go", a, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return false
	}
	fb, err := parser.ParseFile(fset, "b.go", b, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return false
	}
	if importsC(fa) || importsC(fb) {
		return false
	}
	if !slices.Equal(directives(fa), directives(fb)) {
		return false
	}
	return deepEqual(reflect.ValueOf(fa), reflect.ValueOf(fb))
}

func importsC(f *ast.File) bool {
	for _, im := range f.Imports {
		if im.Path != nil && im.Path.Value == `"C"` {
			return true
		}
	}
	return false
}

var directivePrefixes = []string{"//go:", "// +build", "//+build", "//export ", "//line ", "/*line "}

// directives returns the sorted compiler/linker directive comments, which change behavior without changing the AST.
func directives(f *ast.File) []string {
	var out []string
	for _, g := range f.Comments {
		for _, c := range g.List {
			for _, pre := range directivePrefixes {
				if strings.HasPrefix(c.Text, pre) {
					out = append(out, c.Text)
					break
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

var (
	posType     = reflect.TypeOf(token.Pos(0))
	objType     = reflect.TypeOf((*ast.Object)(nil))
	scopeType   = reflect.TypeOf((*ast.Scope)(nil))
	commentType = reflect.TypeOf((*ast.CommentGroup)(nil))
	commentList = reflect.TypeOf([]*ast.CommentGroup(nil))
)

// deepEqual compares AST values ignoring positions and comment text; directive comments are compared separately.
func deepEqual(a, b reflect.Value) bool {
	switch a.Type() {
	case posType, objType, scopeType, commentType, commentList:
		return true
	}
	switch a.Kind() {
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		if a.Kind() == reflect.Interface && a.Elem().Type() != b.Elem().Type() {
			return false
		}
		return deepEqual(a.Elem(), b.Elem())
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if !deepEqual(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice:
		if a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !deepEqual(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map:
		return a.Len() == b.Len()
	default:
		return a.Interface() == b.Interface()
	}
}

// InspectorLevel can only lower the level: logic_changed caps at 2.
func InspectorLevel(logicChanged bool) int {
	if logicChanged {
		return 2
	}
	return 3
}

func Effective(diffLevel, inspectorLevel, globalCap int) int {
	return min(diffLevel, inspectorLevel, globalCap)
}

// Decision maps a level to required approvals (SPEC §9). L3 auto-merge still requires green CI.
// SPEC defines no L1; it is treated like L0 (the conservative side).
func Decision(level int) Approval {
	switch {
	case level >= 3:
		return Approval{AutoMerge: true}
	case level == 2:
		return Approval{HumanApprovesPR: true}
	default:
		return Approval{HumanApprovesPR: true, HumanApprovesPlan: true}
	}
}

// Collect builds FileChanges for base...HEAD in dir through the injected git runner.
func Collect(ctx context.Context, dir, base string, run Runner) ([]FileChange, error) {
	out, err := run(ctx, dir, "git", "diff", "--name-status", "-z", "-M", base+"...HEAD")
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	fields := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	var changes []FileChange
	for i := 0; i < len(fields) && fields[i] != ""; {
		st := fields[i]
		var c FileChange
		switch st[0] {
		case 'R', 'C':
			if i+2 >= len(fields) {
				return nil, fmt.Errorf("malformed git diff output")
			}
			c = FileChange{Status: "renamed", OldPath: fields[i+1], Path: fields[i+2]}
			i += 3
		case 'A', 'M', 'D', 'T':
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("malformed git diff output")
			}
			c = FileChange{Path: fields[i+1]}
			c.Status = map[byte]string{'A': "added", 'M': "modified", 'D': "deleted", 'T': "modified"}[st[0]]
			i += 2
		default:
			return nil, fmt.Errorf("unknown git status %q", st)
		}
		oldPath := c.Path
		if c.OldPath != "" {
			oldPath = c.OldPath
		}
		if c.Status != "added" {
			if c.Old, err = show(ctx, dir, run, base+":"+oldPath); err != nil {
				return nil, err
			}
		}
		if c.Status != "deleted" {
			if c.New, err = show(ctx, dir, run, "HEAD:"+c.Path); err != nil {
				return nil, err
			}
		}
		changes = append(changes, c)
	}
	return changes, markEmbedded(ctx, dir, base, run, changes)
}

// markEmbedded flags doc changes at or below a directory holding a Go file with //go:embed in either tree.
func markEmbedded(ctx context.Context, dir, base string, run Runner, changes []FileChange) error {
	var docs []int
	for i, c := range changes {
		if isDoc(c.Path) || (c.OldPath != "" && isDoc(c.OldPath)) {
			docs = append(docs, i)
		}
	}
	if len(docs) == 0 {
		return nil
	}
	embedDirs := map[string]bool{}
	for _, rev := range []string{"HEAD", base} {
		out, err := run(ctx, dir, "sh", "-c", `git grep -l -e '^//go:embed' "$1" -- '*.go' || true`, "sh", rev)
		if err != nil {
			return fmt.Errorf("git grep: %w", err)
		}
		for _, line := range strings.Split(out, "\n") {
			if f, ok := strings.CutPrefix(line, rev+":"); ok && f != "" {
				embedDirs[path.Dir(f)] = true
			}
		}
	}
	under := func(p string) bool {
		for d := path.Dir(p); ; d = path.Dir(d) {
			if embedDirs[d] {
				return true
			}
			if d == "." || d == "/" {
				return false
			}
		}
	}
	for _, i := range docs {
		c := &changes[i]
		c.Embedded = under(c.Path) || (c.OldPath != "" && under(c.OldPath))
	}
	return nil
}

func show(ctx context.Context, dir string, run Runner, spec string) ([]byte, error) {
	s, err := run(ctx, dir, "git", "show", spec)
	if err != nil {
		return nil, fmt.Errorf("git show %s: %w", spec, err)
	}
	return []byte(s), nil
}
