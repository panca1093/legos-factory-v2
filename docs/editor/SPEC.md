# Editor (Code selection) — spec (draft v1)

Status: draft · 2026-11-19 · not built · prototype: https://claude.ai/artifact/3HjuFb6i8LWPu5K3gnvi9D · v2 workspace prototype: https://claude.ai/artifact/H1bzbbj4iEL1CDVV582RDn (artboard "v2 · Code") · sibling of `docs/review-pr/SPEC.md` (D4 and §Project workspace define the workspace shell, rail and context panel)

Revised 2026-11-19 for the v2 project workspace layout.

**Revised 2026-11-20 — language servers removed.** The factory does not run language servers: no hover, go to definition, references, diagnostics, completion or quick fixes; no Problems card, Language server card, Settings › Editor servers panel, LSP socket or folder trust (`033_editor_drop_lsp.sql` drops `editor_server_paths` and `editor_folders.trusted`). The Code selection is a view-and-quick-edit editor: Monaco syntax highlighting, tabs, Explorer with git markers, quick open, save with conflict check, changed-on-disk banner. Code navigation belongs to the user's own editor. Why: only Go/TS were covered, servers cost RAM per worktree, they run repo code written by agents, and agents never used them. Every LSP, Problems, Language server and trust mention below is superseded by this note.

## Why

Legos produces code, but to read or touch it the user leaves for another editor: to fix one line before a PR, to check what the Builder wrote, or to look something up while chatting with an agent. The workspace's Code selection puts a real editor on the project's own folder inside Legos — Monaco and git markers — so small edits and code reading stay in the same place as the work orders that produce the code.

## Goals

1. `/projects/:id/code` opens the project's local folder in an editor that can edit and save files, inside the project workspace.
2. Explorer card with git markers, file tabs, breadcrumb, editor footer, and the files open in the editor listed in the project rail.
3. ~~LSP in v1~~ — removed 2026-11-20 (see the note above).
4. Edits are never lost silently: saving checks the file did not change on disk, and a file that changes on disk while open shows who changed it.
5. A work order's worktree opens in the same workspace from the work order's Details card ("Open in Code"). Folders outside projects are added as local projects first.

## Non-goals

- Terminal, debugger, test runner, task runner.
- Bundling or installing language servers (we only show the install command).
- Locking files or folders while a work order runs.
- Extensions, settings sync, multi-cursor collaboration, remote (SSH/container) folders.
- Search across files and replace across files (Q13).
- Review PR content. Review PR and Editor share UI pieces (file tree, Monaco, diff) but are separate features.
- Any of this in multi-user (`oidc`) deployments: the editor is local-only.

## Decisions (fixed by the user)

| # | Decision |
|---|---|
| D1 | (Revised 2026-11-19, v2 workspace) The Editor is the **Code** selection of the project workspace (`/projects/:id/code`) and opens the project's local folder. Center = editor card; right context panel = **Explorer** card; the project rail's **Code · N open** section lists the files open in the editor (§Open files). No tabs row on the project page and no shrinking header |
| D2 | (Revised 2026-11-19) In-app editing is required: Monaco in an editor card with file tabs (unsaved dot), Save button, breadcrumb and footer (language, Ln/Col, save state); Explorer card with branch, Find files, collapsible tree with git markers (M modified, U untracked/new) |
| D3 | (Removed 2026-11-20) ~~LSP is in v1: hover (signature + docs), go to definition, find references, diagnostics, completion, quick-fix code actions~~ |
| D4 | (Removed 2026-11-20) ~~Language servers are those installed on the user's machine, never bundled. The backend spawns them per opened folder and proxies JSON-RPC to the browser. A "Language servers" panel shows found / not found per language, a copyable install command and "Check again"~~ |
| D5 | Independent of work orders: no locking. A file watcher shows "`<file>` changed on disk · `<who>`" with Reload / Keep mine / Compare when the open file changes underneath |
| D6 | (Revised 2026-11-19) No "Open a folder" screen. Only project folders (Code selection) and their WO worktrees (from the WO Details card) open; anything else becomes a local project first. |
| D7 | No in-app terminal in v1 |
| D8 | Review PR's spec leaves the Code selection's content to this spec; the two features share UI pieces and the workspace shell only |

## Proposed decisions (need a yes; see Open questions)

| # | Decision | Why | Rejected alternative |
|---|---|---|---|
| P1 | The editor runs only in `dev` auth mode and only for Engineers, behind the same gate as `/local/*` (`Server.local`). In `oidc` mode every editor endpoint returns 404 `NOT_AVAILABLE` | Reading/writing the disk and spawning processes act as the OS user running the backend; in a shared server that user is not the person in the browser | Per-user sandboxing in multi-user mode (a different product) |
| P2 | A **folder** is the unit: every editor URL carries a server-side `folder_id`; file paths are always relative to it and confined with `os.Root` | Paths in requests can never name an absolute location; one confinement point | Absolute paths in the API, checked per request |
| P3 | Two WebSockets per open folder: `…/ws` (control: watch set, change events, git status, server state) and `…/lsp/:server` (raw JSON-RPC, one message per frame). Change events do not go through the SSE stream | Watcher lifetime = control socket lifetime, so no heartbeats or orphaned watchers; the LSP socket stays pure JSON-RPC so standard clients fit unmodified | Change events over SSE (needs `LiveEvent.UserID`, heartbeats to stop watchers) |
| P4 | One language server process per (folder, server), with **one** attached browser client. A second tab on the same folder takes the session over; the first shows "Opened in another tab · Use here" | LSP is single-client; multiplexing needs id rewriting and `didOpen` ref-counting | A JSON-RPC multiplexer in v1 |
| P5 | LSP client: a thin in-house client on plain `monaco-editor` (`vscode-jsonrpc` + `vscode-languageserver-protocol`) for the six D3 features, unless the phase-0 spike shows `monaco-languageclient` is cheaper to own (Q7) | `monaco-languageclient` 11.0.3 replaces `monaco-editor` with the `@codingame/monaco-vscode-api` 37.x service layer (checked on npm 2026-11-19), whose majors move monthly (latest 39.0.5); we need six features | `monaco-languageclient` by default |
| P6 | Language servers start only for **trusted** folders. Project folders and their WO worktrees are trusted. Since D6 was revised no other folder can open, so the prompt only matters if that changes; keep the `trusted` flag for it | gopls runs `go list` (and may download a toolchain via `GOTOOLCHAIN`); tsserver loads `tsconfig` plugins and the folder's own `node_modules/typescript` | Always start servers (VS Code's Workspace Trust exists for this reason) |
| P7 | Explicit save (Cmd/Ctrl+S, "Save all"), no autosave | Agents may be writing the same files (D5); a save is a deliberate, preconditioned write | Autosave after a delay (Q2) |
| P8 | Edit-only: no commit, push, stage or branch switch in v1. The Explorer card header shows branch and changed-file count | Keeps v1 to files; the user's own git/terminal does the rest | Commit + push from the editor (Q3) |

