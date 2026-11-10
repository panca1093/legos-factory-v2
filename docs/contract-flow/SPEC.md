# Contract-first work order flow — spec (v1)

Status: implemented · 2026-11-07 · PRs #44–#50 merged · no live work order run yet · supersedes parts of `docs/foreman-flow/SPEC.md` (§4 plan gate, §6 Architect verification, §7 done gate)

## Why

Two recent work orders show the cost of the current flow:

- `wo_15381843-2a1` (chat image attachments) spent $2.31 on six Opus runs in planning and wrote no code. The plan gate asked for revisions twice: `review_unresolved` is close to always "yes" because the Foreman reviews the plan being judged, and unsettled `scope_creep` / `task_overlap` answers turned into vague "it is unclear whether…" notes. The brief was a design document, the spec ran to about 2,500 words with line numbers and error strings, and each task restated the spec. The human cancelled it because nobody could read it.
- `wo_c6680cdd-92d` reached revision 11 for $19.46. The done gate see-sawed between "criterion not met" and "diff deviates from spec". The Architect wrote a literal spec and then verified it literally. When the spec was silent, the Builder guessed, and the guess was flagged as a deviation.

The method below is taken from three references and adapted to the factory: the loan-platform `voyage` skill, the `zone-v2` plugin and `github/spec-kit`. We keep their methods and drop their details (Jira keys, two PRs per ticket, Go-specific test tiers).

## Principles

| # | Principle | Source |
|---|---|---|
| P1 | **What before how.** The brief and the spec say what and why. How lives in tasks and the facts sheet. | spec-kit specify, zone Coach "map, don't prescribe" |
| P2 | **Contract first.** An approved contract (spec + AC → verification table + tasks) is the standard every later step is judged by. | voyage PR #1 |
| P3 | **Ask, don't guess.** An ambiguity about behaviour goes back upstream: Builder → Architect → Foreman → human. | user rule; spec-kit `[NEEDS CLARIFICATION]` |
| P4 | **The contract is read-only once approved.** It only changes by an amendment that carries a reason. | voyage TRD as read-only input |
| P5 | **Route findings by layer.** Code wrong → Builder. Contract incomplete → Architect amendment. Requirement wrong → Foreman. | voyage Leg 4 |
| P6 | **Append, never redo.** Gaps after a build become new tasks. Earlier tasks and their fixes stay. | spec-kit converge |
| P7 | **A blocker must cite the contract.** Re-review checks only the previous blockers. | zone Center |
| P8 | **Jev judges, code decides.** Gates send typed questions to Jev, and a truth table in Go picks the outcome. | foreman-flow (unchanged) |
| P9 | **Caps everywhere.** Questions, findings, rounds and document length all have limits. | spec-kit |

## Artifacts

Each artifact has one reader and a length budget. Budgets are enforced in the prompt and checked by code (over budget = one rewrite request, then accepted with a warning).

| Artifact | Author | Reader | Budget | Content |
|---|---|---|---|---|
| Brief | Foreman | human, Architect | ~350 words | outcome, why, domain invariants, surfaces touched, constraints, out of scope, assumptions, open questions (≤3) |
| Spec (contract) | Architect | human, all agents | ~750 words | behaviour rules, decisions table, interface contract, out of scope, AC → verification table |
| Tasks | Architect | Builder | ~150 words each | outcome, scope (files), deps, how it is verified; cites spec sections instead of restating them |
| Facts sheet | Foreman, Architect | agents only | max 40 facts per work order (newest win), ≤200 chars each | `path:line — claim`, bound to `kb_base_sha` |
| Clarifications log | factory | all | — | `Q → A (who, when)`, appended to the spec |

No file paths, line numbers or code go in the brief or the spec. They belong in tasks (scope) and the facts sheet.

### Decisions table (in the spec)

`# | Decision | Why | Rejected alternative`. One row per real choice. This replaces long rationale paragraphs.

### AC → verification table (in the spec)

`AC | Verification | Kind`, where Kind is `test` (a named automated test) or `check` (how a reviewer confirms it from the diff and check output; e2e acceptance is a later step). Every acceptance criterion has a row. Gherkin `.feature` files are a later, per-project option.

### Facts sheet

The facts sheet stops agents from re-reading the repository on every run (Foreman runs in `wo_15381843-2a1` re-grepped `BodyLimit` and `promptCapabilities` three times).

