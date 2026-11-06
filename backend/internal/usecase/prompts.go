package usecase

import (
	"fmt"
	"strings"

	"legos-factory/backend/internal/model"
)

// Prompts request schema-validated JSON. Structured outputs are parsed and
// validated before any state transition; free text never drives control flow.

func clarification(wo *model.WorkOrder) string {
	var b strings.Builder
	if len(wo.Clarifications) > 0 {
		b.WriteString("\nThe Foreman answered these questions:\n")
		for _, c := range wo.Clarifications {
			fmt.Fprintf(&b, "Q: %s\nA: %s\n", c.Question, c.Answer)
		}
	}
	if wo.ClarifAnswer != "" {
		fmt.Fprintf(&b, "\nA human answered a clarification:\nQ: %s\nA: %s\n", wo.ClarifQuestion, wo.ClarifAnswer)
	}
	return b.String()
}

// clarificationLog is the Foreman's answers as plain text, for scripted harnesses.
func clarificationLog(wo *model.WorkOrder) string {
	var parts []string
	for _, c := range wo.Clarifications {
		parts = append(parts, c.Question+" -> "+c.Answer)
	}
	return strings.Join(parts, "\n")
}

func scopedTaskList(tasks []model.Task) string {
	var b strings.Builder
	for i, t := range tasks {
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, t.Title, t.Objective)
		if len(t.Scope) > 0 {
			fmt.Fprintf(&b, "   Scope: %s\n", strings.Join(t.Scope, ", "))
		}
	}
	return b.String()
}

func briefSection(wo *model.WorkOrder) string {
	if wo.Brief == "" {
		return ""
	}
	return "\nForeman's brief:\n" + wo.Brief + "\n"
}

func intakePrompt(wo *model.WorkOrder, facts string) string {
	return fmt.Sprintf(`You are the Foreman, leading a software engineering team. Brief the Architect before any
planning starts. You may read the repository to ground the brief.

Work order: %s

Objective:
%s

Acceptance criteria:
%s

%s
Respond with ONLY a JSON object (no prose, no code fences):

{"brief":"<what to build and where, in your words>",
 "constraints":["<behaviour or interfaces that must not change>"],
 "risks":["<what could go wrong>"],
 "suggested_task_shape":"<how to cut the work into tasks>"}`,
		wo.Title, wo.Objective, wo.Acceptance, facts)
}

func foremanAnswerPrompt(wo *model.WorkOrder, facts, question string) string {
	return fmt.Sprintf(`You are the Foreman. The Architect, planning from your brief, asks a question.

Work order: %s

Objective:
%s

Acceptance criteria:
%s
%s%s
%s
Architect's question:
%s

Answer it only if the request, the brief or the repository settles it. If it needs a product
decision nobody has made, escalate to a human — never guess it.
Respond with ONLY a JSON object:
{"decision":"ANSWER"|"ESCALATE","answer":"<required for ANSWER>","reason":"<why, especially for ESCALATE>"}`,
		wo.Title, wo.Objective, wo.Acceptance, briefSection(wo), clarification(wo), facts, question)
}

// foremanPrime restores the Foreman's earlier decisions when a prompt starts a new session.
func foremanPrime(wo *model.WorkOrder) string {
	if wo.Brief == "" && len(wo.Clarifications) == 0 {
		return ""
	}
	return "You are continuing a work order you already started. Your earlier decisions:" + briefSection(wo) + clarification(wo) + "\n---\n\n"
}

func planPrompt(wo *model.WorkOrder, notes, facts string) string {
	extra := briefSection(wo) + clarification(wo)
	if notes != "" {
		extra += fmt.Sprintf("\nRevise the previous draft according to: %s\n", notes)
	}
	return fmt.Sprintf(`You are the Architect of a software engineering team.

Work order: %s

Objective:
%s

Acceptance criteria:
%s
%s
%s
Decompose this objective into the smallest set of implementation tasks.
Respond with ONLY a JSON object (no prose, no code fences) matching:

{"decision":"APPROVE"|"CLARIFY",
 "summary":"<one line>",
 "spec":"<behaviour, interfaces and edge cases; required when decision is APPROVE>",
 "tasks":[{"id":"task-1","title":"...","objective":"...","role":"builder","deps":["task-1"],"scope":["<file or area this task may touch>"]}],
 "question_text":"<only when decision is CLARIFY: the single question that must be answered>"}

Rules:
- If a product decision cannot be inferred from the objective or the brief, use decision CLARIFY and ask — never invent product requirements.
- "deps" lists ids of tasks that must complete first; the graph must be acyclic and tasks must cover the objective.
- "scope" lists the files or areas each task may touch.
- role is "builder" for implementation tasks.`, wo.Title, wo.Objective, wo.Acceptance, extra, facts)
}