## Code facts that shape the design

| Fact | Where | Consequence |
|---|---|---|
| A local-folder project stores the folder's absolute path in `projects.repo_url` (`~` expanded); since #91 every project is a local folder, and a WO is a git worktree of it at `<DataDir>/worktrees/<project>/<wo_id>` on branch `legos/<wo_id>` | `usecase/project.go` `expandPath`, `sandbox/worktree.go` | The Code selection opens the user's real folder. Agents write only in worktrees; branches and commits land in the user's repo, its checked-out tree is never touched |
| Task worktrees live at `<DataDir>/worktrees/<project>/<wo_id>.tasks/<task_id>` on branch `legos/<wo_id>.tasks/<task_id>`; task worktrees go after merge, the WO worktree on PR merged/closed, cancel, decline or delete | `usecase/builder.go` `taskDir`, `sandbox/worktree.go` `Cleanup` | A worktree folder can disappear under the editor; the editor must show "folder removed" |
| Agents write files with their own tools in `cwd`; Legos sees no per-write event (`clientCapabilities.fs.writeTextFile: false`) | `agent/acp.go:529` | Attribution is path-based: folder → WO/task → the run running there |
| `RunRepo.RunningByProject` returns only the latest running run per WO | `store/run.go:83` | Parallel task builders need a per-task lookup (`RunningByWO(woID) []RunningRun`) |
| `Server.local` gates `/local/*` to Engineers in `dev` mode; `loopbackHostOnly` rejects non-loopback `Host` in dev mode | `http/local.go`, `http/server.go` | Reuse both for every editor route (P1) |
| Session cookie is `HttpOnly`, `SameSite=Lax`; CORS allows only `FACTORY_FRONTEND_ORIGIN`; there is no CSRF token | `http/server.go` | WS upgrades need an explicit Origin check; writes must not be reachable by a cross-site simple request (§Security) |
| No WebSocket or fsnotify dependency in `go.mod`; Go 1.26.3 | `backend/go.mod` | Add `github.com/coder/websocket` and `github.com/fsnotify/fsnotify`; `os.Root` (1.24) with `ReadFile/WriteFile/Rename/MkdirAll/RemoveAll` (1.25) is available |
| `LiveEvent` has only `WOID`; `Bus` keys by WO | `model/event.go`, `http/bus.go` | Editor events use the control socket (P3), not the bus |
| Vite proxies `/api` without `ws: true` | `frontend/vite.config.ts` | Dev proxy needs `ws: true` for the editor sockets |
| Existing diff parser and views: `parseUnifiedDiff`, `DiffView.vue`, `DiffFileView.vue`; themes via `[data-theme="dark"]` in `tokens.css`; Cmd+K search dialog in `AppShell.vue` | `frontend/src/…` | Compare uses Monaco's diff editor (two full texts), not the unified parser; Monaco themes are built from tokens; Cmd+K gets no editor entry (D6) |
| Migrations end at `030` (#91); Review PR claims `031` | `store/migrations` | Editor uses `032_editor.sql` |
| On this machine: `gopls` v0.16.2 built with go1.22.5 at `~/Documents/go/bin` (GOPATH is not `~/go`); `go` 1.27.3; `typescript-language-server` and `vue-language-server` not installed; `node` v26.13.0 at `/opt/homebrew/bin` | local check 2026-11-19 | Discovery must ask `go env GOPATH`/`GOBIN`; the panel must show "found but likely too old" for a gopls built with an older Go than the module needs ? verify the exact gopls failure mode |

## Flows

### F1 Open the project's code

1. `/projects/:id/code` → `GET /projects/:id/editor` resolves the project's folder:
   - local-folder project (`repo_url` is an absolute path that exists and is a git repo) → that folder;
   - remote-only project → empty state "This project has no local folder" with **Clone to a folder…** (existing `/local/clone`) and **Choose existing folder…**; the choice is stored per user (`editor_project_folders`) (Q6).
2. Response: `folder_id`, absolute path, branch. The client opens the control socket, loads the root listing into the Explorer card and restores open files from `localStorage` (§Open files).

### F2 Edit and save

1. Open file → `GET …/file?path=` returns `content`, `hash` (sha256), `mtime`, `size`, `eol`. The buffer remembers `base_hash`.
2. Typing marks the tab dirty (dot).
3. Save → `PUT …/file {path, content, base_hash}`:
   - disk hash = `base_hash` → atomic write → `{hash, mtime}`; `base_hash` := new hash; tab clean.
   - disk hash ≠ `base_hash` → 409 `{current_hash, mtime, actor}` → the changed-on-disk banner (F4) opens in conflict mode; nothing is written.
4. Closing a dirty tab asks Save / Don't save / Cancel; leaving the page with dirty tabs triggers `beforeunload`.

### F3 LSP interactions

Removed 2026-11-20 (language servers removed).

### F4 File changed on disk

1. The control socket reports `changed | deleted` for a watched file with `actor` (§Watcher).
2. Open tab **clean** → reload in place, keep cursor, toast "Reloaded · changed by Builder · wo_x".
3. Open tab **dirty** → banner over the editor: "`main.go` changed on disk · Builder · wo_x (task t3)" with:
   - **Reload** — discard my changes, load disk.
   - **Keep mine** — keep buffer; `base_hash` := disk hash so the next save overwrites deliberately.
   - **Compare** — Monaco diff editor, left = disk, right = mine (editable); closing it returns to the banner.
4. Deleted → banner "deleted on disk · `<who>`" with **Save to recreate** / **Close tab**.
5. The folder itself removed (merged task worktree) → full-pane state "This folder no longer exists" with **Open the work order** / **Close**; dirty buffers can be copied out via Compare.

### F5 Open a work-order worktree

Decided 2026-11-19: there is no "Open a folder" screen, Recent list or Home/Cmd+K entry. A folder outside every project is added as a local project first (existing Add project).

- Entry point: work-order selection › **Details** card → **Open in Code** (and per task, in the Progress tab: **Open task worktree**).
- Open → `POST /editor/folders {wo_id, task_id?}` → `/projects/:id/code/:folder_id`: the same Code selection in the same workspace; the content header shows the WO title + "Back to project code"; the Explorer card shows the worktree's branch. The WO stays selectable in the rail.
- Info strip "Edits here ship with work order wo_x if it commits them" (the factory commits the workspace as is) (Q11).

### F6 Missing language server

Removed 2026-11-20 (language servers removed).

## UI

Routes (the Code rows of Review PR §Navigation and routes): `/projects/:id/code[?file=<rel>&line=<n>]` (project folder) and `/projects/:id/code/:folder_id[?file=&line=]` (a WO worktree of that project). Both mount the same `EditorWorkbench.vue` in the workspace's center and the Explorer / Problems / Language server cards in its context panel. Switching files uses `router.replace` (`?file=`); opening the Code selection from another selection is a `push`, so back returns to the work order or PR. The redirect `/projects/:id?tab=code` → `/projects/:id/code` is in Review PR.

Layout (inside the project workspace; rail and panel per Review PR §Project workspace):

```
┌ project rail ─────┬ content ─────────────────────────────────────────────┬ context panel ─────────┐
│ ← All projects    │ server.go                    [Save] [Hide panel]     │ ┌ Explorer ──────────┐ │
│ legos-factory     │ ┌ editor card ─────────────────────────────────────┐ │ │ ⎇ main · 4 changed │ │
│ FACTORY · 3 open  │ │ main.go ● | server.go | …              (tabs)    │ │ │ [Find files…]      │ │
│ PULL REQUESTS · 2 │ │ backend › internal › http › server.go            │ │ │ ▾ backend       M  │ │
│ CODE · 2 open     │ │ [banner: changed on disk · Builder · wo_x …]     │ │ │   server.go  2  M  │ │
│ ● main.go       M │ │                                                  │ │ │   new.go        U  │ │
│   server.go     M │ │   Monaco                                         │ │ └────────────────────┘ │
│                   │ │                                                  │ │ ┌ Problems (3) ──────┐ │
│                   │ │                                                  │ │ │ server.go:42 …     │ │
│                   │ ├──────────────────────────────────────────────────┤ │ └────────────────────┘ │
│ [Usage card]      │ │ Go │ gopls ready │ Ln 42, Col 7 │ LF │ Saved      │ │ ┌ Language server ───┐ │
│                   │ └──────────────────────────────────────────────────┘ │ │ gopls · ready  ›   │ │
└───────────────────┴──────────────────────────────────────────────────────┴────────────────────────┘
```

| Area | Behaviour |
|---|---|
| Content header | File name (or "Code" when no file is open; WO title + "Back to project code" in a worktree), **Save** (disabled when clean; "Save all" in its menu), "Hide panel / Show panel" from the shell. The editor card takes the remaining height of the content area |
| Rail › Code · N open | §Open files |
| Explorer card | Header: branch + changed count (click → filter tree to changed files), **Find files** (opens quick open, same as Cmd+P). Tree: lazy per directory; dirs first, then files, case-insensitive sort; single-child folder chains compacted (shared with Review PR's Changes card); `.git` hidden; ignored entries dimmed (`git check-ignore`). Markers: `M` modified, `U` untracked, `A` added (staged new), `D` deleted (shown struck until refreshed), conflict `!`. Folder carries the strongest descendant marker as a dot. Error badge = error-level diagnostics count (folders roll up). Context menu: New file, New folder, Rename, Delete (Q9), Copy relative path, Reveal in Finder (dev mode `open -R`). Virtualized rows (fixed 24px); the card scrolls internally and takes the free height of the panel |
| Tabs | In the editor card. Preview tab (italic) on single click, pinned on edit or double-click; unsaved dot; middle-click closes; overflow scrolls; the tab set is the open-files list (§Open files) |
| Breadcrumb | Path segments, each a dropdown of siblings; last segment = file. No symbol breadcrumb in v1 |
| Editor | Monaco; theme from tokens (light/dark follow `data-theme`); read-only badge for 2–10 MB files, binary/too-large placeholder with size |
| Banner | F4, inside the editor card above Monaco. Tone `--warning-soft`; actor chip uses `--crew-<role>` colour |
| Footer | Bottom of the editor card: language; that language's server state `starting · indexing n% · ready · crashed · not found · restricted` (click → Settings › Editor); Ln/Col; encoding + EOL; save state `Saved · Unsaved (n) · Saving… · Conflict`; error/warning counts (click → shows the panel if hidden and scrolls to the Problems card) |
| Problems card | Grouped by file; filter Errors / Warnings / All; click → reveal. TS servers only diagnose open files — the card says "Open files only" for those servers. Collapsed to its title bar when empty |
| Language server card | One row per server running or needed for this folder: state, version, last error line; **Restart** when `crashed`; link "Language servers in Settings › Editor" (F6) |
| Panel hidden | Explorer, Problems and server cards are gone; Cmd+P quick open, the rail's open files and the footer still work |
| Language servers panel | Section in Settings › Editor; the footer server item and the Language server card link there (F6) |

### Open files

"Open files" = the editor's tab set for a folder (preview tab included, shown italic). The rail's **Code · N open** section lists them for the project folder; when worktree folders of this project also have open tabs, their files follow under a one-line label "wo_x worktree" (N counts all). A row shows the file name (path tail on hover), the unsaved dot when dirty, and the explorer's git marker (`M`/`U`/`A`). Click → that file's route (`/projects/:id/code?file=` or `/code/:folder_id?file=`) and activates its tab; hover × closes it (same Save / Don't save / Cancel prompt for dirty tabs). Section title opens `/projects/:id/code` with the last active file.

Persistence (proposal, client-side): `localStorage` key `legos-editor:<user_id>:<folder_id>` = `{tabs: [{path, preview, cursor, scroll}], active}`; restored on reload and read by the rail on workspace mount **without** loading Monaco (the rail must show open files while a WO or PR is selected). Unsaved buffer contents are not persisted (reload with dirty tabs is guarded by `beforeunload`; Q15). A tab whose file no longer exists is dropped on restore; a worktree folder that is gone (`FOLDER_GONE`) drops its group. Rejected: server-side tab state (an `editor_tabs` table + sync endpoints) — the editor is local-only and single-user (P1), so there is no second device or user to sync with.

Lifetime across selections: the editor store (folder, tabs, buffers, diagnostics, git status) is scoped to the workspace page, not the Code selection, so switching to a WO or PR and back keeps dirty buffers, cursors and Monaco models; the control socket stays open while the folder has open tabs (so F4 changes are still caught), LSP sockets stay attached until the workspace page unmounts (servers then idle-stop per §Language server manager). Leaving the workspace (another project, a global nav item) with dirty buffers asks Save all / Discard / Cancel.

Keyboard: Cmd/Ctrl+S save, Cmd+Alt+S save all, Cmd+P quick open (fuzzy over `git ls-files` + untracked, capped 50k), F12, Shift+F12, Cmd+. quick fix, Cmd+W close tab. Cmd+K keeps opening the app search dialog (Monaco's Cmd+K chords are disabled).

## Backend

### Layout

- `internal/usecase/editor.go` — `Editor` usecase: folder resolution and registration, trust, allowed-roots policy, size/binary rules, write preconditions, actor attribution, server allowlist and limits. All rules here.
- `internal/sandbox/editorfs.go` — `os.Root`-based listing/read/atomic write/rename/delete, binary sniff, `git status`/`check-ignore`/branch, fsnotify watcher.
- `internal/sandbox/lsp.go` — process manager: discovery, spawn, stdio framing, idle/crash handling.
- `internal/store/editor.go` + `032_editor.sql`.
- `internal/http/editor.go` — handlers and both WebSockets.

Ports (`usecase/ports.go`):

```go
type EditorFS interface {
	List(root, rel string) ([]model.FileEntry, error)
	Read(root, rel string, max int64) (model.FileContent, error)
	Write(root, rel string, content []byte, baseHash string, create bool) (model.FileStat, error) // ErrConflict carries current stat
	Mkdir(root, rel string) error
	Rename(root, from, to string) error
	Remove(root, rel string) error
	GitStatus(ctx context.Context, root string) (model.GitStatus, error) // branch, entries
	Ignored(ctx context.Context, root string, rels []string) (map[string]bool, error)
	Watch(root string) (model.Watcher, error) // Set(dirs []string); Events() <-chan model.FSEvent; Close()
}

type LSPHost interface {
	Discover(ctx context.Context, servers []model.LSPServerSpec, extra map[string]string) []model.LSPServerState
	Start(ctx context.Context, spec model.LSPServerSpec, bin, root string, env []string) (model.LSPProcess, error) // ReadMessage/WriteMessage/Wait/Kill
}

type EditorStore interface {
	Folder(ctx context.Context, userID, id string) (*model.EditorFolder, error)
	UpsertFolder(ctx context.Context, f model.EditorFolder) (*model.EditorFolder, error) // by (user, path)
	DeleteFolder(ctx context.Context, userID, id string) error
	SetTrusted(ctx context.Context, userID, id string, trusted bool) error
	ProjectFolder(ctx context.Context, userID, projectID string) (string, error)
	SetProjectFolder(ctx context.Context, userID, projectID, path string) error
	ServerPath(ctx context.Context, userID, server string) (string, error)
	SetServerPath(ctx context.Context, userID, server, path string) error
}
```

### Persistence (`032_editor.sql`)

```sql
CREATE TABLE editor_folders (
    id TEXT PRIMARY KEY, user_id TEXT NOT NULL,
    path TEXT NOT NULL,                       -- absolute, cleaned, symlinks resolved
    kind TEXT NOT NULL,                       -- project | worktree | folder
    project_id TEXT NOT NULL DEFAULT '', wo_id TEXT NOT NULL DEFAULT '', task_id TEXT NOT NULL DEFAULT '',
    trusted INTEGER NOT NULL DEFAULT 0,
    opened_at INTEGER NOT NULL,
    UNIQUE (user_id, path)
);
CREATE TABLE editor_project_folders (          -- remote-only projects: the user's chosen local folder
    user_id TEXT NOT NULL, project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    path TEXT NOT NULL, PRIMARY KEY (user_id, project_id)
);
CREATE TABLE editor_server_paths (             -- confirmed custom binaries
    user_id TEXT NOT NULL, server TEXT NOT NULL, path TEXT NOT NULL, confirmed_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, server)
);
```

Open tabs, cursor, expanded dirs: client `localStorage` only.

### Folder resolution and allowed roots (usecase)

A folder is registered only through one of:

| Source | Rule | kind / trusted |
|---|---|---|
| Project | `Projects.Authorize`; `repo_url` absolute + git repo, else `editor_project_folders` | project / yes |
| WO workspace | path is `<DataDir>/worktrees/<project>/<wo_id>` or `…/<wo_id>.tasks/<task_id>`; user can access the WO's project | worktree / yes |
| Any folder | user typed or picked it | folder / ask |

Refused (400 with reason): `/`, the user's home directory itself, `<DataDir>` and anything under it except the WO workspace pattern above (holds `factory.db`, `secret.key`, `accounts/`, old `workspaces/` clones), system dirs (`/System`, `/usr`, `/bin`, `/etc`, `/private`, `/Library` on macOS; `/proc`, `/sys`, `/etc` on Linux). The path is cleaned and `filepath.EvalSymlinks`-resolved before the check and before storing.

Read-only external roots for go-to-definition targets (F3): `go env GOROOT`, `$(go env GOMODCACHE)`, and `node_modules` directories of the folder's parent chain up to `$HOME`. Read-only, no tree, opened from a definition jump only.

### Path confinement

- Every operation opens `os.OpenRoot(folder.path)` ✓ (Go ≥1.24) and uses `Root` methods (`Open`, `Stat`, `Lstat`, `ReadFile`, `WriteFile`, `Rename`, `Mkdir`, `Remove`; `ReadFile/WriteFile/Rename/RemoveAll/MkdirAll` need Go 1.25 ✓). `os.Root` rejects `..` escapes and symlinks that resolve outside the root ✓; it follows symlinks that stay inside.
- `rel` validation before `Root`: non-empty, not absolute, no NUL, no `\` on non-Windows, `filepath.IsLocal(rel)` ✓ (Go 1.20), max 4096 bytes. `.git` and anything under it is hidden from the tree and refused for every file operation.
- Symlinks in the tree are listed with `kind: symlink` and `target_inside: bool`; opening an outside target → 403 "link points outside this folder".
- TOCTOU between check and use is covered by `os.Root` (resolution happens per call inside the root) ✓.
- macOS case-insensitive APFS: no special handling; `os.Root` operates on the real FS.

### Read, write and limits

| Rule | Value |
|---|---|
| Editable + LSP | ≤ 2 MB, valid UTF-8 (BOM kept and restored) |
| Read-only, no LSP | 2–10 MB, or non-UTF-8 text |
| Refused | > 10 MB (413), binary (415: NUL byte in the first 8 KB, git's heuristic ✓) — placeholder shows size |
| EOL | detected (`LF`/`CRLF`), preserved on write |
| Write | `base_hash` precondition (sha256 of disk bytes) ✓ conflict → 409 with current stat and last attributed actor; atomic: temp `.<name>.legos-tmp-<rand>` in the same dir via `Root`, fsync, `Rename` over target; file mode preserved; writing to a path that is a symlink writes its in-root target, never replaces the link |
| Create | `create: true`, `base_hash: ""`; exists → 409 |
| Rename | target must not exist (409); open tabs re-keyed by the client |
| Delete | Q9; v1 proposal: confirm dialog; tracked-and-clean files say "recoverable with git", others "cannot be undone"; folders only when empty or after typing the folder name |
| Request body | `PUT …/file` body limit 12 MB (route-specific skip of the global 1 MB limit, like chat uploads) |
| Listing | max 5,000 entries per directory, then `truncated: N more` |
| Quick open | `git ls-files -co --exclude-standard -z`, capped 50,000 paths, cached until the next tree event |

### Git status

- `git status --porcelain=v2 -z --branch --untracked-files=normal` ✓ (untracked dirs collapsed, cheap on big repos), timeout 10 s, run with `GIT_OPTIONAL_LOCKS=0` ✓ so it never takes the index lock an agent's git may need.
- Refresh: on the control socket open, then debounced 500 ms after any watched event, at most once per 2 s; pushed as `git_status` frames (full list, ≤ 20k entries, else `too_many` and markers off).
- Branch from the `# branch.head` line; detached shows the short SHA.
- Ignored dimming: `git check-ignore --stdin -z` on the entries of a directory when it is listed.
- Not a git repo → no markers, branch item hidden.

### Watcher

- `fsnotify` ✓ (`github.com/fsnotify/fsnotify`; kqueue on macOS needs one fd per watched file and directory entry ✓ — so we never watch recursively).
- Watch set (sent by the client over the control socket): parent directories of open tabs + expanded explorer directories + root, deduped, capped at 256 directories (oldest expanded dropped first; open-tab dirs never dropped). Watching directories, not files, also catches write-temp-then-rename saves ✓.
- Ignored paths: `.git/**` except `.git/HEAD` (branch change → git refresh only), `node_modules/**` unless expanded, `.DS_Store`, our own temp files.
- Debounce per path 150 ms; event → stat + hash for open files (skip hash > 10 MB) → `file_changed {path, kind: changed|deleted|created, hash, mtime, actor}`; directory events → `dir_changed {dir}` (client re-lists if expanded).
- Self-write suppression: after our own `Write`, `(path, hash)` is remembered for 2 s; an event whose hash matches is dropped.
- Go raises the soft `RLIMIT_NOFILE` to the hard limit at startup ✓ (Go ≥1.19); the 256-dir cap keeps us far below it on large folders.
- Watcher closes with the control socket (P3).

### Actor attribution (usecase)

Input: folder, changed path, time. Output: `{role, wo_id, task_id, label}` or null.

1. Folder kind `worktree` with `task_id` → the running run of that WO with that task (new `RunRepo.RunningByWO(woID) []RunningRun`, one row per running run) → `Builder · wo_x (task t3)`.
2. Folder kind `worktree`, WO workspace → running run of that WO without task, else the latest running one → `<Role> · wo_x`; none running but one finished ≤ 10 s ago → that run; else `Factory · wo_x` (merges and syncs of task branches write there too).
3. Folder kind `project` / `folder` → no Legos writer exists (agents work only in `<DataDir>/workspaces`) → `null`, banner says "changed on disk". If `.git/HEAD` changed in the same 1 s window → label "after a git checkout".
4. A path inside a project folder can also be a WO's source only through `git clone` — not a write; nothing to attribute.

Cached per control socket for 2 s per WO to avoid a query per event.

### Language server manager

Removed 2026-11-20 (language servers removed).

### WebSocket protocol

Library: `github.com/coder/websocket` ✓ (rejects cross-origin by default; `OriginPatterns` for allowed ones ✓) — version ? verify at implementation.

Upgrade checks (both sockets): `Server.local` gate (dev mode, Engineer), session cookie, `Origin` ∈ {`FACTORY_FRONTEND_ORIGIN`, the server's own origin}, `Host` loopback (existing middleware), folder belongs to the caller. Missing/other `Origin` → 403.

`/editor/folders/:fid/lsp/:server`: each text frame is exactly one JSON-RPC message (the `vscode-ws-jsonrpc` convention ✓); the backend translates to/from `Content-Length` framing on stdio ✓. Close codes: 4001 taken over by another tab, 4002 server not found, 4003 folder untrusted, 4004 too many servers, 4005 server crashed (client shows state; reconnect on user action). Only `initialize` is inspected/rewritten; every other message passes through.

`/editor/folders/:fid/ws` (JSON frames):

| Direction | `type` | Body |
|---|---|---|
| client → | `watch` | `{dirs: [rel], files: [rel]}` (full set each time) |
| → client | `file_changed` | `{path, kind, hash, mtime, actor}` |
| → client | `dir_changed` | `{dir}` |
| → client | `git_status` | `{branch, ahead, behind, entries: [{path, x, y}], too_many}` |
| → client | `server_state` | `{server, state, version, progress?, error?}` |
| → client | `folder_gone` | `{}` |

### HTTP API (`/api/v1`, all behind `Server.local`)

| Method | Path | Body / query | Returns |
|---|---|---|---|
| GET | `/projects/:id/editor` | — | `{folder}` or `{needs_folder: true}` |
| PUT | `/projects/:id/editor` | `{path}` | `{folder}` (remote-only projects) |
| POST | `/editor/folders` | `{wo_id, task_id?}` | `{folder}` for that WO/task worktree (404 when it no longer exists) |
| POST | `/editor/folders/:fid/trust` | `{trusted}` | `{folder}` |
| GET | `/editor/folders/:fid/tree` | `path=<rel dir>` | `{entries: [{name, kind, size, mtime, target_inside?, ignored}], truncated}` |
| GET | `/editor/folders/:fid/files` | — | quick-open paths |
| GET | `/editor/folders/:fid/file` | `path` | `{content, hash, mtime, size, eol, bom, readonly}`; 413 / 415 |
| PUT | `/editor/folders/:fid/file` | `{path, content, base_hash, create?}` | `{hash, mtime}`; 409 `{current_hash, mtime, actor}` |
| POST | `/editor/folders/:fid/mkdir` | `{path}` | 201 |
| POST | `/editor/folders/:fid/rename` | `{from, to}` | 200; 409 |
| DELETE | `/editor/folders/:fid/file` | `path`, `confirm=<rel path>` | 204 |
| GET | `/editor/folders/:fid/external` | `uri` | read-only content under the external roots only |
| GET | `/editor/servers` | — | `[{id, languages, state, version, path, install, error}]` |
| POST | `/editor/servers/check` | — | same, after rediscovery |
| PUT | `/editor/servers/:id` | `{path, confirm: true}` | state after one version probe |
| GET (WS) | `/editor/folders/:fid/ws` | — | control socket |
| GET (WS) | `/editor/folders/:fid/lsp/:server` | — | JSON-RPC socket |

Errors use the existing envelope; codes `NOT_AVAILABLE`, `VALIDATION_FAILED`, `OUTSIDE_FOLDER`, `CONFLICT`, `TOO_LARGE`, `BINARY`, `FOLDER_GONE`, `UNTRUSTED`, `TOO_MANY_SERVERS`.

## Security

- Local-only (P1): `dev` auth mode + Engineer + loopback `Host` (existing) for every editor route and socket. The handler set is not registered at all when `Local == nil`.
- Cross-site writes: all mutating routes are `PUT`/`DELETE` or `POST` that **require** `Content-Type: application/json` (415 otherwise), so a cross-site form post cannot reach them (non-simple requests need a CORS preflight, which only allows `FACTORY_FRONTEND_ORIGIN`). A middleware for `/editor/*` also rejects a present `Origin` that is not allowed.
- WebSocket hijacking: explicit Origin check (above); SameSite=Lax cookie alone is not enough for same-site attackers on other localhost ports, so Origin is mandatory, not optional.
- Paths: folder id → stored absolute path; client only sends relative paths; `os.Root` confinement; refused roots list; `.git` read/write blocked.
- Processes: only allowlisted server ids, fixed args, `exec.Command` argv (no shell); binary path never from a request except `PUT /editor/servers/:id` with `confirm: true`, which runs the version probe once and stores it per user; the panel always shows the full resolved path being run. Env scrubbed of Legos and provider secrets.
- Trust (P6): language servers only in trusted folders.
- The human editor is not an agent: chat-repo secret deny rules (`.env*`) do not apply; nothing read here is sent to a model.
- Logs: request logger already logs path only; file contents and LSP payloads are never logged; stderr tails are shown only to the owner.

## Frontend architecture

- `features/editor/`: `EditorWorkbench.vue` (editor card for the workspace center), `ExplorerCard.vue` (wraps the shared tree), `ProblemsCard.vue`, `ServerCard.vue` (the three mounted in the workspace context-panel slot for the Code selection), `RailOpenFiles.vue` (mounted in the rail's Code section), `EditorTabs.vue`, `MonacoHost.vue`, `ChangedBanner.vue`, `EditorFooter.vue`, `ServersPanel.vue` (mounted in `features/settings`), `editorStore.ts` (Pinia, scoped to the workspace page: folders, tabs, buffers, diagnostics, git status; hydrates open files from `localStorage` without Monaco), `fs.ts` (API client), `control.ts` (control socket), `lsp.ts` (client, P5).
- Shared with Review PR: `components/files/FileTree.vue` (flat-row virtualized tree with compaction, marker and badge slots); the Review PR tree uses the same component.
- Monaco: `monaco-editor` 0.57.0 (npm, 2026-11-19) loaded only by the editor route chunk via `import("monaco-editor/esm/vs/editor/editor.api")` + the basic-language contributions we need ✓; workers with Vite `?worker` (`editor.worker` only). Monaco's built-in TypeScript/JSON language services are **not** loaded (they would compete with `tsls` diagnostics). Bundle ≈ several MB for the editor chunk ? measure in phase 0; it is never part of the main chunk.
- Themes: two Monaco themes built at runtime from `tokens.css` variables (`getComputedStyle`), switched on `data-theme` change.
- LSP client (P5, thin): JSON-RPC over the LSP socket with `vscode-jsonrpc` ✓; capabilities declared for exactly the D3 features; providers registered on Monaco (`registerHoverProvider`, `registerDefinitionProvider`, `registerReferenceProvider`, `registerCompletionItemProvider`, `registerCodeActionProvider`, markers for diagnostics ✓); document sync full or incremental per server capability; `workspace/configuration`, `client/registerCapability`, `window/workDoneProgress/create`, `$/progress`, `window/showMessage`, `workspace/applyEdit` handled; other server requests answered with `MethodNotFound`. URIs are `file://` absolute paths of the folder; models are keyed by that URI. Estimated ≤ 700 LOC ? confirm in spike.
- If the spike picks `monaco-languageclient` instead: 11.0.3 pins `@codingame/monaco-vscode-api` ^37.6.0, `vscode-languageclient` ~10.1.2, `vscode-ws-jsonrpc` ~4.0.3 ✓ (npm); it replaces the `monaco-editor` import with `@codingame/monaco-vscode-editor-api`, so Review PR's diff reuse must use the same alias ? verify; compatibility with Vite 7 and Vue 3 wrappers ? verify.
- Vite: `/api` proxy gets `ws: true`.

## Performance

- Tree: lazy listing, virtualized rows, git status with `--untracked-files=normal`, markers computed client-side from one status list (prefix map) — O(entries) per refresh.
- Large repos: watch cap 256 dirs; quick open cap 50k; status cap 20k entries; file caps above.
- gopls on a large module can use 1 GB+ RAM ? (varies); the 4-server cap and 10-min idle stop bound it.
- Diagnostics: Problems card and badges update in a `requestAnimationFrame` batch; markers per model set once per `publishDiagnostics`.
- Typing: `didChange` debounced 100 ms for full-sync servers; incremental when offered.

## Test plan

Backend unit (`sandbox`, `usecase`):

- Confinement table: `../x`, `/etc/passwd`, `a/../../x`, NUL, `.git/config` write, symlink file → outside (403), symlink dir → outside, symlink chain inside (allowed), symlink replaced between list and read (still confined).
- Refused roots: `/`, `$HOME`, `<DataDir>`, `<DataDir>/workspaces/x`, `<DataDir>/worktrees/<project>/<wo>` (allowed only with project access), `<wo>.tasks/<t>` allowed.
- Read: UTF-8, BOM, CRLF round trip, binary 415, 2 MB/10 MB boundaries, non-UTF-8 → readonly.
- Write: precondition 409 with current hash; atomic (target never half-written: kill between temp write and rename leaves the old file); mode preserved; symlinked target written in place; create-existing 409.
- Git: porcelain v2 `-z` parsing (renames, untracked dir, conflicts, detached head); `GIT_OPTIONAL_LOCKS=0` set; non-repo.
- Watcher (temp dir): debounce, temp-then-rename detected, `.git`/`node_modules` ignored, self-write dropped, cap and eviction order, close stops goroutines.
- Attribution table: task worktree with running builder, WO workspace with architect, finished ≤ 10 s, none → Factory, project folder → null, `.git/HEAD` change label.
- LSP manager with a fake server (test binary re-exec via `os.Args[0]` + env flag): framing both ways incl. multi-byte content, `initialize` rewrite, cached initialize on reconnect, takeover close 4001, idle shutdown, crash backoff and `crashed` state, process-group kill, env scrubbing, 4-server cap, unknown server id 400.
- Discovery with a fake PATH and fake `go env` output: GOPATH/bin found, login-shell PATH merge, Homebrew dir, outdated gopls state, node missing.

HTTP (`editor_test.go`): `oidc` mode → 404 on all routes; Viewer → 403; non-JSON content type on write → 415; WS with foreign Origin → 403; WS without cookie → 401; folder of another user → 404.

Frontend (vitest): tree flatten + virtual window + compaction, marker roll-up, error badge roll-up, dirty/save state machine incl. 409 → banner, banner actions (Reload / Keep mine sets base hash / Compare), clean-tab auto reload, open-files persistence (key per user + folder, restore drops missing files and gone worktrees, rail hydrates without Monaco), store survives a selection switch, leave-workspace prompt with dirty buffers, LSP client: capability negotiation, diagnostics → markers, `applyEdit` on unopened file opens it dirty.

E2E (Playwright, backend with fake LSP): open the Code selection, edit + save, switch to a WO and back (buffer kept, rail lists the file), reload (open files restored), external write → banner → Keep mine → save overwrites; missing server panel copy button.

Live check (manual): gopls on this repo's `backend/` (expect "outdated" with gopls 0.16.2 here, then after upgrade: hover, F12 into stdlib read-only, references, quick fix "add import"); `typescript-language-server` on `frontend/` after install; edit a running WO's task worktree and watch the Builder banner.

## Delivery plan (small PRs, in order)

| # | PR | Depends on |
|---|---|---|
| 0 | Spike (not merged): fake WS proxy + gopls + both client options on one page; measure bundle, LOC, feature coverage → decide P5, record in this spec | — |
| 1 | Migration 032, `EditorStore`, folder resolution + refused roots, `os.Root` FS ops, git status, HTTP file/tree/folder routes, JSON content-type guard; tests | — |
| 2 | Frontend: Code selection in the workspace — editor card (tabs, breadcrumb, Save, footer without LSP), Explorer card (branch, Find files, tree), rail Code section + open-files persistence, workspace-scoped editor store, `FileTree` shared component, Monaco lazy chunk, save + 409 handling, Vite `ws: true` | 1, Review PR #4a (workspace shell) |
| 3 | Control socket + watcher + attribution (`RunningByWO`) + git status push; banner, Compare, auto-reload, folder gone | 1, 2 |
| 4 | LSP manager: allowlist, discovery, spawn/idle/crash, LSP socket with Origin checks; servers endpoints; fake-server tests | 1 |
| 5 | Frontend LSP client (per spike) for D3 features; Problems card, Language server card; tree error badges; servers panel | 3, 4 |
| 6 | "Open in Code" from the WO Details card (worktree folders, rail worktree group), Settings › Editor language servers section, read-only external definitions | 3, 5 |
| 7 | (if Q5 yes) "Open in Code" from Review PR diff lines | 2, Review PR #5 |

## Open questions

1. **Languages in v1.** Recommend Go (`gopls`) + TS/JS (`typescript-language-server`); Vue syntax highlighting only, `vue-language-server` in v1.1 after verifying its v3 hybrid-mode setup. Vue is this repo's own frontend language — say if it must be v1.
2. **Save model.** Recommend explicit save (P7). Alternative: autosave after 1 s idle, still with the hash precondition.
3. **Commit / push from the editor.** Recommend edit-only (P8). If wanted later: stage/commit only, push through the existing publish path.
4. **"Start work order from selection".** Recommend later: needs a WO-intake attachment format for code ranges.
5. **"Open in Code at file:line" from the Review PR diff.** Recommend yes (phase 7, cheap route params). The local checkout may be on another branch than the PR head: open anyway with a strip "Your folder is on `main`; the PR is on `feat/x`".
6. **Remote-only projects (no local folder).** Recommend "Clone to a folder… / Choose existing folder…" stored per user. Rejected: a Legos-managed clone under `<DataDir>/workspaces` (edits would be stranded without commit/push, and `chat-*` checkouts are reset with `git reset --hard`).
7. **LSP client library (P5).** Thin client vs `monaco-languageclient` 11 — decide after the phase-0 spike.
8. ~~Trust prompt for non-project folders~~ — moot after D6 revision (no non-project folders open).
9. **Delete in the explorer.** Confirm-only as proposed, or move to the OS trash (macOS needs `osascript`/Finder; Linux varies), or no delete in v1?
10. **One client per language server (P4).** Is "the newest tab takes over" acceptable, or must two tabs share one session in v1?
11. **Editing a WO worktree.** Edits there are committed with the WO's next commit. Keep the info strip only, or block saves while that WO's Builder run is active?
12. **More LSP features** (rename, format, signature help, inlay hints): any of them required in v1?
13. **Find in files.** Out of v1; `git grep` based search would be a small add if wanted.
14. **Unverified external behaviour** to settle in phase 0/4: gopls 0.16 behaviour on a go 1.26 module and whether to gate on the build Go version; `vue-language-server` 3.x stdio setup; login-shell PATH under the Wails GUI launch; `coder/websocket` current version; Monaco chunk size; `monaco-languageclient` with plain `monaco-editor` and Vite 7.
15. **Unsaved buffers across reload.** Proposed: not persisted (only the open-files list is); `beforeunload` warns. Alternative: keep dirty contents in `localStorage` (size-capped, e.g. 5 MB total) and restore them as dirty tabs, re-checked against the disk hash.
16. **Rail Code section size.** Proposed: no cap on open files, but worktree groups collapse to their label when the current folder is another one. Is a cap (e.g. last 10, with "N more" → tabs) wanted?
17. **Code selection when the panel is hidden.** The Explorer is then gone; proposed: Cmd+P and the rail's open files are enough. Alternative: a narrow Explorer toggle inside the editor card.
