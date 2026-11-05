# Foreman-led work order flow — spec (draft v1)

Status: draft for review · 2026-11-05 · not implemented

## Why

Work order `wo_9680a69b-18d` ended with correct code, but the run showed where the flow is weak:

- The plan swung between 1, 3, 1 and 5 tasks across human revisions. The Architect started from the raw request each time, with no brief.
- The Foreman agent approved the 1-task plan three times; the human rejected it three times. That gate added a step but no value.
- Two Builders implemented the whole feature inside single tasks. Nobody compared the result with the plan.
- "Foreman" names both the Go struct that runs the state machine and the agent that reviews plans. Event logs mix them under one actor.

## Goals

1. The Foreman agent leads every work order: intake, briefing the Architect, plan review and final validation.
2. Fewer human interventions. The Foreman approves plans or sends them back for revision by itself, and only escalates when the rules say a human must decide.
3. Approve, revise and escalate are **decided in Go code** from typed judgments returned by TypeSafe's Jev model. An LLM never makes the decision on its own.
4. The Architect checks the built result against its own spec and plan.
5. Code, logs and the UI make it clear what is deterministic code and what is an agent.

## Non-goals

- Removing human PR approval. Opening a PR stays a human decision.
- Letting Jev or any agent override failing checks or open blocker findings.
- A Go SDK for TypeSafe. A small HTTP client is enough.

## Roles

| Actor | Kind | Responsibility |
|---|---|---|
| `factory` | Go code (renamed from the `Foreman` struct) | State machine, scheduling, workspace, checks, gates, truth tables, PRs |
| Foreman | Agent (Opus) | Intake brief, answers Architect questions, plan review notes, final validation notes |
| Architect | Agent | Spec and plan from the brief, follow-up questions, verification against its spec |
| Builder | Agent | One task at a time, within that task's scope |
| Inspector | Agent (specialists) | Review findings on the diff |
| Jev | TypeSafe System One model | Typed judgments (Nouls) with calibrated probabilities |
| Human | You | Escalations and PR approval |

Event actors follow the same split: `factory` for code, the agent name for agents, `jev` for judgment requests, `human` for you.

## Flow

```
CREATED
 └─ factory: clone, detect checks, baseline, repo facts
REQUIREMENT_ANALYSIS            Foreman intake → brief
TASK_DECOMPOSITION / SPEC_AND_PLAN_GENERATION
 └─ Architect drafts spec + plan from the brief
    └─ CLARIFY → Foreman answers, or escalates to WAITING_FOR_HUMAN
ARCHITECT_REVIEW                Foreman review notes → Jev → plan gate
 ├─ approve  → QUEUED
 ├─ revise   → PLANNING → Architect re-drafts (max 2 rounds)
 └─ escalate → WAITING_FOR_HUMAN
QUEUED → IMPLEMENTING           Builders, one task per job, in dependency order
VERIFYING                       factory checks (gate)
SPECIALIST_REVIEW               Inspector specialists ∥ Architect verification
MERGE_READINESS                 Foreman validation notes → Jev → done gate
 ├─ done     → READY
 ├─ fix      → IMPLEMENTING (shares the revision budget, max 2)
 └─ escalate → NEEDS_HUMAN_REVIEW
READY → human approves PR → PR opened
```

Existing state names are reused so the UI and stored orders keep working. `REQUIREMENT_ANALYSIS` stops being an empty step and becomes Foreman intake. One new transition: `MERGE_READINESS → IMPLEMENTING` (fix).

### 1. Factory preparation (deterministic)

Already done at creation: clone the workspace and detect the checks. Add a **repo facts** record, built by code only:

- detected toolchains and checks, and the baseline result of each check;
- top-level directory tree (depth 2, vendor and build dirs skipped);
- repo docs and playbooks already indexed by the knowledge index;
- tools available on the host (`go`, `node`, `npm`, `cargo`, `git` versions).

The Foreman reads these facts instead of rediscovering them. Facts never come from an LLM.

### 2. Foreman intake (agent)

Input: the request, acceptance criteria, repo facts, knowledge pointers.
Output (`intakeReply`):

```json
{
  "brief": "What to build and where, in the Foreman's words",
  "constraints": ["Keep Cart.add behaviour unchanged", "..."],
  "risks": ["..."],
  "suggested_task_shape": "Small tasks, one per acceptance criterion"
}
```

The brief is stored on the work order and shown on the Blueprint tab.

#### Foreman planning session

From intake until the plan gate decides, the Foreman is **one agent session**: intake, answers to Architect questions and review notes are prompts in the same session, so it remembers its own brief and earlier answers. After the plan gate (validation at the end) the Foreman runs as separate runs, primed with the stored brief and decisions.

