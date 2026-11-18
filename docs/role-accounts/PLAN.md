# Per-role agent accounts (Claude on Anthropic + DeepSeek side by side)

2026-11-18. Goal: the factory can run some Claude roles on the normal Claude login (Opus, Sonnet, …) and others on an
Anthropic-compatible provider (DeepSeek, GLM) at the same time, for the same user.

## Why it doesn't work today

- Each role picks an *agent* (`settings.role_agents`, workspace-wide) and a *model*. The *account* is one per user per
  agent: the active one. `Agents.RunEnv(ctx, agent, user)` returns only `CLAUDE_CONFIG_DIR=<active home>`
  (`backend/internal/usecase/agents.go:80,270`). So every Claude role shares one provider.
- Claude Code 2.1.325 ignores `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_API_KEY` from the
  `env` block of `settings.json` (runs fail with "Not logged in"; `apiKeyHelper` still sends to Anthropic). It *does*
  honor `ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL` there. With the URL and key in the process env, opus → `deepseek-flash[1m]`,
  sonnet → `deepseek-v4-pro`, haiku → `deepseek-chat` all answered (checked with the `claude` CLI on account
  `acc_900e7ee8-b8f`).
- `claude auth status` reports `loggedIn: false` for such an account even with the env set, so the account card shows
  it as signed out.

## Decisions (user, 2026-11-18)

1. The role → account choice lives **on each account** (Settings → Agents), per user. Roles not assigned use the active
   account, as today.
2. Base URL and key come **from the account's own `settings.json`**; the factory lifts them into the process env. No
   new secret storage or form.
3. The account is resolved **at each run**, not snapshotted on the work order.

4. **Codex serves every role** (user, 2026-11-18), accepting that Codex has no hard read-only and reads any file
   without asking. Foreman, Architect, Chat: mode `read-only`, every permission request refused. Builder and
   Inspector: mode `workspace-write`, requests allowed, so Inspector can run tests but could also edit code.

## Step 1 result (ACP spike, 2026-11-18)

PASS. Through `claude-agent-acp`, opus answered as `deepseek-flash[1m]` and sonnet as `deepseek-v4-pro`. Unlike the
CLI, the adapter applies the `settings.json` env block itself: with only `CLAUDE_CONFIG_DIR` set it still routed to
DeepSeek. The env lift is therefore redundant for runs; it is kept as a guard against the adapter changing, and the
factory still derives the account's status itself.

## Design

### Data

New table (migration `028_agent_role_accounts.sql`), because the "System default" account is virtual (`SystemAccountID`,
never stored) and must be assignable too:

```sql
CREATE TABLE agent_role_accounts (
    user_id    TEXT NOT NULL,
    agent      TEXT NOT NULL,
    role       TEXT NOT NULL,
    account_id TEXT NOT NULL,   -- an agent_accounts.id or 'system'
    PRIMARY KEY (user_id, agent, role)
);
```

One row per (user, agent, role), so a role has at most one account. Deleting an account deletes its rows.
Cataloger keeps following Architect, as `AgentSpec.Allows` and `RoleAgent` already do.

### Resolution (usecase)

- `RunEnv(ctx, agent, user, role)`: account = role row if present and the account still exists, else the active account.
  Callers: `factory.RunEnv` (`app.go:208`, has the role), chat runner (`app.go:309`, role chat), model catalog
  (`app/agents.go:110`, no role → active account, unchanged).
- Env lift, Claude only: read `<home>/settings.json`; if `env.ANTHROPIC_BASE_URL` is set, append it and whichever of
  `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_API_KEY` is present to the returned env. Nothing for the System default
  account (it uses `~/.claude` and the backend's own env). Only these three keys; everything else in `settings.json`
  is left to Claude Code.
- Status: a Claude account with a lifted base URL + key reports `signed_in`, plan `API key · <host>` (e.g.
  `api.deepseek.com`), without running `claude auth status`. Usage stays empty, so the Usage card skips it.

### API

- `PUT /api/v1/agents/:agent/accounts/:aid/roles` body `{"roles":["builder","inspector"]}`, engineer role, own
  accounts only (same checks as activate). Setting roles on one account removes those roles from the user's other
  accounts of that agent. Rejects roles the agent can't serve (`AgentSpec.Allows`).
- `GET /api/v1/agents` account view gains `roles: string[]`.

### UI

- `AgentRow.vue`: each account shows role chips (Foreman, Architect, Builder, Inspector, Chat), limited to
  `roles_allowed`; clicking toggles it. Chips taken by another account show which one has them. The active account
  keeps its "Active" marker, worded as the fallback ("Active · used by roles not assigned").
- `RolesTable.vue` `hint()`: check the account that will actually serve the role (role assignment, else active),
  so "isn't signed in" is correct for an API-key account. Show the provider host next to the model when that account
  is an API-key one, because the model list still says "Opus/Sonnet" while DeepSeek answers.

## Steps

Each step ends green on `go test ./...` (backend) or `npm run build` + vitest (frontend).

1. **Spike via ACP, not just the CLI** → verify: start `claude-agent-acp` with `CLAUDE_CONFIG_DIR` + lifted env,
   `session/new`, set model `opus`, one prompt answers from DeepSeek. If it fails, stop and report before building.
2. **Env lift + status** in `usecase/agents.go` → verify: unit tests with a temp home: settings.json with base URL + key
   gives the env and `signed_in`/`API key · host`; no base URL gives today's env; System default untouched.
3. **Role accounts store + resolution** (migration, store methods, `RunEnv` role param, delete cascade) → verify: tests
   for assigned role, unassigned role → active, assigned account deleted → active, cataloger → architect.
4. **API** (`PUT …/roles`, `roles` in view) → verify: http tests: own account only, moving a role between accounts,
   rejects a role the agent can't serve.
5. **UI** (chips in `AgentRow`, `hint()` + provider host in `RolesTable`) → verify in the browser: assign Builder to the
   DeepSeek account, Foreman stays on System default; the Roles table shows the right account status for both.
6. **Live check** → one small work order: Foreman/Architect on System default (Anthropic), Builder on DeepSeek. Check
   the run log shows each role on its provider (DeepSeek usage at platform.deepseek.com moves only for the Builder).

One PR, following the usual pipeline (Opus implements → Fable review → fix → merge).

## Out of scope

- A form for base URL/key (decision 2).
- Snapshotting the account on the work order (decision 3).
- Usage bars for API-key accounts.
- GLM: same mechanism; only its settings.json differs (`https://api.z.ai/api/anthropic`). Not tested.

## Risks / open

- Claude Code may change which settings it honors; the lift only touches three keys, so a future version that honors
  them from settings.json would get the same values twice, which is harmless.
- The key stays plain text in `settings.json` (account dir is 0700).
- Prompt caching, context-window size (`unrecognized_model` warning assumes 1M) and tool-use behavior on DeepSeek's
  Anthropic endpoint are unverified for long factory runs; step 6 is the first real test.
