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

Respond with ONLY a JSON object:
{"decision":"APPROVE"|"REVISE"|"CLARIFY","notes":"<required for REVISE: what the Architect must change>",
 "question_text":"<required for CLARIFY: the single question a human must answer>"}

Use CLARIFY when the plan depends on a product decision nobody has made — never guess it.`,
		wo.Title, wo.Objective, wo.Acceptance, briefSection(wo)+clarification(wo), wo.Spec, scopedTaskList(tasks))
}

func implementPrompt(wo *model.WorkOrder, job model.Job) string {
	reason, _ := job.Payload["reason"].(string)
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
%s
Work only within the assigned workspace. When done, reply with one line describing what changed.`, job.TaskID, wo.Title, wo.Objective, wo.Acceptance, clarification(wo))
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