func criteriaSection(cs []Criterion) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "%s: %s\n", c.ID, c.Text)
	}
	return b.String()
}

// diffSection renders the diff within the gate's hunk budget and says which files were cut.
func diffSection(files []FileDiff) string {
	if len(files) == 0 {
		return "(no file changes available)\n"
	}
	files, trimmed := trimHunks(files, diffHunkBudget)
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "--- %s (+%d -%d)\n%s", f.Path, f.Added, f.Removed, f.Hunks)
		if f.Hunks != "" && !strings.HasSuffix(f.Hunks, "\n") {
			b.WriteString("\n")
		}
	}
	if len(trimmed) > 0 {
		fmt.Fprintf(&b, "(hunks cut or left out for: %s)\n", strings.Join(trimmed, ", "))
	}
	return b.String()
}

func foremanReviewPrompt(wo *model.WorkOrder, tasks []model.Task) string {
	return fmt.Sprintf(`You are the Foreman, orchestrating a software engineering team. The Architect drafted
the plan below. Review it independently before any code is written.

Work order: %s
Objective:
%s
Acceptance criteria:
%s
%s
Architect's spec:
%s

Plan:
%s
Check requirement completeness, task dependencies, scope and assumptions, edge cases,
risks, testing strategy and coverage of every acceptance criterion.

You do not approve or reject the plan: write your review notes only. A separate gate decides
from them. Name every problem you find and say plainly when you find none.

Respond with ONLY a JSON object:
{"notes":"<issues found, or that the plan is sound>",
 "coverage":[{"criterion":"c1","remark":"<which task covers it, or that none does>"}]}`,
		wo.Title, wo.Objective, criteriaSection(splitCriteria(wo.Acceptance)), briefSection(wo)+clarification(wo), wo.Spec, scopedTaskList(tasks))
}

func architectVerifyPrompt(wo *model.WorkOrder, tasks []model.Task, diff []FileDiff, checks string) string {
	return fmt.Sprintf(`You are the Architect. The Builders finished the plan you wrote. Verify the built result against
your own spec and plan. You may read the repository.

Work order: %s

Acceptance criteria:
%s
Spec:
%s

Plan:
%s
Check results:
%s
Diff against the base:
%s
For every acceptance criterion above, report whether the change meets it: "met", "not_met" or
"unclear". A human reads this, so keep each field short and plain:
- evidence: at most three sentences naming the files and what they do.
- gap: only when not_met or unclear; one or two sentences on what is missing or wrong.
- fix: only when not_met or unclear; the concrete change the builder should make, as short steps.
Report as deviations any work that goes beyond your spec or plan, or that departs from it, one
sentence each.

Respond with ONLY a JSON object:
{"verdicts":[{"criterion_id":"c1","status":"met"|"not_met"|"unclear","evidence":"<files and what they do>","gap":"<what is missing>","fix":"<change to make>"}],
 "deviations":["<work outside the spec or plan>"]}`,
		wo.Title, criteriaSection(splitCriteria(wo.Acceptance)), wo.Spec, scopedTaskList(tasks), checks, diffSection(diff))
}

func foremanValidatePrompt(wo *model.WorkOrder, findings, report, checks string) string {
	return fmt.Sprintf(`You are the Foreman. The work below passed its checks and was reviewed. Judge whether it is done.

Work order: %s
Objective:
%s
Acceptance criteria:
%s
%s
Check results:
%s
Inspector findings:
%s
Architect's verification:
%s
You may read the repository. You do not decide: write validation notes only. A separate gate decides
from them. Say which acceptance criteria you consider met and name every problem that is not fixed yet,
or say plainly that you found none.

Respond with ONLY a JSON object:
{"notes":"<whether the work is done and what is still wrong>"}`,
		wo.Title, wo.Objective, criteriaSection(splitCriteria(wo.Acceptance)), briefSection(wo)+clarification(wo), checks, findings, report)
}

