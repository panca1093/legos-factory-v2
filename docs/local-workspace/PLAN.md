# Local workspace model — plan

Status: draft, 2026-11-19. Decisions below are the user's. Nothing is built yet.

Legos stops working on its own clones and works inside the user's repository the way Claude Code and Orca do: git worktrees of the user's repo, the user's git identity and hooks, a base branch the user picks. Every project is a local folder; there is no shared connection and no multi-user mode for now.

## Why

Today (checked in code):

- Chat and every work order work on separate clones under `~/.legos-factory/workspaces/` (`chat-<project>`, `<wo>`, `<wo>.tasks/<task>`, `checks-src-<project>`). Measured on this machine: 2.2 GB in 18 folders. Each `.git` is only ~11 MB; the bulk is per-workspace dependencies (e.g. `web/` 750 MB).
- With `gh` available, those clones are re-pointed at the remote and start from its **default branch** (`sandbox/workspace.go` `retarget`). The user's folder, its branches, unpushed commits and uncommitted changes are never seen, and the result is never pulled back.
- PR branches are pushed straight to the remote; the base is always the default branch.
- Commits are hardcoded to `legos-factory <factory@localhost>` and run with hooks disabled (`-c core.hooksPath=/dev/null`, `--no-verify`) in `Commit`, `MergeTask`, `SyncTask`.
- Remote-URL projects need a **shared** git connection (`usecase/integration.go` `Bind`), a leftover of multi-user server mode.

## Decisions (fixed)

| # | Decision |
|---|---|
| L1 | No shared connections. Every connection is personal to its user. No multi-user mode for now |
| L2 | Every project is a local folder. "Open from GitHub/GitLab/Bitbucket" clones into a folder the user picks first, then adds that folder |
| L3 | A work order runs in a **git worktree of the user's repo**, created under `~/.legos-factory/worktrees/<project>/<wo>` (outside the repo, so the repo needs no `.gitignore` change). Task worktrees live next to it |
| L4 | The base branch is chosen when the work order is created; the PR targets it |
| L5 | The worktree is removed when the work is done. History (events, runs, findings, the final diff) stays in the factory database, so the UI can still show it |
| L6 | Commits use the repo's own git identity (`user.name`/`user.email`, repo-local first, then global) and run the repo's hooks as normal, like Claude Code |
| L7 | A rejected hook is fed back to the Builder as task input (like a failing check), never a stuck work order |
| L8 | Chat reads the project folder directly, live, including uncommitted changes. Chat stays read-only |
| L9 | Dependencies are installed per worktree by default and go away with it. Sharing them (e.g. a pnpm store) is offered only as a suggestion |

## What changes in the code

| Area | Today | After |
|---|---|---|
| Connections | `ScopeShared`/`ScopePersonal` (`model/integration.go:21`), admin-only shared create (`usecase/integration.go:120`), `usable()` scope rules, scope picker in `ProviderRow.vue` | Personal only; scope column dropped or fixed to personal |
| Project binding | `Bind` accepts only shared connections; `ProjectBindingRow.vue` | Removed. The git provider is resolved from the folder's `origin` host + the user's own access (`gh`/`glab`/personal token) |
| Remote-URL projects | Allowed when bound | Not allowed; add flow clones to a folder first |
| WO workspace | `Sandbox.Prepare` clones `repo_url` into `workspaces/<wo>`, `retarget`s to the remote default branch | `git -C <repo> fetch` then `git -C <repo> worktree add -b legos/<wo> ~/.legos-factory/worktrees/<project>/<wo> <base>` |
| Task worktrees | Worktrees of the WO clone at `workspaces/<wo>.tasks/<task>` (`builder.go:463`) | Worktrees of the user's repo at `worktrees/<project>/<wo>.tasks/<task>`, branch `legos/<wo>/<task>` |
| Commit identity + hooks | `-c user.name=legos-factory … -c core.hooksPath=/dev/null … --no-verify` in `Commit`, `MergeTask`, `SyncTask` (`sandbox/workspace.go:248, :497, :518`) | Plain `git commit` / `git merge` in the worktree; identity and hooks come from the repo |
| Hook failure | Cannot happen | Captured output becomes Builder feedback for that task, same path as a failed check; counts toward the revision cap |
| Push + PR | `PushBranch` pushes to `auth.RemoteURL`; PR base = default branch | Push `legos/<wo>` from the worktree to the repo's `origin`; PR base = the chosen base's upstream branch |
| Diff after done | `CommitDiff` reads `workspaces/<wo>` (`decision.go:201`); workspaces are only removed on WO/project delete | Final diff and changed-file list are stored in the DB before the worktree is removed; the Changes & PR tab reads the stored copy |
| Cleanup | `removeWorkspaces` on delete only (`decision.go:257, :295`) | Remove WO + task worktrees (`git worktree remove`, then `prune`) when the WO reaches done (PR merged or closed), is cancelled or deleted |
| Chat | Own clone `workspaces/chat-<project>` via `Checkout` (`app/app.go:357`), refreshed from remote | Agent cwd = the project folder; `Checkout`/refresh removed for chat |
| Check suggestions | Own clone `checks-src-<project>` (`app/app.go:392`) | Read the project folder directly |
| Other readers | KB docs roots (`kbdocs.go:279`), chat session "running" panel (`chatsession.go:344`) point at `workspaces/<wo>` | Point at the worktree path stored on the WO |

