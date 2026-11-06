package usecase

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
)

// Code facts are claims agents verified in the code, shared across the work
// order's agents. They differ from repoFacts, which code detects on its own.

const (
	maxCodeFacts     = 40
	maxCodeFactChars = 200
)

// parseCodeFact reads "path:line — claim"; the line is optional.
func parseCodeFact(s string) (model.CodeFact, bool) {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxCodeFactChars {
		s = string(r[:maxCodeFactChars])
	}
	loc, claim, ok := strings.Cut(s, " — ")
	if !ok {
		loc, claim, ok = strings.Cut(s, " - ")
	}
	loc, claim = strings.TrimSpace(loc), strings.TrimSpace(claim)
	if !ok || loc == "" || claim == "" || strings.ContainsAny(loc, " \n") {
		return model.CodeFact{}, false
	}
	f := model.CodeFact{Path: loc, Claim: strings.Join(strings.FieldsFunc(claim, unicode.IsControl), " ")}
	if p, l, ok := strings.Cut(loc, ":"); ok {
		first, _, _ := strings.Cut(l, "-")
		n, err := strconv.Atoi(first)
		if err != nil || p == "" {
			return model.CodeFact{}, false
		}
		f.Path, f.Line = p, n
	}
	f.Path = path.Clean(strings.TrimPrefix(f.Path, "./"))
	if f.Claim == "" || path.IsAbs(f.Path) || f.Path == ".." || strings.HasPrefix(f.Path, "../") {
		return model.CodeFact{}, false
	}
	return f, true
}

// recordCodeFacts stores an agent's facts at the commit the workspace is at now. Failures never fail the run.
func (f *Factory) recordCodeFacts(ctx context.Context, wo *model.WorkOrder, role model.AgentRole, items []string) {
	if f.CodeFacts == nil || len(items) == 0 {
		return
	}
	sha := wo.KBBaseSHA
	if sha == "" {
		sha = kb.GitHead(f.workspaceDir(wo.ID))
	}
	var facts []model.CodeFact
	for _, it := range items {
		if cf, ok := parseCodeFact(it); ok {
			cf.By, cf.SHA = string(role), sha
			facts = append(facts, cf)
		}
	}
	if sha == "" || len(facts) == 0 {
		return
	}
	if err := f.CodeFacts.Add(ctx, wo.ID, facts, maxCodeFacts); err != nil && f.Log != nil {
		f.Log.Warn("store code facts", "wo", wo.ID, "err", err)
	}
}

// codeFactsText renders the facts no commit has touched since they were written; stale ones are left out.
func (f *Factory) codeFactsText(ctx context.Context, wo *model.WorkOrder) string {
	if f.CodeFacts == nil {
		return ""
	}
	all, err := f.CodeFacts.List(ctx, wo.ID)
	if err != nil || len(all) == 0 {
		return ""
	}
	dir := f.workspaceDir(wo.ID)
	changed := map[string]map[string]bool{} // sha → paths changed since; nil value = git could not tell
	var shas []string
	fresh := map[string][]model.CodeFact{}
	for _, cf := range all {
		if _, seen := changed[cf.SHA]; !seen {
			changed[cf.SHA] = changedSince(dir, cf.SHA)
			shas = append(shas, cf.SHA)
		}
		if c := changed[cf.SHA]; c != nil && !c[cf.Path] {
			fresh[cf.SHA] = append(fresh[cf.SHA], cf)
		}
	}
	var b strings.Builder
	for _, sha := range shas {
		if len(fresh[sha]) == 0 {
			continue
		}
		short := sha
		if len(short) > 8 {
			short = short[:8]
		}
		fmt.Fprintf(&b, "\nCode facts (verified at %s; trust these, open a file only for what they don't cover):\n", short)
		for _, cf := range fresh[sha] {
			loc := cf.Path
			if cf.Line > 0 {
				loc += ":" + strconv.Itoa(cf.Line)
			}
			fmt.Fprintf(&b, "- %s — %s\n", loc, cf.Claim)
		}
	}
	return b.String()
}

const maxCodeMapLines = 300

func codeMapPath(projectID string) string { return "projects/" + projectID + "/notes/code-map.md" }

type mapKey struct {
	path string
	line int
}

