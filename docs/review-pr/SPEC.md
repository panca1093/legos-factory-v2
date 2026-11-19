# Review PR — spec (draft v1)

Status: draft · 2026-11-19 · not built · prototype: https://claude.ai/artifact/H1bzbbj4iEL1CDVV582RDn (artboards "v2 · Factory / Pull request selected / Code")

Revised 2026-11-19 for the v2 project workspace layout.

## Why

Legos turns work orders into PRs, but the user's other daily job — reviewing PRs, their own and other people's — happens in the git host's web UI with no AI help and no link to the project's knowledge. The Inspector already reviews factory diffs; Review PR points the same reviewer at any open PR the user is involved in, lets the user decide each AI comment, and posts the result to the host **under the user's own name**.

## Goals

1. List every open PR in a project's repo where the user is author or requested reviewer, on GitHub, GitLab and Bitbucket Cloud.
2. Review a PR in-app: file tree, diff, AI drafts inline, per-draft Accept / Edit / Discard.
3. Post one review (inline comments + summary + verdict) as the user, only after an explicit confirm.
4. The AI reviewer runs on the user's own agent account; provider data flows through REST adapters with the user's credential.

## Non-goals

- In-app editor / IDE, pushing commits, applying suggestions.
- Bitbucket Data Center / Server.
- Merging, closing or re-requesting reviewers.
- Reading or replying to existing review threads (v1 shows PR state, not the conversation).
- A "Checks" tab.
- The workspace's Code selection content (Editor has its own spec).
- Auto-posting of any kind.
- Provider access through MCP. MCP is allowed only as extra context for the AI reviewer (e.g. a Jira ticket).

## Decisions (fixed by the user)

| # | Decision |
|---|---|
| D1 | Providers: GitHub (incl. GHE via base URL), GitLab (incl. self-managed via base URL), Bitbucket Cloud. No Bitbucket DC in v1 |
| D2 | Data via per-provider REST adapters with the user's credential. Not MCP |
| D3 | Auth: existing sealed token connections, plus borrowed `gh`/`glab` CLI tokens (never stored, account pinned). In-app "Login with gh/glab". Bitbucket = email + API token only. Scope check + expiry warning |
| D4 | (Revised 2026-11-19, v2 workspace) Global top pill nav: Home · Chats · Projects · Reviews · Knowledge. "Reviews" is the cross-project inbox; "Factory" leaves the nav (work orders never span projects); global items never appear in a left sidebar. Inside a project there is **one workspace page, no horizontal tabs**: left **project rail** (Factory · Pull requests · Code sections), center **content** for the rail selection, right **context panel** of floating cards (shown by default, "Hide panel / Show panel" in the content header). The Code selection is the Editor (separate spec). Shell defined in §UI › Project workspace |
| D5 | PR list: open PRs where user is author OR requested reviewer; filters Related to me (default) / Reviewer: me / Author: me; factory PRs count as authored by the user |
| D6 | (Revised 2026-11-19) Pull request selection: center = PR header + diff card with inline drafts; right panel = **Changes** card (file tree) and **Review** card (summary, verdict, post), as described in §UI |
| D7 | Never auto-post; only accepted drafts are sent; confirm dialog names account, PR, count, verdict, visibility |
| D8 | AI reviewer runs on the user's own agent account/subscription |

## Proposed decisions (need a yes; see Open questions)

