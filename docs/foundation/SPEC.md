# legos-factory foundation — specification

Status: draft, 2026-10-26. Decisions are in `ADR.md` (cited as F01…F13).

## 1. Goal

A self-hosted, single-owner factory that turns a work order into a verified pull
request with CI green, using role-specific models on pluggable harnesses, without
an interactive chat agent.

**Done for a work order:** PR open on the target repository and all required CI
checks green (F11).

## 2. Non-goals

- Interactive chat with agents.
- Multi-user, teams, shared credentials (F12).
- Hosting integration infrastructure (Kafka, ES, shared databases) (F08).
- Resolving PR review comments inside the original work order (F11).
- Forking or embedding AO (F01).

## 3. Core entities

| Entity | Key fields | Notes |
|---|---|---|
| Project | repo URL, default branch, SCM credential, protected paths, env binding, verify commands | one repository each |
| WorkOrder | project, objective, source (manual / Jira), level cap | lifecycle in §5 |
| Task | work order, description, owned paths, depends_on | from the Architect's plan |
| Session | role, profile, task?, worktree, harness handle | one agent process |
| Credential | kind (api_key / subscription), provider, encrypted secret, concurrency limit | F04 |
| Profile | harness, credential, model, price table, verified_at | F02, F03 |
| RoleBinding | role → profile | per project, with global defaults |
| Fact | subject, kind, payload, at | the only durable state (F06) |
| Workspace | work order, volume, base commit | F07 |
| Sandbox | workspace, container id, started_at, ended_at | F07 |

## 4. Roles

| Role | Inputs | Outputs | Tools |
|---|---|---|---|
| Foreman | facts of the work order | transition calls | workflow transitions only (§6) |
| Cataloger | repo at base commit, objective | context pack (relevant files, conventions, commands) | read-only |
| Architect | objective, context pack | spec; plan = task graph with owned paths | read-only |
| Builder | task, context pack, owned paths | commits on the task branch | edit, shell inside its worktree |
| Inspector | task diff, verify results, spec | findings with severity, `logic_changed` opinion | read-only |

