# legos-factory foundation — architecture decisions

Status: accepted in brainstorm, 2026-10-26. Supersedes the runtime direction in
`docs/opencode-in-container-design.md` where they conflict. Evidence for many decisions
is in `docs/agent-runtime-lessons.md` (cited as "lesson #N").

Reference system: [OrchestratorInc/agent-orchestrator](https://github.com/OrchestratorInc/agent-orchestrator)
("AO", originally ComposioHQ's open-source `ao`, Go daemon, Apache-2.0). We borrow its
ideas, not its code.

## ADR-F01: Factory-first lean rebuild, not a fork and not a chat agent

**Decision:** rebuild the foundation around a concrete SDLC line (work order → spec → plan
→ build → inspect → verify → PR). Use AO's model (session = agent + worktree + runtime;
durable facts, derived status) as reference. Do not fork AO and do not build an
interactive chat agent.

**Why:** the SDLC line is the product; AO and Hermes cover runtime and chat, not the
line. AO is ~29 MB of Go oriented to a desktop app; bending it into a self-hosted
factory fights its design.

**Consequence:** we own the runtime pieces we need (sessions, worktrees, harness
adapters) and keep them small.

## ADR-F02: Roles map to profiles; model choice is the user's

**Decision:**
- *Role* = prompt + tool policy (deny by default, lesson #3) + context pack.
- *Profile* = harness + credential + model + price table.
- Each role is bound to a profile chosen by the user.

Intended defaults:

| Role | Job | Default model |
|---|---|---|
| Foreman | drives the flow, judges gates | Opus |
| Architect | spec and plan | Sonnet |
| Builder | implements tasks | GLM / Kimi K3.1 |
| Inspector | finds defects in other agents' work | Sonnet (must differ from Builder's model) |
| Cataloger | builds context packs | cheap: MiniMax / Haiku |

**Why:** a capable harness makes most models adequate, but cost and quality per role
differ. Inspector on a different model than Builder avoids shared blind spots.

## ADR-F03: One harness protocol (ACP); an agent is a command

**Decision:** every agent runs as an ACP subprocess (stdio JSON-RPC), driven by the existing Go client (`backend/internal/agent/acp.go`). An agent is configured as a command:

| Agent | Command |
|---|---|
| Claude Code | `claude-agent-acp` (Claude Agent SDK; supports session load/resume) |
| OpenCode | `opencode acp` |
| Codex | `codex-acp` |

No translation proxy or router: models without an Anthropic-compatible endpoint (GPT, OpenCode Go/Zen) run through OpenCode or Codex, not through Claude Code. A `(agent, credential, model)` combination may serve the Builder role only after passing a smoke test (edit a file, run a test, commit).

**Why:** one adapter, one event parser and one test suite against captured real streams (lesson #5). The ACP client already works. Provider tool-use quality still differs, so the smoke test decides; a router adds a failure point and lossy translation.

**Verified (spike 2026-10-30, SPEC §7):** model and tool denies are set at `session/new` via `_meta.claudeCode.options` (`model`, `settings.permissions.deny`); for this agent a `reject_once` permission reply fails only that tool call, so the turn-cancel part of lesson #3 applies to OpenCode only. Each session uses its own `CLAUDE_CONFIG_DIR` on the volume so the owner's settings never leak in; `session/load` works from a new process with the same config dir and cwd. Container credential passthrough is still unverified live.

## ADR-F04: Credentials are API keys or subscription tokens; models derive from them

**Decision:**
- A credential is either an API key or a subscription token obtained by the
  harness's own login (`claude setup-token`, `codex login`,
  `opencode auth login --method device`). Stored encrypted, injected per run.
- The selectable model list is derived from the credential: a subscription shows
  only its plan's models (as OpenCode does for OpenCode Go). The agent list is
  then filtered to those compatible with the credential.
- Concurrency is limited per credential, not only per sandbox.
- Subscription credentials are allowed only in single-owner mode (ADR-F12).

**Why:** lesson #4 (same model name under two providers, silent "Workspace default"),
lesson #6 (Anthropic disallows offering claude.ai login in third-party products; a
single owner using their own token is a grey area, accepted for self-hosting only).

## ADR-F05: Foreman is an agent loop confined by a Go workflow state machine

**Decision:** the Foreman (Opus) runs as an agent loop whose only tools are workflow
transitions (`approve_spec`, `approve_plan`, `dispatch_build`, `request_inspection`,
`retry_task`, `escalate_to_human`, …). Each call is checked by a guard against the
current state; rejected calls return a tool error. Accepted decisions are stored as
facts. On restart the engine replays facts and never re-asks the Foreman for a
decision already made. The Foreman never spawns processes; the engine does, within
turn, budget and concurrency limits.

