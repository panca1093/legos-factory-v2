# Review PR via Amartha workbench: spike findings

Goal: let Review PR use the user's existing workbench Bitbucket sign-in instead of a separate
Bitbucket API token. Both spikes were read-only: nothing was posted, approved, commented or
merged. Probe repo/PR: `Amartha/go-payment-lib` #203 (13 files, +1715) and #201 (6 files).

**Recommendation: build A (curl_session proxy), with B.1 (`tools/call`) as its foundation.**
The existing `integration.Bitbucket` works through the proxy unchanged except for transport
details; the typed tools (B) drop fields the `PRReviewer` contract depends on.

## Spike A: `curl_session` proxy

### What the mint returns

```json
{ "token": "<JWT, HS256>", "expiresIn": 900,
  "proxyBaseUrl": "https://workbench.amartha.id/c",
  "usage": "Send requests to https://workbench.amartha.id/c/<integration>/<path> with Authorization: Bearer <token>" }
```

JWT claims (no secret): `sub` = workbench user id, `ints: ["atlassian-bitbucket"]`,
`aud: "a-workbench-curl"`, `iss: "a-workbench"`, `iat`/`exp` (exp = iat + expiresInSeconds).
The proxy was enabled for Bitbucket; the mint was accepted.

### Proxy behaviour

| Item | Finding |
|---|---|
| Base URL | `https://workbench.amartha.id/c/atlassian-bitbucket` maps to `https://api.bitbucket.org/2.0`. So the path is `/user`, **not** `/2.0/user` (that 404s). Same shape as our `baseURL + "/user"`. |
| Auth | `Authorization: Bearer <token>`; the proxy swaps in the user's real Bitbucket OAuth token (`x-credential-type: oauth2_legacy`). |
| Body | Raw Bitbucket API JSON and the raw unified diff (`text/plain`), byte-identical to what `bitbucket_review.go` already parses. Links inside bodies (`next`, `links.html`) still point at `api.bitbucket.org` / `bitbucket.org`. |
| Scopes | `x-oauth-scopes: pullrequest, account, pipeline`. No `repository` scope listed, yet `/diff` (accepts `repository`) works, since `pullrequest` implies it. |
| Latency | 0.7–1.0 s for small calls, 1.5–3.8 s for 50-row PR lists and the 64 KB diff (it goes through workbench, then api.bitbucket.org). |
| Size | No proxy cap was seen: a 642 KB list page and a 64 KB diff passed through. Our own `bbDiffLimit` (4 MiB) still applies. |
| Rate limit | The proxy adds no rate-limit headers. Bitbucket's own `x-request-count` is passed through, and Bitbucket's hourly limits apply to the user's OAuth app. |
| Expiry | Hard cut at `exp`: 20 s after expiry the same request got `401 {"error":"Invalid or expired curl session token"}`. With no token: `401 {"error":"Authorization: Bearer <curl-session-token> required"}`. Both are JSON 401s, so the client maps them to a provider 401; the token source should re-mint once on a 401 and retry. The token cannot be revoked; it only expires. |
| Network | `workbench.amartha.id` must be reachable from wherever the backend runs. It was blocked in the cloud sandbox until allowed. |

#### Gzip quirk (blocker until handled)

Some endpoints come back with `Content-Encoding: gzip` while the body is **plain JSON**
(seen on `/pullrequests/{id}/diffstat` and `/statuses`; `/pullrequests/{id}` and the list were
really gzipped). Go's transport sends `Accept-Encoding: gzip` by default and transparently
"decompresses", so the client reads an empty body and fails with
`bitbucket API returned unexpected JSON: unexpected end of JSON input`.
With `Accept-Encoding: identity`, every endpoint returned plain bodies with no
`Content-Encoding`, and the unchanged client then worked end to end:

```
Me: ok (login + uuid)
TokenInfo: {Scopes:[pullrequest account pipeline] CanRead:true CanWrite:no}
ListPullRequests(go-payment-lib, no filter): 9
GetPullRequest(#203): head/base 40-char SHAs, 13 files, +1715 -0, CI passing, 10 reviewers
PullRequestFiles(#203): 13 files, all 13 with a patch
```

Other proxies or Bitbucket itself may be mislabelling the encoding. Either way, sending
`identity` from the auth func avoids the problem.

### How much of `integration.Bitbucket` changes

The PR code itself does not change. What is new:

1. A constructor variant: base URL `https://workbench.amartha.id/c/atlassian-bitbucket`, auth func
   `Bearer <token>` plus `Accept-Encoding: identity` (~10 LOC). The token comes from a small
   `func(ctx) (string, error)` that mints through `CallMCPTool(workbench, "curl_session", …)`, caches the token
   in memory until about `exp - 60s`, and re-mints after that. Nothing is persisted (~40 LOC in usecase).
2. `TokenInfo`: an OAuth token with the `pullrequest` scope can comment, approve and request changes.
   Per Atlassian's scope docs, `pullrequest:write` is only needed to create, update, merge or decline PRs. Today
   this reports `CanWrite: no`, so `Reviews` would return `ErrReadOnlyToken` and readiness would
   read "read-only". Treat `pullrequest` as write for PR comments (~2 LOC). This was not confirmed with a
   real write in this spike.
