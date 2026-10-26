# legos-factory foundation — task plan

Status: draft, 2026-10-26. Implements `SPEC.md`; decisions in `ADR.md`.
Each milestone ends with a verifiable check. A milestone does not start until the
previous one's check passes.

## M0 — Decide and measure

| Task | Verify |
|---|---|
| Decide where the rebuild lives (module in this repo vs new repo) and what to keep from `backend/` | Decision recorded in ADR.md |
| Measure peak memory and CPU of `make vet-lsm test-lsm`, `make test-bre`, `make test-integration` (clean loan-platform copy) | Numbers written into SPEC §10 |
| Measure one Claude Code and one OpenCode session's RSS under a real build task | Numbers written into SPEC §10 |
| Check Bitbucket branch protection for auto-merge | SPEC §13 Q2 answered |

## M1 — Walking skeleton (F13)

One work order, one builder, no Foreman loop, no Cataloger, no parallelism.

| Task | Verify |
|---|---|
| Fact store + derived status for a work order | Unit tests: status derived from fact sequences, including restart |
| Workspace volume + sandbox container lifecycle (start, mount, stop) | Integration test: container stops after the run, volume persists |
| `claude` harness adapter (stream-json) emitting the normalized events | Parser tests against captured real streams (lesson #5) |
| Builder run in one worktree with an API-key profile | Fake-repo test: commit produced; no-change run fails as `task_no_changes` |
| Verify step running the project's `verify` commands | Failing command marks the work order `verify_failed` |
| Push branch + open PR (reuse existing SCM clients), observe CI | PR facts and check facts stored |
| CI-failed re-queue resumes the same builder session from the volume | Test: second sandbox continues the session instead of starting a new one |

**Milestone check:** a real work order on a personal test repository ends with a PR
open and CI green.

## M2 — Profiles and credentials (F02–F04)

| Task | Verify |
|---|---|
| Credential kinds: API key, subscription token; encrypted storage reused | Tokens never returned by the API (test) |
| Model catalog per credential; harness filter per credential kind | Subscription credential lists only plan models (test) |
| Anthropic-compatible providers via `ANTHROPIC_BASE_URL`; aux model pinned | Run uses only the configured model (usage facts) |
| Profile smoke test and `verified_at`; Builder binding blocked when unverified | Test: binding an unverified profile to Builder is refused |
| Per-credential concurrency semaphore | Test: N+1th session waits |
| Cost from usage × price table | Test against fixture usage |

**Milestone check:** the same skeleton work order succeeds with a GLM or Kimi
builder and a Sonnet profile elsewhere.

## M3 — Foreman and Architect (F05)

| Task | Verify |
|---|---|
| Workflow state machine with guards for §6 tools | Table tests: every illegal transition rejected |
| Foreman agent loop exposing only transition tools | Test with a scripted model: rejected call returns tool error, state unchanged |
| Replay: restart mid-work-order never re-asks a recorded decision | Kill-and-restart test |
| Architect spec and plan (task graph with owned paths); spec and plan gates | Plan with overlapping parallel paths rejected by `approve_plan` guard |
| M consecutive rejections → escalate | Test |

## M4 — Parallel builders and integration (F07, F10)

| Task | Verify |
|---|---|
| ≤3 builders per sandbox, one worktree each, per-worktree `PORT` | Two builders editing disjoint paths both land |
| Engine merges task branches; conflict recorded as a fact | Conflict fixture produces `integration_conflict` and a Foreman decision |
| Failure classes and retry budgets (§F10) | Table tests per class |
| Global ≤3 sandboxes with queue | Fourth work order waits until a slot frees |
| Retry and Follow-up actions for terminal failures | API test |

## M5 — Inspector and verification tiers (F08)

| Task | Verify |
|---|---|
| Inspector per task; findings back to the same builder session; ≤K rounds | Test: findings resume the builder; K+1 escalates to Foreman |
| Inspector model ≠ Builder model enforced | Binding test |
| Builder prompt and context include targeted test commands | Builder runs package tests before finishing (event facts) |
| Env binding: setup, verify, optional integration, Docker flag | loan-platform project config runs `make vet-lsm test-lsm` in the sandbox |
| Verify failures mapped to owning task | Test with a failing file owned by task B |

## M6 — Cataloger

| Task | Verify |
|---|---|
| Context pack per (repo, commit), cached | Cache hit on the second work order at the same commit |
| Builder and Architect consume the pack | Builder read-only steps before first edit drop vs M5 baseline (lesson #7) |

## M7 — Autonomy and PR lifecycle (F09, F11)

| Task | Verify |
|---|---|
| `diff_level`: docs-only, tests-only, Go AST-equal check; protected paths | Table tests on fixture diffs |
| Inspector `logic_changed` can only lower | Test |
| Auto-merge at L3 + CI green; human approval otherwise | End-to-end on the test repo |
| Rebase when main moves; follow-up work order on a PR branch | Tests |

## M8 — Second harness

| Task | Verify |
|---|---|
| ACP adapter on the normalized event model (port `backend/internal/agent/acp.go`) | Same parser test suite passes for ACP streams |
| Subscription credential via OpenCode (e.g. OpenCode Go) passes smoke test | Profile verified |