| # | Decision | Why | Rejected alternative |
|---|---|---|---|
| P1 | Review PR always needs a **personal identity** (personal connection or CLI login). (Since #91 shared connections no longer exist, so this is the only path) | "Me" (author/reviewer filters) and "Posting as" must be the user; a shared token is an admin's or bot's account | Shared connection for reads + personal for posting: two identities per page, filters computed for the wrong account |
| P2 | v1 anchors are single-line (path + side + line). Multi-line ranges later | Ranges map differently on all three hosts (see §Line mapping) | Ranges in v1 |
| P3 | Posting requires the PR head to equal the head the drafts were made on; otherwise 409 and the user re-runs or refreshes | Avoids comments landing on moved lines | Posting anyway against an old commit (GitHub marks them outdated; GitLab/Bitbucket behaviour differs) |
| P4 | One Inspector run per review, with the routed specialties as a focus list, not one run per specialty | Cost: the factory fans out per specialty; a PR review is interactive and paid by the user | Factory-style fan-out |
| P5 | Review runs run in the Reviews usecase as background goroutines (like chat), not scheduler jobs | `jobs.wo_id` and `runs.work_order_id` are FKs to `work_orders` | Making jobs/runs polymorphic (bigger migration, touches the factory) |

## Flows

### F1 Connect

1. Settings › Connections › Code hosting shows, per provider: token connections (existing) and, for GitHub/GitLab, a "CLI" row: `gh`/`glab` detected? logged in as whom, on which hosts.
2. Each identity shows **Review readiness**: `enough to review` | `read only (cannot post)` | `unknown (fine-grained token, write not probed)` | `expired` / `expires in N days` (warn ≤ 7 days).
3. "Login with gh/glab": the backend runs the CLI's web/device login and streams the one-time code + URL into a dialog. If the code cannot be parsed within 10 s, the dialog switches to: "Run `gh auth login -h <host>` in a terminal, then Refresh".
4. First use of a CLI account on a host **pins** it (`review_identities`). If `gh auth switch` later changes the active account, Legos still asks for the pinned user (`--user`); if that user is gone, the identity is `unavailable` until the user re-pins.

### F2 Find PRs

- Projects (nav) → `/projects`: one card per project (name, repo, "N needs you" badge, WOs running, PRs waiting on the user, shipped in the last 30 days, last activity, review identity or "No review account · Connect" → Settings). Default sort "Needs attention first", alternative A–Z; search, Add project, New work order. Card → `/projects/:id` (the project workspace); the rail starts with "← All projects".
- Reviews (nav, kept as the cross-project inbox): all projects the user can see, grouped by project, same filters. Projects with no resolvable identity show one row "Connect an account to review <host>". A row opens `/projects/:id/pulls/:number` (the PR inside its project's workspace).
- Workspace rail › **Pull requests · N**: the "Related to me" list for this project, one line per PR with a short state label (§Rail). "All pull requests" at the section foot opens the full filterable list in the center (`/projects/:id/pulls`).
- Workspace rail › **Factory · N open**: open work orders only; selecting one shows the existing work-order view in the center. Review PR only adds a "Review PR #n" button on a work order whose PR is open; it selects that PR in the same workspace.

### F3 Review

1. Select a PR (rail, PR list, Reviews inbox, or "Review PR #n" on a WO) → the center loads header and diff, the right panel the Changes and Review cards; data from the provider, local state (drafts, viewed, last run) from the DB.
2. "Run AI review" (or automatically? — open question Q3) starts an Inspector run; drafts appear inline as they are stored at run end.
3. User accepts / edits / discards drafts (Undo on discard), marks files viewed, writes a summary, picks a verdict.
4. "Post review · N comments" → confirm dialog → post → per-draft result. Failed drafts stay accepted with the provider's error, retry posts only those.
5. When the PR head moves: banner "PR updated since this review"; drafts on changed files are marked `outdated`; action becomes Re-review.

## UI

### Navigation and routes

`AppShell.vue` top pill nav becomes Home · Chats · Projects · Reviews · Knowledge; the Factory attention dot moves to Projects. Global items never appear in a left sidebar. Outside a project the existing chat rail stays as is (its per-project "Open in Factory" link now points to `/projects/:id`); on every `/projects/:id…` route the **project rail** takes its slot (same width, same topbar show/hide toggle, `UsageCard` moves to the rail's foot).

Each rail selection is a path segment, so selections are deep-linkable and back/forward steps through them (`router.push` on selection; inner state such as the WO tab, diff mode or file scroll uses `router.replace`):

| Route | Center | Right panel |
|---|---|---|
| `/reviews` | cross-project inbox (unchanged) | — |
| `/projects` | project list, cards sorted "Needs attention first" (unchanged) | — |
| `/projects/:id` | Factory overview: all work orders of the project, filter chips **Open** (default) · **Done** · All (`?filter=done|all`), "New work order" | Details (repo, default branch, review identity "Posting as …" or "Connect", project checks) |
| `/projects/:id/wo/:wo[?tab=]` | existing work-order view (`WorkOrderView.vue`; its `?tab=` handling unchanged) | Crew, Details |
| `/projects/:id/pulls[?filter=]` | full PR list for the project (§PR list row), filter chips | — (panel hidden, toggle disabled) |
| `/projects/:id/pulls/:number[?file=<path>]` | PR header + diff card | Changes, Review |
| `/projects/:id/code[?file=&line=]`, `/projects/:id/code/:folder_id[?file=&line=]` | editor card (Editor spec) | Explorer, Problems, Language server (Editor spec) |

Redirects (client-side, `replace`): `/factory` → `/projects`; `/factory?project=X` → `/projects/X`; `/factory/:wo` → `/projects/<wo.project_id>/wo/:wo` (resolved via the existing `GET /work-orders/:id`; not found → `/projects`); hub URLs from the earlier draft `/projects/:id?tab=factory|pull-requests|code` → `/projects/:id`, `/projects/:id/pulls`, `/projects/:id/code`. A WO id that belongs to another project redirects to its own project.

### Project workspace

One page component (`ProjectWorkspace.vue`) with three regions:

```
┌ top pill nav: Home · Chats · Projects · Reviews · Knowledge ──────────────────────────────┐
├ project rail ─────────┬ content (cards, 40px gutters) ─────────────┬ context panel ───────┤
│ ← All projects        │ header: title · actions · [Hide panel]      │ ┌ card ─────────────┐│
│ legos-factory         │                                             │ │ Crew / Changes /  ││
│ github.com/o/legos    │  ┌ card ─────────────────────────────────┐  │ │ Explorer          ││
│ FACTORY · 3 open   +  │  │ work order / diff / editor            │  │ └───────────────────┘│
│ ● Add login  you      │  │                                       │  │ ┌ card ─────────────┐│
│ ● Fix flaky test      │  └───────────────────────────────────────┘  │ │ Details / Review /││
│ PULL REQUESTS · 2     │                                             │ │ Problems          ││
│ #42 Retry jobs  AI·3  │                                             │ └───────────────────┘│
│ #40 Bump deps   yours │                                             │                      │
│ CODE · 2 open         │                                             │                      │
│ server.go  M          │                                             │                      │
│ [Usage card]          │                                             │                      │
└───────────────────────┴─────────────────────────────────────────────┴──────────────────────┘
```

| Region | Behaviour |
|---|---|
| Style | legos baseline: Emerald tokens, Outfit headings, content cards with 40px gutters; panel cards float (own radius/shadow, no shared border with the content) |
| Rail head | "← All projects" (`/projects`), project name + repo (`host/owner/repo`, or the folder name for local projects), project attention dot |
| Rail › **Factory · N open** | N = open work orders (not `CANCELLED` and not shipped, i.e. `pr_merged_at` unset). One row per open WO: status dot (running / waiting on you / failed or blocked / queued, status colours from tokens), title (one line, ellipsis). The WO waiting on the user (`WAITING_FOR_HUMAN`, `NEEDS_HUMAN_REVIEW`, `BLOCKED`, or a pending escalation answer) carries a "you" chip and sorts first; then by last activity. "+" opens the existing `NewWorkOrderDrawer` with the project preselected. The section title opens the Factory overview (`/projects/:id`), where the **Done** filter lists finished work orders |
| Rail › **Pull requests · N** | N = open PRs where the user is author or requested reviewer ("Related to me", D5). One row: `#n` + title + state label, first match: `AI · n` (n pending drafts; `AI · …` while a run is running) → `yours` (user is author, incl. factory PRs) → `new` (requested reviewer, no `pr_reviews` row yet, i.e. never opened in Legos) → `approve` (requested reviewer, no posted review at current head) → no label, row muted (posted at current head). No identity → one row "Connect an account" (→ Settings › Connections), no count. Rate limited / error → last cached rows + a warning dot with the reason on hover. Section foot "All pull requests" → `/projects/:id/pulls` |
| Rail › **Code · N open** | defined in the Editor spec (files open in the editor; click switches to that file; `M` = modified/unsaved). Section title opens `/projects/:id/code` |
| Rail foot | `UsageCard` (existing component, moved from the chat rail) |
| Content header | title of the selection, its actions, and "Hide panel / Show panel" (right-aligned). The panel is shown by default; the choice is remembered per user in `localStorage` (`legos-project-panel`), not in the URL |
| Context panel | fixed width (proposal 320px), cards stack vertically and scroll independently of the content; a card collapses to its title bar (state per card in `localStorage`) |
| Narrow screens | proposal: below 1280px the panel starts hidden (toggle still works, opens as an overlay); below 1000px the rail behaves like today's chat rail (overlay with scrim). Q13 |
| Keyboard | Rail is a `nav` landmark ("Project"); rows are links (Enter opens, Cmd+click opens a new tab); `[` / `]` move to the previous / next rail row in the same section ? decide in the shell PR |

Live updates of the rail: WO rows and the Factory count update from the existing global SSE stream (`/api/v1/events`, WO-keyed events for WOs of this project trigger a debounced 1 s refetch of the rail's WO part). PR rows update on `pr_review.*` events for this user (draft counts, running state) and refetch the PR part every 60 s while the page is visible and on window focus; the PR part is served from the same cache as the PR list (stale-while-revalidate), so a refetch never blocks. The Code section is client state (Editor spec).

### Work order selection

- Center: the existing work-order view unchanged in content — stage track Brief / Plan / Build / Review / Ship, "Now" card with the current actor and, when escalated, the answer box; tabs Activity / Progress / Review (the Changes & PR tab was removed in #91: the PR opens automatically after the final gate and the escalation decisions live on the Now card). Header actions add **Review PR #n** when the WO's PR is open on the host (→ `/projects/:id/pulls/:n`).
- Right panel: **Crew** (Foreman / Architect / Builder / Inspector, `--crew-<role>` colours, `active` while a run of that role is running, else `idle`) and **Details** (branch, started, "Open in Code" → the WO worktree in the same workspace, Editor spec F5).
- The Crew and Details data come from the WO payload and its runs already loaded by the view; no new endpoint.

### PR list row

| Field | Source |
|---|---|
| Title, #, author (avatar/login), `branch → base` | provider |
| CI | provider (§CI) — `passing` / `failing` / `running` / `none` |
| `+A −D · N files` | provider; GitHub/GitLab/Bitbucket list calls do not return stats, so one detail call per PR, cached by head SHA |
| Your status | derived, first match: `Posted · <verdict>` (posted at current head) → `AI draft ready · N` (N pending drafts) → `AI reviewing…` → `Needs your approval` (user is requested reviewer, no posted review at current head) → `Not reviewed` |
| Action | `Re-review` (posted at an older head) · `Open` (posted at current head) · `Review` (otherwise) |

Filter chips: Related to me (default) / Reviewer: me / Author: me. Empty, no-identity, rate-limited and error states each have one line + action.

### Pull request selection

- **Center — header card:** title, #, author, `head → base`, CI, `+A −D`, "Posting as `<login>` · `<provider host>`" (click → identity popover with source: connection name or `gh`/`glab`), "Open in <provider>", "Run AI review" / "Re-review".
- **Center — diff card:** Unified / Split toggle. Reuse `frontend/src/features/factory/reviews/diff.ts` (`parseUnifiedDiff`), `DiffView.vue`, `DiffFileView.vue`; the backend returns per-file unified text so the parser is unchanged. New: an inline slot under a line for drafts. A draft card: severity chip, title, body (markdown), Accept / Edit / Discard; discarded collapses to "Discarded · Undo". When the panel is hidden, the content header shows "Show panel · N to decide" (N = pending drafts).
- **Right — Changes card:** nested, collapsible folders; single-child folder chains compacted (`internal/adapter`); icon modified / added / deleted / renamed; `+N −N` badge; comment-count badge (drafts on that file, not discarded); **bold = not yet viewed**; card header counter `x / N viewed`. Click → scroll the diff to that file (`?file=` replaced). Viewed is per user, per file, reset when the file's patch changes (hash).
- **Right — Review card:** AI summary + severity counts (blocker/major/minor/info) + Re-run; draft list (click → scroll to line); drafts not anchorable inline listed as "General"; summary comment textarea (prefilled from AI summary? — Q4); verdict Comment / Approve / Request changes (options the provider/role cannot do are disabled with a reason, §Verdicts); button "Post review · N comments" (N = accepted). Hiding the panel never discards decisions; the post button lives only here, so posting needs the panel shown.
- **Confirm dialog:** "Post as `<login>` on `<provider>` to `<repo>#<n>`: N inline comments, summary, verdict `<v>`. Everyone with access to this PR will see it under your name." Buttons Cancel / Post.

## Backend

### Layout

- `internal/integration/` — REST adapters (github.go, gitlab.go, bitbucket.go extended; one new file each for review calls if the files grow past ~300 lines). CLI token borrowing in `ghcli.go` + new `glabcli.go`.
- `internal/usecase/prreview.go` — `Reviews` usecase: identity resolution, list/status derivation, rail summary (`Rail(ctx, userID, projectID, part)`: open-WO rule, "you" rule, PR label derivation), drafts, viewed, posting rules, AI run. All rules live here (team convention), not in store or integration.
- `internal/store/prreview.go` + migration `031_pr_reviews.sql`.
- `internal/http/reviews.go` — handlers.

### Interface additions (`usecase/ports.go`)

`GitProvider` stays as is. A sibling interface, implemented by the same three REST structs:

```go
// PRReviewer reads pull requests and posts reviews as the credential's owner.
type PRReviewer interface {
	Me(ctx context.Context) (model.HostUser, error)
	TokenInfo(ctx context.Context) (model.TokenInfo, error)
	ListPullRequests(ctx context.Context, repo string, f model.PRFilter) ([]model.PRSummary, error)
	GetPullRequest(ctx context.Context, repo string, number int) (model.PRDetail, error)
	PullRequestFiles(ctx context.Context, repo string, number int) ([]model.PRFile, error)
	PostReview(ctx context.Context, repo string, number int, r model.ReviewSubmission) (model.PostedReview, error)
}

type ReviewStore interface {
	Upsert(ctx context.Context, r *model.PRReview) error
	Get(ctx context.Context, userID, provider, host, repo string, number int) (*model.PRReview, error)
	ListForUser(ctx context.Context, userID string, keys []model.PRKey) ([]model.PRReview, error)
	SetDecision(ctx context.Context, id, summary, verdict string) error
	MarkPosted(ctx context.Context, id, headSHA, verdict, ref string, at time.Time) error
	StartRun(ctx context.Context, run model.PRReviewRun) error
	FinishRun(ctx context.Context, id string, res model.AgentResult, summary, errMsg string) error
	FailRunning(ctx context.Context, reason string) (int, error) // boot recovery
	AddDrafts(ctx context.Context, reviewID, runID string, ds []model.PRDraft) error
	Drafts(ctx context.Context, reviewID string) ([]model.PRDraft, error)
	UpdateDraft(ctx context.Context, reviewID, id string, status, body *string) error
	SupersedePending(ctx context.Context, reviewID, keepRunID string) error
	MarkOutdated(ctx context.Context, reviewID string, paths []string) error
	SetPosted(ctx context.Context, draftID, ref, errMsg string) error
	SetViewed(ctx context.Context, reviewID, path, patchHash string, viewed bool) error
	Viewed(ctx context.Context, reviewID string) (map[string]string, error) // path → patch hash
	Identity(ctx context.Context, userID, provider, host string) (*model.ReviewIdentity, error)
	PinIdentity(ctx context.Context, id model.ReviewIdentity) error
}

// CLIAuth borrows a token from an installed CLI; the token is never stored.
type CLIAuth interface {
	Status(ctx context.Context, host string) ([]string, error)          // logged-in users on host
	Token(ctx context.Context, host, user string) (string, error)       // per request
	StartLogin(ctx context.Context, host string) (<-chan model.CLILoginStep, error)
}
```

Model types (`model/prreview.go`, abridged):

```go
type HostUser struct{ Login, ID string }                 // ID: GitHub numeric id, GitLab id, Bitbucket uuid
type TokenInfo struct {
	Scopes    []string
	CanRead   bool
	CanWrite  model.Tri // yes | no | unknown
	ExpiresAt *time.Time
}
type PRFilter struct{ Author, Reviewer HostUser }        // either or both; open PRs only
type PRSummary struct {
	Number int; Title, URL string; Author HostUser
	Head, Base, HeadSHA string; Draft bool
	RequestedReviewers []HostUser; UpdatedAt time.Time
}
type PRDetail struct {
	PRSummary
	BaseSHA, StartSHA string                              // GitLab diff_refs need all three
	Body string; Additions, Deletions, ChangedFiles int
	CI string                                             // passing|failing|running|none
	MyLatestReview *HostReview                            // user's last submitted review: verdict + commit
	SourceRepo string                                     // != repo for fork PRs
}
type PRFile struct {
	Path, OldPath, Status string                          // added|modified|deleted|renamed
	Additions, Deletions int
	Patch string                                          // unified text with ---/+++ header; "" when binary/too large
	Truncated bool
}
type ReviewSubmission struct {
	HeadSHA  string
	Body     string
	Verdict  string // comment|approve|request_changes
	Comments []InlineComment
}
type InlineComment struct {
	DraftID, Path, OldPath string
	Side     string // new|old
	Line     int
	OldLine  int    // context lines: both set (GitLab needs both)
	Body     string
	PostedRef string // set when already posted (Bitbucket retry)
}
type PostedReview struct {
	Ref      string
	Comments map[string]string // draft id → provider ref
	Failed   map[string]string // draft id → provider message
	VerdictErr string
}
```

### Credential resolution (usecase, per request)

Input: user, project. Output: `(PRReviewer, HostUser, source)` or a typed "no identity" error.

1. **Repo and host.** Every project is a local folder (#91): parse its `origin` the way `Integrations.GitFor` does — `github.com` → github; any other host → the user's own connection whose base URL host matches (`api.` prefix ignored).
2. **Pinned identity** for (user, provider, host) in `review_identities` → use it.
3. Else the user's **personal, active, verified** connection for that provider whose base URL host matches. More than one → the user picks once, then pinned.
4. Else **CLI** (`gh` for GitHub, `glab` for GitLab), only when the server runs in local/dev auth mode (same gate as `Integrations.GH` today: the CLI login belongs to the OS user running the backend, so a multi-user server must never lend it). One logged-in user → pin it on first use; several → user picks.
5. Else: `ErrNoReviewIdentity` → UI "Connect an account".
6. There is no shared connection since #91 (P1).

CLI borrowing: `gh auth token --hostname <host> --user <pinned>` (flags confirmed on gh 2.103.3). Each usecase operation borrows once and builds a REST client in memory; nothing is cached on disk or in the DB. After borrowing, `Me()` must equal the pinned login, else `ErrIdentityChanged`.
`glab`: **uncertain** — I could not verify a `glab auth token` equivalent or multi-account support; candidates are `glab config get token --host <host>` and parsing `glab auth status --show-token`, and newer glab may keep the token in the OS keyring. Verify against a current glab before building phase 8.

### Provider API mapping

Confidence marks: ✓ known and used widely · ? verify in the contract/live check.

#### List open PRs with author / reviewer filters

| | Call | Notes |
|---|---|---|
| GitHub | `GET /repos/{o}/{r}/pulls?state=open&per_page=100` (paginate `Link`), filter in usecase by `user.login` and `requested_reviewers[].login` ✓ | A reviewer who has already submitted a review leaves `requested_reviewers` ✓ → also include PRs where `MyLatestReview` exists (needs `GET /pulls/{n}/reviews` per PR; only for PRs not already matched). Team review requests (`requested_teams`) need team membership (`read:org`) — **not counted in v1** (Q6). Search API (`is:pr review-requested:@me`) avoided: 30 req/min limit |
| GitLab | `GET /projects/:id/merge_requests?state=opened&author_username=<u>` and `…&reviewer_username=<u>`, merged ✓? (`reviewer_username` exists on recent GitLab; minimum version on self-managed ?) | Approvals-required-from-me is not "reviewer"; v1 uses reviewers only |
| Bitbucket | `GET /repositories/{ws}/{repo}/pullrequests?state=OPEN&q=author.uuid="{u}"` and `q=reviewers.uuid="{u}"` ? | BBQL on `reviewers.uuid` — verify. Fallback: list all open and filter on `participants[role=REVIEWER]` (needs `fields=+values.participants` ?) |

"Author: me" also includes factory PRs: any listed PR whose ref matches `work_orders.pr_ref` of a WO with `created_by = user` (the factory may have opened it under a shared or gh account).

#### Detail, CI, files and diff

| | Detail / CI | Files + patch |
|---|---|---|
| GitHub | `GET /pulls/{n}` (additions, deletions, changed_files, head.sha, base.sha) ✓; CI = `GET /commits/{sha}/check-runs` + `GET /commits/{sha}/status` combined ✓ | `GET /pulls/{n}/files?per_page=100` ✓; `patch` is hunks only (we prepend `---/+++`), omitted for binary and very large files ✓; API caps at 3000 files ✓ |
| GitLab | `GET /merge_requests/:iid` (`diff_refs.{base,start,head}_sha`, `head_pipeline.status`, `changes_count` as string) ✓ | `GET /merge_requests/:iid/diffs` (paginated, newer) ? else `/changes` (deprecated) ✓; per-file `diff`, `new_file`, `deleted_file`, `renamed_file`; large diffs may be collapsed/`too_large` ? |
| Bitbucket | `GET /pullrequests/{id}` ✓; CI = `GET /pullrequests/{id}/statuses` ✓ | `GET /pullrequests/{id}/diffstat` (status, lines_added/removed, paginated) ✓ + `GET /pullrequests/{id}/diff` (one raw unified text, follows a redirect) ✓, split per file in the adapter |

Large PRs: files beyond 300 or patches beyond 1 MB total are listed with `Truncated` and no inline drafts (the AI gets the list only).

#### Line mapping (single line, P2)

Internal anchor: `{path, side: new|old, line}` where `line` is the new-file line for `new`, old-file line for `old`; context lines are `new` with `OldLine` also recorded. The adapter only accepts anchors that fall inside a hunk of that file's patch; others become "General" drafts (summary bullet) — the usecase decides this before calling the adapter.

| | Inline comment | Constraint |
|---|---|---|
| GitHub | in the single review call: `comments[] {path, line, side: RIGHT|LEFT, body}` + `commit_id` ✓ | Line outside the diff → whole request 422 ✓ — so pre-validation is mandatory |
| GitLab | draft note: `POST /merge_requests/:iid/draft_notes` with `position {position_type: text, base_sha, start_sha, head_sha, old_path, new_path, new_line | old_line | both for context}` ✓?, then `POST /draft_notes/bulk_publish` ? | Context lines need both lines ✓; draft notes API tier/version availability ? Fallback: `POST /discussions` per comment (visible immediately, non-atomic) |
| Bitbucket | `POST /pullrequests/{id}/comments {content.raw, inline {path, to: <new line>}}` or `from: <old line>` ✓ | Posted one by one, visible at once; no batched review via API ? |

#### Verdicts

| Verdict | GitHub | GitLab | Bitbucket Cloud |
|---|---|---|---|
| Comment | `event: COMMENT` ✓ | summary as a note / in bulk publish ✓ | summary as a PR comment ✓ |
| Approve | `event: APPROVE` ✓; not on own PR (422) ✓ | `POST /merge_requests/:iid/approve` ✓ (available on Free ?); author self-approval depends on project setting ✓ | `POST /pullrequests/{id}/approve` ✓; own PR ? |
| Request changes | `event: REQUEST_CHANGES` ✓; not on own PR ✓; body required ✓ | **No reliable API equivalent across tiers/versions.** Newer GitLab has a "request changes" review state in the UI; whether `bulk_publish` or another endpoint sets it via REST, and from which version/tier, is **unverified**. v1 fallback: disable with reason "not supported on this GitLab", unless the contract check proves an endpoint (Q5) | `POST /pullrequests/{id}/request-changes` ? (I believe it exists in Cloud; verify) |

The usecase disables Approve / Request changes when the user is the PR author (all providers) and when the adapter reports unsupported.

#### Post sequence and idempotency

- GitHub: one `POST /pulls/{n}/reviews {commit_id: HeadSHA, body, event, comments}` ✓ — atomic.
- GitLab: create draft notes; on any failure delete the ones created (`DELETE /draft_notes/:id` ?) and fail the whole post; then bulk publish; then approve if chosen. Summary is a draft note too.
- Bitbucket: inline comments one by one, recording `posted_ref` per draft as each succeeds; then summary comment; then verdict. A retry skips drafts with a `posted_ref`. Partial success is reported per draft.
- All: before posting, the usecase re-reads `HeadSHA` (P3) and the identity's `Me()`.

### Persistence (`031_pr_reviews.sql`)

```sql
CREATE TABLE review_identities (           -- pin: which account posts for this user on this host
    user_id TEXT NOT NULL, provider TEXT NOT NULL, host TEXT NOT NULL,
    source TEXT NOT NULL,                  -- connection | gh | glab
    connection_id TEXT NOT NULL DEFAULT '',
    login TEXT NOT NULL, host_user_id TEXT NOT NULL DEFAULT '',
    pinned_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, provider, host)
);
CREATE TABLE pr_reviews (
    id TEXT PRIMARY KEY, user_id TEXT NOT NULL, project_id TEXT NOT NULL,
    provider TEXT NOT NULL, host TEXT NOT NULL, repo TEXT NOT NULL, number INTEGER NOT NULL,
    head_sha TEXT NOT NULL DEFAULT '',     -- head the current drafts were made on
    summary TEXT NOT NULL DEFAULT '', verdict TEXT NOT NULL DEFAULT 'comment',
    posted_at INTEGER, posted_head_sha TEXT NOT NULL DEFAULT '', posted_verdict TEXT NOT NULL DEFAULT '',
    posted_ref TEXT NOT NULL DEFAULT '', posted_as TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
    UNIQUE (user_id, provider, host, repo, number)
);
CREATE TABLE pr_review_runs (
    id TEXT PRIMARY KEY, review_id TEXT NOT NULL REFERENCES pr_reviews(id) ON DELETE CASCADE,
    head_sha TEXT NOT NULL, state TEXT NOT NULL,      -- running|completed|failed
    agent TEXT NOT NULL, harness TEXT NOT NULL, model TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
    tokens_in INTEGER NOT NULL DEFAULT 0, tokens_out INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_micros INTEGER NOT NULL DEFAULT 0,
    started_at INTEGER NOT NULL, finished_at INTEGER
);
CREATE TABLE pr_review_drafts (
    id TEXT PRIMARY KEY, review_id TEXT NOT NULL REFERENCES pr_reviews(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL, old_path TEXT NOT NULL DEFAULT '',
    side TEXT NOT NULL DEFAULT 'new', line INTEGER NOT NULL DEFAULT 0, old_line INTEGER NOT NULL DEFAULT 0,
    inline INTEGER NOT NULL DEFAULT 1,     -- 0 = General (not anchorable)
    head_sha TEXT NOT NULL, severity TEXT NOT NULL, title TEXT NOT NULL,
    body TEXT NOT NULL, ai_body TEXT NOT NULL,       -- ai_body kept to show "edited"
    status TEXT NOT NULL,                  -- pending|accepted|discarded|superseded|outdated|posted
    posted_ref TEXT NOT NULL DEFAULT '', post_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX idx_pr_review_drafts_review ON pr_review_drafts(review_id, status);
CREATE TABLE pr_review_viewed (
    review_id TEXT NOT NULL REFERENCES pr_reviews(id) ON DELETE CASCADE,
    path TEXT NOT NULL, patch_hash TEXT NOT NULL, viewed_at INTEGER NOT NULL,
    PRIMARY KEY (review_id, path)
);
```

Rules (usecase): a file is viewed iff a row exists and `patch_hash` = sha256 of its current patch. Draft transitions: `pending → accepted|discarded`, `discarded → pending` (Undo), `accepted ↔ pending` (un-accept), `accepted → posted`; a Re-run sets earlier `pending` to `superseded` and keeps `accepted`/`discarded`. On head change, drafts not yet posted on files whose patch hash changed become `outdated` (user may re-open them as pending only after Re-run). Provider data (PR list, files) is not persisted; in-memory cache 60 s per (identity, repo, filter), detail/files keyed by head SHA.

### AI review (Inspector)

Trigger: `POST …/review/runs`. One running run per review (409 otherwise). Runs in a goroutine owned by `Reviews` (P5); at boot `FailRunning("interrupted by restart")`.

Agent and account (D8): role `model.AgentInspector`, harness/model resolved for the user — `Agents.RunEnv(ctx, agent, userID, model.AgentInspector)` already takes a user id; `Factory.Resolve` and `Factory.RunEnv` take a `*model.WorkOrder` and need a user-scoped variant (`resolveForUser(role, userID)`). Usage recorded via `UsageStore.Insert` with `UserID`, `Role: "inspector"`, empty `WOID`; `Usage.Reserve` applies as for factory runs.

Workspace: a read-only checkout `review-<project id>-<number>` (via `Sandbox.Prepare` with the identity's `GitAuth`), fetched to the PR head and detached: GitHub `refs/pull/N/head` ✓, GitLab `refs/merge-requests/N/head` ✓, Bitbucket has no such ref ? → fetch the source branch, and for fork PRs the source repo (Q7). Tools: Read/Grep/Glob only with the secret deny rules from `docs/chat-repo/PLAN.md` (`RepoAccess`-style meta), no shell, no git token in the agent's env. Removed when the PR is no longer open at the next list refresh, or after 7 days unused (Q8).

Context given to the agent:

| Item | Source | Cap |
|---|---|---|
| PR title, body, `head → base`, author | provider | body 4K chars |
| Changed files + per-file patches | provider | file list 300; patches 200K chars, rest listed only |
| Focus list | `SelectSpecialists(files)` / the project's Reviewer Routing doc (`reviewers()` logic, refactored to take a project id + files instead of a WO) | — |
| Project knowledge | `KB.Context(projectID, "inspector", …)` | existing budget |
| Linked issue | key matched by `IssueKeyPattern` in title/branch → user's Jira connection via `IssueTracker.GetIssue` (REST); MCP servers the user enabled for the Inspector may add more | 1 issue |
| Earlier decisions | titles of accepted/discarded drafts of this review, so they are not repeated (mirrors `earlierFindingTitles`) | 30 |

The PR body, diff and issue are **untrusted input**; the prompt says so, and nothing the agent returns is ever posted without a human accept.

Reply (validated like `findingsReply`, one retry on invalid JSON):

```json
{"summary":"≤120 words",
 "findings":[{"severity":"blocker|major|minor|info","path":"internal/x.go","side":"new|old","line":42,
              "title":"≤80 chars","body":"the comment as it would be posted, markdown"}]}
```

Max 20 findings, sorted by severity. The usecase validates `path` ∈ changed files and `line` inside a hunk of that side; otherwise the draft is stored with `inline=0` (General). Drafts are stored at run end (no streaming of partial drafts in v1); run progress is published as live events.

### HTTP API (`/api/v1`)

| Method | Path | Body / query | Returns |
|---|---|---|---|
| GET | `/reviews` | `filter=related|reviewer|author` | `[{project, identity, rows: [PRRow]}]` or per-project error |
| GET | `/projects/summary` | — | `[{project_id, needs_you, wo_running, prs_for_me, shipped_30d, last_activity, identity}]` for the Projects list; `prs_for_me` from the same cached list as the PR tab (stale-while-revalidate, never blocks the page) |
| GET | `/projects/:id/rail` | `part=all|wo|pulls` (default all) | `{project: {id, name, repo, needs_you}, work_orders: {open: N, rows: [{id, title, state, stage, waiting_on_you, updated_at}]}, pulls: {identity, count, rows: [{number, title, label: ai|yours|new|approve|none, pending_drafts, running}], stale, error?}}` — one call for the rail (§Project workspace). WO part from the store (open = not cancelled, `pr_merged_at` unset); PR part from the PR list cache, never blocks on the provider (`stale: true` while revalidating, `error` = `no_identity|rate_limited{until}|provider`) |
| GET | `/projects/:id/pull-requests` | `filter` | `{identity, rows: [PRRow]}` |
| GET | `/projects/:id/pull-requests/:number` | — | `{pr: PRDetail, identity, review, run, verdicts: {approve, request_changes: {ok, reason}}}` |
| GET | `/projects/:id/pull-requests/:number/files` | — | `[{path, old_path, status, additions, deletions, patch, truncated, viewed}]` |
| POST | `/projects/:id/pull-requests/:number/review/runs` | — | run (202) |
| GET | `/projects/:id/pull-requests/:number/review/drafts` | — | drafts |
| PATCH | `/projects/:id/pull-requests/:number/review/drafts/:draft` | `{status?, body?}` | draft |
| PUT | `/projects/:id/pull-requests/:number/review/viewed` | `{path, viewed}` | `{viewed, total}` |
| PATCH | `/projects/:id/pull-requests/:number/review` | `{summary?, verdict?}` | review |
| POST | `/projects/:id/pull-requests/:number/review/post` | `{confirm: true, expect: {login, head_sha, comments, verdict}}` | `PostedReview` per draft; 409 when `expect` differs from the server's view |
| GET | `/review-identities` | — | per provider/host: connections, CLI users, pinned, readiness, expiry |
| PUT | `/review-identities` | `{provider, host, source, connection_id?, login?}` | pinned identity |
| POST | `/review-identities/cli/login` | `{tool: gh|glab, host}` | `{login_id}`; steps via live events: `code`, `url`, `done`, `failed`, `fallback` |

The work-order selection and the Factory overview reuse the existing Factory views and endpoints (`GET /work-orders?project_id=`, `GET /work-orders/:id`); only the rail summary is new. Live events (`pr_review.run_started|run_finished|run_failed`, `cli_login.*`) go over the existing SSE stream; `LiveEvent` has only `WOID` today, so it needs a `UserID` and the stream must deliver these only to that user.

All handlers: project membership via `Projects.Authorize`; the review row must belong to the caller.

### Errors and rate limits

- `client.go`: `ProviderError` gains `RetryAfter time.Duration`, filled from `Retry-After`, GitHub `X-RateLimit-Remaining: 0` + `X-RateLimit-Reset` ✓, GitLab `RateLimit-Reset` ✓; 429 and GitHub's 403-with-rate-limit message are classified as rate limits (not permanent).
- The usecase never retries a POST automatically (posting is user-initiated); GETs retry once after `RetryAfter` ≤ 10 s, else return `rate_limited{until}` to the UI.
- GitHub conditional requests with `ETag` / `If-None-Match` for list and detail (a 304 does not count against the primary limit ✓).
- List fan-out (detail per PR for stats/CI) is capped at 50 PRs per list and runs with concurrency 4.
- Error → UI mapping: 401 → identity `invalid` ("token rejected"); 403 → `insufficient scope` with the scope needed; 404 → repo/PR not found or no access; 409 (ours) → head moved / expectation mismatch; 422 on post → per-draft error where the provider identifies it, else whole post failed with the provider message.
- `ErrIdentityChanged`, `ErrGHMissing`, `ErrGHUnauthenticated` reuse/extend the existing model errors.

### Scope and expiry checks (`TokenInfo`)

| | Scopes | Write | Expiry |
|---|---|---|---|
| GitHub | `X-OAuth-Scopes` on `GET /user` for classic and gh OAuth tokens ✓ (`repo`, or `public_repo` for public repos only) | classic/OAuth: from scopes; fine-grained: header absent ✓ → `unknown` (read probed with `GET /repos/{o}/{r}`; needs Pull requests: write to post) | `github-authentication-token-expiration` header ✓ |
| GitLab | `GET /personal_access_tokens/self` → `scopes`, `expires_at` ✓ (PATs; OAuth tokens from glab → 4xx ? → `unknown`) | `api` ⇒ yes; `read_api` only ⇒ no | `expires_at` |
| Bitbucket | no introspection endpoint known ? | probe read with `GET /repositories/{ws}/{repo}`; write `unknown` (needs pull-request write scope) | not readable via API ? → not shown |

## Security

- Posts go out only from `POST …/review/post` with `confirm: true` and a matching `expect` block; no code path posts from an agent run, a job, or a retry loop.
- Only `accepted` drafts are sent; the server re-reads statuses at post time, ignoring the client's list.
- Identity is pinned; every post re-checks `Me()` against the pin.
- CLI tokens: borrowed per operation, kept in memory only, never written to the DB, logs, events, error messages or agent env. CLI borrowing is off in multi-user auth mode.
- Connection tokens: unchanged sealing (`Sealer`); `errorMessage` never echoes request headers (existing test `TestProviderErrorsArePermanentOnAuthFailure` extended to the new calls).
- Base URLs keep `ValidateBaseURL` (https, no embedded credentials).
- The AI reviewer: read-only tools, secret-path deny rules, no network beyond its model and the MCP servers the user enabled for the Inspector; PR content treated as untrusted in the prompt.
- `exec` of `gh`/`glab` uses argv (no shell); host and user values validated (`^[A-Za-z0-9.-]+$`, login charset) before use.
- Least scope documented in Settings help text per provider.

## Test plan

Integration (extend `contract_test.go` `fakeAPI`, one test per provider):

- `ListPullRequests`: exact query strings (author/reviewer filters, pagination via `Link` / GitLab `X-Next-Page` / Bitbucket `next`), merge + de-dup of the two lists.
- `GetPullRequest`/`PullRequestFiles`: patch header normalisation, renamed/added/deleted/binary, truncation, GitLab `diff_refs`.
- `PostReview`: GitHub single body (`commit_id`, `event`, `comments[].{path,line,side}`); GitLab draft-note `position` for added / removed / context lines, cleanup on failure, `bulk_publish`, `approve`; Bitbucket `inline.to` / `inline.from`, per-comment refs, verdict endpoint last, retry skips posted drafts.
- `TokenInfo`: scopes/expiry headers and `/personal_access_tokens/self`.
- Errors: 401/403/404/422 mapping, 429 + `Retry-After` → `RetryAfter`, token never in `err.Error()`.
- `ghcli_test.go` pattern (fake `Runner`) for `gh auth token --hostname --user`, `gh auth status` parsing, login output parsing incl. the fallback when the code line is absent; same for glab once its commands are verified.

Usecase (`prreview_test.go`, fakes for `PRReviewer`, `ReviewStore`, `CLIAuth`, harness):

- Resolution order table (pinned > personal connection > CLI > none; shared never used; CLI off in multi-user mode; pinned CLI user missing → error; `Me()` mismatch → `ErrIdentityChanged`).
- Status/action derivation table for list rows, incl. factory PRs counted as authored.
- Rail table: open-WO rule (cancelled and merged excluded, ready-with-open-PR included), "you" rule and ordering, PR label precedence (`ai` → `yours` → `new` → `approve` → `none`), no identity / rate-limited parts never fail the WO part.
- Draft rules: transitions, Re-run supersedes pending only, outdated on patch-hash change, General drafts for out-of-diff lines.
- Posting: only accepted sent; head moved → 409; `expect` mismatch → 409; own PR → approve/request changes refused; partial Bitbucket failure then retry.
- AI run: reply validation + one retry, path/line validation, caps, usage recorded with user id, boot recovery.

Frontend (vitest): route table + redirects (`/factory`, `/factory?project=`, `/factory/:wo`, old `?tab=` URLs, WO of another project), back/forward across selections, rail sections and counts from a fixture, panel shown by default + Hide/Show remembered, "Show panel · N to decide" when hidden, tree compaction of single-child chains, viewed counter + bold, status label mapping, confirm dialog text.

Live check (manual, sandbox repo per provider, own account): list filters, inline comment on added/removed/context line, each verdict, rate-limit banner, `gh auth switch` with a pinned account.

## Delivery plan (small PRs, in order)

| # | PR | Depends on |
|---|---|---|
| 1 | Model types + `PRReviewer` read side (Me, TokenInfo, List, Get, Files) for GitHub/GitLab/Bitbucket + contract tests; `ProviderError.RetryAfter` | — |
| 2 | Migration 031 + `ReviewStore` + store tests | — |
| 3 | `Reviews` usecase: identity resolution (connections + gh), pinning, list/status derivation, files + viewed; HTTP list/detail/files/viewed/identities | 1, 2 |
| 4a | Frontend workspace shell: nav change (Projects replaces Factory, Reviews added), `/projects` list page, `ProjectWorkspace.vue` (rail + content + context panel framework with Hide/Show, card slots per selection), Factory overview with Open/Done filter, WO selection (existing view moved in) + Crew/Details cards + "Open in Code" placeholder, route table + all redirects. Backend `GET /projects/:id/rail` WO part (PR part returns `no_identity` until 4b). Code section and Code route are placeholders | 3 |
| 4b | Rail PR section + labels, `/projects/:id/pulls` list, "Review PR #n" on WOs, live refresh of rail counts | 3, 4a |
| 5 | Frontend PR selection: header card, diff card, Changes card (tree, viewed) | 3, 4a |
| 6 | AI review run: user-scoped Inspector resolution, review checkout, prompt/reply, drafts API + live events; frontend inline drafts + Review card | 3, 5 |
| 7 | `PostReview` per provider + contract tests; post endpoint + confirm dialog + per-draft results | 6 |
| 8 | Settings: readiness/expiry display, CLI detection rows, in-app gh login with fallback; glab after its commands are verified | 3 |

## Open questions

1. ~~Project page~~ — resolved 2026-11-19, revised the same day: `/projects/:id` is the v2 project workspace (rail + content + context panel, no tabs; see D4 and §Project workspace); the chat rail's project link moves to it.
2. ~~Local projects on non-standard hosts~~ — resolved by #91: matched against the user's connections' base URL hosts (`Integrations.GitFor`).
3. **AI run trigger.** Does opening a PR start the AI review automatically, or only on "Run AI review"? (Cost is on the user's subscription.)
4. **Summary comment.** Prefilled from the AI summary, or empty?
5. **GitLab "Request changes".** Hide it, or fall back to a comment prefixed "Changes requested" + unapprove? Needs the verified API behaviour per GitLab version/tier first.
6. **Team review requests** (GitHub `requested_teams`, GitLab group reviewers): count as "Reviewer: me" in v1?
7. **Fork PRs** on Bitbucket (no PR head ref): support in v1 or show "AI review unavailable for fork PRs"?
8. **Review checkout lifetime** and disk budget: delete when the PR closes, after N days, or keep one per project?
9. **Manual comments.** Can the user add their own inline comment (not from AI) in v1? The decisions only cover AI drafts.
10. **P1** (settled by #91: no shared connections) and P2–P5 above need a yes.
11. **Draft PRs** (GitHub/GitLab draft, Bitbucket draft): listed, hidden, or listed with a badge?
12. **Unverified external behaviour** to settle in phase 1/7 contract work before relying on it: glab token borrowing and multi-account; GitLab `reviewer_username` minimum version, `/diffs` endpoint, draft notes + `bulk_publish` availability per tier; Bitbucket BBQL `reviewers.uuid`, `request-changes` endpoint, own-PR approve, batched/pending comments, token scope introspection and expiry; parseability of `gh`/`glab` login output when run without a TTY.
13. **Narrow screens.** Proposed: below 1280px the context panel starts hidden and opens as an overlay; below 1000px the rail becomes an overlay too (panel collapses first, rail second). Breakpoints and order need a yes; the prototype shows desktop only.
14. **Finished work orders.** Proposed: not in the rail; the Factory overview (`/projects/:id`, rail "Factory" title) has an Open / Done / All filter, Done = cancelled or PR merged. Alternative: a "Done · N" link at the foot of the rail's Factory section that opens the overview pre-filtered. Pick one (or both).
15. **Default selection on `/projects/:id`.** Proposed: the Factory overview. Alternative: auto-select the WO marked "you" (then the first open WO), which saves a click but makes the bare project URL non-deterministic.
16. **Rail with many items.** Proposed: each section shows at most 8 rows ("you" and AI-labelled rows first), then "Show N more" expanding in place; sections collapsible, state in `localStorage`. Alternative: one scrolling rail with no caps.
17. **Collapsible rail.** Proposed: the rail reuses the topbar sidebar toggle (hidden ⇒ content + panel only, rail opens as overlay). Is a 56px icon-only mini rail wanted instead?
18. **"Ready" WOs with an open PR.** Counted as open (shown in the rail) until the PR merges or the WO is cancelled. A ready WO whose PR was closed unmerged: open (needs the user) or done?
19. **Right panel on the full PR list** (`/projects/:id/pulls`): proposed none (toggle disabled). Alternatively a filter/summary card.
