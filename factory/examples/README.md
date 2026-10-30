# Project config examples

`loan-platform.json` is a project file (SPEC §11, ADR-F08).

- `id`, `repo_url`, `repo`, `default_branch`, `scm_kind`: where the code lives. Add `scm_token_env` (name of the env var holding the SCM token) before use.
- `env.setup`: run once per sandbox per worktree, for example `make deps`. Empty here: loan-platform vendors its modules, and `make deps` runs `go mod tidy`, which would dirty the tree.
- `env.verify`: tier 2, ordered, run in the sandbox after all tasks merge. A failure goes back to the task whose owned paths appear in the output.
- `env.test_pkg`: tier 1, the targeted test command the builder is told to run, `{pkg}` being the touched package path relative to the repo root. Defaults to `go test ./{pkg}/...` when `go.mod` exists.
- `env.integration` (optional, not set here): tier 3, runs after verify passes and before the PR.
  `{"command": "...", "endpoints": {"kafka": "host:9092"}, "secret_env": ["UAT_TOKEN"]}`. Endpoints become `LF_ENDPOINT_<NAME>`; each `secret_env` name is read from the factory's environment and passed to this step only, by name. The environment itself must already exist.
- `env.docker`: mounts the host docker socket into the sandbox (root-equivalent on the host; single-user deployments only).
- `protected_paths`: placeholders, adjust to the real directories. They always need a human (ADR-F09); enforced from M7.
- `required_checks`: CI check names that must pass.
- Top-level `setup` and `verify` still work as fallbacks when `env` omits them.