// portCodeMap merges wo's code facts into the project's Code Map at ship. Facts a commit has touched since they were
// written are skipped, and so are files that no longer exist; with no workspace left it keeps what is stored.
func (f *Factory) portCodeMap(ctx context.Context, wo *model.WorkOrder) {
	if f.CodeFacts == nil || f.KB == nil || f.KB.Bundle == nil || f.KB.Idx == nil {
		return
	}
	all, err := f.CodeFacts.List(ctx, wo.ID)
	if err != nil || len(all) == 0 {
		return
	}
	dir := f.workspaceDir(wo.ID)
	_, statErr := os.Stat(dir)
	haveDir := statErr == nil
	changed := map[string]map[string]bool{}
	var fresh []model.CodeFact
	for _, cf := range all {
		if haveDir {
			if _, ok := changed[cf.SHA]; !ok {
				changed[cf.SHA] = changedSince(dir, cf.SHA)
			}
			if c := changed[cf.SHA]; c == nil || c[cf.Path] {
				continue
			}
		}
		fresh = append(fresh, cf)
	}
	if len(fresh) == 0 {
		return
	}
	mu := f.KB.projectLock(wo.ProjectID)
	mu.Lock()
	defer mu.Unlock()
	rel := codeMapPath(wo.ProjectID)
	var existing []string
	if _, body, err := f.KB.Bundle.Read(rel); err == nil {
		existing = strings.Split(body, "\n")
	}
	var order []mapKey
	lines := map[mapKey]string{}
	put := func(cf model.CodeFact) {
		k := mapKey{cf.Path, cf.Line}
		if _, ok := lines[k]; ok {
			order = slices.DeleteFunc(order, func(o mapKey) bool { return o == k })
		}
		order = append(order, k)
		loc := cf.Path
		if cf.Line > 0 {
			loc += ":" + strconv.Itoa(cf.Line)
		}
		lines[k] = fmt.Sprintf("- %s — %s", loc, neutralise(oneLine(sanitize(cf.Claim, maxCodeFactChars))))
	}
	for _, l := range existing {
		if rest, ok := strings.CutPrefix(l, "- "); ok {
			if cf, ok := parseCodeFact(rest); ok {
				put(cf)
			}
		}
	}
	for _, cf := range fresh {
		put(cf)
	}
	if haveDir {
		order = slices.DeleteFunc(order, func(k mapKey) bool {
			_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(k.path)))
			return os.IsNotExist(err)
		})
	}
	if len(order) > maxCodeMapLines {
		order = order[len(order)-maxCodeMapLines:]
	}
	var b strings.Builder
	b.WriteString("This is the project's Code Map: facts about the code that agents verified in finished work orders, one per line as `path:line — claim`. A line may be out of date; the code wins.\n\n## Facts\n\n")
	for _, k := range order {
		b.WriteString(lines[k] + "\n")
	}
	front := map[string]any{
		"type": model.KBTypeCodeMap, "title": "Code Map", "status": model.KBStatusStable, "project": wo.ProjectID,
		"description": "Code facts agents verified in this project's finished work orders, merged one line per path and line.",
		"generated":   map[string]any{"by": "legos-factory/cataloger", "at": time.Now().UTC().Format(time.RFC3339)},
	}
	if err := f.KB.WriteFactory(ctx, rel, front, b.String(), "Code Map from "+wo.ID); err != nil {
		f.Log.Warn("code map: write failed", "wo", wo.ID, "err", err)
	}
}

// codeMapPointer names the project's Code Map for an agent prompt; "" when the project has none yet.
func (k *KB) codeMapPointer(ctx context.Context, projectID string) string {
	docs, err := k.Idx.List(ctx, model.KBQuery{
		ProjectID: projectID, Sources: []string{model.KBSourceFactory}, Types: []string{model.KBTypeCodeMap}, Limit: 1,
	})
	if err != nil || len(docs) == 0 || docs[0].ProjectID != projectID {
		return ""
	}
	return fmt.Sprintf("\n\nProject Code Map: %s — code facts earlier work orders verified, one `path:line — claim` per line. Search it for the file or symbol you need before grepping; a line may be out of date and the code wins.\n",
		filepath.Join(k.Bundle.Root(), filepath.FromSlash(docs[0].Path)))
}

// specDecisions lists the rows of the spec's Decisions table as "- decision — why (rejected: x)".
func specDecisions(spec string) []string {
	var out []string
	in := false
	for _, l := range strings.Split(spec, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "#") {
			in = strings.EqualFold(strings.Trim(l, "# "), "Decisions")
			continue
		}
		if !in || !strings.HasPrefix(l, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(l, "|"), "|")
		if len(cells) < 4 || strings.Trim(strings.Join(cells, ""), " -:") == "" || strings.EqualFold(strings.TrimSpace(cells[0]), "#") {
			continue
		}
		line := "- " + safeLine(cells[1]) + " — " + safeLine(cells[2])
		if r := safeLine(cells[3]); r != "" {
			line += " (rejected: " + r + ")"
		}
		out = append(out, clip(line, 160))
	}
	return out
}

// changedSince lists paths that differ between sha and the working tree, so uncommitted edits count; nil when git cannot tell, which makes every fact at sha stale.
func changedSince(dir, sha string) map[string]bool {
	out, err := exec.Command("git", "-C", dir, "diff", "--no-renames", "--name-only", sha).Output()
	if err != nil {
		return nil
	}
	m := map[string]bool{}
	for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if p != "" {
			m[p] = true
		}
	}
	return m
}
