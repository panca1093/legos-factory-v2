# Project hub — prerequisites (PARKED)

Status: **parked 2026-11-19**. The design is being reworked. These are code facts found while writing `docs/review-pr/SPEC.md` and `docs/editor/SPEC.md`. Whatever the new design becomes, they must be settled before Review PR or the Editor is built.

✓ = checked in code by hand. ~ = reported by the spec agent, not yet checked by hand.

## Identity and accounts

| # | Fact | Where | Prerequisite |
|---|---|---|---|
| 1 ✓ | A project can only bind a **shared** git connection | `backend/internal/usecase/integration.go` (`Bind`) | Anything that acts "as me" (PR filters, posting reviews) needs a personal identity resolver: a personal connection or CLI login, never the shared one |
| 2 ~ | Borrowing the `gh`/`glab` CLI token is only safe in single-user local/dev mode | `Integrations.GH` gate | Reuse that gate; there is no CLI borrowing in oidc mode |
| 3 ~ | `Factory.Resolve`, `Factory.RunEnv` and `reviewers()` all take a work order; `Agents.RunEnv(ctx, agent, userID, role)` is already per user | factory usecase | Need a user-scoped agent run path for work that has no work order (AI review) |

## Events and runs

| # | Fact | Where | Prerequisite |
|---|---|---|---|
| 4 ✓ | `LiveEvent` carries only `WOID` (no user) | `backend/internal/model/event.go:17` | Add `UserID` and per-user delivery before any per-user live feature |
| 5 ~ | `jobs.wo_id` and `runs.work_order_id` are foreign keys to `work_orders` | migrations | Runs without a work order need their own table, or jobs/runs become polymorphic |
| 6 ~ | `RunRepo.RunningByProject` returns only the latest running run per work order | store | Add `RunningByWO` so parallel task builders can be listed |

## Files and agents

| # | Fact | Where | Prerequisite |
|---|---|---|---|
| 7 ✓ | Agents never write to the project folder. Work orders clone into `<DataDir>/workspaces/<wo>`, and task worktrees live in `<wo>.tasks/<task>` | `backend/internal/usecase/builder.go:463` | "Changed on disk by <agent>" can only work for work-order folders; a project folder can only say "changed on disk" |
| 8 ✓ | ACP sets `fs.writeTextFile: false`, so Legos never sees individual agent writes | `backend/internal/agent/acp.go:529` | Who changed a file has to be inferred from the path (folder → work order/task → running run), unless ACP file writes are turned on |
| 9 ~ | Task worktrees are deleted after merge | builder | Every view of a worktree needs a "folder no longer exists" state |
| 10 ~ | A local-folder project stores its absolute path in `projects.repo_url` | store | Opening "the project's code" works directly only for local projects. Remote-only projects need a clone/choose-folder step |

## Navigation

| # | Fact | Where | Prerequisite |
|---|---|---|---|
| 11 ~ | There is no project page; the rail links a project to `/factory?project=` | `frontend/src/app/layouts/AppShell.vue`, router | The project hub needs a new route plus a redirect from the old link |

## Security and transport

| # | Fact | Where | Prerequisite |
|---|---|---|---|
| 12 ~ | The session cookie is SameSite=Lax and there is no CSRF token | http middleware | New write or WebSocket routes need Origin checks and `Content-Type: application/json` |
| 13 ~ | File and process routes must stay local-only (`Server.local` gate + loopback Host middleware) | http server | In oidc mode the editor is off (404) |
| 14 ✓ | `go.mod` declares go 1.26.3 and has no WebSocket or fsnotify library | `backend/go.mod` | New dependencies (`coder/websocket`, `fsnotify`) need a yes. `os.Root` is available for path confinement |
| 15 ~ | The Vite dev proxy has no `ws: true` | `frontend/vite.config.*` | Must be set before any WebSocket feature works in dev |

## Tooling on this machine

| # | Fact | Prerequisite |
|---|---|---|
| 16 ~ | `gopls` v0.16.2 (built with go1.22) is in `~/Documents/go/bin`; GOPATH is not `~/go` | Discover servers through `go env GOPATH` too; show an "outdated" state |
| 17 ~ | `typescript-language-server` and `vue-language-server` are not installed | The Language servers panel must handle "not found" from day one |
| 18 ~ | `monaco-languageclient` 11 pins an older `@codingame/monaco-vscode-api` and replaces the plain `monaco-editor` import | Spike: our own thin LSP client vs the library |

## Migrations

Next free numbers when parked: `029` (Review PR), `030` (Editor). Recheck before building; other work may take them first.