- The factory keeps the ACP connection and session open between prompts (today every run opens and closes its own).
- The session closes when the plan gate decides, on escalation to a human (waits can be long), on cancel, or on backend restart.
- After a close, the next Foreman prompt starts a new session primed with the stored facts: brief, Q&A so far, review notes. Planning never depends on a session surviving.

### 3. Architect spec and plan (agent)

Input: brief, request, criteria, repo facts.
Output: `planReply` gains a `spec` field (behaviour, interfaces, edge cases) next to `summary` and `tasks`. Each task gets `scope`: the files or areas it may touch.

**Follow-up questions.** When the brief is unclear, the Architect replies `CLARIFY` with a question. The question goes to the **Foreman first**:

- Foreman reply `ANSWER`: the answer is stored and the Architect re-drafts.
- Foreman reply `ESCALATE`: the question goes to the human, as today.
- At most 2 Foreman answers per planning round. A third question escalates.

### 4. Plan gate (Foreman notes + Jev + code)

The Foreman agent writes review notes: issues found and how each acceptance criterion is covered. It does not return a decision.

The factory then sends **one** Jev request. State:

```json
{
  "request": "...",
  "acceptance_criteria": {"c1": "...", "c2": "..."},
  "brief": "...",
  "spec": "...",
  "plan": {"t1": {"title": "...", "objective": "...", "scope": ["..."], "deps": []}},
  "foreman_review": "..."
}
```

Questions (all Nouls; ids are not sent to the model):

| Id | Question | Fan-out |
|---|---|---|
| `covers_<c>` | Does a task in `plan` implement `acceptance_criteria.<c>`? | per criterion |
| `scope_creep` | Does `plan` include work that `request` and `acceptance_criteria` do not ask for? | once |
| `touches_schema` | Does any task in `plan` change a database schema or migration? | once |
| `touches_authentication` | Does any task in `plan` change how users or services prove who they are? | once |
| `touches_authorization` | Does any task in `plan` change who is allowed to do what? | once |
| `touches_payments` | Does any task in `plan` change payment, billing or money-movement code? | once |
| `touches_secrets` | Does any task in `plan` change how secrets are stored, read or passed? | once |
| `task_overlap` | Do two tasks in `plan` implement the same behaviour? | once |
| `review_unresolved` | Does `foreman_review` report a problem that `plan` does not address? | once |

Thresholds: yes > 0.7, no < 0.3, between = unsettled.

Truth table, first matching row wins:

| # | Condition | Outcome |
|---|---|---|
| 1 | no acceptance criteria, Jev call failed, no API key, state over the context limit, or any asked question unanswered | escalate |
| 2 | `touches_schema` or any `touches_*` is yes | escalate |
| 3 | any `covers_*` is no, or `scope_creep` / `task_overlap` / `review_unresolved` is yes | revise |
| 4 | any answer unsettled | revise |
| 5 | otherwise | approve |

`revise` sends the Architect a note the code builds from the answers that triggered it (e.g. "criterion c3 is not covered; tasks t2 and t4 overlap") plus the Foreman's notes. When `plan_revisions` has reached 2, a revise outcome becomes **escalate**. A human revision resets the counter.

There is **one mode**. The `guided` / `assisted` / `delegated` autonomy levels are removed: an approve outcome starts the build with no human click, and a human is only asked when the table escalates. The manual Pause and Cancel controls stay.

### 5. Build (factory + Builder)

Unchanged except:

- Tasks run strictly in dependency order (the dependency-loss bug is fixed in `451ffb5`).
- The Builder prompt names the task's `scope` and lists the other tasks as "do not implement".
- On a retry after failed checks, the Builder gets the failing check's name, command and the tail of its output.

### 6. Verification (factory + Inspector ∥ Architect)

- factory runs the detected checks. Failure loops back to the Builder, max 2 (already implemented).
- When the checks pass, the Inspector specialists and the **Architect verification** run in parallel.
- Architect verification input: spec, plan, diff, check results. Output: per criterion `met` / `not_met` / `unclear` with evidence, plus `deviations` (work outside the spec or plan).

### 7. Done gate (Foreman notes + Jev + code)

The Foreman agent writes validation notes from the Inspector findings and the Architect report.

The factory sends one Jev request. State: criteria, spec, a diff summary (per file: path, lines changed, the hunks up to a size budget; lock and generated files keep counts only), check results, open findings, the Architect verdicts (`architect_verdicts`: criterion, `met`/`not_met`/`unclear`, evidence) and `deviations`, and the Foreman notes.

