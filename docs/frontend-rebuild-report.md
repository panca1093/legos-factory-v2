# Approved prototype frontend rebuild

Implementation branch: `codex/rebuild-approved-frontend`.
Recovery checkpoint: `301232d`, retained on `codex/checkpoint-before-frontend-rebuild`. The original `main` head remains `c5d39f1`.

## Components rebuilt

- Application entry, authenticated shell, responsive sidebar/project navigation, topbar, global search, notifications, themes and keyboard dialogs.
- Original five agent characters, extracted from the approved prototype SVG artwork; original light/dark tokens, typography, spacing and responsive CSS.
- Overview greeting, dotted animated crew floor, monitoring strip, pending decisions, project summaries, recent observed activity and usage.
- Factory project/status/search filters, board/list, project profile and project creation.
- Three-step Intent → Crew → Review work-order drawer, including validation and recovery when a saved order cannot start planning.
- Order Blueprint, actual dependency plan, contextual sidebar, clarification/revision dialogs and Markdown export.
- Layered SVG DAG with arrows, pan/zoom/fit, keyboard-accessible task nodes, contextual task/run details and cycle/missing-dependency diagnostics.
- Actor-attributed Activity and labeled run output, revision-specific Quality/evidence inspection, findings, Builder output and all three local PR dispositions.
- Settings sections with supported governance/model/budget mutations and explicit unavailable states; optional Cataloger concept preview.

## Design differences resolved

The archived implementation used a compact icon crew, different characters, a missing desktop topbar, generic monitoring cards and a simplified Factory/detail experience. The active application now uses the original full-body illustrations, crew-floor composition, four-column monitoring hierarchy, project chips/board, contextual detail layout, prototype drawer, settings navigation and Knowledge hero.

Paired visual inspection caught and corrected misplaced modal controls, mobile Factory heading overflow, board toggle styling, incorrect creation-step capture, and cold-start project preselection. The crew/review drawer layouts were brought back to the prototype's cards and spacing after comparison.

## Existing functionality preserved

Unchanged Go endpoints and cookie credentials support session bootstrap/dev login, OIDC entry, logout, project/repository creation, work-order creation, planning, approval/revision, owner/admin clarification, execution observation, cancellation, real quality/finding output, PR dispositions, model discovery and settings.

The rebuild owns one global event subscription, closes per-order streams on identity changes and unmount, and prevents old detail responses from overwriting a newer selection. Creation results remain accessible after follow-up planning or list-refresh failures. Viewer/admin/creator boundaries are reflected in controls and preserved at the unchanged API boundary.

Settings initialization cannot save blank defaults on a loading failure; independently loaded settings survive ancillary discovery failures. Authenticated API 401 responses invalidate the session, reset workspace state, close live streams and return the UI to sign-in.

The backend and database migrations were not modified. The archived app remains intact. Nothing was pushed, published or merged.

## Verification

Final verification on 2026-10-24 after the independent review fixes:

- `npm run typecheck`: passed, strict application/component type checking.
- `./node_modules/.bin/tsc -p tsconfig.e2e.json --noEmit`: passed, browser-test configuration and fixture type checking.
- `npm run lint`: passed, ESLint, TypeScript and Vue rules.
- `npm test`: passed, 15 files / 43 tests, Vitest plus Vue Test Utils.
- `npm run build`: passed, type checking and Vite production assets in `frontend/dist`, compatible with the existing build script.
- `npm run e2e`: passed, 9 tests in 28.5 seconds; Playwright; separate backend on port 18081, separate Vite instance on 15173, separate prototype server on 15174, temporary data and disposable fixture repositories.

The live-backend tests use the backend's explicitly labeled scripted mock harness. They exercise real API calls, SQLite state transitions, SSE, filesystem changes and Go verification in disposable repositories. They do not certify a live external model provider or external PR delivery.

## Visual verification

[Interactive comparison viewer](screenshots/frontend-rebuild/comparison.html).

49 reference/rebuild pairs (98 PNGs): 16 desktop states at 1440×1110, 16 tablet states at 1024×900, and 17 mobile states at 390×844. Each viewport includes Overview/light/dark, Factory board/list, Blueprint, graph/task details, Activity, Quality/evidence, Changes & PR, all three drawer steps, Settings and Knowledge; mobile also includes the navigation scrim.

Automated checks compare background tokens and sidebar width, pin desktop floor geometry, detect page overflow, verify expected creation steps and record browser errors. Manual paired inspection checks composition, spacing, typography, artwork and dialogs. These are comparisons against the original prototype, not self-approved snapshots of the rebuilt application.

Screenshots use API responses exclusively inside the test harness to put both products in corresponding states. Production stores contain no demo records. The comparison manifests identify viewport and image paths for every pair. Data-driven content, task titles/counts and unavailable backend controls differ intentionally; no pixel-identical claim is made for those differences.

The final recaptured graph, mobile graph, evidence dialog and dark Overview were also inspected after the review fixes.

## Remaining UI/backend gaps

The UI visibly identifies unsupported capabilities instead of simulating execution:

- Agent availability/capacity and machine CPU/RAM/health are not reported by the current API.
- Foreman model configuration and per-order governance snapshots are not exposed; post-creation model changes and personal overrides are unavailable.
- Pause/resume, manual stepping/autoplay and manual recheck controls have no supported mutation endpoints.
- Team invitations/membership changes, project permission administration, provider connection setup, editable resource/concurrency limits and quality profiles are unavailable.
- Builder run output is available; an authoritative Git diff and external PR publication endpoint are not.
- Cataloger ingestion, retrieval, freshness and memory curation remain a clearly labeled concept preview.
- Runtime permission behavior is checked with API fixtures and the existing backend contracts; a live production OIDC provider and external model connection were not exercised.

## Final review

An independent read-only whole-branch review identified two Important recovery issues (Settings initialization and expired sessions), both reproduced with failing regression tests and fixed. The missing prototype execution-control area was also restored as disabled, explained controls rather than simulated execution. The final regression suite and browser tests pass; no review finding remains deferred. Final visual acceptance was completed by the implementing agent after recapture. No external model/provider, production OIDC or external PR verification is implied. Pre-existing Go backend semantics were outside this frontend rebuild.

## Execution decisions

- Actual API data replaces prototype-only totals, sparklines, simulated diffs and machine telemetry. This preserves truthful reporting; visual content differs where the backend cannot provide the prototype data.
- The existing assisted default and create-then-plan behavior were retained while adopting the prototype drawer presentation. Changing these defaults would change existing workflow behavior.
- Screens were visually compared together in the final verification stage, after their shared shell and interactions existed. Earlier unit-test passes were not treated as proof of visual fidelity.

No speculative backend expansion was performed to close these gaps.
