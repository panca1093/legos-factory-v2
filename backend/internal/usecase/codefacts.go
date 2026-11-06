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

const codeMapSeeder = "code-map"

// mapLine renders "- path:line @sha — claim"; the sha lets a later order check the line against its own workspace.
func mapLine(cf model.CodeFact) string {
	loc := cf.Path
	if cf.Line > 0 {
		loc += ":" + strconv.Itoa(cf.Line)
	}
	return fmt.Sprintf("- %s @%s — %s", loc, cf.SHA, neutralise(oneLine(sanitize(cf.Claim, maxCodeFactChars))))
}

func parseMapLine(l string) (model.CodeFact, bool) {
	rest, ok := strings.CutPrefix(l, "- ")
	loc, claim, ok2 := strings.Cut(rest, " — ")
	fs := strings.Fields(loc)
	if !ok || !ok2 || len(fs) != 2 || !strings.HasPrefix(fs[1], "@") || len(fs[1]) < 2 {
		return model.CodeFact{}, false
	}
	cf, ok := parseCodeFact(fs[0] + " — " + claim)
	cf.SHA = fs[1][1:]
	return cf, ok
}

func (f *Factory) readCodeMap(projectID string) []model.CodeFact {
	_, body, err := f.KB.Bundle.Read(codeMapPath(projectID))
	if err != nil {
		return nil
	}
	var out []model.CodeFact
	for _, l := range strings.Split(body, "\n") {
		if cf, ok := parseMapLine(l); ok {
			out = append(out, cf)
		}
	}
	return out
}

// portCodeMap merges wo's code facts into the project's Code Map once its PR is merged. Facts, and Map lines, on files
// the order edited are dropped, and so are files that no longer exist; with no workspace left it keeps what is stored.
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
	var edited map[string]bool
	if haveDir && wo.KBBaseSHA != "" {
		edited = changedSince(dir, wo.KBBaseSHA)
	}
	var order []mapKey
	lines := map[mapKey]string{}
	put := func(cf model.CodeFact) {
		k := mapKey{cf.Path, cf.Line}
		if _, ok := lines[k]; ok {
			order = slices.DeleteFunc(order, func(o mapKey) bool { return o == k })
		}
		order = append(order, k)
		lines[k] = mapLine(cf)
	}
	for _, cf := range f.readCodeMap(wo.ProjectID) {
		if !edited[cf.Path] {
			put(cf)
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
	b.WriteString("This is the project's Code Map: facts about the code that agents verified in merged work orders, one per line as `path:line @commit — claim`. A line may be out of date; the code wins.\n\n## Facts\n\n")
	for _, k := range order {
		b.WriteString(lines[k] + "\n")
	}
	front := map[string]any{
		"type": model.KBTypeCodeMap, "title": "Code Map", "status": model.KBStatusStable, "project": wo.ProjectID,
		"description": "Code facts agents verified in this project's merged work orders, merged one line per path and line.",
		"generated":   map[string]any{"by": "legos-factory/cataloger", "at": time.Now().UTC().Format(time.RFC3339)},
	}
	if err := f.KB.WriteFactory(ctx, codeMapPath(wo.ProjectID), front, b.String(), "Code Map from "+wo.ID); err != nil {
		f.Log.Warn("code map: write failed", "wo", wo.ID, "err", err)
	}
}

// seedCodeFacts gives wo the newest Code Map lines it has no fact for, each keeping its own sha so changedSince hides the stale ones.
func (f *Factory) seedCodeFacts(ctx context.Context, wo *model.WorkOrder) {
	if f.CodeFacts == nil || f.KB == nil || f.KB.Bundle == nil {
		return
	}
	have, err := f.CodeFacts.List(ctx, wo.ID)
	if err != nil || len(have) >= maxCodeFacts {
		return
	}
	known := map[mapKey]bool{}
	for _, cf := range have {
		known[mapKey{cf.Path, cf.Line}] = true
	}
	var seeds []model.CodeFact
	for _, cf := range f.readCodeMap(wo.ProjectID) {
		if !known[mapKey{cf.Path, cf.Line}] {
			cf.By = codeMapSeeder
			seeds = append(seeds, cf)
		}
	}
	if room := maxCodeFacts - len(have); len(seeds) > room {
		seeds = seeds[len(seeds)-room:]
	}
	if len(seeds) == 0 {
		return
	}
	if err := f.CodeFacts.Add(ctx, wo.ID, seeds, maxCodeFacts); err != nil && f.Log != nil {
		f.Log.Warn("seed code facts", "wo", wo.ID, "err", err)
	}
}

// auditCodeMap adds the Code Map to the run's kb_docs when a seeded fact is in the prompt text.
func (f *Factory) auditCodeMap(ctx context.Context, wo *model.WorkOrder, text string, opts *runOpts) {
	all, _ := f.CodeFacts.List(ctx, wo.ID)
	for _, cf := range all {
		if cf.By != codeMapSeeder || !strings.Contains(text, "- "+codeFactLoc(cf)+" — "+cf.Claim+"\n") {
			continue
		}
		ids, _ := opts.Audit["kb_docs"].([]string)
		audit := map[string]any{}
		for k, v := range opts.Audit {
			audit[k] = v
		}
		audit["kb_docs"] = append(slices.Clone(ids), kb.DocID(model.KBSourceFactory, wo.ProjectID, codeMapPath(wo.ProjectID)))
		opts.Audit = audit
		return
	}
}

func codeFactLoc(cf model.CodeFact) string {
	if cf.Line > 0 {
		return cf.Path + ":" + strconv.Itoa(cf.Line)
	}
	return cf.Path
}

// specDecisions lists the rows of the spec's Decisions table as "- decision — why (rejected: x)".
func specDecisions(spec string) []string {
	var out []string
	in, fenced := false, false
	for _, l := range strings.Split(spec, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		if strings.HasPrefix(l, "#") {
			in = strings.Contains(strings.ToLower(l), "decisions")
			continue
		}
		if !in || !strings.HasPrefix(l, "|") {
			continue
		}
		cells := splitRow(l)
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

// splitRow splits a markdown table row on unescaped pipes.
func splitRow(l string) []string {
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(l); i++ {
		switch {
		case l[i] == '\\' && i+1 < len(l) && l[i+1] == '|':
			cur.WriteByte('|')
			i++
		case l[i] == '|':
			cells = append(cells, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(l[i])
		}
	}
	if last := cur.String(); strings.TrimSpace(last) != "" {
		cells = append(cells, last)
	}
	return cells[1:]
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
