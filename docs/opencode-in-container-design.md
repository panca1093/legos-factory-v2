# OpenCode inside the factory deployment — design

Status: draft for review (2026-10-25). Nothing implemented yet.

Scope: **single-user self-hosting.** One person runs the factory for themselves and
the agents use that person's own subscription logins. Multi-user deployments and
shared/BYO credentials are out of scope (see §10).

## Problem

Agents run in the OpenCode **desktop app** on the host (`:5001`). The harness never
passes a directory, so every session opens in the desktop app's default project — this
repo. The work order workspace (`/data/workspaces/<wo>`) only exists inside the
container, so OpenCode cannot reach it, and an autonomous agent with a bash tool has a
shell on the host Mac. Agent `go test` (macOS toolchain) and factory checks (Linux
container) can also disagree.

## Goals

- Agents read and edit **only** the work order workspace.
- Agents and quality checks share one Linux toolchain.
- Factory sessions are isolated from the user's personal OpenCode history.
- OpenCode version is pinned and never self-updates.

- Agents authenticate with the owner's own subscription logins, logged in once and kept
  in the deployment's private state.

Non-goals: fixing provider quota/privacy limits (account-level), multi-host scaling,
multiple users, per-workspace credentials, reselling or sharing subscription access.

## Facts verified against OpenCode v2.1.3 (host binary + `/openapi.json`)

| Need | What exists |
|---|---|
| Server | `opencode serve --hostname H --port P` |
| Server auth | env `OPENCODE_SERVER_PASSWORD` (basic auth user `opencode`) |
| Session directory | `POST /api/session` body accepts `location: {directory: <abs path>}` |
| Per-session permissions | same body accepts `permissions: [{action, resource, effect: allow\|deny\|ask}]` |
| Credentials | `opencode auth export` (JSON incl. secrets) / `opencode auth import`; env `OPENCODE_API_KEY` |
| Self-update | env `OPENCODE_DISABLE_AUTOUPDATE` |

Not yet verified (spike before coding, see §8): Linux install method for 2.1.3, which
permission `action` names exist, whether `location` is honoured by the tools.

## 1. Process model

**Recommended: sidecar service, same image.** Compose runs two services from one image:
`factory` (existing) and `opencode` (`command: opencode serve`). Both mount the data
volume at the same path, so `/data/workspaces/<wo>` means the same thing to both.

Why sidecar instead of two processes in the factory container:
- separate health checks, restarts and logs (`docker compose logs opencode`);
- no process supervisor script inside the factory container;
- same image ⇒ identical Go/Node/Git toolchain for agents and checks.

Trade-off: the user asked for "inside the factory container". The sidecar keeps
everything inside the deployment and off the host; if a single container is preferred,
an entrypoint script can start `opencode serve` in the background and `exec factory`
(costs: manual supervision, interleaved logs). **Decision needed.**

## 2. Version pin

```dockerfile
ARG OPENCODE_VERSION=2.1.3
RUN <install opencode ${OPENCODE_VERSION} for linux/$TARGETARCH>   # method from spike
ENV OPENCODE_DISABLE_AUTOUPDATE=1
```

- Upgrading = bump the ARG, rebuild, rerun the harness live test.
- Factory logs the OpenCode version at startup (`GET` a version/health route — exact route
  from spike) so a mismatch is visible.

## 3. Auth

Two separate secrets, both in gitignored files:

1. **Server password** (factory → OpenCode): existing `FACTORY_OPENCODE_PASSWORD` in
   `.env`, passed to the sidecar as `OPENCODE_SERVER_PASSWORD`. The OpenCode port is
   **not published** to the host; only the compose network can reach it.
2. **Provider credentials** (OpenCode → model providers) are the owner's own
   subscription logins (e.g. OpenCode Go). They are OAuth-style tokens that expire and
   refresh, so they must live somewhere **writable** and persistent:
   - Credentials live in the `opencode-state` volume, never in the image, `.env`, git
     or logs.
   - **Preferred: log in inside the container**, once:
     `docker compose exec opencode opencode auth login`. Tokens never leave the
     deployment. Depends on the login flow working headless (device code or pasted
     code) — to confirm in the spike.
   - **Fallback: copy from the owner's machine**, once:
     `opencode auth export > secrets/opencode-auth.json` on the host, then the
     entrypoint imports it **only when the state volume has no credentials yet** — so a
     restart never overwrites refreshed tokens with stale ones. `secrets/` is
     gitignored and the file is deleted after the first import.
   - `docker compose exec opencode opencode auth list` shows what is logged in;
     re-login uses the same command as the first login.
   - The factory checks at startup that OpenCode lists at least one model for each role's
     configured provider, and reports "OpenCode has no login for provider X" instead of
     failing on the first agent run.