## Effect on parked prerequisites (`docs/project-hub/PREREQUISITES.md`)

- #1 (projects bind only shared connections): gone with L1/L2.
- #7 (agents never write to the project folder): agents write in worktrees of the user's repo. Branches and commits land in the user's repo; the user's checked-out working tree is still never touched.
- #8 (ACP `writeTextFile: false`): unchanged; attribution by path still works (worktree path → WO/task).
- #9 (task worktrees disappear): now also WO worktrees, by design (L5).
- #10 (remote-only projects): gone with L2.
- #19 (commit author): solved by L6.
- #2 and #13 (local-only gates): simpler, since there is no multi-user mode, but keep the gate code.

## Delivery plan (one PR each, in order)

| # | PR | Notes |
|---|---|---|
| 1 | Remove shared connections | Migration turns existing shared connections into personal ones owned by their creator (or drops them; see Q1). Drop `Bind`, scope picker, binding row |
| 2 | Local-only projects | Add-from-remote = clone to a chosen folder, then add. Existing remote-URL projects: migrate or ask (Q2). Provider resolved from the folder's `origin` |
| 3 | Commit identity + hooks | Remove `-c` overrides, `hooksPath=/dev/null`, `--no-verify`; hook failure → Builder feedback (L7) |
| 4 | Worktree workspaces + base branch | New `Prepare`/task worktree/push paths; `base_branch` on the WO (migration) + picker in New work order; stored final diff; cleanup at done |
| 5 | Chat and check suggestions read the folder | Remove `chat-*` and `checks-src-*` checkouts; keep chat read-only |
| 6 | Clean up old data | Remove `~/.legos-factory/workspaces` once no live WO uses it; migrate in-flight WOs or let them finish on the old path (Q5) |

PR 3 can ship before PR 4 on the current clones; PR 1 and 2 are independent of 3–5.

## Answers (user, 2026-11-19)

1. Shared connections: delete them.
2. Remote-URL projects: delete them (only `legos-pos-testbed` exists today).
3. Chat works on whatever branch is checked out in the folder. The factory always starts from the remote default branch, unless the work order explicitly names a base branch.
4. Local unpushed commits or changes on the base are ignored: the factory branches from the fetched remote default branch and never modifies the user's local branches.
5. No work order is running (the factory is stopped), so no in-flight migration.
6. Local `legos/<wo>` branches are deleted together with the worktree at cleanup.
7. Chat may do everything except change files (edit/write tools denied).
8. Warn on work-order creation when the folder has uncommitted changes. Renaming "work order" (task/story/epic) is a separate change, not in this PR.

Also decided: the Changes & PR tab goes away. When the final gate passes, the factory opens the PR straight away and the work order is in Ship until the PR is merged; the diff is reviewed on the PR (later in the Review PR page). No final diff needs to be stored in the DB.

Delivery: one PR instead of six.
