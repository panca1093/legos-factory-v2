---
type: Playbook
title: How the Foreman suggests a project's checks
description: Derive the deterministic checks from what the repo already runs in CI; copy commands, cite file:line, never invent.
status: stable
role: foreman
specialty: checks
generated:
  by: legos-factory/defaults
  at: 2026-11-17T00:00:00Z
---

# How the Foreman suggests a project's checks

The checks are what the repo already treats as passing. Copy them; do not design them.

## Where to read, in order

1. CI first: `.github/workflows/*`, `.gitlab-ci.yml`, `bitbucket-pipelines.yml`, `.circleci/`. Its steps are the checks.
2. `Makefile` / `justfile` targets that CI or the README name.
3. `package.json` scripts, per package in a monorepo.
4. The compose file, for the services the tests need and their container ports.
5. The env the code and tests read (`os.Getenv`, `process.env`, config structs). Every variable a test reads to reach a service (database, cache, object store, mail) means that service belongs in `services` and its env in `env`, even when CI does not start it: tests that skip without it are not checks.

## Rules

- Copy commands as written. Never invent a command or a flag. A CI step that runs `make test` becomes `make test`.
- Every check has a `source`: `file:line` of the line you copied it from.
- Use the narrowest `dir` that works: the package the command runs in (`api`, `web`), not the repo root.
- Dependency installs go in `setup`, not in `checks`. Use the lockfile install (`npm ci`); skip `go mod download`, since Go fetches modules on demand. Migrations go in `setup` with `per_run: true`.
- A check that only proves the module or lock file is tidy (`go mod tidy && git diff --exit-code`, `npm install` then diff) needs the module network and is `optional: true`.
- A check that needs something this host may not have (a loopback alias, a GPU, an external network, a paid API) is `optional: true`. When such a test sits inside a broad command (`go test ./...`), keep the command required and name the test and its need in `notes`.
- `env` values come only from the compose file, the CI workflow or test config in the repo. Never from `.env`, never a real secret. Use placeholders only where the value depends on the run: `{{<service>}}` for a service address, `{{db}}` for the run's database, `{{bucket}}` for the same name as an S3 bucket, `{{redis_db}}` for its Redis index. A secret the app refuses to start without (`JWT_SECRET`, a seal key) may be a short, clearly fake test value such as `test-only-not-a-secret`; never a real or `.env` value.
- `services.names` must exist in the compose file; `ports` maps each to its container port. `database.admin_url` connects to the existing database on that service.
- List every file you read to build the profile in `sources` (`path` only; hashes are added for you).
- Do not run commands, install anything or edit files. Read and search only.

## No compose file

Leave `services` empty and omit `database`. Every check that needs a service (database, cache, queue, object store) is `optional: true`, with the reason in `notes` under the check name: `"needs postgres; no compose file"`. Never write a compose file.

## Smoke (optional)

Look for it: a web app (Next, Vite, …) with a login page, and a dev seed (SQL or fixture) that creates a user. When both exist, add `smoke`; otherwise omit it. Never set `blocking`.

- `start`: one entry per process the app needs (API, web), commands copied from the Makefile, README or `package.json` dev script. Each has `port_env` (the env var the app reads its port from), `ready` (a health path starting with `/`, or `/`), and `env`.
- Use `localhost`, not `127.0.0.1`: Next dev rejects 127.0.0.1 origins and cookies need one host. Give the web process the API address as `http://localhost:{{port.<api process>}}` in the env var the web code reads, and the API its CORS/app URL as `http://localhost:{{port.<web process>}}`. `{{port.<name>}}` works only in `smoke.start[].env`.
- `base`: the process the browser opens. `routes_from`: the glob of page files (`web/app/**/page.tsx`).
- `login`: `path` plus `user_env` and `password_env`; put the seed account's credentials from the repo's dev seed file into top-level `env` under those names. Test data only, never a real account.
- `params`: a literal, or `sql:<query>` against the seed data (needs `database`), for each dynamic `[id]` segment.
- Mandatory app secrets go in `env` as throwaway values, as above.

## Reply

One JSON object and nothing else: a profile with `"version": 2`, `language`, `sources`, `services`, `database`, `env`, `setup`, `checks`, an optional `smoke`, and an optional `notes` object mapping a check name to one line of reasoning (why it is optional, what it needs).