- The Foreman writes facts at intake and the Architect may append at planning. Every role reads them. (The Builder reply is free text, so it does not write facts.)
- Prompt rule: trust the facts; open a file only for something they do not cover.
- A fact is stale when `git diff --no-renames --name-only <sha>` (working tree, so uncommitted edits count) touches its file. Stale facts are hidden, with no LLM involved.
- At Port (below), stable facts become a **Code Map** document in the project's knowledge. It is promoted automatically (no human verify); a later contradicting fact replaces the line.
- Hidden in the UI by default, like the journal.

## Flow

```
CREATED → factory prep (unchanged)
REQUIREMENT_ANALYSIS   1 Intake: Foreman brief + facts; ≤3 open questions → human clarify
SPEC_AND_PLAN_GENERATION 2 Contract: Architect spec + tasks; may ask the Foreman
ARCHITECT_REVIEW       3 Contract gate: Foreman analysis → Jev → approve | revise | escalate
IMPLEMENTING           4 Build: Builder per task; may ask the Architect
VERIFYING              5 Checks (unchanged)
SPECIALIST_REVIEW      6 Converge: Architect gap list ∥ Inspector → Jev → converged | append | escalate
PR_PREPARATION …       7 Ship (unchanged) + Port: facts → Code Map
```

Existing state names are kept. Converge runs inside `SPECIALIST_REVIEW`, as the done gate does today.

### 1. Intake (Foreman)

- Writes the brief within its budget, and the facts sheet.
- Marks real unknowns as open questions, at most 3, ranked scope > security > behaviour > technical. Anything else gets a reasonable default recorded under Assumptions.
- When open questions remain, the human gets them one at a time (max 5 per work order) as multiple choice with a recommended answer. Answers go into the clarifications log and the brief is updated in place. This uses the existing `WAITING_FOR_HUMAN` path.

### 2. Contract (Architect)

- Writes the spec and tasks from the brief and the facts. Revisions edit the previous draft (unchanged from foreman-flow §3).
- When the contract cannot be written without a behaviour decision, the Architect asks the Foreman (existing `CLARIFY` path, max 2 per round).
- When contract tests are on for the project (see Contract tests), the first task writes the tests.

### 3. Contract gate (Foreman analysis + Jev + code)

The Foreman review becomes a structured, read-only **analysis** (spec-kit `analyze`):

```json
{"findings":[{"id":"F1","severity":"critical|high|low","layer":"contract|requirement","cites":"c2 | brief: constraint 3","issue":"…","fix":"…"}],
 "coverage":[{"criterion":"c1","verification":"test|check|missing"}]}
```

- At most 10 findings. Only `critical` findings block. `high` and `low` go to the Builder as notes.
- On round 2 and later, the Foreman gets its previous critical findings and only checks whether each is resolved, plus a short regression scan. It does not run a fresh full review.
- A `requirement`-layer finding is the Foreman's own: it amends the brief (or asks the human), then the Architect revises.

Jev questions change from "does the review report an unaddressed problem" to checks against the contract:

| Id | Question |
|---|---|
| `covers_<c>` | Does a task implement `acceptance_criteria.<c>`? (unchanged) |
| `verified_<c>` | Does the AC → verification table give `<c>` a concrete test or check? |
| `critical_resolved` | Is every critical finding in `analysis` addressed by the spec or a task? (asked only when the analysis has a critical finding) |
| `open_questions` | Does the spec still contain an open question or a guessed behaviour? |
| `scope_creep` | Does a task build something the request and criteria do not ask for? (unchanged) |
| `touches_*` | Risk questions (unchanged) |

Truth table, first match wins:

| # | Condition | Outcome |
|---|---|---|
| 1 | no criteria, Jev failed or off, over the limit, unanswered question | escalate |
| 2 | any `touches_*` yes | escalate |
| 3 | any `covers_*` / `verified_*` no, `critical_resolved` no, `open_questions` yes, `scope_creep` yes | revise |
| 4 | an unsettled `touches_*` | escalate (risk is a human call) |
| 5 | an unsettled answer on `covers_*`, `verified_*` or `critical_resolved`; an unsettled `covers_<c>` or `verified_<c>` counts as yes when the Foreman's coverage marks `<c>` as `test` or `check` | revise |
| 6 | otherwise | approve |