| Id | Question |
|---|---|
| `met_<c>` | Does `diff` implement the behaviour `acceptance_criteria.<c>` describes? (diff only; the Architect is judged by code, below) |
| `deviates` | Does the diff change behaviour that `spec` does not describe? |
| `notes_unresolved` | Do `foreman_notes` report a problem that is not fixed in the diff? |

Truth table:

| # | Condition | Outcome |
|---|---|---|
| 1 | a check failed or a blocker finding is open | handled by the existing loops; never done |
| 2 | no acceptance criteria, Jev call failed or not configured, state over the limit, or any asked question unanswered | escalate |
| 3 | any `met_*` is no, `deviates` / `notes_unresolved` is yes, the Architect marks a criterion `not_met`, or the Architect reports deviations | fix |
| 4 | any answer unsettled, or the Architect is `unclear` about a criterion or gave no verdict for it | fix |
| 5 | otherwise | done → READY |

`fix` returns to the Builder with a code-built note and uses the same revision budget as failed checks. When the budget is spent, fix becomes escalate (NEEDS_HUMAN_REVIEW).

## Jev integration

- Endpoint `POST https://api.typesafe.ai/v1/systemone`, `Authorization: Bearer $TYPESAFE_API_KEY`.
- Model pinned in config: `TYPESAFE_MODEL`, default `jev-1.14.0`, never `jev-latest`, so gate behaviour only changes when we change it.
- Context limits: 64k tokens per request, 32k per question. The factory estimates size before sending; over the limit is escalate, never a silent cut of criteria or the plan. Diff hunks are trimmed to a budget, and the trim is recorded.
- Price: $0.042 per million input tokens; output is free. A gate request is roughly 5–20k tokens, well under one cent.
- A failed call is an error that escalates. It is never read as "no".
- **Jev is on exactly when `TYPESAFE_API_KEY` is set.** There is no separate setting. Jev is a paid API and cannot run without a key.
- No API key: the plan gate escalates every plan to the human (the Foreman still writes the brief, answers Architect questions and writes review notes), and the done gate is skipped, leaving today's deterministic readiness rules (checks pass, no open blockers). Settings → Integrations shows whether Jev is on.

### Code layout

| File | Content |
|---|---|
| `internal/integration/typesafe.go` | HTTP client: request/response types, timeout, error mapping |
| `internal/usecase/ports.go` | `Judge` interface: `Ask(ctx, state any, questions map[string]Noul) (JudgeResult, error)`; `JudgeResult` carries answers, the exact request body, requested and answering model, and token usage |
| `internal/usecase/gates.go` | All question wording, thresholds and both truth tables in one file |
| `internal/usecase/foreman.go` → `factory.go` | The renamed `Factory` struct |

### Audit

Every gate run records an event (`actor: jev`) with the exact request payload, the answers, the truth-table row that matched and the outcome. Gate outcomes are reproducible from the payload.

### Privacy

The plan gate sends the request, criteria, brief, spec, plan and Foreman notes. The done gate also sends diff hunks. All of it goes to TypeSafe. Setting `TYPESAFE_API_KEY` is the opt-in.

## Testing

- Unit: both truth tables with a fake `Judge`, covering every row, the thresholds and the revision budget.
- Unit: the Architect → Foreman → human clarification path.
- Integration: the existing lifecycle tests run with a fake `Judge`.
- Live probing: only with your permission and a budget, on recorded payloads from real orders (e.g. the plans from `wo_9680a69b-18d`). Payloads and answers are kept so they can be rerun when the model changes.

## Implementation order

| # | Step | Needs a TypeSafe key |
|---|---|---|
| 1 | Rename `Foreman` struct to `Factory`; split event actors (`factory` vs `foreman`). No behaviour change. | no |
| 2 | Repo facts, Foreman intake (brief) in a planning session; the Architect plans from the brief and returns `spec` and task `scope`. | no |
| 3 | Architect clarification routed through the Foreman session. | no |
| 4 | Jev client, `Judge` port, plan gate and truth table; remove the autonomy levels (API, UI; the column stays unused). | yes, for live runs |
| 5 | Builder scope and failed-check output in prompts. | no |
| 6 | Architect verification in parallel with the Inspector. | no |
| 7 | Foreman validation and the done gate. | yes, for live runs |

Each step is its own commit with tests. Tests always use a fake `Judge`, so they never need a key.

## Decisions

1. The Foreman is one session from intake to the plan gate, and separate runs afterwards (see Foreman planning session).
2. One mode: the autonomy levels are removed.
3. The state keeps the name `ARCHITECT_REVIEW`: it is still the review of the Architect's spec and plan.
4. Jev is on when `TYPESAFE_API_KEY` is set, off otherwise.
