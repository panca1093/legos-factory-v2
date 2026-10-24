package usecase

import (
	"fmt"
	"strings"

	"legos-factory/backend/internal/model"
)

// Prompts request schema-validated JSON. Structured outputs are parsed and
// validated before any state transition; free text never drives control flow.

func planPrompt(wo *model.WorkOrder, notes string) string {
	extra := ""
	if wo.ClarifAnswer != "" {
		extra += fmt.Sprintf("\nA human answered a clarification:\nQ: %s\nA: %s\n", wo.ClarifQuestion, wo.ClarifAnswer)
	}
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
Decompose this objective into the smallest set of implementation tasks.
Respond with ONLY a JSON object (no prose, no code fences) matching:

{"decision":"APPROVE"|"CLARIFY",
 "summary":"<one line>",
 "tasks":[{"id":"task-1","title":"...","objective":"...","role":"builder","deps":["task-1"]}],
 "question_text":"<only when decision is CLARIFY: the single question that must be answered by a human>"}

Rules:
- If a product decision cannot be inferred from the objective, use decision CLARIFY and ask a human — never invent product requirements.
- "deps" lists ids of tasks that must complete first; the graph must be acyclic and tasks must cover the objective.
- role is "builder" for implementation tasks.`, wo.Title, wo.Objective, wo.Acceptance, extra)
}

func foremanReviewPrompt(wo *model.WorkOrder, tasks []model.Task) string {
	return fmt.Sprintf(`You are the Foreman, orchestrating a software engineering team. The Architect drafted
the plan below. Review it independently before any code is written.

Work order: %s
Objective:
%s
Acceptance criteria:
%s

Plan:
%s
Check requirement completeness, task dependencies, scope and assumptions, edge cases,
risks, testing strategy and coverage of every acceptance criterion.

Respond with ONLY a JSON object:
{"decision":"APPROVE"|"REVISE"|"CLARIFY","notes":"<required for REVISE: what the Architect must change>",
 "question_text":"<required for CLARIFY: the single question a human must answer>"}

Use CLARIFY when the plan depends on a product decision nobody has made — never guess it.`,
		wo.Title, wo.Objective, wo.Acceptance, taskList(tasks))
}

func implementPrompt(wo *model.WorkOrder, job model.Job) string {
	reason, _ := job.Payload["reason"].(string)
	if reason != "" {
		return fmt.Sprintf(`You are the Builder. Work order %q requires remediation.

Reason: %s
Objective: %s

Acceptance criteria:
%s

Implement the change and any behavioral tests needed to satisfy the criteria.
Work only within the assigned workspace. When done, reply with one line describing what changed.`,
			wo.Title, reason, wo.Objective, wo.Acceptance)
	}
	return fmt.Sprintf(`You are the Builder. Implement task %q of work order %q.

Objective: %s

Acceptance criteria:
%s

Work only within the assigned workspace. When done, reply with one line describing what changed.`, job.TaskID, wo.Title, wo.Objective, wo.Acceptance)
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

Changed files:
%s

Respond with ONLY:
{"findings":[{"severity":"blocker"|"major"|"minor"|"info","title":"...","detail":"...","location":"file:line"}]}

Report only findings within your focus that are supported by evidence in the workspace. If there are none, report an empty findings list.`,
		pick.Specialty, wo.Title, pick.Reason, specialtyFocus[pick.Specialty], wo.Objective, wo.Acceptance, changed)
}
