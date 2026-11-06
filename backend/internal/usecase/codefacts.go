package usecase

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"strconv"
	"strings"
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
