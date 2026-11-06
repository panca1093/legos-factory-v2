package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
)

func firstRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Learning drafts are deterministic templates: a human edits and approves
// them, or discards them. Until approved they are never offered to agents.

func (f *Factory) writeDraft(ctx context.Context, wo *model.WorkOrder, name, title, role, specialty, body string, refs []string) {
	front := map[string]any{
		"type": model.KBTypePlaybook, "title": title, "description": firstRunes(oneLine(body), 200),
		"status": model.KBStatusDraft, "role": role, "source_refs": refs,
		"generated": map[string]any{"by": "legos-factory/cataloger", "at": time.Now().UTC().Format(time.RFC3339)},
	}
	if specialty != "" {
		front["specialty"] = specialty
	}
	rel := "projects/" + wo.ProjectID + "/drafts/" + name + ".md"
	if err := f.KB.WriteFactory(ctx, rel, front, body, "Propose playbook draft: "+title); err != nil {
		f.Log.Warn("draft: write failed", "wo", wo.ID, "path", rel, "err", err)
		return
	}
	f.event(wo.ID, "cataloger", "draft_proposed", "Playbook draft proposed: "+title+". Review it in Knowledge.",
		map[string]any{"path": rel})
}

func (f *Factory) draftFromDispute(ctx context.Context, wo *model.WorkOrder, fd *model.Finding, note string) {
	if f.KB == nil {
		return
	}
	body := fmt.Sprintf("# %s: %s\n\nInspector %s reported %q (%s) and a human disputed it: %s.\n\nProposed rule: do not report this kind of finding as %s unless …\n\nEdit the condition above, then approve this draft to make it part of the %s inspector's playbook.\n",
		fd.Specialty, fd.Title, fd.Specialty, fd.Title, fd.Severity, strings.TrimRight(note, ". "), fd.Severity, fd.Specialty)
	f.writeDraft(ctx, wo, "dispute-"+fd.ID, fmt.Sprintf("Draft: %s inspector, %s", fd.Specialty, fd.Title),
		string(model.AgentInspector), fd.Specialty, body, []string{"finding:" + fd.ID, "wo:" + wo.ID})
}

func (f *Factory) draftFromClarification(ctx context.Context, wo *model.WorkOrder, question, answer string) {
	if f.KB == nil {
		return
	}
	evs, err := f.WO.ProjectEvents(ctx, wo.ProjectID, "clarification_answered", 200)
	if err != nil {
		f.Log.Warn("draft: clarification lookup failed", "wo", wo.ID, "err", err)
		return
	}
	for _, e := range evs {
		prevQ, _ := e.Payload["question"].(string)
		if e.WOID == wo.ID || e.Actor != "human" || sharedWords(question, prevQ) < 2 {
			continue
		}
		body := fmt.Sprintf("# Draft: %s\n\nThe Foreman asked a similar question before.\n\nEarlier (%s):\nQuestion: %s\nAnswer: %s\n\nNow (%s):\nQuestion: %s\nAnswer: %s\n\nProposed rule: when the Foreman meets this kind of question, decide it as the human did and state the assumption instead of asking … (edit the rule to match the answers above).\n",
			oneLine(question), e.WOID, prevQ, strings.TrimSpace(e.Message), wo.ID, question, answer)
		f.writeDraft(ctx, wo, "clarification-"+wo.ID, "Draft: "+firstRunes(oneLine(question), 80), string(model.AgentForeman), "", body,
			[]string{fmt.Sprintf("event:%d", e.ID), "wo:" + wo.ID})
		return
	}
}

// sharedWords counts distinct words of 4+ letters that both texts use.
func sharedWords(a, b string) int {
	words := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) {
			if len(w) >= 4 {
				m[w] = true
			}
		}
		return m
	}
	wb, n := words(b), 0
	for w := range words(a) {
		if wb[w] {
			n++
		}
	}
	return n
}
