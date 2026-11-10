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
	if answers := answered(wo.Clarifications); len(answers) > 0 {
		b.WriteString("\nQuestions answered so far:\n")
		for _, c := range answers {
			fmt.Fprintf(&b, "Q: %s\nA: %s\n", c.Question, c.Answer)
		}
	}
	if wo.ClarifAnswer != "" && findClarification(wo.Clarifications, wo.ClarifQuestion) == nil {
		fmt.Fprintf(&b, "\nA human answered a clarification:\nQ: %s\nA: %s\n", wo.ClarifQuestion, wo.ClarifAnswer)
	}
	return b.String()
}

// clarificationLog is the Foreman's answers as plain text, for scripted harnesses.
func clarificationLog(wo *model.WorkOrder) string {
	var parts []string
	for _, c := range answered(wo.Clarifications) {
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
%s%s
The brief says WHAT and WHY, never HOW. No file paths, line numbers or code. Keep it under %d words.
If a brief exists above, update it in place: fold in the answers, drop the questions they settle,
keep the rest. Do not start over.

Open questions are real unknowns that only a human can settle. At most %d, ranked scope >
security > behaviour > technical, each with options and a recommended answer. Anything else you
can settle with a reasonable default: record it under assumptions.

Respond with ONLY a JSON object (no prose, no code fences):

{"outcome":"<what must be true when this is done, in one or two sentences>",
 "why":"<the reason the request exists>",
 "domain_invariants":["<rules of the domain that must keep holding>"],
 "surfaces_touched":["<the user-facing or system-facing areas this changes>"],
 "constraints":["<behaviour or interfaces that must not change>"],
 "out_of_scope":["<what this work order must not do>"],
 "assumptions":["<default you chose for something unsaid>"],
 "open_questions":[{"question":"<one question>","options":["<a>","<b>"],"recommended":"<your pick and why>"}]}

Optionally add "code_facts": up to 10 strings "path:line — claim" for things you verified in the code that later
agents would otherwise re-read (one line, under 200 characters each; the line number is optional). They are kept
apart from the brief, so the no-paths rule does not apply to them.`,
		wo.Title, wo.Objective, wo.Acceptance, briefSection(wo)+clarification(wo), facts, briefWords, maxOpenQuestions)
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

func planPrompt(wo *model.WorkOrder, notes, facts string, prev []model.Task) string {
	extra := briefSection(wo) + clarification(wo)
	if wo.Spec != "" || len(prev) > 0 {
		extra += "\nPrevious draft:\nSpec:\n" + wo.Spec + "\nTasks:\n" + previousTasks(wo.ID, prev)
	}
	if notes != "" {
		extra += fmt.Sprintf(`
Revise the previous draft according to these notes:
%s

This is a targeted edit, not a redraft: keep every part the notes do not criticise, change only what is needed,
address every numbered item, keep task ids stable where the task survives, and say in summary what changed.
`, notes)
	}
	return fmt.Sprintf(`You are the Architect of a software engineering team.

Work order: %s

Objective:
%s

Acceptance criteria:
%s
%s
%s
Write the contract (spec) and decompose it into the smallest set of implementation tasks.
Respond with ONLY a JSON object (no prose, no code fences) matching:

{"decision":"APPROVE"|"CLARIFY",
 "summary":"<one line>",
 "spec":"<markdown contract, required when decision is APPROVE>",
 "tasks":[{"id":"task-1","title":"...","objective":"...","role":"builder","deps":["task-1"],"scope":["<file or area this task may touch>"]}],
 "question_text":"<only when decision is CLARIFY: the single question that must be answered>"}

The spec is under %d words, with these sections in order:
## Behaviour rules — numbered, one testable rule each.
## Decisions — a table with the columns "# | Decision | Why | Rejected alternative", one row per real choice.
## Interface contract — what callers or users see: names, inputs, outputs, errors. No code.
## Out of scope — what this work must not do.
## Acceptance — a table with the columns "AC | Verification | Kind". Every acceptance criterion has a row. Kind is "test" (a named automated test) or "check" (how a reviewer confirms it from the diff and the check output).
The spec says what and why. No file paths, line numbers or code in it.

Each task objective is under %d words. Cite spec sections ("Behaviour rule 2") instead of restating them. "scope" lists the files or areas the task may touch; file paths belong here, not in the spec.

Rules:
- If the behaviour is unclear and the objective, the brief and the clarifications do not settle it, use decision CLARIFY and ask — never guess or invent requirements. Choices about how, inside your role, are yours: decide and put them in the Decisions table.
- "deps" lists ids of tasks that must complete first; the graph must be acyclic and tasks must cover the objective.
- role is "builder" for implementation tasks.
- Optionally add "code_facts": up to 10 strings "path:line — claim" for things you verified in the code (one line, under 200 characters each). They are kept apart from the spec.`, wo.Title, wo.Objective, wo.Acceptance, extra, facts, specWords, taskWords)
}

func previousTasks(woID string, tasks []model.Task) string {
	var b strings.Builder
	for _, t := range tasks {
		deps := make([]string, len(t.Deps))
		for i, d := range t.Deps {
			deps[i] = strings.TrimPrefix(d, woID+"_")
		}
		fmt.Fprintf(&b, "- %s: %s — %s (scope: %s; deps: %s)\n", strings.TrimPrefix(t.ID, woID+"_"), t.Title, t.Objective,
			strings.Join(t.Scope, ", "), strings.Join(deps, ", "))
	}
	return b.String()
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

// foremanReviewPrompt asks for the contract analysis. On a re-review (scoped)
// prev holds the critical findings of the last round and the Foreman only
// checks those, plus a short regression scan.
func foremanReviewPrompt(wo *model.WorkOrder, tasks []model.Task, prev []reviewFinding, scoped bool) string {
	task := `Analyse the contract (spec, acceptance criteria and plan) against the brief. Read only; you do not approve
or reject: a separate gate decides from your findings. Report at most 10 findings. Severity:
- critical: a defect that stops the Builder from building the right thing. It MUST cite the contract
  ("c2", "spec: decisions 3", "brief: constraint 1"); a critical finding without a citation is invalid.
- high / low: worth knowing, does not block. These go to the Builder as notes.
Layer: "contract" when the spec or plan is incomplete or wrong (the Architect fixes it); "requirement"
when the brief itself is wrong or silent (you fix it). For a requirement finding put the brief change in "fix",
or put the question in "question" when only a human can decide it.
Also state, for every acceptance criterion, how the spec's AC -> verification table verifies it: "test", "check" or "missing".`
	finding := `{"severity":"critical|high|low","layer":"contract|requirement","cites":"c2","issue":"...","fix":"...","question":"only for a requirement finding that needs a human"}`
	shape := `{"findings":[` + finding + `],
 "coverage":[{"criterion":"c1","verification":"test|check|missing"}]}`
	if scoped {
		var b strings.Builder
		for _, f := range prev {
			b.WriteString("- " + findingLine(f) + "\n")
		}
		if b.Len() == 0 {
			b.WriteString("(none)\n")
		}
		task = `This is a re-review after the Architect revised the contract. Do NOT run a fresh full review.
1. For each previous critical finding below, say whether the revised contract resolves it.
2. Run a short regression scan for problems the revision introduced. A new critical finding is allowed only
   when it cites the contract; anything else is high or low.
Previous critical findings:
` + b.String()
		shape = `{"resolved":[{"id":"F1","status":"resolved|unresolved"}],
 "findings":[<only NEW findings from the regression scan, each exactly ` + finding + `; [] when none>],
 "coverage":[{"criterion":"c1","verification":"test|check|missing"}]}`
	}
	return fmt.Sprintf(`You are the Foreman, orchestrating a software engineering team. The Architect drafted
the contract below. Review it independently before any code is written.

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
%s

Respond with ONLY a JSON object:
%s`,
		wo.Title, wo.Objective, criteriaSection(splitCriteria(wo.Acceptance)), briefSection(wo)+clarification(wo), wo.Spec, scopedTaskList(tasks), task, shape)
}

// convergePrompt asks the Architect to compare the change with the contract. Earlier "done" claims are not input:
// every gap must stand on the diff, the checks or the repository.
func convergePrompt(wo *model.WorkOrder, tasks []model.Task, diff []FileDiff, checks, builderNotes string) string {
	return fmt.Sprintf(`You are the Architect. The Builders finished the tasks below. Compare the change with the contract
and list every gap. Do not trust any earlier claim that work is done: check the diff, the check results and the
repository yourself. You may read the repository.

Work order: %s

Acceptance criteria:
%s
Spec (with its amendments; the acceptance table maps each criterion to its verification):
%s

Tasks built so far:
%s%s
Check results:
%s
Diff against the base:
%s
A gap is a difference between the change and the contract:
- type: "missing" (the contract asks for it, the change lacks it), "partial" (done in part), "contradicts" (the change
  does the opposite), "unrequested" (the change adds what the contract does not ask for).
- layer: "code" when the change is wrong or incomplete (a Builder task fixes it), "contract" when the spec is silent,
  wrong or ambiguous (the spec is amended), "requirement" when the requirement itself is wrong or undecided (the Foreman decides).
- cites: the acceptance criterion ("c3") or spec section ("spec §2") the gap is measured against. Cite a criterion only when it is unmet.
- evidence: "file:line" or the test name that shows the gap. A gap without evidence is not a gap.
- fix: one or two sentences on what must change.
At most %d gaps, the most important first. Set "converged" to true when no gap needs work.

Respond with ONLY a JSON object:
{"gaps":[{"type":"missing|partial|contradicts|unrequested","layer":"code|contract|requirement","cites":"c3 | spec §2","evidence":"file:line or test name","fix":"..."}],
 "converged":true|false}`,
		wo.Title, criteriaSection(splitCriteria(wo.Acceptance)), wo.Spec, scopedTaskList(tasks), builderNotes, checks, diffSection(diff), maxConvergeGaps)
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

// answered drops the plan review's notes, which are not answers to anyone's question.
func answered(list []model.Clarification) []model.Clarification {
	out := make([]model.Clarification, 0, len(list))
	for _, c := range list {
		if c.By != builderNotesBy {
			out = append(out, c)
		}
	}
	return out
}

func specSection(wo *model.WorkOrder) string {
	if wo.Spec == "" {
		return ""
	}
	return "\nSpec (the Architect verifies the result against this):\n" + wo.Spec + "\n"
}

// planNotesSection is for Builder prompts only: no other role is shown the review notes.
func planNotesSection(wo *model.WorkOrder) string {
	var notes []string
	for _, c := range wo.Clarifications {
		if c.By == builderNotesBy {
			notes = append(notes, "- "+c.Question+": "+c.Answer)
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return "\nReview notes (do not block the work; keep them in mind):\n" + strings.Join(notes, "\n") + "\n"
}

const notesHeading = "Notes for review:"

const notesInstruction = `Then end your reply with a section headed exactly "` + notesHeading + `" followed by at most 5 one-line items, each something you left unverified, guessed, or that needs a change outside your task's scope (the spec, another task). If there is nothing, write the single word "none".`

// fixContext is what every fix prompt shares: the spec, the plan and the numbered brief.
func fixContext(wo *model.WorkOrder, job model.Job, tasks []model.Task) (ctx, tail string) {
	ctx = specSection(wo) + planNotesSection(wo)
	if len(tasks) > 0 {
		ctx += "\nPlan:\n" + scopedTaskList(tasks)
	}
	tail = "Work only within the assigned workspace. When done, reply with one line describing what changed.\n\n" + notesInstruction
	if brief, _ := job.Payload["brief"].(string); brief != "" {
		ctx += "\nFix brief. Address every numbered item and change only what the items require. Items under \"Already addressed\" were fixed in earlier revisions and must still hold after your changes; if resolving a numbered item would undo one, say so in your per-item reply instead of undoing it:\n" + brief + "\n"
		tail = `Work only within the assigned workspace. When done, reply with one line stating for each item number either "N: fixed (file:line)" or "N: not fixed because <reason>".` + "\n\n" + notesInstruction
	}
	return ctx, tail
}

func implementPrompt(wo *model.WorkOrder, job model.Job, tasks []model.Task) string {
	reason, _ := job.Payload["reason"].(string)
	fixCtx, tail := fixContext(wo, job, tasks)
	if failed, _ := job.Payload["failed_checks"].(string); reason == "fix_failed_verification" && failed != "" {
		return fmt.Sprintf(`You are the Builder. Work order %q failed deterministic verification.

Verification failed on these checks; fix the cause. Do not edit .legos/checks.json or weaken tests to make them pass.

%s

Objective: %s

Acceptance criteria:
%s
%s%s
%s`,
			wo.Title, failed, wo.Objective, wo.Acceptance, clarification(wo), fixCtx, tail)
	}
	if reason != "" {
		return fmt.Sprintf(`You are the Builder. Work order %q requires remediation.

Reason: %s
Objective: %s

Acceptance criteria:
%s
%s%s%s
Implement the change and any behavioral tests needed to satisfy the criteria.
%s`,
			wo.Title, reason, wo.Objective, wo.Acceptance, clarification(wo), fixCtx, taskSection(job, tasks), tail)
	}
	return fmt.Sprintf(`You are the Builder. Implement task %q of work order %q.

Objective: %s

Acceptance criteria:
%s
%s%s%s
Work only within the assigned workspace. When done, reply with one line describing what changed.

%s

Do not guess what should happen. If the task or spec is ambiguous about behaviour, stop without finishing and end
your reply with a final line "%s <one question>"; a reply that asks a question has no notes section. Decide anything about how yourself (naming, layout, a choice between
equivalent implementations) and state it as an assumption in your reply.`, job.TaskID, wo.Title, wo.Objective, wo.Acceptance, clarification(wo), specSection(wo)+planNotesSection(wo), taskSection(job, tasks), notesInstruction, needsContextMarker)
}

// architectAnswerPrompt asks the Architect to settle a Builder's behaviour question from the contract.
func architectAnswerPrompt(wo *model.WorkOrder, tasks []model.Task, question string) string {
	return fmt.Sprintf(`You are the Architect. A Builder, implementing your plan, asks what should happen.

Work order: %s

Acceptance criteria:
%s
%s%s
Plan:
%s
Builder's question:
%s

Answer it only if the request, the brief, the spec or the repository settles it, or it is a small gap in the spec you
can close. If it needs a product decision nobody has made, escalate — never guess it.
Respond with ONLY a JSON object:
{"decision":"ANSWER"|"ESCALATE","answer":"<required for ANSWER>","reason":"<why, especially for ESCALATE>"}`,
		wo.Title, wo.Acceptance, briefSection(wo)+specSection(wo), clarification(wo), scopedTaskList(tasks), question)
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

// A specialist that stays silent on an unfixed finding closes it: only a note may be dropped, never a blocker or an unmet criterion.
const earlierFindingsHead = "Already reported as notes in earlier revisions. Do not repeat one as a note. If one is still present and is a blocker or leaves an acceptance criterion unmet, report it again with fresh evidence:"

func inspectPrompt(wo *model.WorkOrder, pick SpecialistPick, files []string, builderNotes string, earlier []string) string {
	changed := "(no file list available)"
	if len(files) > 0 {
		if len(files) > 50 {
			files = append(files[:50:50], fmt.Sprintf("… and %d more", len(files)-50))
		}
		changed = strings.Join(files, "\n")
	}
	spec, reported := "", ""
	if wo.Spec != "" {
		spec = "\nSpec (the contract the change was built against):\n" + wo.Spec + "\n"
	}
	if len(earlier) > 0 {
		reported = "\n" + earlierFindingsHead + "\n- " + strings.Join(earlier, "\n- ") + "\n"
	}
	return fmt.Sprintf(`You are the %s Inspector. Independently review the implementation of work order %q in the workspace.
You were selected because the change %s.

Focus: %s

Objective:
%s

Acceptance criteria:
%s
%s%s
Changed files:
%s
%s%s
Respond with ONLY:
{"findings":[{"severity":"blocker"|"major"|"minor"|"info","title":"...","detail":"...","location":"file:line","cites":"c2"}]}

Report only findings within your focus that are supported by evidence in the workspace. If there are none, report an empty findings list.
Only a blocker, or a finding that cites an acceptance criterion in "cites" (leave it out otherwise; cite only a criterion the change does not meet), sends the work back to the Builder. Every other finding is a note for the next Builder run.
A choice recorded in the spec's Decisions table or Behaviour rules is settled: do not report the change following it as a finding. If you believe a decision itself breaks an acceptance criterion, report ONE finding that cites both the decision and the criterion.`,
		pick.Specialty, wo.Title, pick.Reason, focusOf(pick.Specialty), wo.Objective, wo.Acceptance, clarification(wo), spec, changed, builderNotes, reported)
}
