# legos-factory — implementation report

Date: 24 October 2026 · all times Asia/Jakarta

## Architecture implemented

Pragmatic modular monolith, exactly within the mandated stack:

```text
backend/  (Go module legos-factory/backend)
  cmd/factory            entrypoint: config → app wiring → graceful shutdown
  internal/config        env configuration, validated (auth mode, sandbox mode)
  internal/store/sqlite  modernc pure-Go SQLite (WAL, FK, busy_timeout, 1-writer pool) + goose migrations (001 init, 002 job-unique)
  internal/workorder     WorkOrder + Task models, persistent transition table, DAG validation (cycle/unknown/self/empty-id), repository, events
  internal/project       project repository (repo URL, language, quality profile)
  internal/agents        Harness interface (Start/Cancel/Result), structured-JSON extraction
    agents/mock          deterministic scripted harness: architect plan/review, real file-writing builder, scripted inspector regression
    agents/opencode      REAL OpenCode v2 adapter: model discovery, session create, async prompt, message polling, measured tokens/cost, interrupt
  internal/orchestration Foreman: plan/review/implement/verify/inspect/evaluate handlers, bounded remediation loop (MaxRevisionCycles=2), budget reservation, resume-on-start, human decisions
  internal/scheduler     SQLite-backed queue: atomic claim in tx, worker pool bound, retry w/ backoff, ErrPermanent fast-fail, orphan reconcile, CancelWO
  internal/quality       language presets (go/ts/rust/node), real check execution (shell, timeout, broken-GOROOT tolerance), PASS/FAIL/UNVERIFIED
  internal/identity      dev login + OIDC (discovery, code exchange), sessions table, roles OWNER/ADMIN/ENGINEER/VIEWER
  internal/usage         usage records + in-memory budget reservation (mutex-guarded)
  internal/execenv       per-WO isolated workspace (git clone / plain copy)
  internal/eventbus      in-process fan-out for SSE
  internal/httpx         Echo server: request-id, recover, slog logging, body limit, CORS, session auth, RBAC, error envelope {error:{code,message,request_id}}, SSE streams, static embed w/ SPA fallback
frontend/ (Vue 3 + TS strict)
  src/app                AppShell (rail navigation, skip link, responsive), router (URL routes/deep links)
  src/features           overview / factory (board+list, drawers) / workorders (5 tabs) / knowledge / settings
  src/components/ui      BrickBot personas (original SVG: amber hardhat, violet beret, blue cap, mint glasses, rose book), StatusBadge
  src/services           typed fetch client with ApiError, SSE subscriber with named-event coverage
  src/stores             session (auth/RBAC getters), factory (typed state + actions)
  tests/unit             vitest: stores, sse, components (8 tests)
  tests/e2e              Playwright: full journey against live backend
```

## Completed features (all executed, not scaffolding)

| Feature | Evidence |
|---|---|
| Persistent state machine, invalid transitions rejected | `internal/workorder/model_test.go`; API 409 responses |
| DAG validation (cycles, unknown deps, self-deps) | unit tests |
| Work order lifecycle: plan → clarify → approve → build → verify → inspect → fix → re-verify → readiness → human PR decision | `tests/integration/lifecycle_test.go` (mock harness + REAL go test/govet execution in an isolated clone) |
| Clarification flow with re-review + stale-answer 409 | integration `TestClarificationPath` |
| Revision identity: baseline FAIL evidence at rev 0; fix loop; findings resolved on clean re-inspection | integration test assertions |
| Scheduler: atomic claiming, pool bound, retry/backoff, permanent failures, orphan reconcile | scheduler tests + `-race` |
| Budget enforcement with reservations | live verified: 10kµ$ cap blocks agent runs with fail-fast error; persisted across restart |
| Restart recovery | live verified: kill mid-plan → restart → auto-resume planning → approve → NEEDS_HUMAN_REVIEW |
| AuthN/AuthZ | 401 unauthenticated, 403 viewer write, expired session 401, error envelope with request_id |
| OpenCode adapter | unit tests on fake v2 server + **live verification** (81 models discovered; real execution `OKAY` with measured tokens/cost; real cancellation) |
| Model discovery API | `/api/v1/models` (mock catalog or real OpenCode models) |
| Usage attribution | per-run tokens/cost rows; `/api/v1/usage` + overview budget bars |
| SSE live updates | browser journey: UI progressed QUEUED→…→NEEDS_HUMAN_REVIEW→READY without reloads |
| Vue frontend | vue-tsc strict clean; 8 vitest tests; Playwright E2E journey green (5.7s) |
| Production serving | `scripts/build.sh` → single binary, embedded dist, SPA fallback (root/deep-link/health all 200) |
| Browser-verified design migration | prototype tokens/personas preserved; screenshots taken during the live run |

