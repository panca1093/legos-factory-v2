package autonomy

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"reflect"
	"strings"
)

type FileChange struct {
	Path    string
	Status  string // added|modified|deleted|renamed
	OldPath string
	Old     []byte
	New     []byte
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
	if onlyPaths(c, isDoc) || onlyPaths(c, isTest) {
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

func onlyPaths(c FileChange, f func(string) bool) bool {
	return f(c.Path) && (c.OldPath == "" || f(c.OldPath))
}

func isDoc(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".mdx", ".txt", ".rst", ".adoc":
		return true
	}
	return strings.HasPrefix(p, "docs/")
}

func isGo(p string) bool   { return path.Ext(p) == ".go" }
func isTest(p string) bool { return strings.HasSuffix(p, "_test.go") }

func matchesProtected(p string, patterns []string) bool {
	for _, pat := range patterns {
		pat = strings.TrimPrefix(strings.TrimSpace(pat), "./")
		if pat == "" {
			continue
		}
		if dir, ok := strings.CutSuffix(pat, "/**"); ok {
			if strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/") {
				return true
			}
			continue
		}
		dir := strings.TrimSuffix(pat, "/")
		if p == dir || strings.HasPrefix(p, dir+"/") {
			return true
		}
		if ok, _ := path.Match(pat, p); ok {
			return true
		}
	}
	return false
}

func astEqual(a, b []byte) bool {
	fset := token.NewFileSet()
	fa, err := parser.ParseFile(fset, "a.go", a, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	fb, err := parser.ParseFile(fset, "b.go", b, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	return deepEqual(reflect.ValueOf(fa), reflect.ValueOf(fb))
}

var (
	posType   = reflect.TypeOf(token.Pos(0))
	objType   = reflect.TypeOf((*ast.Object)(nil))
	scopeType = reflect.TypeOf((*ast.Scope)(nil))
)

// deepEqual compares AST values ignoring positions; comments are absent because ParseComments is off.
func deepEqual(a, b reflect.Value) bool {
	if a.Type() == posType || a.Type() == objType || a.Type() == scopeType {
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
	return changes, nil
}

func show(ctx context.Context, dir string, run Runner, spec string) ([]byte, error) {
	s, err := run(ctx, dir, "git", "show", spec)
	if err != nil {
		return nil, fmt.Errorf("git show %s: %w", spec, err)
	}
	return []byte(s), nil
}
