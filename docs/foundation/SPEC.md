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
    Role         Role
    Profile      Profile           // agent, credential, model
    AgentCommand []string          // e.g. ["claude-agent-acp"]
    Dir          string            // worktree
    Env          map[string]string
    ToolPolicy   ToolPolicy
    Limits       Limits            // steps, tokens, wall time
}
```

There is one adapter: ACP (port of `backend/internal/agent/acp.go`). Agents:
- `claude-agent-acp`: credentials from `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, or `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN` for Anthropic-compatible providers (GLM, Kimi, MiniMax). Pin the small/aux model (`ANTHROPIC_DEFAULT_HAIKU_MODEL`) to the run's model. Tool denies go in Claude Code settings, not ACP permission replies.
- `opencode acp`: per-role config through `OPENCODE_CONFIG_CONTENT`; model via `session/set_config_option`.
- `codex-acp`: for ChatGPT subscription or OpenAI API key.

Cost is computed by the factory from token usage × the profile's price table. It is
never taken from a harness's own estimate, which uses Anthropic prices for every
model.

### claude-agent-acp findings (spike 2026-10-30)

Tested `@agentclientprotocol/claude-agent-acp` 0.99.1 (SDK 0.3.312, ACP v1) with a
Go-client-like driver over stdio, 6 live prompts on haiku against the machine's Claude Max
login. Captured streams: `factory/internal/harness/testdata/claude-agent-acp-*.jsonl`
(`deny`, `permission-reject`, `bypass-write`; email, paths and the personal `agent`
config option redacted, `available_commands_update` and `_auth/status_update` dropped).
Each bullet is marked Verified-live, From-source or Unknown.

- **Package.** `@zed-industries/claude-agent-acp` (0.23.1) and `@zed-industries/claude-code-acp` both exist but are deprecated: the project moved to `@agentclientprotocol/claude-agent-acp`. The bin is still `claude-agent-acp`. Pin the new package. Only the new one was run. (Verified-live: npm metadata, bin.)
- **Model.** Three ways, all Verified-live by the model name in `usage_update._meta["_claude/model"]` and `model_usage`:
  - `session/set_config_option {configId:"model", value}` after `session/new` (value from `configOptions[id=model].options`, e.g. `haiku`, `opus`, or a full id like `claude-haiku-4-5-20251001`);
  - `session/new` `_meta.claudeCode.options.model` (forwarded as an SDK option);
  - env `ANTHROPIC_MODEL` on the adapter process (highest priority, then `settings.model`; From-source). It accepts any string, so a gateway model like `glm-...` reaches the API as sent (Verified-live against a local fake endpoint).
  - `session/new` returns `{sessionId, modes, configOptions}` only. There is no `models` field. `configOptions` has `mode`, `model`, `effort` (low..max), `ultracode`, `agent`. The model list is the SDK's catalog (aliases plus pinned ids), not derived from the credential (From-source; only one credential was tried).
  - Aux calls do NOT follow the main model: the session-title request went to `claude-haiku-5-5` (about 1.2k tokens) until `ANTHROPIC_DEFAULT_HAIKU_MODEL` was set, after which `model_usage` held only the pinned model. Always set it. (Verified-live)
- **Tool deny.** Works without any `session/request_permission`. A denied tool is removed from the model's tool list; if the model calls it anyway it gets a `tool_call_update status:"failed"` ("No such tool available: Write ... disabled for this session") and the turn still ends `end_turn`. Two equivalent mechanisms, both Verified-live (zero permission requests, no file written):
  - project `<cwd>/.claude/settings.json` `{"permissions":{"deny":["Write","Edit","NotebookEdit","Bash"]}}`;
  - inline, per session, no files: `session/new` `_meta.claudeCode.options.settings = {"permissions":{"deny":[...]}}`. `_meta.claudeCode.options.disallowedTools` and `.tools` (explicit tool list) also exist (From-source).
  - Prefer the inline form: the factory needs no settings file in the worktree, which the agent could commit or edit. The `deny` fixture captures the project-file form and ran with ambient config; M1 adds an inline-deny capture.