OpenCode state (session DB, credentials, logs) persists in the named volume
`opencode-state`. Backing up that volume backs up the logins — treat it as a secret.

**Single-user guard.** Because every agent run spends the owner's subscription, the
deployment must not be usable by anyone else:
- Bind the factory to `127.0.0.1` (as today) or put it behind the owner's own reverse
  proxy auth; never expose it publicly in `dev` auth mode.
- Add `FACTORY_SINGLE_USER=1` (default in this compose file): the factory refuses to
  create or invite a second user and says why.

## 4. Session directory and sandboxing

Harness change (`backend/internal/agent/opencode.go`, `Start`): create sessions with

```json
{
  "location": {"directory": "<req.WorkspaceDir>"},
  "permissions": [ ...role rules... ]
}
```

Role rules (action names to confirm in spike):

| Role | edit/write | bash | read/grep/glob | outside workspace |
|---|---|---|---|---|
| builder | allow | allow | allow | deny |
| architect, foreman, inspector | deny | allow (read-only commands) | allow | deny |

- **No rule may resolve to `ask`.** A headless session that asks for permission blocks
  until the 15-minute timeout. Every action the agent can use gets an explicit
  allow/deny.
- The container is the real boundary; permissions keep agents out of other work orders'
  workspaces and stop reviewers from editing.

## 5. Compose changes

```yaml
services:
  factory:
    build: .
    environment:
      FACTORY_OPENCODE_URL: http://opencode:4096
      FACTORY_OPENCODE_PASSWORD: ${FACTORY_OPENCODE_PASSWORD:?set in .env}
    depends_on:
      opencode: { condition: service_healthy }
    volumes:
      - factory-data:/data
    # extra_hosts host.docker.internal removed (no host OpenCode)

  opencode:
    build: .
    entrypoint: ["/usr/local/bin/opencode-entrypoint"]   # imports auth, then serves
    command: ["opencode", "serve", "--hostname", "0.0.0.0", "--port", "4096"]
    init: true
    environment:
      OPENCODE_SERVER_PASSWORD: ${FACTORY_OPENCODE_PASSWORD:?set in .env}
      OPENCODE_DISABLE_AUTOUPDATE: "1"
    volumes:
      - factory-data:/data                    # same path as factory
      - opencode-state:/root/.local/share/opencode   # sessions + credentials (writable)
      - ./secrets:/run/secrets:ro                    # optional one-time auth import
    healthcheck: { test: [...health route...], interval: 10s, retries: 5 }
    restart: unless-stopped
    # no ports: reachable only from the compose network

volumes:
  factory-data:
  opencode-state:
```

`.env` keeps only `FACTORY_OPENCODE_PASSWORD`; `FACTORY_OPENCODE_URL` moves into
compose. Using the desktop app stays possible by overriding the URL, but is no longer
the default.

## 6. Code changes (when approved)

- `opencode.go`: send `location` and role `permissions` on session create; unit test
  asserts the request body.
- `Dockerfile`: install pinned OpenCode; add `opencode-entrypoint` script.
- `compose.yaml`, `.env` example, `.gitignore` (`secrets/`), README deployment section.
- Startup: factory logs OpenCode version; fails fast with a clear message if OpenCode is
  unreachable or has no login for a configured provider.
- `FACTORY_SINGLE_USER` guard on user creation/invites, with a test.
- `opencode-entrypoint`: import `/run/secrets/opencode-auth.json` only when the state
  volume has no credentials, then delete-safe (never overwrites refreshed tokens).

## 7. Risks

- **Credentials in a container**: kept only in the `opencode-state` volume; never baked
  into the image, committed, or logged.
- **Subscription terms**: see §10 — the main non-technical risk of this scope.
- **Image size**: OpenCode binary is ~200 MB.
- **Version drift**: pinned; live harness test gates upgrades.
- **Quota**: unchanged — the same account limits apply wherever OpenCode runs.

## 8. Spike before coding (≈1 hour, throwaway)

1. Install OpenCode 2.1.3 in the runtime image for linux/arm64 and linux/amd64.
2. `opencode serve` with `OPENCODE_SERVER_PASSWORD`; confirm basic auth and a health route.
3. `opencode auth login` inside the container (does the subscription login work
   headless?); fallback `auth import` from an exported file; `GET /api/model` lists the
   expected models; restart the container and confirm the login survives and refreshes.
4. Create a session with `location.directory=/data/workspaces/x`; confirm `read`/`bash`
   run there and a path outside is denied.
5. List permission action names; confirm none fall back to `ask` with our ruleset.