// taskSection names a plan task's own objective and scope and lists the other
// tasks as off limits, so parallel Builders do not redo each other's work.
func taskSection(job model.Job, tasks []model.Task) string {
	var mine *model.Task
	var others []string
	for i, t := range tasks {
		if t.ID == job.TaskID {
			mine = &tasks[i]
		} else {
			others = append(others, "- "+t.Title)
		}
	}
	if mine == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nYour task: %s — %s\n", mine.Title, mine.Objective)
	if len(mine.Scope) > 0 {
		fmt.Fprintf(&b, "Scope (the files or areas you may touch): %s\n", strings.Join(mine.Scope, ", "))
	}
	if len(others) > 0 {
		b.WriteString("Other tasks — do not implement these, other Builders own them:\n" + strings.Join(others, "\n") + "\n")
	}
	return b.String()
}

func implementPrompt(wo *model.WorkOrder, job model.Job, tasks []model.Task) string {
	reason, _ := job.Payload["reason"].(string)
	if failed, _ := job.Payload["failed_checks"].(string); reason == "fix_failed_verification" && failed != "" {
		return fmt.Sprintf(`You are the Builder. Work order %q failed deterministic verification.

Verification failed on these checks; fix the cause. Do not edit .legos/checks.json or weaken tests to make them pass.

%s

Objective: %s

Acceptance criteria:
%s
%s
Work only within the assigned workspace. When done, reply with one line describing what changed.`,
			wo.Title, failed, wo.Objective, wo.Acceptance, clarification(wo))
	}
	if note, _ := job.Payload["note"].(string); reason == "fix_done_gate" && note != "" {
		return fmt.Sprintf(`You are the Builder. The final review of work order %q found the change incomplete.

Fix what the review found; do not weaken tests or edit .legos/checks.json to make it pass:
%s

Objective: %s

Acceptance criteria:
%s
%s
Work only within the assigned workspace. When done, reply with one line describing what changed.`,
			wo.Title, note, wo.Objective, wo.Acceptance, clarification(wo))
	}
	if reason != "" {
		return fmt.Sprintf(`You are the Builder. Work order %q requires remediation.

Reason: %s
Objective: %s

Acceptance criteria:
%s
%s
Implement the change and any behavioral tests needed to satisfy the criteria.
Work only within the assigned workspace. When done, reply with one line describing what changed.`,
			wo.Title, reason, wo.Objective, wo.Acceptance, clarification(wo))
	}
	return fmt.Sprintf(`You are the Builder. Implement task %q of work order %q.

Objective: %s

Acceptance criteria:
%s
%s%s
Work only within the assigned workspace. When done, reply with one line describing what changed.`, job.TaskID, wo.Title, wo.Objective, wo.Acceptance, clarification(wo), taskSection(job, tasks))
}

func resolvePrompt(wo *model.WorkOrder, job model.Job, files []string) string {
	return fmt.Sprintf(`You are the Builder. Your task %q of work order %q was implemented, but other tasks were merged
into the work order first. Their changes are now merged into your working copy and conflict in:
%s

Resolve every conflict so both your task and the merged work are kept, remove all conflict markers,
and make sure the project still builds and its tests pass.

Objective: %s
%s
Work only within the assigned workspace. When done, reply with one line describing the resolution.`,
		job.TaskID, wo.Title, strings.Join(files, "\n"), wo.Objective, clarification(wo))
}

func inspectPrompt(wo *model.WorkOrder, pick SpecialistPick, files []string) string {
	changed := "(no file list available)"
	if len(files) > 0 {
		if len(files) > 50 {
			files = append(files[:50:50], fmt.Sprintf("… and %d more", len(files)-50))
		}
		changed = strings.Join(files, "\n")
	}
	return fmt.Sprintf(`You are the %s Inspector. Independently review the implementation of work order %q in the workspace.
You were selected because the change %s.

Focus: %s

Objective:
%s

Acceptance criteria:
%s
%s
Changed files:
%s

Respond with ONLY:
{"findings":[{"severity":"blocker"|"major"|"minor"|"info","title":"...","detail":"...","location":"file:line"}]}

Report only findings within your focus that are supported by evidence in the workspace. If there are none, report an empty findings list.`,
		pick.Specialty, wo.Title, pick.Reason, focusOf(pick.Specialty), wo.Objective, wo.Acceptance, clarification(wo), changed)
}