3. Git checkout for the review worktree: the proxy is the REST API only and does not serve git smart-HTTP, so
   `reviewCtx.gitAuth()` (username/token basic auth) has nothing to use. Use
   `bitbucket_get_clone_url` through `CallMCPTool` right before the fetch, pass it to `PrepareReview`
   for that one fetch, and never write it into `.git/config` or the DB (~25 LOC).
4. Connection/UI: a "Bitbucket via workbench" choice that points at the user's workbench MCP
   server instead of asking for username + API token (~40 LOC backend, ~60 frontend).

### Risk

- The minted token can make **any** call the user's Bitbucket OAuth grant allows (here:
  `pullrequest`, `account`, `pipeline`: comment, approve, trigger pipelines; plus
  `pullrequest`-implied repository read) until `exp`, and it cannot be revoked. Mitigations: mint
  with `expiresInSeconds` = 300–900, keep the token in memory only, never log the token or the
  `Authorization` header, and mint lazily per review session rather than at login.
- `curl_session` is labelled "HIGH RISK — do not call without explicit user approval" for agents.
  A backend calling it programmatically is a product decision: the user opts in when they choose
  "via workbench". If workbench later gates the mint behind an interactive approval, A breaks.
- The token is a bearer secret to a gateway, so a leak goes beyond Bitbucket only if `ints`
  is widened. Always mint with exactly `["atlassian-bitbucket"]`.

### Pre-existing bugs found on the way (affect direct Bitbucket too)

1. **BBQL drops `state=OPEN`.** When `q=` is present, Bitbucket ignores the `state` query
   parameter. `ListPullRequests(…, Reviewer: me)` on `loan-platform` returned 50 PRs
   (31 MERGED, 10 DECLINED, 9 OPEN). With `q=state="OPEN" AND reviewers.uuid="…"` it returned the 9 open ones.
   Fix: AND `state="OPEN"` into every BBQL query.
2. **The local-filter fallback can't match reviewers.** List rows carry no `reviewers` or
   `participants` (0 on every row seen), so the 400-fallback path filters every PR out for a reviewer filter.
3. Cross-workspace endpoints are gone (`/2.0/workspaces`, `/2.0/repositories?role=member`,
   `/2.0/user/permissions/workspaces`, `/2.0/pullrequests/{user}`: all 404 "no API hosted at this URL").
   We don't call them today; any future "PRs for me across repos" must go per repo or through
   `/user/workspaces` + `/repositories/{ws}`.

## Spike B: typed tools via `execute_tools`

### B.1: `tools/call` in the MCP client (done, on this branch)

`agent.CallMCPTool(ctx, server, name, args) (string, error)` sits beside `ListMCPTools`. The handshake
is shared through `connectMCP`. It returns `structuredContent` when present, else the joined text content;
`isError: true` becomes an error carrying the tool's text. `mcpfake` now answers `tools/call`
and has `Grant()` to issue an access token without the OAuth dance. Test:
`TestMCPClientCallsToolAgainstFakeProvider` (text, structured, isError, 401). A workbench call is
`CallMCPTool(ctx, wb, "execute_tools", {"executions":[{"tool":"bitbucket_get_pr","args":{…}}]})`.
**A needs this too**, for `curl_session` and `bitbucket_get_clone_url`.

Not done: no per-session connection reuse (each call does initialize + call), and no OAuth refresh
around it. The caller should pass a server whose `Secret` is already fresh, the way `usecase/mcp.go`
already does for discovery.

### B.2: response shapes (trimmed, people redacted)

`whoami` gives `{ "id": "<workbench uuid>", "email": "<user>@amartha.com" }`. This is workbench identity only, with no Bitbucket uuid.

`list_integrations` gives `{ "integrations": [{ "name": "atlassian-bitbucket", "version": "1.0.0", "connected": true }, …] }`

`bitbucket_list_prs {workspace, repoSlug, state?, page?, pagelen?=10}`:
```json
{ "prs": [{ "id": 203, "title": "ANN-5103", "state": "OPEN", "author": "<display name>",
            "source": "ANN-5103", "destination": "master", "updated_on": "2026-11-20T01:57:53Z" }],
  "hasMore": true }
```

`bitbucket_get_pr {workspace, repoSlug, pullRequestId}`:
```json
{ "id": 203, "title": "…", "description": "…", "state": "OPEN", "author": "<display name>",
  "source": "ANN-5103", "destination": "master",
  "reviewers": [{ "display_name": "<name>", "uuid": "{…}", "approved": true }],
  "comment_count": 3, "task_count": 0, "created_on": "…", "updated_on": "…",
  "url": "https://bitbucket.org/Amartha/go-payment-lib/pull-requests/203" }
```

`bitbucket_get_pr_diff {…, prId, diffstat: true}`:
```json
{ "files": [{ "status": "modified", "old": "go.work", "new": "go.work", "lines_added": 1, "lines_removed": 0 },
            { "status": "added", "new": "baas/go.mod", "lines_added": 3, "lines_removed": 0 }],
  "hasMore": false }
```

