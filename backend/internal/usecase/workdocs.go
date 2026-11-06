package usecase

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"legos-factory/backend/internal/kb"
	"legos-factory/backend/internal/model"
)

// Work-order documents: the brief, spec, task DAG and task summary a work
// order leaves in the factory bundle. Deterministic, written when the stage
// completes; a failed write is logged and never fails the workflow.

const (
	slugWords    = 3
	slugWordLen  = 16
	maxDocText   = 20000
	notRecorded  = "not recorded"
	workDocLimit = 500
)

type workDocKind struct{ dir, suffix, label string }

var workDocKinds = map[string]workDocKind{
	model.KBTypeBrief:       {"brief", "", "Brief"},
	model.KBTypeSpec:        {"spec", "", "Spec"},
	model.KBTypeTaskDAG:     {"task", "-dag", "Task DAG"},
	model.KBTypeTaskSummary: {"task", "-summary", "Task Summary"},
}

// workDocLinks lists the siblings a document may link to: only ones written before it.
var workDocLinks = map[string][]string{
	model.KBTypeSpec:        {model.KBTypeBrief},
	model.KBTypeTaskDAG:     {model.KBTypeSpec, model.KBTypeBrief},
	model.KBTypeTaskSummary: {model.KBTypeBrief, model.KBTypeSpec, model.KBTypeTaskDAG},
}

// docSlug is the first three words of title as lowercase ASCII, each cut at 16 characters.
func docSlug(title string) string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			if len(cur) > slugWordLen {
				cur = cur[:slugWordLen]
			}
			words = append(words, string(cur))
			cur = nil
		}
	}
	for _, r := range norm.NFD.String(title) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			cur = append(cur, unicode.ToLower(r))
		default:
			flush()
		}
		if len(words) == slugWords {
			break
		}
	}
	flush()
	if len(words) > slugWords {
		words = words[:slugWords]
	}
	if len(words) == 0 {
		return "untitled"
	}
	return strings.Join(words, "-")
}

// docCode is the last 6 characters of the origin id with hyphens removed.
func docCode(originID string) string {
	id := strings.ReplaceAll(originID, "-", "")
	if len(id) > 6 {
		id = id[len(id)-6:]
	}
	return id
}

func workDocPrefix(created time.Time, title, originID string) string {
	return created.Format("2006-01-02") + "-" + docSlug(title) + "-" + docCode(originID)
}

func workDocCreated(wo *model.WorkOrder) time.Time {
	if wo.CreatedAt.IsZero() {
		return kb.Clock()
	}
	return wo.CreatedAt.In(kb.Clock().Location())
}

// workDocState finds the work order's existing documents by origin_id and the
// name prefix they share; without one the prefix is computed from the order.
func (f *Factory) workDocState(ctx context.Context, wo *model.WorkOrder) (have map[string]bool, prefix string) {
	have = map[string]bool{}
	docs, err := f.KB.Idx.List(ctx, model.KBQuery{
		ProjectID: wo.ProjectID, Sources: []string{model.KBSourceFactory},
		Types:    []string{model.KBTypeBrief, model.KBTypeSpec, model.KBTypeTaskDAG, model.KBTypeTaskSummary},
		OriginID: wo.ID, Limit: workDocLimit,
	})
	if err != nil {
		f.Log.Warn("work docs: lookup failed", "wo", wo.ID, "err", err)
	}
	for _, d := range docs {
		if d.ProjectID != wo.ProjectID || d.Meta["origin_id"] != wo.ID {
			continue
		}
		have[d.Type] = true
		if prefix == "" {
			prefix = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(path.Base(d.Path), ".md"), "-dag"), "-summary")
		}
	}
	if prefix == "" {
		prefix = workDocPrefix(workDocCreated(wo), wo.Title, wo.ID)
	}
	return have, prefix
}

func workTitle(wo *model.WorkOrder) string {
	if t := docLine(wo.Title, 200); t != "" {
		return t
	}
	return "(untitled)"
}

var (
	localLinkRe = regexp.MustCompile(`<([^>\n]*(?:file:///|/Users/|/home/)[^>\n]*)>`)
	headingRe   = regexp.MustCompile(`^\s{0,3}#{1,6}(\s|$)`)
	fenceRe     = regexp.MustCompile("^\\s{0,3}(```|~~~)")
	ruleRe      = regexp.MustCompile(`^\s*(-{3,}|={3,})\s*$`)
	linkSyntax  = strings.NewReplacer("](", "] (", "[[", "[ [", "]:", "] :")
)