## 9. Verification after implementation

- Unit: harness request body; existing harness/usecase/frontend suites.
- Compose smoke: `docker compose up`, create a work order on a real repo with a linked
  connection, confirm the builder's edits appear in `/data/workspaces/<wo>` and nowhere
  on the host, checks run, and a PR branch is pushed.

## 10. Scope and subscription terms caveats

This design deliberately targets **one owner using their own subscriptions for their
own work**. That is the narrowest use that fits how consumer subscriptions are sold, but
it is not a guarantee of compliance. Check each provider's current terms before relying
on it; nothing below is legal advice, and terms change.

- **Subscriptions are personal and interactive by design.** Consumer plans are priced
  for one person using the vendor's own apps. An always-on server running autonomous
  agents on that login may be treated differently from interactive use (rate limits,
  "automated access" or "no sharing" clauses). Confirm the plan allows it.
- **OpenCode Go.** Terms for server/automated use are unknown to us. Two account-level
  conditions already surfaced in practice: a 5-hour usage cap (`429 Go usage limit
  exceeded`) and a data-use condition (`400 This Go model trains on request data…`,
  fixed only in the owner's OpenCode privacy settings). Both apply wherever OpenCode
  runs; the factory can only report them, not avoid them.
- **Claude Pro/Max.** Anthropic's Agent SDK documentation says that, unless previously
  approved, third-party developers may not offer claude.ai login or subscription rate
  limits in their products, and should use API keys. A future Claude harness must
  therefore **not** be offered as "log in with your Claude subscription" to anyone else.
  Even for the owner alone, using a Claude subscription through a third-party runtime
  (OpenCode or this factory) may not be permitted — verify before building it; the
  conservative path for Claude is an API key.
- **No sharing, no reselling.** The single-user guard (§3) exists so the owner's
  subscription is never spent by another person. Adding users, teams or a hosted offer
  means switching to API-key credentials per workspace — a different auth design, not
  a configuration change.
- **Data handling.** Code from the owner's repositories is sent to whichever provider
  each role uses, under that subscription's data policy (including training opt-ins).
  Private or client code should only go to providers whose policy the owner accepts.

### What changes if the scope ever widens

| From | To | Needs |
|---|---|---|
| Single user, own subscriptions | Multiple users | Per-workspace API-key credentials, encrypted at rest, injected per run; remove `FACTORY_SINGLE_USER` |
| OpenCode only | + Claude Agent SDK / Hermes harness | Same `Harness` interface; Claude via API key; Hermes server mode to be verified |

## 11. Spike findings (2026-10-25, throwaway container from the factory image, arm64)

| # | Question | Result |
|---|---|---|
| 1 | Install 2.1.3 on Linux | ✅ `npm i -g @opencode/cli@2.1.3` (v2 lives under `@opencode/*`; `opencode-ai` and GitHub releases are the 1.x line). Platform builds for linux arm64/x64 (glibc + musl). **+448 MB** to the image. |
| 2 | Serve + password | ✅ `opencode serve --hostname 0.0.0.0 --port 4096` with `OPENCODE_SERVER_PASSWORD`; `/api/*` → 401 without, 200 with basic auth `opencode:<pw>`. |
| 2b | Health route | `/global/health` is the web UI shell (always 200) — **not** a health check. Use authenticated `GET /api/info` → `{"version":"2.1.3",…}` for the compose healthcheck and the factory's startup version log. |
| 3 | Login headless | ⚠️ Partially verified. `opencode auth login [target] --server <url> --method <id> --answer key=value` is scriptable; completing a **subscription** login needs the owner and was not done. Fresh container has *no* integrations but already lists 9 free `opencode/*` models. Credentials + sessions live in `/root/.local/share/opencode/opencode.db` and `/root/.config/opencode/` — both must be on the `opencode-state` volume (there is no `auth.json`). |
| 4a | `location.directory` | ✅ Honoured: `pwd` = workspace, relative paths resolve there. |
| 4b | Default permissions | ❌ **Nothing is sandboxed by default**: with no rules the agent read another work order's workspace without asking. Rules are mandatory. |
| 4c | `external_directory` deny | ✅ Blocks file tools outside the workspace (`Permission denied: external_directory`). ⚠️ Does **not** stop `shell` (`cat /data/workspaces/other/...` succeeded). |
| 5 | Read-only roles | ❌ Denying only `edit`/`write`/`patch`/`apply_patch` is useless — the model wrote files via `shell`. ✅ Also denying `shell` and `execute` makes the role truly read-only. No rule fell back to `ask`; denied tools fail or are hidden, so no session hung. |
| — | Model selection | ❌ **Existing bug**: the harness sends `model` in the *prompt* body; 2.1.3 accepts it (200) but ignores it — the session keeps the server default. The model must be set in the **session create** body (`model: {providerID, id}`), which was verified to work. Whether past factory runs used the intended model is unverified (desktop server was not running during the spike). |

Permission action names observed: `read`, `shell`, `write`, `edit`, `patch`,
`apply_patch`, `list`, `glob`, `grep`, `websearch`, `webfetch`, `task`, `todowrite`,
`execute`, `external_directory`, `doom_loop`. A ruleset of `allow *` followed by
specific `deny` rules behaved as expected (later, more specific rules won).

### Design changes from the spike

- Session create sends `model`, `location` and `permissions`; the prompt sends only `text`.
- Role rulesets:
  - **builder**: allow `*`, deny `external_directory`. Shell can still reach other
    workspaces — acceptable for single-user scope (all workspaces belong to the owner);
    the container remains the hard boundary to the host.
  - **architect, foreman, inspector**: allow `*`, deny `external_directory`, `edit`,
    `write`, `patch`, `apply_patch`, `shell`, `execute`. Checks are run by the factory's
    sandbox, not by these agents, so they do not need a shell.
- Healthcheck = authenticated `GET /api/info`.
- `opencode-state` volume mounts both `/root/.local/share/opencode` and
  `/root/.config/opencode`.
- Still to verify with the owner present: subscription login inside the container,
  login survival across `docker compose down/up`, token refresh.

## 12. ACP spike (2026-10-25): Go client ↔ `opencode acp`

A ~250-line throwaway Go client (stdio, newline-delimited JSON-RPC 2.0, no SDK) drove
`opencode acp` (2.1.3) inside the factory image, using a free `opencode/*` model.

| Check | Result |
|---|---|
| Handshake | ✅ `initialize` → protocolVersion 1, `agentInfo` OpenCode 2.1.3, `loadSession`, session `resume`/`close`/`list`/`fork`, `authMethods: [opencode-login]` ("Run `opencode auth login` in the terminal"). |
| Session dir | ✅ `session/new {cwd}` — tools run in the work order workspace. |
| Model | ✅ `configOptions` has `id=model, category=model` (list of provider/model values) and `id=mode` (`build`, `plan` = read-only agent). `session/set_config_option` switches the model; agent confirms with `config_option_update`. |
| Live steps | ✅ Pushed, no polling: `tool_call` / `tool_call_update` with `kind` (read/edit/execute), `rawInput` (path/command/content), status pending→in_progress→completed/failed; `usage_update`; `agent_message_chunk` for the reply. |
| End of turn | ✅ `session/prompt` result: `stopReason` (`end_turn`, `cancelled`) **plus token usage** (input/output/thought/cached). |
| Default permissions | ❌ Same as HTTP: nothing asked, other workspaces readable. |
| `"ask"` via config | ✅ Every edit/shell/outside-workspace read arrives as `session/request_permission` with kind, `locations` and a diff; factory auto-answers. ⚠️ A `reject_once` **cancels the whole turn** (`stopReason: cancelled`) — usable only as a safety net. |
| `"deny"` via config | ✅ Best: denied tools are hidden, outside reads fail with `external_directory`, turn completes with `end_turn` and a full answer. Config is per process (`OPENCODE_CONFIG_CONTENT`), and ACP runs one process per agent run, so each role gets its own rules. |
| Cleanup | ✅ Closing stdin + kill leaves no `opencode acp` process behind. |

### What this means for the design

- **ACP replaces the OpenCode HTTP sidecar.** Agents run as subprocesses of the factory
  inside the factory container (`opencode acp` per run); no extra service, no server
  password, no port, no HTTP polling, no newest-first message ordering.
- **One Go `ACPHarness`** (stdio JSON-RPC client) implements the existing `Harness`
  interface; OnStep comes from `tool_call_update`, usage from the prompt result.
  Claude (`claude-agent-acp`) or Codex (`codex-acp`) later = install + config, same
  harness — each still needs its own permission/config check.
- **Per-role rules** go in `OPENCODE_CONFIG_CONTENT` at spawn (deny for read-only roles,
  `external_directory: deny` for all); ACP permission requests are answered by a
  path-aware policy as a second line of defence.
- **Model** is set with `session/set_config_option` (category `model`), not in the prompt.
- **Auth**: agent-managed (`opencode auth login` inside the container); credentials
  still live in the OpenCode state dirs, so the `opencode-state` volume stays.
- Still unverified: subscription login in the container; long multi-step builder runs;
  concurrency (several `opencode acp` processes sharing one state DB); `session/cancel`.