Tool policy is deny by default per role (lesson #3). The container is the real
boundary.

Context packs are cached per (repository, commit).

## 5. Work order lifecycle

Stored as transition facts; display status is derived.

```
queued
 → cataloging → specifying → [gate: spec] → planning → [gate: plan]
 → building (≤3 builders) ⇄ inspecting (≤K rounds per task)
 → integrating (engine merges task branches into the work-order branch)
 → verifying (repo CI-equivalent checks; optional integration against env binding)
 → pr_open            sandbox stops, slot released
 → ci_failed          re-queue → building (owning builder resumes)
 → ci_green           → awaiting_human | auto_merge (L3)
 → needs_rebase       main moved and conflicts → re-queue
 → merged | closed
any → failed (terminal, with class) | cancelled
```

Gates are judged by the Foreman. A human is required at a gate only when the
effective level demands it (§9).

## 6. Foreman transition tools

| Tool | Guard (must hold) | Effect |
|---|---|---|
| `approve_spec(note)` | state = spec ready | → planning |
| `reject_spec(reason)` | state = spec ready, rounds < limit | Architect revises |
| `approve_plan(note)` | state = plan ready; owned paths of parallel tasks are disjoint | → building |
| `reject_plan(reason)` | state = plan ready, rounds < limit | Architect revises |
| `dispatch_build(task_ids)` | deps done; ≤3 active builders; paths disjoint from active tasks | spawn builders |
| `request_inspection(task_id)` | task has new commits | spawn Inspector |
| `retry_task(task_id, change)` | task failed; retry budget left; change ∈ {same, other_model, split} | re-run |
| `serialize(task_a, task_b)` | integration conflict recorded | re-dispatch b on top of a |
| `escalate_to_human(reason)` | always allowed | → awaiting_human |

A rejected call returns a tool error to the Foreman. M consecutive rejections →
`escalate_to_human` (F10).

## 7. Harness interface

```go
type Harness interface {
    Start(ctx context.Context, s SessionSpec) (Session, error)
}

type Session interface {
    Prompt(ctx context.Context, text string) error
    Events() <-chan Event        // normalized: Step, ToolCall, ToolUpdate, Usage, Stop, Error
    Resume(ctx context.Context) error
    Close() error
}

type SessionSpec struct {
    Role       Role
    Profile    Profile          // harness, credential, model
    Dir        string           // worktree
    Env        map[string]string
    ToolPolicy ToolPolicy
    Limits     Limits           // steps, tokens, wall time
}
```

Adapters:
- `claude`: runs `claude -p --output-format stream-json`. Credentials come from
  `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, or `ANTHROPIC_BASE_URL` +
  `ANTHROPIC_AUTH_TOKEN` for Anthropic-compatible providers. Pin the small/aux model
  (`ANTHROPIC_DEFAULT_HAIKU_MODEL`) to the run's model.
- `acp`: the existing `backend/internal/agent/acp.go` approach.

Cost is computed by the factory from token usage × the profile's price table. It is
never taken from a harness's own estimate, which uses Anthropic prices for every
model.

## 8. Credentials, catalog and capability

- Catalog per credential:
  - API key: the provider's model list;
  - subscription: the plan's models only.
- Compatibility matrix credential kind → harnesses, for example:
  - Claude subscription → `claude` only;
  - ChatGPT subscription → Codex or OpenCode;
  - OpenCode Go → OpenCode;
  - Z.ai or Moonshot API → `claude` (compatible endpoint) or OpenCode.
- Smoke test per profile: create file, run a test command, commit. It records
  `verified_at`. Unverified profiles cannot bind to Builder.
- Concurrency semaphore per credential.

## 9. Autonomy

```
effective_level = min(3, diff_level(diff, protected_paths), inspector_level)
```

| diff_level | Condition |
|---|---|
| 3 | only docs; or only `_test.go`; or Go AST equal ignoring comments and formatting |
| 2 | any other code change outside protected paths |
| 0 | touches a protected path |

- L3 + CI green → auto-merge.
- L2 → human approves the PR.
- L0 → human approves the plan and the PR.

The Inspector's `logic_changed=true` caps the level at 2.

## 10. Sandbox and workspace

- Image: harness CLIs, git, language toolchains. Repository-specific tools come
  from the project's setup command.
- Volume layout: `/ws/repo` (main clone), `/ws/wt/<task>` (worktrees),
  `/ws/home` (harness session state such as `~/.claude`).
- Shared read-mostly caches across sandboxes: `GOCACHE`, `GOMODCACHE`, npm cache.
- Per-worktree `PORT` allocation by the engine.
- Limits: ≤3 sandboxes, ≤3 builders per sandbox, CPU and memory limits per
  container (values from measurement, plan M0).
- Lifecycle: start on dispatch, stop on PR push or terminal state. The volume is
  deleted after merged or closed.

## 11. Environment setup path

The project declares an env binding:
- `setup`: a command run once per sandbox, for example `make deps`;
- `verify`: an ordered list of commands, for example `make vet-lsm test-lsm`;
- `integration` (optional): command plus endpoints and secrets for an existing env
  (local compose, UAT);
- `docker`: whether the sandbox needs Docker access (DinD) for that env.

The factory injects these; it never provisions the env's infrastructure.

## 12. Observability (minimum)

- Every session step is a fact; steps are grouped per run.
- A stall flag after about 3 minutes without a new step (lesson #5).
- Each run shows provider, billing kind and resolved model (lesson #4).
- Failure facts carry class, model and provider message.

## 13. Open questions

1. Peak memory of verify (race tests) and integration runs. These set the
   container limits and the VM size. Not yet measured: the local loan-platform
   checkout has inconsistent `vendor/modules.txt`.
2. Bitbucket branch protection: does L3 auto-merge need a bot account with merge
   rights?
3. Claude subscription tokens in a self-hosted factory: confirm the terms of use
   before enabling.
4. Which providers pass the Builder smoke test through the `claude` harness
   (GLM, Kimi K3.1, MiniMax)?
5. Where the rebuild lives: new module in this repo vs a new repository; what to keep
   from `backend/` (store, scheduler, SCM integrations, ACP adapter).
6. K (inspection rounds) and M (Foreman rejections) defaults.