// neutralise breaks link syntax so free text can never create a link the lint gates would check.
func neutralise(s string) string {
	return linkSyntax.Replace(localLinkRe.ReplaceAllString(s, "($1)"))
}

// docLine makes untrusted text safe for a heading, title or one-line value: redacted, one line, no brackets, no leading #.
func docLine(s string, max int) string {
	s = oneLine(sanitize(s, max))
	s = strings.NewReplacer("[", "(", "]", ")").Replace(s)
	return neutralise(strings.TrimLeft(s, "# "))
}

// safeLine redacts and flattens text that sits inside a template line of the summary.
func safeLine(s string) string { return neutralise(oneLine(sanitize(s, maxDocText))) }

// docBlock makes untrusted multi-line text safe under a fixed ## heading: no headings or rules of its own, fences balanced.
func docBlock(s string) string {
	s = neutralise(strings.TrimSpace(sanitize(s, maxDocText)))
	var out []string
	fence := ""
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r")
		if m := fenceRe.FindStringSubmatch(l); m != nil {
			switch fence {
			case "":
				fence = m[1]
			case m[1]:
				fence = ""
			}
		} else if fence == "" {
			if headingRe.MatchString(l) {
				if t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#")); t != "" {
					l = "**" + t + "**"
				} else {
					l = ""
				}
			} else if ruleRe.MatchString(l) {
				l = ""
			}
		}
		out = append(out, l)
	}
	if fence != "" {
		out = append(out, fence)
	}
	return strings.Join(out, "\n")
}

// putWorkDoc writes one document and returns its bundle path, or "" when it could not be written.
func (f *Factory) putWorkDoc(ctx context.Context, wo *model.WorkOrder, typ, intro, desc string, extra map[string]any, body func() string) string {
	if f.KB == nil || f.KB.Bundle == nil || f.KB.Idx == nil {
		return ""
	}
	have, prefix := f.workDocState(ctx, wo)
	k := workDocKinds[typ]
	rel := fmt.Sprintf("projects/%s/%s/%s%s.md", wo.ProjectID, k.dir, prefix, k.suffix)

	var links []string
	for _, st := range workDocLinks[typ] {
		if !have[st] {
			continue
		}
		sk := workDocKinds[st]
		target := fmt.Sprintf("../%s/%s%s.md", sk.dir, prefix, sk.suffix)
		if sk.dir == k.dir {
			target = prefix + sk.suffix + ".md"
		}
		links = append(links, fmt.Sprintf("[%s](%s)", sk.label, target))
	}
	text := intro
	if len(links) > 0 {
		text += "\n\nRelated: " + strings.Join(links, ", ") + "."
	}
	text += "\n\n" + body()

	front := map[string]any{
		"type": typ, "title": k.label + " — " + workTitle(wo), "description": desc,
		"origin": "work_order", "origin_id": wo.ID, "project": wo.ProjectID,
		"created_at": workDocCreated(wo).Format(time.RFC3339),
	}
	if wo.CommitSHA != "" {
		front["commit_sha"] = wo.CommitSHA
	}
	for key, v := range extra {
		front[key] = v
	}
	if err := f.KB.WriteFactory(ctx, rel, front, text, fmt.Sprintf("%s for %s", k.label, wo.ID)); err != nil {
		f.Log.Error("work docs: write failed", "wo", wo.ID, "type", typ, "err", err)
		return ""
	}
	return rel
}

func docText(s string) string {
	if s = strings.TrimSpace(docBlock(s)); s == "" {
		return notRecorded
	}
	return s
}

// splitBrief separates the Constraints block the Foreman's brief embeds from the rest.
func splitBrief(brief string) (rest, constraints string) {
	var other, cons []string
	for _, block := range strings.Split(strings.TrimSpace(brief), "\n\n") {
		if strings.HasPrefix(block, "Constraints:") {
			cons = append(cons, strings.TrimSpace(strings.TrimPrefix(block, "Constraints:")))
		} else {
			other = append(other, block)
		}
	}
	return strings.Join(other, "\n\n"), strings.Join(cons, "\n")
}

