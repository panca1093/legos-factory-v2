# legos-factory — autonomous software engineering factory

A local-first, multi-project factory that turns engineering objectives into reviewed plans, bounded agent execution, verified implementations, and an explicit human PR decision.

- **Backend**: Go + Echo, SQLite (WAL) with goose migrations, SSE, persistent state machine, SQLite-backed job scheduler.
- **Frontend**: Vue 3 (Composition API) + TypeScript strict + Pinia + Vue Router + Tailwind + Lucide icons.
- **Agent harness**: OpenCode v2 HTTP adapter (verified live) or a deterministic scripted mock harness for offline demos and tests.

> The original interactive prototype (`index.html` + `PRD.md`, `ADR.md`, `UX-SPEC.md`, `VERIFICATION.md`) remains in this repository as the visual and interaction design baseline. The Vue frontend preserves its design tokens, personas and layout.

## Quick start (development)

Requires Go ≥1.26 and Node ≥20.

```sh
# terminal 1 — backend (auth mode dev signs in a local owner automatically)
cd backend
go run ./cmd/factory
#   listens on 127.0.0.1:8080, SQLite at ./data/factory.db

# terminal 2 — frontend (Vite dev server proxies /api and /auth to :8080)
cd frontend
npm install
npm run dev
#   open http://localhost:5173
```

Environment (all optional):

| Variable | Default | Purpose |
|---|---|---|
| `FACTORY_ADDR` | `127.0.0.1:8080` | HTTP listen address |
| `FACTORY_DATA_DIR` | `./data` | SQLite db + workspaces |
| `FACTORY_AUTH_MODE` | `dev` | `dev` or `oidc` |
| `FACTORY_OIDC_ISSUER/_CLIENT_ID/_CLIENT_SECRET` | — | OIDC login (mode `oidc`) |
| `FACTORY_OPENCODE_URL` | — | base URL of `opencode serve`; enables the real harness |
| `FACTORY_OPENCODE_PASSWORD` | — | basic-auth password printed by `opencode serve` |
| `FACTORY_MAX_CONCURRENT_RUNS` | `2` | worker pool size |
| `FACTORY_SANDBOX_MODE` | `local` | `local` (trusted) or `docker` (**not implemented** — see limitations) |
| `FACTORY_SECRET_KEY` | — | base64 32-byte key that encrypts provider credentials. Unset in `dev` mode: generated into `<data>/secret.key`; unset otherwise: connections are disabled |

### Provider connections (GitHub, GitLab, Bitbucket, Jira)

Settings → Integrations stores tokens encrypted at rest; they are never returned by the API. A connection is *unverified* until **Test** succeeds against the provider. Shared connections (admins) can be bound to a project together with its `owner/repo` slug; the project's repository URL must then be the clone URL. When a human approves a work order in a bound project, its verified commit is pushed to `legos-factory/<work-order>` and a pull request is found-or-created — nothing is merged. Jira connections are read-only and prefill new work orders. The clients are covered by contract tests against fake provider APIs; they have not been exercised against real accounts in this repository.

### Using the real OpenCode harness

```sh
opencode serve --hostname 127.0.0.1 --port 14099   # note the printed password
FACTORY_OPENCODE_URL=http://127.0.0.1:8080-independent-server   # see below
FACTORY_OPENCODE_URL=http://127.0.0.1:14099 \
FACTORY_OPENCODE_PASSWORD=<printed password> \
go run ./cmd/factory
```

With OpenCode configured, model discovery (`GET /api/v1/models`) lists the real model catalog and agent runs execute on real models with measured token/cost accounting. Without it, the scripted mock harness runs; the UI labels every run with its harness (`mock` vs `opencode`).

## Quick start (production single binary)

```sh
./scripts/build.sh        # builds Vue → embeds into Go → ./factory-server
FACTORY_ADDR=127.0.0.1:8090 ./factory-server
# http://127.0.0.1:8090 (SPA served with fallback routes)
```

## Tests

```sh
cd backend
go test ./...                    # all packages
go test ./internal/scheduler/ ./tests/integration/ -race
# real OpenCode live check (optional):
LF_LIVE_OPENCODE_URL=http://127.0.0.1:14099 LF_LIVE_OPENCODE_PASSWORD=… \
  go test ./internal/agent/ -run TestLiveOpenCode -v

cd ../frontend
npm run typecheck                # vue-tsc strict
npm test                         # vitest (8 tests)
npm run e2e                      # Playwright journey (needs backend on :8080)
```

## The verified work order journey

```text
Create Project → Create Work Order → Foreman planning (Architect draft)
→ Architect review → (clarification ⇄ human) → human plan approval
→ baseline verification (failing evidence at revision 0) → Builder tasks (DAG)
→ deterministic checks (go test / go vet, really executed)
→ Inspector review (blocker finding on rev 0) → bounded fix (rev 1)
→ re-verify → clean re-inspection → merge readiness → NEEDS_HUMAN_REVIEW
→ human PR decision (approve → READY / request changes / decline)
```

Every state transition is validated by a persistent state machine and streamed live to the UI over SSE. Nothing is ever published, pushed or merged — the PR decision is a local disposition.

## Known limitations (honest boundaries)

- **Sandbox**: `local` mode executes builder changes and quality commands in an isolated copy of the repo under `data/workspaces/`, but quality commands run as host shell processes — repository scripts run with your user's permissions. The `docker` sandbox mode is **not implemented**; do not enable it expecting isolation.
- **Auth**: `dev` mode is for local use only. OIDC is implemented (discovery + code exchange) but only tested against a fake IdP; the login state parameter is not validated yet. SameSite=Lax cookies mitigate basic CSRF; no CSRF tokens; no `Secure` cookie flag (no TLS locally).
- **Authorization**: single workspace, role checks (OWNER/ADMIN/ENGINEER/VIEWER) at the API boundary; per-project membership matrix is future work.
- **Budget**: reservations are in-memory (single process) and estimated; measured costs come from OpenCode where available, mock runs record clearly-labeled estimates.
- **Execution recovery**: planning auto-resumes after restart; execution-phase interruptions are marked FAILED and require a human to re-drive (intentional, per reconciliation policy).

See `docs/IMPLEMENTATION-REPORT.md` for the full report.
