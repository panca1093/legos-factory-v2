# Frontend rebuild: discovery and preservation inventory

Discovered on 2026-10-24, before rebuilding product code.

## Recovery point

- Original branch: `main`, head `c5d39f1`.
- Existing user changes: Vue application moved to `frontend-archive/`; nine prior implementation screenshots deleted; `.gitignore` updated to ignore the archive.
- Checkpoint branch: `codex/checkpoint-before-frontend-rebuild`, commit `301232d`. All 32 archived application/config/test files are tracked in the checkpoint despite the ignore rule.
- Implementation branch: `codex/rebuild-approved-frontend`, created from that checkpoint.
- The only pre-existing untracked files under `frontend/` are generated `.vite/deps` metadata. They were excluded from the checkpoint.
- No remote push or merge is authorized.

## Design source

`index.html` is the original self-contained interactive prototype. It contains the original five persona SVGs, light/dark tokens, responsive CSS, conveyor animation, shell, board/list, order tabs, three-step creation drawer, dialogs, settings and Cataloger preview. `preview-overview.png` and `preview-factory-dark.png` are additional visual references, not screenshots of the archived Vue app.

Read with it: `PRD.md`, `ADR.md`, `UX-SPEC.md`, `VERIFICATION.md`, `INSTRUCTION.md`, `README.md`, and `docs/IMPLEMENTATION-REPORT.md`. The user supplied the frontend rebuild mission as an attachment. The root product documents describe a prototype; the rebuild mission and live Go contracts override their simulated execution behavior.

No additional filesystem `AGENTS.md` or `CLAUDE.md` was found in the repository. The instructions supplied directly in chat apply.

The reference overview was visually inspected. Distinctive elements include the 216px sidebar, desktop top bar, serif greeting, full-width dotted crew floor, original full-body characters, four unboxed monitoring columns, decision/project split, budget sidebar and resource strip. The archived Vue app replaces several of these with generic cards and different SVG characters.

## Existing integration surface

All paths below are relative to `/api/v1`. Requests use same-origin cookies. Error responses use `{ error: { code, message } }`.

| Workflow | Existing contract | Preservation requirement |
| --- | --- | --- |
| Session | `GET /auth/session`; `POST /auth/dev-login`; `POST /auth/logout`; `GET /auth/oidc/start`; callback `/auth/oidc/callback` | Preserve dev/OIDC modes, logout and role-sensitive actions. The archive router refers to a missing login route; the rebuilt entry must actually offer OIDC sign-in. |
| Projects | `GET /projects`; `GET /projects/:id`; `POST /projects` with `name`, `repo_url`, `language` | Preserve project creation, repository binding and language/quality profile. Add prototype project selection as a client-side filter over real projects. |
| Order creation | `POST /work-orders` with `project_id`, `title`, `objective`, `acceptance`, `autonomy`, `builder_model`, `inspector_model` | Preserve field names and create-then-generate sequence. Trim required input; the approved drawer additionally requires observable acceptance criteria. Keep the created order accessible if drafting subsequently fails. |
| Order listing/detail | `GET /work-orders`; `GET /work-orders/:id` returning `work_order`, `plan`, `runs`; `GET /work-orders/:id/plan` | Keep IDs, revision, model IDs, autonomy and authoritative state. Preserve navigation to an individual order. |
| Planning | `POST /work-orders/:id/plan/generate`; `POST /work-orders/:id/plan/decisions` with `decision: approve/revise`, `notes` | Preserve draft, approve and revision. Collect actual human revision notes instead of silently substituting a canned reason. |
| Clarification | `POST /work-orders/:id/clarifications` with `answer` | Preserve recorded question/answer and rereview. Only creator or admin may answer; viewers cannot mutate. |
| Cancellation | `POST /work-orders/:id/cancel` | Preserve cancellation subject to current backend state and role. |
| Activity/runs | `GET /work-orders/:id/activity`; `GET /work-orders/:id/runs` | Show returned actor, events, output/error, state, harness, costs and tokens. Do not fabricate elapsed time, throughput or percentages. |
| Quality | `GET /work-orders/:id/quality`; `GET /work-orders/:id/findings` | Preserve checks by revision, command/output/exit code/outcome and finding severity/location/status. Put findings in Quality as well as in decision context. |
| PR disposition | `POST /work-orders/:id/pr-decision` with `decision: approve/request_changes/decline`, `reason` | Preserve all three actions and state gates. Explain that this is a local disposition; no PR publishing endpoint exists. |
| Models | `GET /models` returning provider/model/name/harness | Preserve discovery and Builder/Inspector selection during creation. Never turn scripted mock runs into apparently real model execution. |
| Governance | `GET /settings`; admin `PUT /settings` with budget cap, Architect/Builder/Inspector defaults and allowed model IDs | Preserve supported fields and permissions. Opaque `extra` values can be returned by the backend but are not writable settings. |
| Usage | `GET /usage`; `GET /overview` | Display authoritative aggregates and budget reservations. Zero cost is `$0.00`; an unset cap is not a zero-dollar limit. |
| Live updates | `GET /events`; `GET /work-orders/:id/events` | Preserve named events, credentials, reconnect/replay and unsubscribe. Share one global subscription and clean up per-order subscriptions during route changes/unmount. |
| Exports | Client-generated Blueprint Markdown | Preserve export using the selected real work order and current plan. Revoke download object URLs after use. |