// writeBriefDoc writes the Foreman's brief, with the clarification Q&A known so far.
func (f *Factory) writeBriefDoc(ctx context.Context, wo *model.WorkOrder) {
	title := workTitle(wo)
	f.putWorkDoc(ctx, wo, model.KBTypeBrief,
		fmt.Sprintf("This is the Foreman's brief for work order %q: what was asked and the context the Architect planned from.", title),
		fmt.Sprintf("Brief for work order %q: the request, context, constraints and questions answered before planning.", title),
		nil, func() string {
			ctxText, cons := splitBrief(wo.Brief)
			qa := make([]string, 0, len(wo.Clarifications)+1)
			for _, c := range wo.Clarifications {
				qa = append(qa, "Q: "+docLine(c.Question, 1000)+"\nA: "+docLine(c.Answer, 2000))
			}
			if wo.ClarifQuestion != "" && wo.ClarifAnswer != "" {
				qa = append(qa, "Q: "+docLine(wo.ClarifQuestion, 1000)+"\nA: "+docLine(wo.ClarifAnswer, 2000))
			}
			qaText := "none"
			if len(qa) > 0 {
				qaText = strings.Join(qa, "\n\n")
			}
			return "## Request\n\n" + title + "\n\n" + docText(wo.Objective) +
				"\n\n## Context\n\n" + docText(ctxText) +
				"\n\n## Constraints\n\n" + docText(cons) +
				"\n\n## Questions and answers\n\n" + qaText + "\n"
		})
}

// writePlanDocs writes the spec and the task DAG of the plan approvedBy approved.
func (f *Factory) writePlanDocs(ctx context.Context, wo *model.WorkOrder, tasks []model.Task, approvedBy string) {
	if f.KB == nil {
		return
	}
	title := workTitle(wo)
	if len(wo.Clarifications) > 0 || wo.ClarifQuestion != "" {
		f.writeBriefDoc(ctx, wo)
	}
	f.putWorkDoc(ctx, wo, model.KBTypeSpec,
		fmt.Sprintf("This is the approved spec for work order %q: what must be true when the work is done.", title),
		fmt.Sprintf("Spec for work order %q: objective, acceptance criteria, scope and approach of the approved plan.", title),
		nil, func() string {
			return "## Objective\n\n" + docText(wo.Objective) +
				"\n\n## Acceptance criteria\n\n" + docText(wo.Acceptance) +
				"\n\n## Out of scope\n\n" + notRecorded +
				"\n\n## Approach\n\n" + docText(wo.Spec) + "\n"
		})
	f.putWorkDoc(ctx, wo, model.KBTypeTaskDAG,
		fmt.Sprintf("This is the approved task plan for work order %q: how the work is split into tasks, in what order, and the scope of each.", title),
		fmt.Sprintf("Task DAG for work order %q: %d task(s) with their dependencies, roles and scope.", title, len(tasks)),
		map[string]any{"approved_by": approvedBy}, func() string { return dagBody(tasks) })
}

func dagBody(tasks []model.Task) string {
	pos := make(map[string]int, len(tasks))
	for i, t := range tasks {
		pos[t.ID] = i + 1
	}
	var graph, list strings.Builder
	for i, t := range tasks {
		n := i + 1
		var deps []string
		for _, d := range t.Deps {
			if p, ok := pos[d]; ok {
				deps = append(deps, fmt.Sprint(p))
			}
		}
		dep := strings.Join(deps, ", ")
		if dep == "" {
			fmt.Fprintf(&graph, "%d\n", n)
			dep = "none"
		} else {
			fmt.Fprintf(&graph, "%d ← %s\n", n, dep)
		}
		role, scope := docLine(t.Role, 50), docLine(strings.Join(t.Scope, ", "), 500)
		if role == "" {
			role = "builder"
		}
		if scope == "" {
			scope = "not set"
		}
		fmt.Fprintf(&list, "### %d. %s\n\nRole: %s · Depends on: %s · Scope: %s\n\n%s\n\n", n, docLine(t.Title, 300), role, dep, scope, docText(t.Objective))
	}
	return "## Graph\n\n```\n" + graph.String() + "```\n\n## Tasks\n\n" + strings.TrimRight(list.String(), "\n") + "\n"
}

// writeSummaryDoc writes the terminal task summary and returns its path.
func (f *Factory) writeSummaryDoc(ctx context.Context, wo *model.WorkOrder, outcome string) string {
	if f.KB == nil {
		return ""
	}
	title := workTitle(wo)
	return f.putWorkDoc(ctx, wo, model.KBTypeTaskSummary,
		fmt.Sprintf("This is the task summary of work order %q, which ended %s: how it ended, what is left open and what changed.", title, outcome),
		fmt.Sprintf("Task summary for work order %q: outcome %s, final verification, open items, decisions and main changes.", title, outcome),
		nil, func() string { return neutralise(sanitize(f.taskSummary(ctx, wo, outcome), maxDocText)) + "\n" })
}
