# MCP connections and one settings page — plan

Three parts: a safety fix found during the spike, MCP servers for factory agents, and a
rebuilt settings page. Scope agreed on 2026-11-11: workspace level only — no per-project MCP
setup, no personal preferences section — and both auth paths (static token and OAuth).

## Part 0 — Safety fix (do first, on its own)

**Finding (measured on `main`):** a Builder session on claude-agent-acp can see the claude.ai
connectors of the logged-in account. A control run listed `Gmail`, `Slack`, `Notion`,
`Google_Calendar`, `Claude_Docs`, `Excalidraw` and `Amartha_workbench`. `answerPermission`
allows everything for the Builder, so a Builder could send mail or post to Slack as you. The
connectors load asynchronously, so they showed in 3 of 5 Builder runs.

**Cause:** `claudeBuilderMeta` (`backend/internal/agent/acp.go:111`) has no `strictMcpConfig`.
The read-only and chat metas have it; none of 10 runs with it ever showed a connector.

**Fix:** add `"strictMcpConfig": true` to `claudeBuilderMeta`. With it, two Builder runs found no
connector, and a server passed through ACP still worked.
→ verify: a test that the Builder's session meta carries `strictMcpConfig: true`; one live run that searches for `mcp__claude_ai` tools and finds none.

## Part 1 — MCP servers for factory agents

### Spike results (claude-agent-acp 0.100.0, Sonnet)

| Question | Result |
|---|---|
| Does `strictMcpConfig: true` drop servers passed in ACP `session/new` → `mcpServers`? | No. They load and their tools are callable, even with `tools: [Read, Grep, Glob]` |
| Transports | stdio (`command`, `args`, `env`) works. HTTP with an `Authorization` header works; the server saw the bearer token. The agent advertises `mcpCapabilities: {http, sse}` |
| Do read-only roles stay read-only? | Not by themselves. A tool the server marks `readOnlyHint: true` ran in plan mode with **no permission request**. A tool without it raised a permission request (`kind: "other"`, with the tool and server name), which the factory rejects for read-only roles and allows for the Builder |
| Can one tool be removed? | Yes. `mcp__<server>__<tool>` in `disallowedTools` removes it from the session |
| Token cost | Two tiny tools added about 150 tokens per model request. Real servers carry far more; the Builder loads MCP tools on demand, read-only roles get the full list up front |

Consequence: plan mode and the server's own labels are not a boundary. The factory keeps a
per-tool allowlist and enforces it twice: `disallowedTools` for everything not allowed, and the
permission handler for any call that still asks.

### Decisions

| # | Decision | Why | Rejected |
|---|---|---|---|
| 1 | The factory owns the server list and passes it per session through ACP `mcpServers` | Works under `strictMcpConfig`, is recorded per run, same path for every role | Reusing your `~/.claude` MCP config: Claude only, drags in hooks and plugins, no per-role control |
| 2 | Workspace scope only. Each server has a role list (foreman, architect, builder, inspector, chat), empty by default | Agreed. Every enabled server costs tokens on every run of that role | Per-project enablement |
| 3 | Per-tool allowlist, filled from discovery. New tools start **off**; an admin turns them on. Tools without `readOnlyHint` can be enabled for the Builder only | The spike showed labels alone decide nothing | Trusting `readOnlyHint` |
| 4 | Own table `mcp_servers`, shown in the same Connections list as git and Jira | Needs transport, command, headers, OAuth tokens, roles and tool policy — none fit `connections` | A `provider = "mcp"` row in `connections` |
| 5 | Admin only. A stdio server runs a command on the factory host | It is code execution by configuration | Engineer-managed servers |
| 6 | claude-acp first. OpenCode-ACP gets servers only after its own spike | Per-tool removal was verified on Claude only | Shipping both unverified |

### Limits (what the allow set does and does not guarantee)

- Every MCP call is checked against the allow set: the session carries an `ask` rule `mcp__<server>` for each mounted server, so even a `readOnlyHint` tool or one added after discovery raises a permission request, and `answerPermission` allows it only if that exact tool name is enabled (verified live on claude-agent-acp for the Architect and Builder).
- A tool's read-only status is the server's own annotation at discovery time. A server that later changes a tool's behaviour or annotation is trusted until it is rediscovered.
- For the Builder the allow set is not a hard boundary: it has Bash and inherits the process environment, and the repository's own `.claude/settings.json` is loaded.
- claude-agent-acp starts stdio servers with the agent's environment at run time; the minimal environment applies to discovery only (not verified).

### Data and flow

- `mcp_servers`: id, name (the `mcp__<name>__` prefix, unique, `^[a-z0-9]+(_[a-z0-9]+)*$`, at most 40), transport (`stdio` | `http`),
  command + args, url, auth (`none` | `token` | `oauth`), sealed secret (env or headers, or OAuth
  access + refresh token + expiry + client registration), roles, tools (name, read-only flag,
  enabled), status, last error, discovered at. Sealed with the existing `Sealer`.
- **Discover / Test:** the backend opens the server itself (MCP `initialize` + `tools/list`),
  stores the tool list and shows the count and schema size, so the token cost is visible before a role is enabled.
- **Session start:** for the run's role, the harness gets the enabled servers; `AgentRequest` gains
  `MCP []MCPServer` and a list of disallowed tool names. `acp.go` sends them in `session/new` and
  appends the names to `disallowedTools`. `answerPermission` allows an MCP call only when that tool
  is enabled for the role.
