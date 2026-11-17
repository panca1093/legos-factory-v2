# Project check profile — spec (draft v1)

Status: draft · 2026-11-17 · service stack and Playwright smoke spiked

## Why

Work order `wo_5e5bdb93-4eb` (bimbel-tryout) ran **0 deterministic checks** in every revision:

- `detectProfile` only looks at the repo root (`go.mod`, `package.json`, `Cargo.toml`). bimbel-tryout is a monorepo (`api/`, `web/`), so nothing matched.
- The repo has no `.legos/checks.json`, and the user does not want factory files in project repos.
- Without checks, converge could only mark criteria "unverified". Readiness ended in `NEEDS_VERIFICATION` → `NEEDS_HUMAN_REVIEW` every time.
- The PR it opened (taruna-hub#51) still fails `TestEmojiIsCatalogOnlyAndShapesAreBuiltIn` at `item_package_test.go:473`. A check run would have caught it.

The repo already knows how it is verified: `.github/workflows/ci.yml` starts Postgres and Redis, runs `make migrate-up`, `go test ./... -race`, then `tsc`, lint and build in `web/`. The factory should reuse that, without writing anything into the repo.

## Decisions (user, 2026-11-17)

1. Checks live in the factory, per project (`projects.quality_profile`). Nothing is written to the repo.
2. `.legos/checks.json` support is removed entirely.
3. Deriving checks is a **suggestion button** ("Suggest checks"). It never runs on its own.
4. Services run in **one factory-owned stack per project**, check runs are **serialized per project**, and each run gets **its own database**.
5. UI smoke testing uses **the factory's own Playwright**. Nothing is added to the repo.

## Non-goals

- Running the repo's CI provider (GitHub Actions) locally.
- A stack per work order.
- Writing e2e tests for the project.

## Profile v2

Stored as JSON in `projects.quality_profile`. Profile v1 (`language`, `setup` string, `checks[{name, command}]`) still parses, so existing `wo.CheckProfile` snapshots keep working.

```json
{
  "version": 2,
  "language": "go+typescript",
  "sources": [
    { "path": ".github/workflows/ci.yml", "sha256": "…" },
    { "path": "api/Makefile", "sha256": "…" },
    { "path": "docker-compose.yml", "sha256": "…" }
  ],
  "services": {
    "compose": "docker-compose.yml",
    "names": ["postgres", "redis", "rustfs"],
    "ports": { "postgres": 5432, "redis": 6379, "rustfs": 9000 }
  },
  "database": {
    "service": "postgres",
    "admin_url": "postgres://tryout:tryout@{{postgres}}/tryout?sslmode=disable"
  },
  "env": {
    "DATABASE_URL": "postgres://tryout:tryout@{{postgres}}/{{db}}?sslmode=disable",
    "REDIS_URL": "redis://{{redis}}/{{redis_db}}",
    "S3_ENDPOINT": "{{rustfs}}",
    "S3_ACCESS_KEY": "tryout",
    "S3_SECRET_KEY": "tryout123",
    "S3_BUCKET": "{{db}}",
    "S3_USE_SSL": "false"
  },
  "setup": [
    { "name": "web deps", "dir": "web", "command": "npm ci --no-audit --no-fund" },
    { "name": "migrate", "dir": "api", "command": "make migrate-up", "per_run": true }
  ],
  "checks": [
    { "name": "api vet", "dir": "api", "command": "go vet ./...", "source": "ci.yml:63" },
    { "name": "api test", "dir": "api", "command": "go test ./... -race", "source": "ci.yml:68" },
    { "name": "web types", "dir": "web", "command": "npx tsc --noEmit", "source": "ci.yml:93" },
    { "name": "web lint", "dir": "web", "command": "npm run lint", "source": "ci.yml:94" },
    { "name": "api schema in sync", "dir": "web", "command": "npm run gen:api && git diff --exit-code lib/api/schema.ts" },
    { "name": "smtp", "dir": "api", "command": "go test ./internal/infra -run SMTP", "optional": true }
  ],
  "smoke": null
}
```

Rules:

- **Placeholders** are filled by the factory at run time and nowhere else. `{{<service>}}` becomes `127.0.0.1:<host port>`, `{{db}}` becomes the run's own database name (`run_<wo>_<rev>` for verify, `run_<wo>_<task>` for a Builder task, sanitised), and `{{redis_db}}` becomes a Redis index leased to the run (1–15; index 0 is never used).
- **`dir`** is relative to the workspace root, must stay inside it, and defaults to the root.
- **`optional: true`**: the result is recorded and shown, but never fails verification and never counts as evidence for readiness.
- **`per_run` setup** runs after the run database exists. Other setup runs once per workspace, as `runSetup` does today.
- **`env` values** come only from the compose file, the CI workflow or test config in the repo. A value from `.env`, or one that looks like a live secret, is rejected at validation.
- **`sources`** are hashed when the profile is approved. A changed hash shows "Checks may be stale · Suggest again" on the project. It never re-derives the profile on its own.

## Service stack

New `usecase/stack.go` (one file; it touches docker only through a small port, so tests use a fake).

- **Compose project:** `legos-<project-id>`, built from the repo's own compose file (read from the WO workspace), plus a factory-generated override file at `<DataDir>/stacks/<project-id>/override.yml`:
  ```yaml
  services:
    postgres:
      ports: !override ["127.0.0.1::5432"]
  ```
- **Startup:** `docker compose -p … -f <repo compose> -f <override> up -d --wait <names>`. Host ports are discovered with `docker compose port <svc> <port>`.
- **Lifetime (user decision 2026-11-17):** the stack stays up between runs (start costs ~6 s) and goes down after **2 hours with no lease**. A ticker in the factory checks every 5 minutes and runs `down -v`; the next run brings it back up. It also goes down when the project is deleted, or from a "Stop check services" action on the project.
- **Factory restart:** on startup the factory lists compose projects named `legos-*` (`docker compose ls`). Each one starts its idle clock at startup time, so a stack orphaned by a crash still goes down.
- **Isolation instead of a run-wide lock:** every run, whether verify, baseline or a Builder task, gets its own database, Redis index and S3 bucket (`{{db}}`). Runs can therefore overlap, including parallel Builder tasks. An in-process mutex per project only guards stack up/down and the lease table. When all 15 Redis indexes are leased, the next run waits for one.
- **Per-run database:** created by running `CREATE DATABASE` through `database.admin_url`, and dropped after the run (also on failure, via `defer`). Without a `database` block, runs share the services as they are.
- **Preflight:** a missing docker, or compose without `!override` support (added in the v2.24 line; exact minimum to confirm), fails the run as `UNVERIFIED` with a clear message. It never silently passes.

Spike (2026-11-17, compose v5.5, bimbel-tryout):

- Stack up beside the user's dev stack (5433/6380/9002): no port clash, 6 s to healthy.
- `CREATE DATABASE` plus `make migrate-up`: 1.5 s.
- Full `api` `go test ./...` with the env above: 39 s. DB, Redis and S3 tests ran instead of skipping.
- `down -v` removed everything. `git status` stayed clean in both the repo and the workspace.

## Check runner changes

- `model.Check` gains `Dir`, `Optional` and `Source`. `model.Profile` gains the v2 fields above. `ParseProfile` reads v1 and v2.
- `sandbox.RunCheck` takes an extra env (`[]string`). The factory resolves the placeholders before calling it.
- `inspector.go` verify: optional failures do not send work back. Readiness counts only non-optional `PASS` results as evidence.
- `verification_skipped` message: "No checks for this project yet · Suggest checks", in place of the `.legos/checks.json` hint.
- `repoProfile`: the override-file branch is deleted. `detectProfile` stays as the fallback, extended to one level of subfolders (`*/go.mod`, `*/package.json`) with `dir` set. The work order shows a suggestion banner when it falls back.
- Precedence is unchanged: project profile, then `wo.CheckProfile` snapshot, then `repoProfile`.

## Builder access (user decision 2026-11-17)

The Builder agent gets the same resolved env while it works on a task, so it can run DB-, Redis- and S3-backed tests itself instead of waiting for verify.

- **Lease:** before a Builder task starts, the factory brings the stack up, leases a database (`run_<wo>_<task>`), runs the `per_run` setup (migrations) in the task directory, and leases a Redis index. Everything is released when the run ends, including on failure or cancel.
- **Passing the env:** the env is set on the agent process for that task. Not yet verified: whether the claude-acp harness accepts a per-run env, or only one env per process; check this first in phase 1. The task prompt gets one line listing the variable names (not values) and saying the services are disposable for this task.
- **Evidence:** Builder test runs stay notes, not evidence. Readiness still counts only the factory's own verify checks. This keeps PR #75's rule: a Builder note is never a check.
- **Cost:** stack start is paid once per project. Each task adds about 1.5 s for create db + migrate on bimbel-tryout.
- **Not covered:** the Builder cannot change the stack (compose, ports). A task that needs a new service ends in a converge gap for the human, as today.

## Suggest checks

**Trigger:** a "Suggest checks" button on the Project profile, and the same button on a work order whose verification was skipped or used the fallback.

**Run:** one agent run, not a work order.

- **Model:** the Builder-tier model (default Sonnet), not Opus. This is reading and listing, and the Architect/Foreman token budget stays protected.
- **Repo access:** a read-only checkout of the project's default branch, as the chat feature already does.
- **Playbook:** the new playbook `kb/defaults/playbooks/foreman/checks.md` (specialty `checks`):
  - Read CI first: it is what the repo already treats as passing. Then the Makefile/justfile, `package.json` scripts, and compose for services.
  - Copy commands, don't invent them. Give every check a `source` (`file:line`).
  - Mark checks that need host-specific setup (extra loopback alias, GPU, external network) `optional`.
  - Env values only from compose, CI or test config. Never from `.env`.
  - Prefer the narrowest working directory per check.
- **Reply:** a profile v2 JSON object, parsed with `runStructured`'s one-retry rule.
- **Cost:** recorded in `usage_records` with `wo_id` NULL (the column is already nullable).

**Validation (deterministic, before the human sees it):**

1. Schema checks: `dir` inside the repo, every placeholder known, every service present in the compose file, env rules above.
2. A **dry run** on a clean checkout of the default branch through the stack. Every check gets `PASS`, `FAIL` or `UNVERIFIED`, with its output tail.

**Approval UI** (Project profile, replacing "Custom quality-profile editing is unavailable"):

- Each proposed check shows its command, dir, source and dry-run result. A check that fails on the default branch is labelled **"fails on main"**, with a one-click "make optional".
- The services and env are listed, so the human sees every value the checks will get.
- The human can edit, then **Approve** (saved to `projects.quality_profile` with the source hashes) or **Discard**.

bimbel-tryout today would show `api test` as "fails on main" (the emoji assertion), which is exactly the signal that was missing.

## Playwright smoke (factory-owned)

Optional, off until it is configured in the profile's `smoke` block. Runs in verify only, after the checks, under its own run lease (own database, Redis index, bucket). It is skipped when a required check already failed, since the Builder is sent back anyway.

```json
"smoke": {
  "start": [
    { "name": "api", "dir": "api", "command": "go run ./cmd/server", "port_env": "PORT", "ready": "/healthz",
      "env": { "CORS_ORIGINS": "http://localhost:{{port.web}}", "APP_URL": "http://localhost:{{port.web}}" } },
    { "name": "web", "dir": "web", "command": "npm run dev", "port_env": "PORT", "ready": "/",
      "env": { "NEXT_PUBLIC_API_URL": "http://localhost:{{port.api}}" } }
  ],
  "base": "web",
  "login": { "path": "/login", "user_env": "SMOKE_USER", "password_env": "SMOKE_PASSWORD" },
  "routes_from": "web/app/**/page.tsx",
  "params": { "id": "sql:SELECT id FROM tryouts WHERE title = 'Tryout SD Matematika'" },
  "ready_timeout_sec": 90,
  "blocking": false
}
```

- **Run** (`usecase/smoke.go`, runner `usecase/smoke_runner.cjs`, embedded):
  1. Routes come from the diff (`ChangedFiles`). A page counts when its `page.tsx` changed, a `layout.*` above it changed, or it imports a changed file (one level, `@/` resolves to the parent of the `routes_from` base, relative imports too). Route groups `(x)` and slots `@x` drop out of the URL. More than 20 pages are capped with a note. No affected page: the check is recorded as PASS with "no affected pages" and nothing is started.
  2. `[id]` segments are filled from `params`: a literal, or `sql:<query>` run against the run's own database (first column of the first row; needs the profile's `database` block). A page whose param has no value is skipped with a note; if every page is skipped the result is UNVERIFIED.
  3. Every `start` process gets a free port up front (so `{{port.<name>}}` works in any process's `env`), then starts in order in its own process group, with the resolved profile env plus its own `env` plus `port_env`. Each must answer HTTP 200 on `127.0.0.1:<port><ready>` within `ready_timeout_sec` (default 90) or the check FAILs with the process's output tail. All process groups are killed on every exit path.
  4. The browser opens `http://localhost:<base port>` (**localhost, not 127.0.0.1**: Next 16 dev refuses to hydrate from 127.0.0.1 unless `allowedDevOrigins` is set in the repo, and without hydration the login form submits as a plain GET). Cookie host and CORS origin must therefore also use `localhost`.
  5. With `login`, the runner signs in once: first visible non-password input gets the user, the password input gets the password, `button[type=submit]` is clicked (up to 3 tries, since a form submitted before hydration falls back to a native GET), and it waits for the URL to leave the login path. The values come from the profile env vars named by `user_env` / `password_env`, which must hold seed test credentials.
  6. Each page passes when it has no HTTP status >= 500 on any response, no uncaught page error, no `console.error` (browser "Failed to load resource" lines are ignored; a 5xx is caught by the response rule), no Next error overlay (`nextjs-portal` shadow root with a dialog) and no redirect back to the login page. One screenshot per page lands in `<DataDir>/smoke/<wo>/<revision>/`.
- **Browser:** the factory's own Playwright. `playwright-core` is resolved from `Factory.PlaywrightDir`, else `LEGOS_PLAYWRIGHT_DIR`, else a `frontend/` with `node_modules/playwright-core` found above the working directory or the binary. Chrome is `LEGOS_SMOKE_CHROME`, else the system Chrome (`channel: chrome`), else Playwright's bundled Chromium (needs `npx playwright install chromium` once, in the factory, never in a project). Nothing is installed into the project; the factory writes `runner.cjs` and a short-lived `spec.json` (removed after the run, it holds the login) under its data dir.
- **Result:** recorded as the check `smoke` with one line per page (`PASS /student (200) [screenshot …]`, `FAIL /x (200): …`). It is optional unless `blocking` is true. Outcomes: any page failing, a process not becoming ready, or the login failing is FAIL; no browser, no Playwright install, a missing credential env var, a missing param or an unavailable stack is UNVERIFIED, never PASS.
- **Per-run setup helper:** per-run setup steps also see `LEGOS_COMPOSE_PROJECT` and `LEGOS_DB`, so a repo without a host `psql` can seed with `docker compose -p $LEGOS_COMPOSE_PROJECT exec -T postgres psql postgres://u:p@127.0.0.1:5432/$LEGOS_DB -v ON_ERROR_STOP=1 -q -f - < api/seed/dev.sql`. A `{{bucket}}` placeholder is `{{db}}` with `_` replaced by `-`, because S3 bucket names reject underscores.

Spike (2026-11-17, bimbel-tryout, Chrome via Playwright 1.5x): login, dynamic route and overlay detection verified; findings are in the phase 3 report. `next dev` needed about 17 s for its first page compile, so the default ready timeout of 90 s is enough but should not be lowered.

## Phases (one PR each, sequential)

1. **Profile v2 + stack + runner.** Model, `ParseProfile`, `stack.go` with the docker port, env plumbing in `RunCheck`, per-run database, Redis/bucket leases, optional semantics, Builder env lease, checks.json removal, subfolder `detectProfile`, messages. Tests with a fake docker port, plus one integration test behind a `docker` build tag.
2. **Suggest checks.** Playbook, agent run, validation and dry run, API (`POST /projects/{id}/checks/suggest`, `PUT /projects/{id}/checks`), Project profile approval UI, WO suggestion banner, stale-source notice.
3. **Playwright smoke.** Spiked login and dynamic routes first, then the `smoke` block and runner (built). UI toggle goes with the Suggest checks UI.

**Done for phases 1–2** means: on bimbel-tryout, Suggest checks proposes the CI-derived checks above, and the dry run shows `api test` failing on main. After the human approves (with the emoji test fixed or marked optional), a fresh work order runs the checks with DB, Redis and S3, and readiness reaches `READY` without a human verification step.

## Settled questions

- **Repos without compose (user decision 2026-11-17).** When the repo has no compose file, `services` stays empty, and Suggest checks marks every check that needs a service `optional`, with the reason "needs <service>; no compose file". The factory never generates a compose file.