## Actual OpenCode vs mocked functionality

- **Real (verified live)**: model/provider discovery, session lifecycle, prompt execution with measured tokens + cost, cancellation via interrupt.
- **Mock (labeled `harness: mock` in DB + UI)**: builder file changes (scripted content, real filesystem), inspector findings (scripted first-revision blocker), architect plans.
- **Not verified**: an OpenCode-driven *builder* run editing a workspace (requires a per-workspace `opencode serve` cwd; the shared-server adapter sends workspace context in prompts but the server's cwd is the deciding factor). Documented as the next priority.

## Tests executed and results

- `go vet ./...` clean.
- `go test ./...` — 5 packages, all ok (integration included).
- `go test -race` on scheduler + integration — ok.
- `vue-tsc --noEmit` — clean (strict).
- `vitest run` — 8/8 passed.
- `playwright test` — 1/1 journey passed.
- Live checks: OpenCode round-trip; kill/restart recovery; budget exhaustion; production binary serving.

## Known limitations / unresolved risks (explicit)

**Concurrency**
- The unique active-job index covers task-carrying jobs; `verify/inspect/evaluate/plan` jobs (NULL task_id) rely on in-process `hasActiveJob` checks — a crash between enqueue and completion could in theory allow a duplicate verify job after restart. Impact: duplicate check evidence, no external side effects.
- Two parallel implement completions can race `maybeAdvanceAfterImplementation`; the unique index now dedupes, but the loser logs no warning. Acceptable for MVP scale.

**Authentication/authorization**
- OIDC login is implemented but only exercised against a fake IdP; `state` parameter validation is not implemented. Do not ship oidc mode to production before fixing that and adding `Secure` cookies + CSRF tokens.
- No per-project membership matrix; a workspace ENGINEER can act on all projects. PRD-level "per-project access" is future work.

**Sandbox**
- Quality commands and builder operations run on the host in `local` mode. Repository scripts are untrusted by definition; only directory isolation and timeouts exist. Docker sandbox is not implemented (daemon unavailable in this environment); the config validates the value but there is no docker executor behind it.
- No network egress restrictions, no CPU/memory limits in local mode.

**Recovery**
- Execution-phase (post-approval) work orders are not auto-resumed after restart — they stay in their last state and require human re-drive (e.g. approve again from a compatible state or cancel). This is a deliberate reconciliation-policy trade-off, not a crash bug.
- In-memory budget reservations vanish on restart (spent records persist; reservation leakage is impossible post-restart).

**Scope gaps vs the full PRD**
- Git provider adapters (GitHub/GitLab/Bitbucket), Jira import, PR creation — not implemented; no credentials exist in this build (documented as unverified in Settings UI).
- Cataloger second brain — placeholder only (by design).
- Specialist inspector categories (security/frontend/…) — single Inspector persona in MVP.
- Per-user budgets/policies — workspace-level budget only.

## Startup commands

```sh
# dev
cd backend && go run ./cmd/factory
cd frontend && npm run dev    # http://localhost:5173

# prod
./scripts/build.sh && FACTORY_ADDR=127.0.0.1:8090 ./factory-server
```

## Recommended next priorities

1. Per-workspace `opencode serve` spawn for real builder runs (verify a real OpenCode implementation task end-to-end).
2. Docker sandbox adapter behind `FACTORY_SANDBOX_MODE=docker` with real cgroup/image isolation.
3. OIDC hardening (state validation, Secure cookies, CSRF) before multi-user use.
4. GitHub/GitLab adapter for real PR creation behind explicit authorization.
5. Specialist inspector composition + finding disposition workflows.