The change from today: unsettled `scope_creep` and `task_overlap` no longer trigger a revise. They become a note to the Builder. `task_overlap` is dropped, because task `scope` already makes overlap visible to code. Budget: 2 revisions, then escalate (unchanged).

### 4. Build (Builder) and ask-back

- Unchanged task scheduling, scope and fix brief (foreman-flow §5).
- New: a Builder reply may be `{"status":"NEEDS_CONTEXT","question":"…"}` when the task or spec is ambiguous about behaviour. The factory routes the question to the Architect (same session if kept), which replies `ANSWER` (written into the spec as an amendment with a reason) or `ESCALATE` (to the Foreman, then the human). Max 2 questions per task. A third question escalates.
- The Builder decides on its own anything about how, within its scope (naming, layout, a choice between equivalent implementations), and records it as an assumption in its reply.
- A Builder that finishes ends its reply with "Notes for review:" (at most 5 one-line items, or `none`): what it left unverified, guessed, or that needs a change outside its task. Converge and the Inspector receive them as leads to check, never as evidence.

### 5. Checks

Unchanged.

### 6. Converge (Architect ∥ Inspector + Jev + code)

Converge replaces Architect verification, Foreman validation and the done gate.

The Architect compares the diff with the contract and returns gaps, without trusting earlier "done" claims:

```json
{"gaps":[{"id":"G1","type":"missing|partial|contradicts|unrequested","layer":"code|contract|requirement","cites":"c3 | spec §2","evidence":"file:line or test name","fix":"…"}],
 "converged": true}
```

- Only `missing` and `contradicts` in the `code` layer become **new tasks**, appended after the existing ones with new ids. Existing tasks are never rewritten.
- A `contract`-layer gap goes to the Architect as an amendment (the spec changes with a reason), then any resulting code gap becomes a task.
- A `requirement`-layer gap goes to the Foreman.
- `partial` and `unrequested` are notes unless they cite an acceptance criterion.
- Inspector findings block only when their severity is `blocker` or they cite an acceptance criterion. Every other finding is a note. Blocker findings follow the existing remediation loop.
- With contract tests on, layer sorting is mostly mechanical: a criterion whose test is red is `code`; a criterion whose test is green but the gap still claims it unmet is `contract` (the test is too weak).

Jev converge gate: `gap_valid_<G>` (is the gap real, given the evidence?) for each blocking gap, and `met_<c>` for each criterion with no test. Outcome: **converged** (no valid blocking gap, every criterion met) → PR preparation, as `done` does today; **append** → new tasks, then back to Build; **escalate**. Budget: 2 append rounds, then escalate.

### 7. Ship and Port

Ship is unchanged (PR open; see `docs/foreman-flow/SPEC.md` section 8). Port is the existing Cataloger journal step plus: stable facts → Code Map (auto-promoted); decisions table rows → knowledge record.

## Contract tests (optional, per project)

Setting `contract_tests: off | on`, default `off`.

When `on`:

1. Task `t0` writes one test per `test` row in the AC table.
2. The factory runs the checks and requires each new test to **fail on an assertion** (red proof). A compile error does not count. It records the result.
3. Jev gets one question per criterion: does test X really check `<c>`?
4. The factory stores a hash of each contract test file. A later edit by the Builder is flagged and needs an amendment.
5. Converge treats a criterion as met only when its test is green and unchanged.

No human review is needed: the red proof and the hash lock are checked by code, and whether a test really checks its criterion is judged by Jev.

## Playbook change

`playbooks/all/how-we-work.md` currently says "When two readings are valid, name both and pick one with a reason". This contradicts P3. New wording:

- An ambiguity about **behaviour** (what should happen): do not guess; ask upstream (Builder → Architect → Foreman → human).
- A choice about **how**, within your role (naming, layout, equivalent implementations): decide yourself and record the assumption.

## Risks

| Risk | Effect | Mitigation |
|---|---|---|
| Ask-back ping-pong | many cheap rounds instead of one build | max 2 questions per task, then escalate |
| Converge never ends | a reviewer keeps finding new gaps | only `missing` / `contradicts` in the code layer append; 2 rounds; Jev validates each gap |
| Budgets cut needed detail | the Builder lacks a fact it needs | details live in task scope and the facts sheet, which have no word cap |
| Facts go stale | an agent trusts a wrong line | sha-based staleness drops facts whose file changed |

