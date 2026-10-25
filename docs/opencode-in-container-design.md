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