## Truthful display rules

- `/overview.metrics.pending_jobs` combines pending and running scheduler jobs. It must not be labeled as a count of running tasks.
- The API exposes work-order states and per-order runs, not persona capacity or an agent availability service. Show observed role activity where supported; otherwise say availability is not reported.
- Plan tasks have IDs, dependencies, role, title and objective. Determine task status from returned runs. Do not assign an invented progress percentage.
- `NEEDS_HUMAN_REVIEW` is a pending decision, not an assembled/completed order. `READY` is the completed local disposition. `BLOCKED` is not completion.
- No authoritative machine CPU/RAM/health telemetry or configured concurrency is returned by these endpoints. Preserve the resource-strip appearance with an unavailable label where necessary.
- Changes are Builder run output, not an authoritative Git diff. Label the output accordingly rather than rendering an invented diff.

## Prototype-only capabilities

These have no supported mutation contract and must be visibly unavailable or explicitly local previews:

- Foreman model setting; personal governance preferences; per-order model/autonomy edits after creation.
- Pause/resume/next-step/autoplay and manual remediation/recheck controls.
- Provider credential connections, GitHub/GitLab/Jira connection setup and external PR publishing.
- Team invitations, membership changes, per-user budgets and project permission administration.
- Editable quality profiles, resource/concurrency limits and maximum-autonomy policy.
- Cataloger ingestion, retrieval and memory promotion.

Theme, search, notifications, project filtering, dialogs, graph inspection, pan/zoom and Markdown export can operate locally over real API data. Test fixtures may model reference states, but must never enter production stores or act as evidence of a real agent run.

## Baseline verification

Run from `frontend-archive/` before product changes:

- `npm run typecheck`: passed.
- `npm test`: passed, 3 files / 8 tests, including Vue Test Utils component tests.
- ESLint: no script/configuration or dependency is present.
- Production build and Playwright: not run during this discovery/checkpoint step. The existing E2E expects a backend on port 8080; future runs must use a separate temporary data directory and fixture repository.

## Rebuild acceptance evidence

Keep reference and rebuilt screenshots at identical viewport, theme and corresponding UI state. Start with 1440×1110, 1024×900 and 390×844; include Overview, Factory board/list, Blueprint, graph with selected task, Activity, Quality evidence, Changes & PR, creation drawer and Settings. Record the actual reference viewport for each pair. Check light and dark themes, keyboard dialogs, reduced motion and horizontal overflow.

Visually compare structure, spacing, typography, tokens, SVG artwork and interaction states. For real-backend captures, document content differences separately from design differences. UI test fixtures are allowed only in test harnesses. A passing screenshot assertion alone is not a visual review.
