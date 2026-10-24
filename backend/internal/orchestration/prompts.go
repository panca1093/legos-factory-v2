package orchestration

import (
	"fmt"

	"legos-factory/backend/internal/model"
)

// Prompts request schema-validated JSON. Structured outputs are parsed and
// validated before any state transition; free text never drives control flow.

func planPrompt(wo *model.WorkOrder) string {
	return fmt.Sprintf(`You are the Architect of a software engineering team.

Work order: %s

Objective:
%s

Acceptance criteria:
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
- role is "builder" for implementation tasks.`, wo.Title, wo.Objective, wo.Acceptance)
}

func reviewPrompt(wo *model.WorkOrder) string {
	return fmt.Sprintf(`You are the Architect. A human clarified the requirements of work order %q:

Answer: %s

The original objective was:
%s

Confirm the plan is now complete. Respond with ONLY:
{"decision":"APPROVE","notes":"..."}
or
{"decision":"CLARIFY","question_text":"..."}`, wo.Title, wo.ClarifAnswer, wo.Objective)
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

func inspectPrompt(wo *model.WorkOrder) string {
	return fmt.Sprintf(`You are the Inspector. Independently review the implementation of work order %q in the workspace.

Objective:
%s

Acceptance criteria:
%s

Respond with ONLY:
{"findings":[{"severity":"blocker"|"major"|"minor"|"info","title":"...","detail":"...","location":"file:line"}]}

Report findings that are supported by evidence in the workspace. If the implementation satisfies the criteria, report an empty findings list.`,
		wo.Title, wo.Objective, wo.Acceptance)
}