`bitbucket_get_pr_diff {…, diffstat: false}` gives a JSON **string** holding the raw `diff --git …` text
(the same text as `/diff`; ~64 KB for #201 came back whole).

`bitbucket_list_pr_comments {…, prId, pagelen?=20}`:
```json
{ "comments": [{ "id": 894057203, "author": "<display name>", "text": "…",
                 "inline": { "path": "baas/aladin/oauth.go", "line": 197 }, "created_on": "…" }],
  "hasMore": false }
```

Write tool schemas (read via `get_tool_schema`, not called):
- `bitbucket_add_pr_comment {workspace, repoSlug, prId, text, inline?: {path, line}}`, where `line` is
  "Line number in the new version of the file". The result is `{ id, created_on }`.
- `bitbucket_request_changes {workspace, repoSlug, prId}` gives `{ requested: true }`
- `bitbucket_approve_pr {workspace, repoSlug, prId}` gives `{ approved: true }` (effect: destructive)
- `bitbucket_get_clone_url {workspace?, repoSlug}` returns an `https://x-token-auth:<token>@bitbucket.org/…` URL, valid ≤2h.
  Not called in this spike.

There is no Bitbucket "me" tool, no commit-status tool and no single-commit tool.
`bitbucket_list_pr_commits` returns 12-char hashes.

### B.3: `PRReviewer` mapping

| Method | Tool calls | Gap |
|---|---|---|
| `Me` | none fits. `whoami` gives a workbench email; `bitbucket_search_users` matches name/nickname only, not email | **No Bitbucket uuid for the user.** Could guess it via search_users(name) or by reading our own participant entry on a PR. Both are fragile. |
| `TokenInfo` | `list_integrations` (connected) | No scopes. CanWrite stays unknown. |
| `ListPullRequests` | `bitbucket_list_prs` per page (10 default) | No reviewer filter and no author uuid (display name only), no head hash, no reviewers. "For me" = list + `bitbucket_get_pr` per PR (**N+1**; 36 open PRs in go-customer = 37 calls, each a gateway round trip). Can be batched in one `execute_tools`, but it is still N upstream calls. Author filter only by display name. |
| `GetPullRequest` | `bitbucket_get_pr` + `get_pr_diff(diffstat)` | **No head/base commit SHA** (needed for stale detection, worktree checkout and `StartSHA`), no source repo full name (forks), no draft flag, no CI statuses, no per-reviewer `changes_requested` state (only `approved` bool), so `MyLatestReview` can't show "changes requested". Head could come from `list_pr_commits` (12-char, needs another call); base: none. |
| `PullRequestFiles` | `get_pr_diff(diffstat=true)` + `get_pr_diff(diffstat=false)`, then reuse `splitDiff` | Works. The full diff travels inside a JSON string, with no visible size cap. A huge PR can hit MCP/gateway response limits (unknown). |
| `PostReview` | per inline comment `bitbucket_add_pr_comment`, summary `bitbucket_add_pr_comment`, then `bitbucket_request_changes` / `bitbucket_approve_pr` | **No old-side anchor**: `inline` is `{path, line}` on the new file only, so drafts with `Side: "old"` (deleted lines, `from` in the REST API) can't be posted inline. They would degrade to a general comment with `path:line (old)` text. No batch review (same as today: Bitbucket has none). Partial-failure semantics stay as today (one call per comment). |
| Worktree fetch | `bitbucket_get_clone_url` right before `git fetch`, passed in memory | Same for A and B. Never persist the URL. |

Rough size of B: new `PRReviewer` ~400 LOC + tests ~300, and it still loses old-side comments,
CI, changes-requested state and a reliable `Me`. Several of these are contract regressions in
`docs/review-pr/CONTRACT.md`, not just cosmetic ones.

## Recommendation

Build **A**:

| Piece | Rough LOC |
|---|---|
| `tools/call` (B.1, done here) | ~100 incl. test |
| workbench token source (mint via `curl_session`, in-memory cache, re-mint near `exp`) | ~40 + ~60 test |
| `NewBitbucket` variant: proxy base, bearer, `Accept-Encoding: identity` | ~10 |
| `TokenInfo`: `pullrequest` counts as review-write | ~2 |
| clone via `bitbucket_get_clone_url` for `PrepareReview` | ~25 |
| connection kind "Bitbucket via workbench" + UI | ~40 backend, ~60 frontend |
| BBQL `state="OPEN"` fix (independent bug, do anyway) | ~3 + test |

Total is about 200 LOC of product code, against about 400 for B, which also has known gaps. Keep B's typed tools as a fallback
if workbench ever disables the curl proxy for Bitbucket. The mapping above shows what would have to be
accepted in that case.

Open before building: (1) confirm a real comment/approve through the proxy works with the
`pullrequest` scope on a throwaway PR; (2) confirm the backend host can reach `workbench.amartha.id`;
(3) ask workbench whether the gzip mislabel is theirs (then it's a bug to report) or Bitbucket's.