## Non-goals

- Foreman e2e acceptance (see Later below).
- Gherkin `.feature` files (later, per project).
- Browser UI acceptance testing (later, per project).
- Making contract tests mandatory (later, once measured).
- Changing human PR approval.

## Decisions

1. Contract tests are optional per project for now (token cost); mandatory later once measured.
2. Jev decides approve / revise / escalate on the contract; no human gate by default.
3. AC → verification table only; Gherkin later, per project.
4. Requirement-layer findings go to the Foreman.
5. Converge replaces Architect verify, Foreman validate and the done gate.
6. Foreman e2e acceptance is deferred.
7. Code Map lines are promoted automatically.
8. Budgets: brief ~350, spec ~750, task ~150 words.

## Later

### Acceptance (Foreman e2e)

Not in this build. Kept here so the design is not lost. The **run profile** below exists only for this step.

The Foreman tests the result as a user would, against the AC → verification table.

- Runs only after Converged, and only when the project has a **run profile**: setup, start command, health check, base URL and seed data (stored next to `check_profile`). With no run profile, acceptance is recorded as "not run" and readiness continues to human PR review with that note.
- **The factory boots the app, not the Foreman.** If boot or the health check fails, every `check` row becomes `cannot_verify` and the Foreman is not started. The Foreman never debugs the environment.
- The Foreman gets exec tools for this run only: a shell in the workspace sandbox, with network limited to localhost. Other Foreman runs stay read-only.
- Scope: every `check` row, plus a smoke test of `test` rows. API and CLI level first; browser UI testing is a later per-project option.
- Output per criterion: `pass | fail | cannot_verify` with evidence. The evidence is the commands and their output, taken from the run's recorded tool calls, not from the Foreman's own summary.
- Caps: 20 tool steps and one model tier set per project (default: the Foreman model).

Jev acceptance gate: `fail_valid_<c>` per failed criterion. Outcome: **ship** (no valid fail) → PR preparation; **append** (a valid fail in the code layer) → a new task, Build, Converge, Acceptance again (this would share the converge budget); **escalate** (requirement-layer fail, or any `cannot_verify`).

A fail is only valid when it reproduces: the Foreman re-runs the failing command once before reporting it.


Acceptance-specific risks (why it was deferred):

| Risk | Effect | Mitigation |
|---|---|---|
| Flaky environment (port taken, missing env or secrets, DB or external service down) | false fails start append loops, or the Foreman burns tokens debugging setup | the factory boots and health-checks; boot failure → `cannot_verify`; re-run before reporting a fail |
| Cost | every turn resends the whole context: ~9.5K harness floor (claude-agent-acp preset + tool schemas, measured) + the Foreman prompt + every earlier curl output | step cap, AC-table scope, run profile handed in; most of it is cache read at 0.1× price |
| Exec-tool side effects | e2e calls reach real services or mutate shared data | sandbox, localhost-only network, seeded data |
| Self-reported passes | pass without proof | evidence from recorded tool output |
| Foreman not independent | happy-path testing | the AC table sets what is tested |
| UI-only criteria | cannot be checked at API level | `cannot_verify` → human |

## Implementation order

| # | Step | Depends on |
|---|---|---|
| 1 | Playbook wording fix (ask-back) and artifact budgets in the Foreman and Architect prompts (brief / spec / tasks shape, decisions table, AC → verification table) | — |
| 2 | Facts sheet: stored per work order, written at intake, read by every role, sha staleness | — |
| 3 | Contract gate: structured Foreman analysis, scoped re-review, new Jev questions and truth table | 1 |
| 4 | Builder ask-back to the Architect (NEEDS_CONTEXT, amendments in the spec) | 1 |
| 5 | Converge replaces Architect verify, Foreman validate and the done gate | 3 |
| 6 | Contract tests option (t0, red proof, hash lock) | 5 |
| 7 | Port: facts → Code Map (auto-promoted) | 2 |

Each step is a PR with tests (fake `Judge`). Measure each one on a real work order: revisions, runs, tokens (fresh, cache read, cache write) and cost, compared with `wo_15381843-2a1` and `wo_c6680cdd-92d`.