- **Rejecting a permission request does not cancel the turn here.** Replying with the `reject_once` option made only that tool call fail ("User refused permission to run tool") and the turn went on to `end_turn` (Verified-live). The `{outcome:"cancelled"}` reply is also turned into a denial, not a turn abort (From-source, `acp-agent.js` ~8080). The turn-cancel part of lesson #3 (its last bullet) is OpenCode-specific; deny-by-default still applies to every agent. Pick the option by `kind == "reject_once"`, not by `optionId`. Not run: leaving the request unanswered.
- **Permission modes.** Default is `default` ("Manual": asks for edits and commands). `session/set_config_option {configId:"mode"}` accepts `acceptEdits`, `plan`, `auto`, `bypassPermissions`; `bypassPermissions` Verified-live (write with no request). A mode can also come from `permissions.defaultMode` in settings. `bypassPermissions` is refused when the adapter runs as root unless `IS_SANDBOX` is set, in which case it silently clamps to `default` (From-source, `permissions/modes.js`). Containers running as root must set `IS_SANDBOX=1` or run as a non-root user.
- **Credentials.** The adapter builds the SDK env as `{...process.env, ..._meta.claudeCode.options.env}`, so `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` set on the subprocess reach the Claude Code CLI (From-source, `acp-agent.js` ~9000). Verified-live without secrets: with an empty `CLAUDE_CONFIG_DIR`, `ANTHROPIC_BASE_URL=http://127.0.0.1:...` and `ANTHROPIC_AUTH_TOKEN=dummy`, `/v1/messages` requests arrived at the fake endpoint with `Authorization: Bearer dummy...` and the model from `ANTHROPIC_MODEL`. The `ANTHROPIC_API_KEY` and `CLAUDE_CODE_OAUTH_TOKEN` paths were not exercised (no secrets); the adapter's auth-env list names them (From-source). Do not pass `--hide-claude-auth` if subscription tokens are used: it refuses subscription accounts (From-source).
- **Ambient config leaks in.** The SDK loads `settingSources: ["user","project","local"]`: the HOME / `CLAUDE_CONFIG_DIR` settings, plugins, skills and CLAUDE.md all apply. On a developer machine one "create a file" prompt cost about 100-150k total tokens (mostly cache read/write) and pulled the user's default model from their settings. Run each agent session with its own empty `CLAUDE_CONFIG_DIR` under `/ws/home/<session>` (kept on the volume for `session/load`) (plus the inline `settings`) so nothing but the factory's config applies. (Verified-live: tokens, default model; From-source: setting sources.) Also, a vague "create z.txt" made haiku write into the SDK's per-session scratchpad dir rather than the cwd: give explicit paths in prompts (Verified-live, one occurrence).
- **Persistence.** `initialize` advertises `loadSession: true` and `sessionCapabilities` `resume`, `fork`, `list`, `close`, `delete` (stable fields, no unstable flag needed). State is the Claude Code transcript `<CLAUDE_CONFIG_DIR>/projects/<cwd with / and . replaced by ->/<sessionId>.jsonl` (+ a sibling `<sessionId>/` dir for tool results). Verified-live: a fresh adapter process with `CLAUDE_CONFIG_DIR` pointing at a directory holding only the copied `projects/<encoded cwd>` folder answered `session/load {sessionId, cwd, mcpServers}` with the saved mode (`bypassPermissions`) and replayed history as `user_message_chunk`, `tool_call`, `tool_call_update`, `agent_message_chunk`; a following prompt in a normal login remembered the earlier turn ("z.txt"). The transcript key includes the cwd, so the workspace path inside the container must be identical across containers. Not exercised: `session/resume`; a prompt after load in the copied dir failed with `Authentication required` because that dir had no credentials (expected: auth is separate from the transcript).
- **Prompt result.** `{stopReason:"end_turn", usage:{inputTokens, outputTokens, cachedReadTokens, cachedWriteTokens, totalTokens}, _meta:{quota:{token_count, model_usage:[{model, token_count}]}}}`. `model_usage` lists every model used (main plus aux), so price per model from it. `usage.totalTokens` excludes aux calls: cost and token accounting use `model_usage`, not `usage`. (Verified-live)
- **Session updates.** `usage_update {used, size, cost?:{amount, currency}}` per API call (cost only on the last, Anthropic prices: ignore per Section 7); `session_info_update {title, updatedAt}` (title from the aux model); `available_commands_update` (all skills and commands, large); `tool_call {name, status:"pending", kind, content:[], rawInput:{}}` followed by `tool_call_update` frames that fill `title`, `rawInput` and `locations`, then `content:[{type:"diff", path, oldText, newText}]` for edits or `content:[{type:"content", content:{type:"text"}}]` for output, then `status` `completed`/`failed` with `rawOutput` (a string). `_meta.claudeCode.toolName` carries the tool name. `kind` is `edit`, `other` etc. `tool_call.content` is an array; message chunks are objects (same split as lesson #5). `ToolSearch` appears as a tool_call too. `plan` updates were not triggered. In the `bypass-write` fixture the write landed in the SDK scratchpad, not the cwd: parser tests must not assert a worktree path. (Verified-live except plan)
- **Provider errors** come back as a JSON-RPC error on `session/prompt` (e.g. `-32000 Authentication required`), with `errorKind` data for usage limit/budget/context cases (From-source, `acp-agent.js` ~5465), so they map to lesson #2's permanent/transient split without parsing message text.

### opencode acp findings (2026-10-30)

Tested `opencode acp` 2.1.3 (the owner's local binary) with a Python stdio driver, in an empty `HOME`/`XDG_*` and a stripped environment (no provider keys), on the free model `opencode/big-pickle`, 8 tiny prompts. Captured streams: `factory/internal/harness/testdata/opencode-*.jsonl` (`edit`, `deny`, `load`, `error-free-limit`, `error-auth`, `error-bad-model`; paths redacted, `available_commands_update` dropped). Marked Verified-live, From-source or Unknown.

- **Model.** `session/new` returns `{sessionId, configOptions}` only. `configOptions`: `model` (category `model`, values `provider/model`, e.g. `opencode/big-pickle`), `mode` (`build`, `plan`) and `effort` (`thought_level`). In an empty environment the model list holds only the 11 free `opencode/*` models. `session/set_config_option {configId:"model", value}` switches it and the agent answers with the full option list plus a `config_option_update`. An unknown value fails with `-32602 Invalid params: model not found: ...`; the harness checks the value against the list first and fails permanently. (Verified-live)
- **Credentials.** Env keys add that provider's models to the list (Verified-live, list only): `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `MOONSHOT_API_KEY` (provider id `moonshotai`, not `moonshot`), `MINIMAX_API_KEY`, `ZHIPU_API_KEY` (adds `zai/*`; `ZAI_API_KEY` adds nothing), and `OPENCODE_API_KEY` (adds paid `opencode/*` and `opencode-go/*`, so an OpenCode Go key can be passed as an env var). The `auth.json` the profile used to write to `$XDG_DATA_HOME/opencode/auth.json` is NOT read by 2.1.3 for built-in providers (no models appeared) and a custom provider's key in it never reached the endpoint (checked against a local fake endpoint). A key in `OPENCODE_CONFIG_CONTENT` `provider.<id>.options.apiKey` does arrive as `Authorization: Bearer`. v2 stores logins in `opencode.db` and offers `opencode auth export` / `auth import`; the export shape was not obtained (`auth login --answer` field names unknown), so ChatGPT-through-OpenCode login files stay Unknown.
- **Provider errors.** A 401 from the provider is `-32000 Authentication required: provider authentication required` on `session/prompt`. The free-tier cap is `-32603 Internal error: Free usage limit reached. Connect a provider or subscribe to OpenCode Go...` with `data.errorName:"provider.free-limit"`; it hit after about 7 prompts in a few minutes and then cleared. Both classify as permanent. (Verified-live)
- **Permissions.** With no permission config nothing is asked: the edit capture has zero `session/request_permission`. With the harness config (`external_directory`, write tools denied) the write tools are hidden: the model listed `read, glob, grep, question, skill, subagent, webfetch, websearch`, did not create the file and the turn ended `end_turn`. (Verified-live) Gaps: `webfetch`/`websearch` stay available to read-only roles; the `subagent` tool ran, and its child session's tool calls are streamed into the parent (`_meta.opencode/child-session-updates`, titles like `Create notes.txt: read`), so tool names are descriptions, not tool ids. The harness now also denies `question` (nobody answers it headless) for every role and `task` for read-only roles; both permission names come from the OpenCode permission list and were not re-verified live. `"*": "deny"` for the Foreman is the design-doc behaviour; both live attempts ended in the free-tier cap, so it is Unknown whether the turn completes with no tools.
- **Usage.** Prompt result: `usage {inputTokens, outputTokens, thoughtTokens, cachedReadTokens, totalTokens}` with `totalTokens = input + output + thought + cachedRead` (checked on three results) and no cache-write field. `inputTokens` is net of cache. Reasoning is outside `outputTokens`, so the harness adds `thoughtTokens` to output tokens for OpenCode. `usage_update.cost.amount` was 0 on a free model. (Verified-live)
- **Persistence.** `loadSession: true` plus `resume`, `fork`, `list`, `close`, `delete`. A second process with the same `XDG_DATA_HOME` answered `session/load` by replaying `user_message_chunk` / `agent_message_chunk` and then remembered the earlier turn. Sessions live in `$XDG_DATA_HOME/opencode/opencode.db`, so that dir must persist per session (the harness uses `<session dir>/.local/share`). (Verified-live)
- **Stop.** `end_turn` on every successful turn. A rejected permission request cancelling the turn is from the 2025 spike (design doc section 12), not re-run. Not run: `session/cancel`, concurrency on one state dir.

### codex-acp findings (2026-10-30)

Package: `@agentclientprotocol/codex-acp` 2.5.0 (bin `codex-acp`, bundles `@openai/codex` 0.165.0 with linux x64 and arm64 builds). `@zed-industries/codex-acp` (0.16.0) is deprecated in its favour. No account was logged in. Run live: `initialize`, then `session/new` without credentials, then `authenticate {methodId:"api-key"}` with a made-up `OPENAI_API_KEY` in an empty `CODEX_HOME` and `session/new` again (nothing but the model-list refresh, which got a 401, left the machine). No prompt was run, so every turn-time behaviour below is From-source (`dist/index.js`). Captures: `testdata/codex-acp-new.jsonl`, `codex-acp-error-auth.jsonl` (live); `codex-acp-{edit,deny-command,deny-filechange,error-quota}-synthetic.jsonl` are hand-built from the source's shapes and named `-synthetic`.

- **Auth.** `initialize` advertises one method, `api-key` (ChatGPT login is added unless `NO_BROWSER=1`, which the harness sets). Without auth `session/new` fails `-32000 Authentication required`. With an API key in env (`CODEX_API_KEY` wins over `OPENAI_API_KEY`) the client must call `authenticate {methodId:"api-key"}` first; the harness does when either variable is set. Whether a ChatGPT `auth.json` in `CODEX_HOME` lets `session/new` pass without `authenticate` is Unknown (no login). `OPENAI_BASE_URL` did not redirect the model refresh to a local endpoint (Verified-live, negative); `CODEX_CONFIG` (JSON merged into the Codex config) and `MODEL_PROVIDER` exist for gateways (From-source, one live try with `model_providers` was inconclusive).
- **Session shape.** `session/new` returns `{sessionId, models, modes, configOptions}`. `configOptions`: `mode` (`read-only`, `workspace-write`, `agent`, `agent-full-access`; default `agent` = "Auto review"), `collaboration_mode`, `model` (`gpt-6.1-sol`, ...), `reasoning_effort` (category `thought_level`: `low`..`ultra`), `fast-mode`. `models.availableModels[].modelId` is `<model>[<effort>]`. The harness accepts `gpt-6.1-sol` or `gpt-6.1-sol[high]` and sets the `model` and `thought_level` options. (Verified-live)
- **Read-only roles.** `mode` = `read-only` means a read-only sandbox with `on-request` approvals; `agent-full-access` means no sandbox and no approvals (From-source, `AgentMode`). The harness sets `read-only` for every non-builder role and for the Foreman, and `agent-full-access` for the builder: the container is the boundary, and Codex's own sandbox needs kernel features (Landlock/bwrap) that an unprivileged container may lack (Unknown; if reads fail with a sandbox error in the image, read-only roles need a different mode). The first non-read request from a read-only role is rejected by the permission policy.
- **Permission requests** (From-source). Command approvals offer `allow_once`, `allow_always`, `reject_once` (`decline`: carry on) and `reject_once` (`cancel`). File-change approvals offer `allow_once`, `allow_always` and only `reject_once` (`cancel`), which ends the turn (`stopReason: cancelled` expected). The harness picks the first `reject_once`, so `decline` for commands. It never falls back to an allow option for a denied request.
- **Tool deny.** Codex has no per-tool deny list: `ToolPolicy.Deny` is ignored and `AllowNone` means read-only mode plus rejecting every permission request; read tools stay available. (From-source)
- **Tool updates** (From-source). `tool_call` carries `kind` (`execute`, `edit`, `read`), a descriptive `title` ("Run `cat main.go`", "Edit files", "Read file 'x'") and optional `name`; edits come as `content:[{type:"diff"}]`; the harness reports the title as the tool name.
- **Usage** (From-source). Prompt result `usage {inputTokens, cachedReadTokens, cachedWriteTokens, outputTokens, thoughtTokens, totalTokens}`; `inputTokens` is already net of cache. `thoughtTokens` is the reasoning subset of `outputTokens` in OpenAI accounting, so unlike OpenCode it is not added. Unknown until a real turn is run.
- **Errors** (From-source). A usage-limit turn fails `-32603 Internal error` with `data {message, codexErrorInfo:"usageLimitExceeded"}`; the harness folds `data.message` into the error text and classifies `codexErrorInfo` (usage limit, budget, context window, unauthorized, bad request and policy are permanent; rate limit and overload are transient). Other turn failures, without the typed-failure extension, are written as an `agent_message_chunk` and the stop reason may still be `end_turn`: a turn that only apologises looks like success, so the engine's no-change check (lesson 2) must catch it.
- **Persistence.** `loadSession: true` and `resume`, `fork`, `list`, `close`, `delete` are advertised (Verified-live); history lives under `CODEX_HOME`, which the harness points at `<session dir>/.codex`. `session/load` was not run.

### Per-agent behaviour in the harness

| | claude-agent-acp | opencode acp | codex-acp |
|---|---|---|---|
| Model | `_meta.claudeCode.options.model` (or option on load) | `set_config_option` on `provider/model` | `set_config_option` `model` + `thought_level`, or `set_model` |
| Read-only role | inline `settings.permissions.deny` | `OPENCODE_CONFIG_CONTENT` deny list | `mode: read-only` + reject non-read requests |
| `AllowNone` | `tools: []` | `"*": "deny"` | read-only mode + reject all requests |
| Permission reply | reject for read-only roles | always allow (reject cancels the turn) | reject for read-only roles and `AllowNone` |
| Auth | `CLAUDE_CODE_OAUTH_TOKEN` / API key / gateway env | provider env key, `OPENCODE_API_KEY`, config `apiKey` | `authenticate api-key` + `OPENAI_API_KEY` / `CODEX_API_KEY`; `auth.json` in `CODEX_HOME` |
| Output tokens | `outputTokens` | `outputTokens + thoughtTokens` | `outputTokens` |
| Error tag | `errorKind` | `errorName` | `codexErrorInfo` |

## 8. Credentials, catalog and capability

- Catalog per credential:
  - API key: the provider's model list;
  - subscription: the plan's models only.
- Compatibility matrix credential kind → harnesses, for example:
  - Claude subscription → `claude-agent-acp` only;
  - ChatGPT subscription → `codex-acp` or `opencode acp`;
  - OpenCode Go → `opencode acp`;
  - Z.ai or Moonshot API → `claude-agent-acp` (compatible endpoint) or `opencode acp`.
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

- Image: harness CLIs, Node (for `claude-agent-acp`), git, language toolchains. Repository-specific tools come
  from the project's setup command.
- Volume layout: `/ws/repo` (main clone), `/ws/wt/<task>` (worktrees),
  `/ws/home/<session>` (per-session harness state, e.g. `CLAUDE_CONFIG_DIR`). The worktree path must be identical across containers because the agent keys transcripts by cwd.
- Shared read-mostly caches across sandboxes: `GOCACHE`, `GOMODCACHE`, npm cache.
- Per-worktree `PORT` allocation by the engine.
- Limits: ≤3 sandboxes, ≤3 builders per sandbox, CPU and memory limits per
  container (config values until measured, §13 Q1).
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
   checkout has inconsistent `vendor/modules.txt`. Until measured, container limits
   are config values (default 4 CPU, 8 GiB).

## 14. Resolved questions (2026-10-30)

| # | Question | Answer |
|---|---|---|
| 2 | Bitbucket auto-merge rights | The factory merges with the project's SCM credential. If branch protection refuses the merge, the work order goes to `awaiting_human`; no bot account is required. |
| 3 | Claude subscription tokens | Allowed (single-owner, F12). |
| 4 | Which providers serve Builder through `claude-agent-acp` | The user's choice in config. Any profile that passes the smoke test may bind to Builder; no provider list is hard-coded. |
| 5 | Where the rebuild lives | This repository, new Go module `factory/` on branch `feat/new-legos-architect`. `backend/` is frozen; the ACP client, SCM clients and secret encryption are ported (copied and adapted), not imported. |
| 6 | K and M defaults | K = 3 inspection rounds per task, M = 3 consecutive Foreman rejections. Both are config values. |

## 15. Code layout

One Go module `factory/` (`module legos-factory/factory`), SQLite via `modernc.org/sqlite`.

| Package | Owns |
|---|---|
| `cmd/factory` | process entry, config loading |
| `internal/fact` | fact store, derived status (F06) |
| `internal/harness` | ACP client, `Harness`/`Session`, normalized events (§7) |
| `internal/sandbox` | workspace volume, container lifecycle, worktrees, ports (§10) |
| `internal/scm` | push, PR, checks, merge (GitHub, Bitbucket) |
| `internal/profile` | credentials, catalog, profiles, smoke test, semaphores, cost (§8) |
| `internal/workflow` | work-order state machine and guards (§5, §6) |
| `internal/engine` | runs work orders: roles, Foreman loop, verify, integration, retries |
| `internal/autonomy` | `diff_level`, effective level (§9) |
| `internal/api` | HTTP API for projects, credentials, profiles, work orders |
