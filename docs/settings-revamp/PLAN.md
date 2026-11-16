# Settings revamp — implementation plan

Prototype: https://claude.ai/artifact/FqoVjdvomhi18wR3VPfWVm (source `docs/settings-revamp/prototype.html`, v3).
Date: 2026-11-16. Status: plan only, nothing built.

## Decisions (from the user)

1. AI accounts are **per user**.
2. v1 supports **Claude, Codex and OpenCode**.
3. **Budget section removed.** A budget for API-key mode comes later.
4. **Contract tests leave Settings.**
5. Each role (Foreman, Architect, Builder, Inspector, Chat) picks its own **agent + model**.
6. Permissions are set **per role**. Foreman, Architect and Inspector stay locked. Builder and Chat get a choice.
7. Connections are **per provider** (GitHub, GitLab, Bitbucket, Jira, …), not per repo. One provider row holds its accounts and every project on that provider uses them.

## What the code does today (main 72aed84)

| Area | Today | File |
|---|---|---|
| Harness | One harness for the whole process, from `FACTORY_HARNESS` + `FACTORY_ACP_COMMAND`. | `app/config.go`, `app/app.go:108` |
| Role → harness | `resolve(role, wo)` already exists but always returns the same harness. **This is the hook for decision 5.** | `app/app.go:119` |
| Role → model | `ResolveModel(role, wo, settings, defaults)`. Models are listed from the one harness. | `app/app.go:128`, `usecase` |
| Process env | Each ACP run spawns its own subprocess. `cmd.Env = os.Environ() + h.Env`, so a per-run env (account) can be injected. | `agent/acp.go:461` |
| Read-only lockdown | Claude: `claudeReadOnlyMeta` / `claudeBuilderMeta`. OpenCode: `OPENCODE_CONFIG_CONTENT`. **Nothing for Codex.** | `agent/acp.go:111-124` |
| Concurrency | `scheduler.New(..., cfg.MaxConcurrentRuns)` is fixed at start. | `app/app.go`, `scheduler/scheduler.go:39` |
| Jev | On only if `TYPESAFE_API_KEY` is set in env. | `app/app.go` |
| Budget | `Usage.SetBudgetLimit` plus a reservation before each run (`factory.go:159`). Zero means no cap. | `http/handlers.go:417` |
| Connections | Provider is one of `github / gitlab / bitbucket / jira`. Clients for all four exist. Name is free text and the project binding points at one connection. | `model/integration.go`, `integration/*.go` |
| Contract tests | Per project, `contract_tests: off/on`, wired into gates and readiness. | `usecase/contracttests.go`, `model/project.go:19` |

Login status can be read from the machine:
- `claude auth status` returns JSON: `loggedIn`, `email`, `subscriptionType`, `planTier`, `loginExpiresAt`.
- `codex login status` prints "Logged in using ChatGPT".
- `opencode auth list` lists providers.

## Phase 0: spikes (1 session, no PR)

Each spike is short and ends in a yes/no fact that the briefs depend on.

- **S1 Codex over ACP.** Install `codex-acp` (Zed's adapter). Check four things:
  - It drives a session (cwd, model list, tool calls, usage).
  - **There is a way to make a session read-only.** If there is none, Codex is only offered for roles that may write (Builder), and that is said in the UI.
  - Reject_once behaviour.
  - MCP via `session/new`.
- **S2 Account isolation.** Per account:
  - Claude: `CLAUDE_CONFIG_DIR=<dir>`. Does `claude auth status` / login work per dir, and does `claude-agent-acp` honour it?
  - Codex: `CODEX_HOME=<dir>`.
  - OpenCode: which env var (`XDG_DATA_HOME`?) moves `auth.json`.

  Fallback for Claude is `claude setup-token`, which gives a long-lived token passed as `CLAUDE_CODE_OAUTH_TOKEN`.
- **S3 Usage limits.** Is there a stable source for the 5-hour and weekly subscription usage (Claude first)?
  - If there is none, the usage bars are cut from v1 and the row shows plan + login expiry only.
  - Unknown today. **Do not promise the bars until S3 says yes.**

## PRs (sequential; Sonnet implements, Opus reviews once, one fix round, then merge)

### PR 1 — Agent registry and per-user accounts (backend)
- **Known agents table** (in code): id, name, binary, ACP command, account env var, login-status command, install hint. Rows: claude, codex, opencode.
- **`GET /agents`:** detects installed agents (`exec.LookPath`), adds the caller's accounts and the status of each account (parsed from the status command, run with that account's env), and returns not-installed agents as "available to install".
- **Accounts store** (migration): `agent_accounts(id, user_id, agent, label, home_dir, active, created_at)`.
  - `home_dir` lives under `DATA_DIR/accounts/<user>/<agent>/<id>`.
  - "System default" is implicit: no home dir, so the user's normal login is used.