- **Audit:** the `agent_started` event records server names and enabled tools.

### Auth paths

- **Token:** entered once, sealed, sent as a header (http) or env var (stdio).
- **OAuth** (remote servers such as Atlassian or Notion): the factory runs the flow —
  protected-resource and authorization-server discovery, dynamic client registration, authorization
  code with PKCE, callback at `/api/v1/mcp/oauth/callback` — then stores and refreshes the tokens
  and sends the access token as a bearer header. "Connect" in the UI opens the provider's consent page.

## Part 2 — One settings page

### What is wrong today (from the inventory)

- **Edited in two places:** project ↔ git connection binding (Add project dialog and Settings → Integrations → "Project repositories"), with different and partly wrong help text ("without one, work orders cannot start").
- **In the wrong place:** project contract tests under Integrations; Jev status under Integrations.
- **Tabs that do nothing:** "Team & access" (everything unavailable), "My preferences" (one theme switch), three permanently disabled inputs under Usage.
- **Wrong under the current harness:** the "OpenCode" card reads "Not reported" on ACP; help text says models come from "OpenCode discovery".
- **Hidden coupling:** chat "Auto" silently uses the Architect default; "snapshotted" Foreman and Architect models are not snapshotted when the default is blank.
- **Dead:** `cataloger_summaries` (stored, never read), `FACTORY_RUN_TIMEOUT` and `FACTORY_SANDBOX_MODE` (parsed, never used), `autonomy` on work-order create (ignored), `projects.quality_profile` (no writer), `allowed_models` (no UI, chat only).
- **Fragile:** `PUT /settings` overwrites every key, so an omitted field is blanked.

### New page — one scroll, four sections, no tabs

| Section | Holds | Removed or merged |
|---|---|---|
| **Agents** | Model per role, with chat as its own row; one line stating the order: work order choice → workspace default → first available model | "Governance" tab; the hidden chat coupling |
| **Connections** | One list: git providers, Jira, MCP servers. Add, test, disable, remove, rename, rotate token. Under it, one row per project: which connection it publishes through, `owner/repo`, and contract tests | "Integrations" tab; the OpenCode card; the separate "Project repositories" block |
| **Runtime** | Read-only truth from the backend: harness and command in use, Jev on/off and model, credential storage, concurrent runs. Each line says where it is set | Jev card; the three disabled inputs; scattered env banners |
| **Budget** | The cap and current usage | "Usage & resources" tab |

Out of Settings: the theme switch moves to the app shell; "Team & access" is removed until it does something.

Project binding and contract tests stay, in **one** place: the project rows of the Connections
section. There is no project settings screen. The Add project dialog mounts the same row component
for the choice at creation, so there is one implementation and one wording; the wrong sentence
"without one, work orders cannot start" goes.

### Backend changes

- `PATCH /settings`: only the keys sent change. `chat_model` becomes its own key (blank = Architect default, as today).
- `GET /settings/runtime`: harness name, command, Jev state and model, secret-key state, max concurrent runs.
- Snapshot the *resolved* Foreman and Architect model on work-order creation, so the text is true.
- Remove the dead items listed above.

## Steps

1. **Part 0 fix** → verify as above. Own PR, merged first.
2. **OAuth spike** (no merge) against one real remote server → verify: a note stating whether discovery, registration, the callback on `127.0.0.1` and refresh work, and what the provider requires.
3. **MCP backend** — table, sealed secrets, discover/test, role and tool policy, harness wiring, audit → verify: store and policy tests; an agent test that the session carries the servers and the disallowed names; a permission test per role; one live run with a local test server.
4. **MCP token path in the UI** → verify in the running app: add a server, discover, enable one tool for one role, see it used in a run and listed in the run's audit.
5. **MCP OAuth path** (shaped by step 2) → verify: connect a real server, restart the factory, run again without reconnecting.
6. **Settings backend** — `PATCH`, runtime endpoint, `chat_model`, resolved snapshot, dead items removed → verify: handler tests; a test that an omitted key is untouched.
7. **Settings page** → verify in the running app: every control saves and survives reload; no tab, no disabled placeholder, no text naming a harness that is not running.

## Split

| Agent | Steps | Files |
|---|---|---|
| A | 1 | `agent/acp.go` + test |
| B | 3 | new `usecase/mcp.go`, `store/mcp.go`, migration, `agent/acp.go`, `model/agent.go`, `http` routes |
| C | 6 | `http/handlers.go`, `store/settings.go`, `usecase/governance.go`, `usecase/workorder.go`, `app/config.go` |
| D | 4, 7 | `frontend/src/features/settings/*`, app shell, Add project dialog |

A first. B and C in parallel (different files, except `http/server.go` routes: B adds, C edits the settings group). D starts from the API contract in this plan once B and C have fixed it. Step 5 after step 2.

## Uncertain

- Whether the remote servers you want accept dynamic client registration and a `127.0.0.1` callback. Step 2 answers it; if not, OAuth needs a pre-registered client per provider.
- Whether OpenCode-ACP honours per-tool removal.
- Whether `disallowedTools` also hides a tool that is loaded on demand in a Builder session. Verified for the read-only profile only.
- Whether the Add project dialog should keep the connection choice at creation (same component, as planned) or drop it and leave the choice to Settings alone.
- Removing `FACTORY_RUN_TIMEOUT` and `FACTORY_SANDBOX_MODE` changes what `.env` files may contain; an old `.env` that sets them must still start.
