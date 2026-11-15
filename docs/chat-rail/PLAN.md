# Chat session rail — plan

Status: approved 2026-11-16 · prototype https://claude.ai/artifact/6aCnMPDCac7nf2ATXqxMUc

The chat's right panel becomes a session rail with four collapsible sections:
Project, Context, MCP, From this conversation. Chats stay optionally bound to a project.

Two PRs: **A backend** (this data), **B UI** (rail + thin scrollbar).

## A. Backend

### A1. Record more per assistant turn (no migration: `chat_messages.usage` is JSON)

Extend `model.ChatUsage` (all `omitempty`):

| field | json | value |
|---|---|---|
| CacheRead, CacheWrite | `cache_read`, `cache_write` | from `AgentResult` |
| RepoSHA | `repo_sha` | checkout SHA the turn read (`cur.sha`), repo chats only |
| NewSession | `new_session` | true when this turn started a harness session (Start, not Continue) |
| PromptBytes | `prompt_bytes` | `len(req.Prompt)` actually sent this turn |
| FactsBytes | `facts_bytes` | bytes of the repo-facts seed, new sessions only |
| MCPToolBytes | `mcp_tool_bytes` | sum of `MCPTool.Size` for the tools offered to this session (enabled, usable) |
| AttachBytes | `attach_bytes` | text file bytes + image bytes sent this turn |
| MCPCalls | `mcp_calls` | map `"server/tool"` → count, from `OnStep` tool names `mcp__<server>__<tool>` |

`Chat.answer` returns these alongside the result (e.g. a small `turnInfo` struct); `Send` stores them on the assistant message. A failed turn stores whatever is known.

### A2. `GET /api/v1/chats/:id/session` (owner only, same auth as `GET /chats/:id`)

```json
{
  "project": null | {
    "id": "…", "name": "…",
    "local": null | {"path": "/abs", "branch": "main", "head": "98b4039…", "dirty": 1},
    "worktrees": [{"path": "/abs", "branch": "feat/x", "head": "…", "main": false}],
    "checkout": null | {"sha": "…", "behind_local": 0 | null},
    "running": [{"work_order_id": "…", "title": "…", "state": "IMPLEMENTING", "role": "builder", "task_id": "…", "since": "RFC3339", "workspace": "/abs"}]
  },
  "context": {
    "model": "claude/sonnet", "window": 200000 | 0,
    "used": 64100,
    "parts": [{"key": "agent|mcp|facts|files|messages", "tokens": 0}],
    "cache": null | {"read": 0, "write": 0, "uncached": 0, "hit_pct": 94, "last_turn_at": "RFC3339", "warm_until": "RFC3339"}
  },
  "totals": {"turns": 6, "tokens_in": 0, "tokens_out": 0, "cache_read": 0, "cache_write": 0, "cost_micros": 0},
  "mcp": [{"id": "…", "name": "workbench_amartha", "transport": "http", "auth": "oauth", "status": "active",
           "offered": true, "tools_enabled": 9, "tools_total": 9, "asks": true, "calls": {"whoami": 1}}]
}
```

Rules:
- **project.local**: only when the project's `repo_url` is an existing local directory (not a remote URL). Git runs with `exec.CommandContext` (no shell), 3 s timeout total, args `git -c core.fsmonitor=false -c core.hooksPath=/dev/null -C <dir> …` (a repo's own config must not run commands). Commands: `rev-parse --abbrev-ref HEAD`, `rev-parse HEAD`, `status --porcelain` (count lines), `worktree list --porcelain`. Any git failure → that field null/empty, never a 500.
- **checkout**: SHA from the latest assistant message with `repo_sha`; `behind_local` = `rev-list --count <sha>..HEAD` in the local dir, null when unknown.
- **running**: work orders of this project in non-terminal states with a RUNNING run; role from the run kind; workspace = the WO's factory workspace dir. Read from existing stores; no new tables.
- **context.used** = last successful turn's `tokens_in + tokens_out` (tokens_in already includes cache read/write).
- **parts** (estimates, tokens ≈ bytes/4), over the current session = messages since the latest `new_session` turn:
  - `mcp` = session-start MCPToolBytes/4; `facts` = FactsBytes/4; `files` = sum AttachBytes/4 since session start;
  - `agent` = max(0, start.tokens_in − start.PromptBytes/4 − start.MCPToolBytes/4) (the agent's own system prompt and built-in tools);
  - `messages` = max(0, used − agent − mcp − facts − files).
  No new_session turn recorded (old chats): parts = `[]`, used still shown.
- **window**: one table in one place: `claude/*` → 200000; model id containing `[1m]` → 1000000; else 0 (UI shows tokens without %). `// TODO: uncertain — ACP model ids and real windows not verified per model`.
- **cache**: from the last successful turn; uncached = tokens_in − read − write (≥0); hit_pct = round(read*100/tokens_in); warm_until = last_turn_at + 5 min. `// TODO: uncertain — TTL may be 1h on subscription logins`.
- **mcp**: every active-or-not MCP server; `offered` = server lists role `chat` and has ≥1 enabled usable tool (same rule as `MCPServers.Selection`); `asks` = chat asks for any enabled non-read-only tool; `calls` summed over all assistant messages of this chat. Never return secrets or headers.

### Pitfalls (must handle)
- Owner check and project access (`Projects.Authorize`) exactly like `Chat.Get`/`Send`; a chat whose project the user lost shows `project: null`.
- Git on a path from the DB: never through a shell; disable fsmonitor/hooks; time out; ignore errors.
- The session endpoint must not take the chat lock or block on a running turn.
- `MCPCalls`: OnStep is called from the harness goroutine; guard with the existing turn mutex.
- Keep `json` field names exactly as above (UI PR B is built against them in parallel).
- Tests: usecase tests for parts math (session start, continue, old chat), MCP calls counting, git helper with a temp repo (incl. a repo whose config sets core.fsmonitor to a command that would create a file: assert the file is NOT created), handler auth (other user → 404).

## B. UI (separate PR)

Replace `ChatContextPanel.vue` content with the rail from the prototype's Main/General frames: sections Project / Context / MCP / From this conversation, each collapsible (state kept per section in localStorage, try/catch). Data from `GET /chats/:id/session`, refetched after each turn's `done`/`error` and when the project changes. Keep existing features: project select (bind/unbind), create work order, related knowledge, attachments list, delete chat. Running agents link to `/factory/<wo>`. Context bar segments with a legend that shows numbers (colour is never the only cue); "estimated" hint on parts. Cache card: Warm with minutes left / Cold. Thin scrollbar: transparent track, thumb on hover for `.ch-scroll, .ch-ctx, .chatlist .ch-items`.