- **Endpoints:**
  - `POST /agents/:id/accounts`: create a dir and return the login command to run with the env set.
  - `POST …/accounts/:aid/activate`
  - `DELETE …/accounts/:aid`: remove the dir.
  - `POST /agents/:id/enabled`
- **Pitfalls for the brief:**
  - The status command can hang: use a timeout.
  - Never return tokens.
  - The dir must not be readable by other users.
  - Deleting the active account falls back to System default.

### PR 2 — Per-role agent, model and permission (backend)
- **Settings:** add `role_agents map[role]agentID` and `role_permissions` (Builder: `edit | ask`; Chat: `ask | read`). Locked roles are not stored.
- **Harnesses:**
  - Build one harness per enabled agent (lazily).
  - `resolve(role, wo)` returns the harness of the role's agent.
  - The account comes from the **work order creator's** active account (`wo.CreatedBy`). For chat it is the chat owner's.
  - Pass the account env per run, by extending `spawn`.
- **Models:** `GET /models?agent=` lists per agent. `ResolveModel` checks that the model belongs to the role's agent and otherwise falls back to that agent's first model.
- **Snapshot:** a work order keeps the agent it started with, the same way Foreman and Architect already keep their model.
- **Pitfalls for the brief:**
  - Read-only roles on an agent without a verified lockdown are refused when saving (see S1).
  - Steering only exists on agents that advertise it.
  - The MCP ask rule is Claude-only today. Do not pass MCP servers to other agents until verified (same rule as OpenCode now).
  - Parallel runs of different agents.
  - A role whose agent is turned off or signed out must fail the job with a clear event, not hang.
  - Restart: resolve again from the snapshot.

### PR 3 — Workspace settings cleanup (backend)
- **`max_concurrent_runs` moves to settings.** The scheduler gets `SetWorkers(n)` (resize a semaphore). Running jobs are not killed. Env stays as the first-boot default.
- **Jev:** add a `jev_enabled` toggle and a key stored through the sealer. The env key still works and wins if set.
- **Budget:**
  - Remove `budget_limit_micros` from the settings API and UI.
  - The tracker keeps recording usage (Home "usage by agent" needs it) with no cap.
  - The reservation code stays and is a no-op at zero.
- **Contract tests:** remove the project setting from Settings and the Add-project dialog. See open question Q1 for what happens to the feature.
- **Runtime:** remove the readout endpoint fields that are now settings. Keep `credentials_configured` for the banner.

### PR 4 — Connections per provider (backend + migration)
- **Provider list:**
  - Code hosting: GitHub, GitLab, Bitbucket.
  - Work tools: Jira.
  - MCP servers stay as they are.
- **Existing rows:** keep them, grouped by provider as accounts. No data loss. Each account shows "used by" from the project bindings.
- **Project binding** keeps pointing at a connection. Adding a project picks the provider's active account by default.
- Google Calendar is **not** in this PR (no client exists; see Q2).

