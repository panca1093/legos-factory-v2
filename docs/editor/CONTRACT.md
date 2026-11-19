# Editor — build contract

Status: 2026-11-20, written before the parallel build. `SPEC.md` is the design; this file pins shared names, answers to the open questions taken in full-auto mode (the user delegated them; each is the spec's recommendation or the safest option), and file ownership.

## Answers to open questions (auto, 2026-11-20)

| Q | Answer |
|---|---|
| P1–P8 | Accepted as proposed |
| Q1 Languages | `gopls` + `tsls` (TS/JS) in v1. Vue: syntax highlighting only; `vuels` not in the allowlist yet |
| Q2 Save | Explicit save (Cmd/Ctrl+S, Save all), no autosave |
| Q3 Git | Edit-only: no stage/commit/push/branch switch |
| Q4 | Later |
| Q5 Open in Code from the PR diff | Yes (step 7), with the "your folder is on `<branch>`, the PR is on `<head>`" strip |
| Q6 Remote-only projects | Moot since #91 (every project is a local folder); `editor_project_folders` table kept per SPEC but no UI for it |
| Q7 LSP client | Thin in-house client on plain `monaco-editor` + `vscode-jsonrpc` + `vscode-languageserver-protocol`; no spike PR, no `monaco-languageclient` |
| Q9 Delete | Not in v1 (no delete route, no UI). Mkdir, create file and rename are in |
| Q10 | Newest tab takes over (close code 4001, "Opened in another tab · Use here") |
| Q11 WO worktree edits | Info strip only, no blocking |
| Q12 More LSP features | None beyond D3 |
| Q13 Find in files | Out |
| Q15 Unsaved buffers | Not persisted; `beforeunload` warns when a tab is dirty |
| Q16 | No cap on open files |
| Q17 | Cmd+P quick open + rail open files when the panel is hidden |

New dependencies (user approved fsnotify + coder/websocket on 2026-11-19; Monaco is required by D2): Go `github.com/fsnotify/fsnotify`, `github.com/coder/websocket`; npm `monaco-editor`, `vscode-jsonrpc`, `vscode-languageserver-protocol`. Pin exact versions that are at least two weeks old.

Safety: builders never run a language server against the user's real folders outside tests (tests use temp dirs and a fake LSP binary), never delete files outside temp dirs, and never weaken the local-only gate.

## Go (on the branch before builders start)

- Types: `backend/internal/model/editor.go`; errors `ErrOutsideFolder`, `ErrTooLarge`, `ErrBinary`, `ErrFolderGone`, `ErrUntrusted`, `ErrTooManyLSP`, `*EditorConflict` in `model/errors.go`. Ports `EditorFS`, `LSPHost`, `EditorStore` in `backend/internal/usecase/ports.go` (SPEC §Layout adjusted: no `Remove` (Q9), `List` returns the truncated count, `Files` for quick open, no project-folder methods (Q6)).
- Migration number `032_editor.sql` (SPEC §Persistence).

## File ownership

| Builder | Owns |
|---|---|
| A files backend | `internal/sandbox/editorfs.go` (os.Root FS ops, binary sniff, git status/check-ignore/ls-files), `internal/store/editor.go` + `migrations/032_editor.sql`, `internal/usecase/editor.go` (folder resolution, refused roots, limits, write precondition, Open in Code folder for WO/task), `internal/http/editor.go` (REST routes, JSON content-type + Origin guard, local gate, 12 MB body limit for PUT file), its registration line in `server.go`, wiring in `app.go` |
| B live backend | `internal/sandbox/watch.go` (fsnotify, cap 256 dirs, debounce, self-write suppression), `internal/sandbox/lsp.go` (allowlist, discovery, spawn with Setpgid + scrubbed env, framing, idle/crash/backoff, limits), `internal/usecase/editor_live.go` (attribution incl. new `RunRepo.RunningByWO` in `store/run.go`, server state), `internal/http/editor_ws.go` (control + LSP WebSockets, Origin checks, close codes), `/editor/servers*` routes, its registration line in `server.go`, wiring lines in `app.go`, Vite `ws: true` in `frontend/vite.config.ts` |
| C editor UI | `frontend/src/features/editor/*` except `lsp.ts`, `ProblemsCard.vue`, `ServerCard.vue`, `ServersPanel.vue`; shared `frontend/src/components/files/FileTree.vue` (Review PR's Changes card switches to it only if trivial); Code route + rail Code section (`RailOpenFiles.vue` mounted in `ProjectRail.vue`); "Open in Code" on WO Details (`WorkOrderPanel.vue`) and on PR diff lines (`pulls/*`); `beforeunload` |
| D LSP UI | `frontend/src/features/editor/lsp.ts`, `ProblemsCard.vue`, `ServerCard.vue`, `ServersPanel.vue` (mounted in Settings › Editor), diagnostics in the editor store via the hook below |

Seam between C and D: `editorStore.ts` (owned by C) exposes `monaco` (the loaded module or null), `models: Map<uri, ITextModel>`, `folder`, `onModelOpened(cb)`, `onModelClosed(cb)`, and `diagnostics: Map<uri, Diagnostic[]>` that D fills; C renders tree error badges and the footer from it. D's `lsp.ts` exports `connectLanguageServers(store)` and C calls it once when the workbench mounts (guarded so it is a no-op until D's file exists).