**Why:** keeps agent-loop flexibility while flow, resume and audit stay deterministic
(Temporal's workflow/activity split; slaude's "enforcement lives in gateway, never
model").

## ADR-F06: Store facts, derive status

**Decision:** persist only facts (agent activity, transitions, Foreman decisions,
task commits, PR, check and comment facts). Display status (`building`, `ci_failed`,
`awaiting_human`, …) is computed at read time.

**Why:** AO's observe → update → derive model; avoids status columns updated from
many places.

## ADR-F07: Workspace is a volume, sandbox is a container

**Decision:**
- One work order = one workspace volume (clone, worktrees, harness session state)
  living from start until PR + CI green.
- A sandbox is an ephemeral container mounting that volume while agents work.
  It dies when the PR is pushed. A CI failure re-queues the work order and a new
  sandbox resumes the owning builder's session from the volume.
- At most 3 builders per sandbox, one git worktree each (lesson #1).
- At most 3 sandboxes run at once. The rest wait in the queue. A slot belongs to a
  running container, not to a work order.

**Why:** waiting 10–30 min for CI must not hold a slot. Session state on the volume
makes resume real rather than a restart.

## ADR-F08: Verification tiers; integration environments live outside the factory

**Decision:**
1. Builder: targeted `go test` on touched packages, no `-race`, inside its own loop.
2. Work-order verify (1 per work order, in its sandbox): the repository's CI-equivalent
   checks (for loan-platform: `make vet-X test-X`, race on). The Inspector reads the
   results.
3. Integration and E2E run against an environment the project already has (local dev
   env, UAT, a compose stack). The factory provides only the **environment setup
   path**: a per-project setup command, endpoints and secrets injected into the
   sandbox. The factory does not host shared Kafka, ES or databases.

**Why:** loan-platform's CI gates only on vet and race unit tests; its
`docker-compose.integration.yml` (39 containers, about 4 GB) and DEV/UAT already exist.
Every serious project has its own env; duplicating it inside the factory is waste.

## ADR-F09: Autonomy levels: global cap L3, reduced by the diff

**Decision:** `effective_level = min(3, diff_level, inspector_level)`.
- `diff_level` is deterministic: docs-only or tests-only changes, or code whose Go AST
  is unchanged after ignoring comments and formatting, may reach L3. Any other code
  change is L2 at most. Protected paths declared per repository (migrations, money
  calculations, state transitions) always require a human.
- `inspector_level` can only lower the level, never raise it.
- L3 also requires CI green. The level applies to company repositories too.

**Why:** "does not change logic" must be provable, not an LLM opinion, in loan and
billing domains.

## ADR-F10: Failures are classed facts with owners

| Class | Example | Decides |
|---|---|---|
| provider | 5xx, insufficient funds | engine: transient → backoff retry; permanent → fail once (lesson #2) |
| infra | sandbox OOM or crash | engine: new sandbox from the volume, resume from last fact |
| task | no changes, Inspector rejects > K times | Foreman: change model, split task, escalate |
| integration | merge conflict between task branches | Foreman: re-dispatch on top of the other task, or serialize |
| verify | tests fail | engine maps failing files to the owning task → its builder; unmapped → Foreman |
| foreman | M consecutive rejected transitions | escalate to human |

Task commits are checkpoints, so steps are idempotent. Terminal failures reach a human
with Retry and Follow-up actions (lesson #8).

## ADR-F11: Scope ends at PR + CI green; the work order tracks through merge

**Decision:** the factory's job ends when the PR is open and CI is green. The work
order keeps tracking PR facts through merge or close (rebase when main moves). Review
comments are resolved by a separate follow-up work order on the PR branch.

**Why:** sandbox capacity is limited; comment resolution is a different, interactive
loop.

## ADR-F12: Single owner, self-hosted on a VM or locally

**Decision:** one owner per deployment, on a VM or a laptop. Multi-user is out of scope.

**Why:** makes subscription credentials acceptable and keeps auth simple.

## ADR-F13: Walking skeleton first

**Decision:** the first milestone is one work order, one builder, no parallelism, no
Cataloger, reaching a real PR with CI green.

**Why:** two earlier runs of the current factory never reached a PR. End-to-end proof
comes before parallelism and policy.

## ADR-F14: The rebuild is a new module in this repository

**Decision:** the foundation lives in this repository as a new Go module `factory/`,
developed on `feat/new-legos-architect` with one branch per milestone. `backend/` and
`frontend/` are frozen. Pieces worth keeping (ACP client, GitHub/Bitbucket clients,
secret encryption) are copied into `factory/` and adapted, never imported across modules.

**Why:** one place for docs and history; the old backend keeps running unchanged
until the new factory reaches its M1 check; copying avoids coupling to the old
model types.

## ADR-F15: Foreman calls transitions as one JSON object per reply

**Decision:** the Foreman runs as a read-only ACP session with every agent tool denied.
Each reply ends with exactly one JSON object `{"tool": "<transition>", "args": {...}}`.
The engine parses it, checks the guard (`internal/workflow`) and either appends the
decision facts or sends the guard error back as the next prompt ("tool error"). A reply
without a parseable call counts as a rejected call.

**Why:** ACP agents expose their own tools; injecting factory tools would need an MCP
server reachable from inside the sandbox. A text protocol keeps one adapter and works
for every agent. Revisit with an MCP server if parse failures are frequent.