### PR 5 — Settings page (frontend)
- Rebuild `features/settings/` from the prototype: Agents (installed, available, roles table), Connections (provider groups, ⋯ menus, inline connect), Advanced.
- **Remove:** Budget, Runtime readout, contract tests UI, duplicate help texts.
- Keep `#agents / #connections / #advanced` anchors. Map the old `?tab=` values.
- **Checks:**
  - Vitest for each section.
  - Type-check.
  - A browser check of add-account, role change, connect, and the secret-key banner, in light and dark.

## Verification before each merge (by me)
- Full backend tests with `-race`, frontend tests and type-check.
- PR 2: a live work order with Builder on Claude and Inspector on a second agent (whichever S1 clears), on legos-pos-testbed.
- PR 1: add a second Claude account and see a run use it (check `claude auth status` with that dir).

## Answered (2026-11-16)
- **Q1:** delete the contract-test feature completely: t0, red proof, lock, the project field, gates/readiness hooks and their tests. This needs a migration that drops `contract_tests`.
- **Q2:** Google Calendar belongs to a later phase (personal tasks). Not in v1.
- **Q3:** no shared server for now. Accounts live on the local host only. OIDC mode is untouched.

## Delivery: 2 PRs, built in parallel
The user wants it fast and accurate, so the 5 slices above become **2 PRs**:
- **PR A, backend:** slices 1 + 2 + 3 + 4, plus deleting contract tests. One branch, with commits per slice.
- **PR B, frontend:** slice 5. It starts at the same time as PR A, from the API contract below, so it does not wait for the backend merge. It is checked live against PR A's branch before merging. Merge order is A, then B.

Each PR gets one Opus review and one fix round. I check the fixes myself.

## Spike results (2026-11-16)

**S1 Codex over ACP**
- **Package.** Use `@agentclientprotocol/codex-acp` 2.6.0, now installed globally. `@zed-industries/codex-acp` is deprecated and broken (`model_not_found`), so it was removed.
- **Capabilities.** `loadSession`; MCP over http only (no sse); image and embeddedContext prompts. Auth needed comes back as `-32000 "Authentication required"`, already at `session/new`.
- **Config.** Set through `session/set_config_option {configId, value}`. `set_mode` and `set_model` do not work.
  - `mode`: read-only / workspace-write / agent / agent-full-access
  - `model`: gpt-6.1-sol (default), gpt-6-astra, gpt-6.1-terra, gpt-6-sol, gpt-6-luna, gpt-5.6-*
  - `reasoning_effort`, `collaboration_mode`, `fast-mode`
- **Writes.** A write arrives as tool_call `execute`, then `request_permission` with options allow_once / allow_always / cancel. **Reject ends the turn** (`stopReason: cancelled`). This differs from Claude, where the turn continues after a reject.
- **There is no hard read-only.** In `read-only` mode an allowed command runs unsandboxed. The only guarantee is that the factory rejects every permission request. Plain file reads need no approval.
  - So a read-only role on Codex means mode `read-only` plus reject everything.
  - **Do not offer Codex for Inspector**, because its test runs would need permission and the reject would end the turn.
  - Foreman and Architect on Codex are allowed, read-only by rejection.
  - Builder on Codex runs in `workspace-write` with allow.
- **Usage.** `usage_update {used,size}` arrives during the turn. The prompt response carries `usage` plus `_meta.quota`.
- **Leak to fix.** Codex loads `~/.codex/config.toml`, including that file's **MCP servers**. This is the same leak as the Claude connectors. Every Codex run must get its own `CODEX_HOME`, holding the account's `auth.json` and a factory-written `config.toml` with no MCP servers.

**S2 Account isolation, verified for all three**
- **Claude: `CLAUDE_CONFIG_DIR=<abs dir>`.**
  - The Keychain item is per dir (`Claude Code-credentials-<hash>`), so keep the path stable.
  - Login with `claude auth login` under that env; it opens the browser.
  - `claude-agent-acp` honours it. A missing login fails only at `session/prompt` with `-32000 "Authentication required"`.
  - An empty dir drops the user's CLAUDE.md, plugins and skills. That is fine for Builder, which drops them already. Chat currently loads `~/.claude/CLAUDE.md`, and an account dir changes that.
  - "System default" means no env var set.
