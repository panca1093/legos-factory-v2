# Review PR — build contract

Status: 2026-11-19, written before the parallel build. `SPEC.md` is the design; this file pins the names every builder must share and the answers to the spec's open questions taken in full-auto mode (the user delegated them; each is the spec's proposal or the safest option).

## Answers to open questions (auto, 2026-11-19)

| Q | Answer |
|---|---|
| P1–P5 | Accepted as proposed (personal identity only, single-line anchors, post requires unchanged head → 409, one Inspector run per review, runs as goroutines in `Reviews`) |
| Q3 AI run trigger | Only on "Run AI review" (never automatic; cost is the user's) |
| Q4 Summary | Prefilled from the AI run summary, editable |
| Q5 GitLab "Request changes" | Hidden on GitLab (`verdicts.request_changes = {ok:false, reason}`) |
| Q6 Team review requests | Not counted in v1 |
| Q7 Fork PRs | AI review disabled when `source_repo != repo` with reason "AI review is not available for fork pull requests yet"; listing and manual decisions still work |
| Q8 Review checkout | A detached git worktree of the project's own folder at `<DataDir>/worktrees/<project>/review-<number>`, created at run start from the fetched PR head (`refs/pull/N/head`, `refs/merge-requests/N/head`, Bitbucket: source branch) and removed when the run ends |
| Q9 Manual comments | Not in v1 |
| Q11 Draft PRs | Listed with a "Draft" badge |
| Q12 | Provider calls are built against the documented APIs and tested with httptest fakes; nothing is called live during the build. `glab` borrowing and in-app glab login are **not** built (GitLab = token connection only); GitHub CLI rows and in-app `gh auth login` are |
| Q13–Q19 | As shipped in #92 |

Safety: no builder may call a real provider API, run `gh auth login`, or post anything. Tests use fakes.

## Go (already on the branch)

- Types: `backend/internal/model/prreview.go` (JSON tags are the wire names).
- Interfaces: `PRReviewer`, `ReviewStore`, `CLIAuth` in `backend/internal/usecase/ports.go`.
- `model.LiveEvent.UserID`: when set, the SSE stream delivers the event only to that user.
- Migration number: `031_pr_reviews.sql` (schema in SPEC §Persistence, unchanged).

## Wire shapes (`/api/v1`)

```ts
type Identity = { provider: string; host: string; source: "connection"|"gh"|"glab"; login: string;
                  readiness: "ready"|"read_only"|"unknown"|"expired"|"invalid"; expires_at?: string } | null;
type PRRow = { number: number; title: string; url: string; author: HostUser; head: string; base: string;
               draft: boolean; updated_at: string;
               role: "author"|"reviewer"|"both";            // relative to the identity
               label: "ai"|"yours"|"new"|"approve"|"none";   // SPEC §Project workspace label rule
               pending_drafts: number; running: boolean; factory_wo_id?: string };
type ProviderErr = { code: "no_identity"|"rate_limited"|"provider"|"not_found"|"forbidden"; message: string; until?: string };
```

| Endpoint | Response |
|---|---|
| `GET /projects/:id/pull-requests?filter=related\|reviewer\|author` | `{identity: Identity, rows: PRRow[], stale: boolean, error?: ProviderErr}` |
| `GET /reviews?filter=` | `[{project: {id, name}, identity, rows: PRRow[], error?: ProviderErr}]` |
| `GET /projects/:id/rail` pulls part | `{identity, count, rows: [{number, title, label, pending_drafts, running}], stale, error?}` — `error.code` as above |
| `GET /projects/:id/pull-requests/:n` | `{pr: PRDetail, identity, review: PRReview \| null, run: PRReviewRun \| null, verdicts: {approve: {ok, reason?}, request_changes: {ok, reason?}}, ai: {ok, reason?}}` |
| `GET …/:n/files` | `[PRFile & {viewed: boolean}]` |
| `POST …/:n/review/runs` | 202 `PRReviewRun`; 409 when one is running; 422 `{error}` when `ai.ok` is false |
| `GET …/:n/review/drafts` | `PRDraft[]` |
| `PATCH …/:n/review/drafts/:draft` `{status?, body?}` | `PRDraft` (400 on an illegal transition) |
| `PUT …/:n/review/viewed` `{path, viewed}` | `{viewed: number, total: number}` |
| `PATCH …/:n/review` `{summary?, verdict?}` | `PRReview` |
| `POST …/:n/review/post` `{confirm: true, expect: {login, head_sha, comments, verdict}}` | `PostedReview`; 409 `{error, current: {login, head_sha, comments, verdict}}` on mismatch |
| `GET /review-identities` | `[{provider, host, pinned: ReviewIdentity\|null, connections: [{id, name, login, readiness, expires_at?}], cli: {tool: "gh", installed: boolean, users: string[]} \| null}]` |
| `PUT /review-identities` `{provider, host, source, connection_id?, login?}` | `ReviewIdentity` |
| `POST /review-identities/cli/login` `{tool: "gh", host}` | `{login_id}`; live events `cli_login.code\|url\|done\|failed\|fallback` with payload `{login_id, ...CLILoginStep}` |
| `GET /projects/summary` | `[{project_id, needs_you, wo_running, prs_for_me: number\|null, shipped_30d, last_activity, identity}]` |

Live events for review runs: `pr_review.run_started|run_finished|run_failed`, payload `{project_id, number, run_id, review_id}`, `UserID` set.

Errors use the existing `{error: {code, message}}` envelope of `internal/http`.

## File ownership during the parallel build

| Builder | Owns |
|---|---|
| A providers | `internal/integration/*` (github/gitlab/bitbucket review calls, `client.go` RetryAfter, `ghcli.go` CLIAuth for gh), integration tests |
| B store | `internal/store/prreview.go`, `internal/store/migrations/031_pr_reviews.sql`, store tests |
| C usecase + HTTP | `internal/usecase/prreview*.go`, `internal/usecase/rail.go` (pulls part), `internal/sandbox/review.go`, `internal/http/reviews.go` + routes in `server.go`, SSE per-user delivery (`internal/http/bus.go`, events handler), `internal/app/app.go` wiring |
| D frontend PR UI | `frontend/src/features/projects/pulls/*` (new), PR rail section in `ProjectRail.vue`, `ReviewsView.vue`, routes for `/pulls*`, `frontend/src/types/review.ts`, `frontend/src/stores/reviews.ts` |
| E settings | `frontend/src/features/settings/*` review-identity rows, `ProjectsView.vue` "PRs for you" from `/projects/summary` |
