# Project check profile — spec (draft v1)

Status: draft · 2026-11-17 · spike for the service stack passed; Playwright smoke not spiked

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
- **Lifetime:** the stack is left running between runs (start costs ~6 s). It is torn down with `down -v` when the project is deleted, or from a "Stop check services" action on the project.
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

Optional, off until it is configured in the profile's `smoke` block. Runs after the checks, in the same stack lock.

```json
"smoke": {
  "start": [
    { "name": "api", "dir": "api", "command": "go run ./cmd/server", "port_env": "PORT", "ready": "/healthz" },
    { "name": "web", "dir": "web", "command": "npm run dev", "port_env": "PORT", "ready": "/" }
  ],
  "base": "web",
  "login": { "path": "/login", "user_env": "SMOKE_USER", "password_env": "SMOKE_PASSWORD" },
  "routes_from": "web/app/**/page.tsx"
}
```

- **Run:** the factory starts the listed processes on free ports, waits for `ready`, then opens the pages whose `page.tsx` (or its imports, up to 1 level) is in the diff. Each page passes when it has no HTTP 5xx, no uncaught console error and no Next error overlay. The browser is the factory's own Playwright install, so nothing goes into the project.
- **Result:** recorded as an optional check named `smoke`, with a screenshot attached. It only becomes blocking after the human flips it in the profile.
- **Not spiked yet:**
  - **Login.** The seed user and its credentials come from the repo's dev seed, which is test data, but the Suggest checks run has to find it.
  - **Dynamic routes** (`[id]`) need a real id from the seed.
  - **Start time** of `next dev` inside the lock.

## Phases (one PR each, sequential)

1. **Profile v2 + stack + runner.** Model, `ParseProfile`, `stack.go` with the docker port, env plumbing in `RunCheck`, per-run database, Redis/bucket leases, optional semantics, Builder env lease, checks.json removal, subfolder `detectProfile`, messages. Tests with a fake docker port, plus one integration test behind a `docker` build tag.
2. **Suggest checks.** Playbook, agent run, validation and dry run, API (`POST /projects/{id}/checks/suggest`, `PUT /projects/{id}/checks`), Project profile approval UI, WO suggestion banner, stale-source notice.
3. **Playwright smoke.** Spike login and dynamic routes first, then the `smoke` block, runner and UI toggle.

**Done for phases 1–2** means: on bimbel-tryout, Suggest checks proposes the CI-derived checks above, and the dry run shows `api test` failing on main. After the human approves (with the emoji test fixed or marked optional), a fresh work order runs the checks with DB, Redis and S3, and readiness reaches `READY` without a human verification step.

## Open questions

1. **Stack idle teardown.** Is keeping it running forever acceptable, or should it go down after N hours idle?
2. **Repos without compose,** where services come from CI `services:` only. Generate a compose file into `<DataDir>/stacks/`, or leave those checks optional?