- **Codex: `CODEX_HOME=<dir>`.** Login with `codex login --device-auth`.
- **OpenCode: `XDG_DATA_HOME` / `XDG_CONFIG_HOME` / `XDG_STATE_HOME` / `XDG_CACHE_HOME` plus `--standalone`.**
  - A running v2 background service ignores the XDG vars unless `--standalone` is passed.
  - Login with `opencode auth login --standalone`.
  - Before using `opencode acp --standalone`, check that it accepts the flag.

**S3 Usage limits**
- **Codex:** use the official `codex app-server` stdio RPC `account/rateLimits/read`. It returns `primary`/`secondary` `{usedPercent, windowDurationMins, resetsAt}`, run under the account's `CODEX_HOME`. The protocol is experimental, so pin the parsing.
- **Claude:** the only source is the undocumented `GET https://api.anthropic.com/api/oauth/usage` with the subscription OAuth token from the Keychain item. It returns `five_hour` / `seven_day` `{utilization, resets_at}`. This is the route Orca uses, and it is a ToS grey area.
  - **Plan:** an opt-in toggle "Show Claude usage", **off by default**. When it is off the row shows plan + login expiry from `claude auth status`.
  - 401, 429 or an unexpected response shape shows "usage unavailable". The factory never refreshes tokens itself.
- **OpenCode:** no usage source. Show the plan/provider from `opencode auth list`.

### API contract (both PRs build against this)
All JSON. `{agent}` is one of `claude | codex | opencode`. Roles are `foreman | architect | builder | inspector | chat`.

```
GET  /api/agents
→ { agents: [{
      id, name, command: "claude-agent-acp", installed: bool, enabled: bool,
      install_hint: "npm install -g @agentclientprotocol/codex-acp",
      roles_allowed: ["foreman","architect","builder","chat"],   // codex: no inspector
      accounts: [{ id, label, system: bool, active: bool,
                   status: "signed_in"|"signed_out"|"unknown",
                   who: "you@x.com", plan: "max_5x", expires_at?: RFC3339,
                   usage?: { session?: {used_pct, resets_at}, week?: {used_pct, resets_at} },
                   usage_error?: "unavailable" }] }],
    claude_usage_opt_in: bool }
POST   /api/agents/{agent}/enabled           { enabled }
POST   /api/agents/{agent}/accounts          { label }  → { account, login_command, env_hint }
POST   /api/agents/{agent}/accounts/{id}/activate
POST   /api/agents/{agent}/accounts/{id}/refresh       → account (re-reads status + usage)
DELETE /api/agents/{agent}/accounts/{id}             (system account cannot be deleted)
PATCH  /api/me/preferences                    { claude_usage_opt_in }
GET  /api/models?agent={agent}                → existing ModelInfo[] (Harness = agent id)

GET/PATCH /api/settings adds:
  role_agents:      { foreman: "claude", architect: "claude", builder: "claude", inspector: "claude", chat: "claude" }
  role_permissions: { chat: "ask"|"read" }   // Builder fixed "edit": work orders have no approval UI, so "ask" is cut from v1
  max_concurrent_runs: int (1..16)
  jev_enabled: bool        (+ PATCH only: jev_key: string, write-only, sealed)
  removes: budget_limit_micros
GET /api/settings/runtime → { credentials_configured: bool, jev_key_from_env: bool }

GET /api/connections → { providers: [{
      id: "github"|"gitlab"|"bitbucket"|"jira", group: "code"|"work",
      accounts: [Connection + { used_by: [{project_id, name}] }] }] }
  (POST/PATCH/DELETE/test on /api/connections stay as they are)
Removed: PUT /api/projects/:id/contract-tests, project.contract_tests
```

Accounts are per user: the `user_id` comes from the session. A run uses the active account of the **work-order creator** for its role's agent, and a chat run uses the chat owner's.
