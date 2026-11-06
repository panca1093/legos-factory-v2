# Foreman-led work order flow — spec (draft v1)

Status: implemented · 2026-11-05 · live runs with a TypeSafe key still to be done

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
 └─ escalate → stays in ARCHITECT_REVIEW, waiting for the human to approve or revise
QUEUED → IMPLEMENTING           Builders, one task per job, in dependency order
VERIFYING                       factory checks (gate)
SPECIALIST_REVIEW               Inspector specialists ∥ Architect verification
                                then Foreman validation notes → Jev → done gate
 ├─ done     → PR_PREPARATION → MERGE_READINESS → NEEDS_HUMAN_REVIEW (as today)
 ├─ fix      → IMPLEMENTING (shares the revision budget, max 2)
 └─ escalate → PR_PREPARATION → MERGE_READINESS → NEEDS_HUMAN_REVIEW, with the reason
NEEDS_HUMAN_REVIEW → human approves the PR → READY → PR opened
```

Existing state names are reused so the UI and stored orders keep working. `REQUIREMENT_ANALYSIS` stops being an empty step and becomes Foreman intake. **No new transition**: the done gate runs while the order is still in `SPECIALIST_REVIEW` (in the `evaluate` job, before `PR_PREPARATION`), so a fix uses the existing `SPECIALIST_REVIEW → IMPLEMENTING` transition. Readiness still ends in `NEEDS_HUMAN_REVIEW`, where the human approves the PR; `done` only means the gate found nothing to fix.

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

**Revisions edit the previous draft.** A re-draft (gate revise, human revision, Foreman-answer re-draft) receives the stored spec and task list (id, title, objective, scope, deps) with the notes, and is told to change only what the notes criticise, keep surviving task ids stable and say in `summary` what changed.

**Follow-up questions.** When the brief is unclear, the Architect replies `CLARIFY` with a question. The question goes to the **Foreman first**:

- Foreman reply `ANSWER`: the answer is stored and the Architect re-drafts.
- Foreman reply `ESCALATE`: the question goes to the human, as today.
- At most 2 Foreman answers per planning round. A third question escalates.

### 4. Plan gate (Foreman notes + Jev + code)

The Foreman agent writes review notes: issues found (`notes`) and optional per-criterion coverage remarks (`coverage: [{criterion, remark}]`, criteria numbered `c1..cn`). It does not return a decision; `APPROVE` / `REVISE` / `CLARIFY` are gone from the review reply. The review runs in the Foreman's planning session, which closes when the gate decides.

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

`revise` sends the Architect a numbered note the code builds from the answers that triggered it, with nothing dropped: each criterion with its full text, and for `scope_creep` / `task_overlap` the plan's task titles and scopes. The Foreman's notes follow in full. When `plan_revisions` has reached 2, a revise outcome becomes **escalate**. A human revision (or a human answer to a clarification) resets the counter. `plan_revisions` is a column on the work order (migration 010) and is also returned in the work order JSON.

Wiring: `approve` moves `ARCHITECT_REVIEW → QUEUED` with actor `factory` and queues the root tasks. `revise` moves to `PLANNING` and enqueues a plan job. `escalate` keeps the order in `ARCHITECT_REVIEW` with `plan_review = approved` (the marker the existing approve/revise UI keys on) and records a `plan_escalated` event with the reason; it does not use `WAITING_FOR_HUMAN`, which stays reserved for Architect questions. Plan task ids are sent to Jev as `t1..tn`, not the stored `wo_..._task` ids.

There is **one mode**. The `guided` / `assisted` / `delegated` autonomy levels are removed (backend, API and UI): an approve outcome starts the build with no human click, and a human is only asked when the table escalates. The manual Pause and Cancel controls stay. The create request still accepts an `autonomy` field and ignores it; the `autonomy` column stays unused and the work order JSON no longer carries it.

### 5. Build (factory + Builder)

Unchanged except:

- Tasks run strictly in dependency order (the dependency-loss bug is fixed in `451ffb5`).
- The Builder prompt names the task's own title, objective and `scope`, and lists the other tasks' titles as "other tasks — do not implement these".
- On a retry after failed checks, the Builder gets the failing check's name, command and the tail of its output.
- Every Builder prompt carries the spec (the one the Architect verifies against) and, on fix paths, the plan.
- Every fix path (`fix_findings`, `human_revision`, `human_retry`, `fix_done_gate`) sends one numbered **fix brief** in the job payload, nothing filtered or truncated: the human's reason, the done-gate issues and the Foreman's notes, all open findings (severity, title, detail, location), every non-`met` Architect verdict of the current revision (evidence, gap, fix) and every deviation. Open findings are those of the current inspection. The brief is stored in the `fix_requested` / `revision_requested` event, so a human retry after a failed fix build resends it. A retried plan task keeps its task section.
- Each fix brief ends with an "Already addressed in earlier revisions" bullet list: the items of the stored briefs of this order's earlier fix revisions, deduplicated, so a fix does not undo what an earlier one settled (the done gate otherwise see-saws between "criterion not met" and "unspecified behaviour"). Deliberate cap: only the 3 most recent earlier revisions are included, so the brief cannot grow without limit; the current numbered items are never capped. A retry resends the stored brief as is.
- The Builder must keep the "Already addressed" items intact and say so in its per-item reply if a numbered item would undo one. It must address every numbered item and answer in its one-line reply with `N: fixed (file:line)` or `N: not fixed because <reason>`; the reply is not parsed.

### 6. Verification (factory + Inspector ∥ Architect)

- factory runs the detected checks. Failure loops back to the Builder, max 2 (already implemented).
- When the checks pass, the Inspector specialists and the **Architect verification** run in parallel.
- Architect verification input: spec, plan, acceptance criteria numbered with the same `c1..cn` ids the gates use, the diff (changed files with hunks, within the same hunk budget as the done gate) and the check results. Output: `verdicts: [{criterion_id, status, evidence}]` with status `met` / `not_met` / `unclear` (case and `-`/space variants are normalised), plus `deviations` (work outside the spec or plan).
- The verification runs **only when Jev is on** (`Judge` set): nothing else reads the verdicts, so without a key the flow stays what it was before. With Jev on, the two run concurrently inside the `inspect` job; both are ordinary accounted runs. The verification is stored on the work order (`architect_report`, migration 010: revision, verdicts, deviations) and recorded as an `architect_verified` event and an `architect_verification` knowledge record. A permanent failure (invalid reply twice, budget) or a panic does not fail the inspection: it records `architect_verify_failed` and stores nothing, and the done gate then escalates (below). A transient error is returned after the specialists finish, so the job requeues; on its last attempt it is reported as `architect_verify_failed` instead. If the inspection returns early, the verification run is cancelled.

### 7. Done gate (Foreman notes + Jev + code)

The Foreman agent writes validation notes (`{"notes": "..."}`) from the Inspector findings, the check results and the Architect report. This is a separate Foreman run, not the planning session.

If there are no acceptance criteria, or the Architect report is missing or was made on an older revision, the done gate escalates without a Foreman run or a Jev request. If the Foreman validation itself fails permanently (or on the job's last attempt) the gate records an escalation `Foreman validation failed: …` and readiness continues to `NEEDS_HUMAN_REVIEW`; the order is never left in `SPECIALIST_REVIEW`.

The inspection's own findings stay open until the gate has decided (earlier revisions' findings are resolved by the clean inspection), so `open_findings` reflects the current inspection. The `evaluate` job is idempotent: it does nothing unless the order is in `SPECIALIST_REVIEW`, and finishes a half-done fix when it finds the order already back in `IMPLEMENTING`. A human retry after a failed fix build keeps the gate's note and brief. Without a Judge the done gate (and the Foreman validation run) is skipped entirely.

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

`fix` bumps the revision and returns to the Builder with reason `fix_done_gate` and the code-built note (rendered in the Builder prompt). It shares the `review_fixes` budget with blocker-finding remediation (`review_fixes < 2`). When the budget is spent, fix becomes escalate. `escalate` and `done` both continue to `PR_PREPARATION → MERGE_READINESS`; an escalation shows up as a `readiness_needs_human_review` event carrying the reason.

### 8. Ship = PR merged

Ship, the work order's last stage in the UI, is complete only when its pull request is **merged**. Human PR approval (`NEEDS_HUMAN_REVIEW → READY`, which opens the PR) is unchanged; state stays `READY` afterwards.

The factory polls at boot and then every 2 minutes (fixed, no config). It checks `READY` orders that have a `pr_ref` and no `pr_merged_at`, asking the provider (`GitProvider.PullRequestStatus`, from the stored `pr_ref`) for the PR state:

- merged → `pr_merged_at` is set, `pr_closed_at` is cleared, and event `pr_merged` is recorded (`actor: factory`);
- closed without merging → `pr_closed_at` is set once and event `pr_closed` is recorded once (`actor: factory`). Polling continues, because Ship means merged: a PR that is reopened and then merged still ends in `pr_merged`;
- open → nothing;
- transient provider error → logged and retried next tick; it never fails the order;
- permanent error (provider 400/401/403/404/422, a `pr_ref` the provider cannot parse, a project no longer bound to a git connection) → one `pr_track_failed` event (`actor: factory`, with the reason) and `pr_track_failed_at` is set, which stops polling that order.

The timestamp fields are exposed on the work order JSON. The event is appended before the timestamp is written, so a crash can repeat an event but never lose one. Events go through the normal publisher, so SSE clients see them.

#### Local folder projects (gh publishing)

With `FACTORY_AUTH_MODE=dev` (backend on the user's own machine) a project can be a local git folder with no connection. When the folder's `origin` is on github.com, `GitFor` returns a `gh`-CLI provider (`publish_via: "gh"`, `github_repo: owner/repo`); a bound connection always wins. The workspace is cloned from the folder, its `origin` is re-pointed at the folder's own origin URL (https or ssh) and it starts from the freshly fetched default branch (falling back to the folder's own copy of that branch, else preparation fails); the user's folder is never touched, and in gh mode the push always targets the remote URL, never a local path. The branch is pushed with the user's own git credentials (`gh auth setup-git` or their ssh keys) and the PR is found-or-created and tracked through `gh`. A missing or logged-out `gh` fails the open-PR job visibly (`pr_open_failed`) instead of skipping it. The `/api/v1/local/*` helpers (folder picker, inspect, clone, init, gh status) exist only in dev mode.

## Jev integration

- Endpoint `POST https://api.typesafe.ai/v1/systemone`, `Authorization: Bearer $TYPESAFE_API_KEY`.
- Model pinned in config: `TYPESAFE_MODEL`, default `jev-1.14.0`, never `jev-latest`, so gate behaviour only changes when we change it.
- Context limits: 64k tokens per request, 32k per question. The factory estimates size before sending; over the limit is escalate, never a silent cut of criteria or the plan. Diff hunks are trimmed to a budget, and the trim is recorded.
- Price: $0.042 per million input tokens; output is free. A gate request is roughly 5–20k tokens, well under one cent.
- A failed call is an error that escalates. It is never read as "no".
- **Jev is on exactly when `TYPESAFE_API_KEY` is set.** There is no separate setting. Jev is a paid API and cannot run without a key.
- No API key: the plan gate escalates every plan to the human (the Foreman still writes the brief, answers Architect questions and writes review notes), and the done gate is skipped, leaving today's deterministic readiness rules (checks pass, no open blockers). Settings → Integrations shows whether Jev is on (`jev_enabled` on the settings response, true iff `TYPESAFE_API_KEY` is set).

### Code layout

| File | Content |
|---|---|
| `internal/integration/typesafe.go` | HTTP client: request/response types, timeout, error mapping |
| `internal/usecase/ports.go` | `Judge` interface: `Ask(ctx, state any, questions map[string]Noul) (JudgeResult, error)`; `JudgeResult` carries answers, the exact request body, requested and answering model, and token usage |
| `internal/usecase/gates.go` | All question wording, thresholds and both truth tables in one file |
| `internal/usecase/foreman.go` → `factory.go` | The renamed `Factory` struct |

### Audit

Every gate run records an event (`actor: jev`, kind `plan_gate` or `done_gate`) with the exact request payload, the answers, the truth-table row that matched and the outcome. Gate outcomes are reproducible from the payload. The stored event holds the full payload; the live SSE copy drops `request` and `unsent_request` (they can be 100KB). With no Judge nothing is sent, so no gate event is recorded for it (the plan escalation is recorded as `plan_escalated`).

Event kinds added by this flow: `plan_reviewed` (Foreman notes), `plan_gate`, `plan_revision_requested`, `plan_escalated`, `architect_verified`, `architect_verify_failed`, `validation_started`, `foreman_validated`, `done_gate`, `fix_requested`.

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

Steps 1 to 7 are implemented. Not yet exercised: a live run against the real TypeSafe API.

Each step is its own commit with tests. Tests always use a fake `Judge`, so they never need a key.

## Decisions

1. The Foreman is one session from intake to the plan gate, and separate runs afterwards (see Foreman planning session).
2. One mode: the autonomy levels are removed.
3. The state keeps the name `ARCHITECT_REVIEW`: it is still the review of the Architect's spec and plan.
4. Jev is on when `TYPESAFE_API_KEY` is set, off otherwise.
