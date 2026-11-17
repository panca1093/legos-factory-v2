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

## Rules

- Copy commands as written. Never invent a command or a flag. A CI step that runs `make test` becomes `make test`.
- Every check has a `source`: `file:line` of the line you copied it from.
- Use the narrowest `dir` that works: the package the command runs in (`api`, `web`), not the repo root.
- Dependency installs (`npm ci`, `go mod download`) go in `setup`, not in `checks`. Migrations go in `setup` with `per_run: true`.
- A check that needs something this host may not have (a loopback alias, a GPU, an external network, a paid API) is `optional: true`.
- `env` values come only from the compose file, the CI workflow or test config in the repo. Never from `.env`, never a real secret. Use placeholders only where the value depends on the run: `{{<service>}}` for a service address, `{{db}}` for the run's database, `{{redis_db}}` for its Redis index.
- `services.names` must exist in the compose file; `ports` maps each to its container port. `database.admin_url` connects to the existing database on that service.
- List every file you read to build the profile in `sources` (`path` only; hashes are added for you).
- Do not run commands, install anything or edit files. Read and search only.

## No compose file

Leave `services` empty and omit `database`. Every check that needs a service (database, cache, queue, object store) is `optional: true`, with the reason in `notes` under the check name: `"needs postgres; no compose file"`. Never write a compose file.

## Reply

One JSON object and nothing else: a profile with `"version": 2`, `language`, `sources`, `services`, `database`, `env`, `setup`, `checks`, and an optional `notes` object mapping a check name to one line of reasoning (why it is optional, what it needs).
