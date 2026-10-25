# OpenCode inside the factory deployment — design

Status: draft for review (2026-10-25). Nothing implemented yet.

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

Non-goals: fixing provider quota/privacy limits (account-level), multi-host scaling.

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
2. **Provider credentials** (OpenCode → model providers): recommended path is
   `opencode auth export > secrets/opencode-auth.json` on the host once, mounted
   read-only into the sidecar and imported at startup (`opencode auth import`). Works
   for any provider the user has logged into. `secrets/` is added to `.gitignore`.
   Alternative for OpenCode-only providers: `OPENCODE_API_KEY` in `.env` (simpler, but
   covers fewer providers — to confirm in spike).

OpenCode state (session DB, logs) persists in a named volume `opencode-state`.

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
      - opencode-state:/root/.local/share/opencode
      - ./secrets/opencode-auth.json:/run/secrets/opencode-auth.json:ro
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
  unreachable or has no provider credentials.

## 7. Risks

- **Credentials in a container**: auth file mounted read-only, never baked into the
  image, never logged.
- **Image size**: OpenCode binary is ~200 MB.
- **Version drift**: pinned; live harness test gates upgrades.
- **Quota**: unchanged — the same account limits apply wherever OpenCode runs.

## 8. Spike before coding (≈1 hour, throwaway)

1. Install OpenCode 2.1.3 in the runtime image for linux/arm64 and linux/amd64.
2. `opencode serve` with `OPENCODE_SERVER_PASSWORD`; confirm basic auth and a health route.
3. `auth import` from an exported file; `GET /api/model` lists the expected models.
4. Create a session with `location.directory=/data/workspaces/x`; confirm `read`/`bash`
   run there and a path outside is denied.
5. List permission action names; confirm none fall back to `ask` with our ruleset.

## 9. Verification after implementation

- Unit: harness request body; existing harness/usecase/frontend suites.
- Compose smoke: `docker compose up`, create a work order on a real repo with a linked
  connection, confirm the builder's edits appear in `/data/workspaces/<wo>` and nowhere
  on the host, checks run, and a PR branch is pushed.
