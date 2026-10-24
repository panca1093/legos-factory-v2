# Approved Prototype Frontend Rebuild Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This plan proposes inline execution; no subagent dispatch is authorized by this document.

**Goal:** Rebuild the Vue frontend to match the approved interactive prototype while preserving the existing real-backend work-order journey.

**Architecture:** Build the active app in `frontend/`, preserving `frontend-archive/` as checkpointed reference. Reuse the typed REST/SSE boundary and Pinia domain behavior while replacing presentation with Vue components derived from the original HTML/CSS/SVG. All execution state remains authoritative on the unchanged Go backend; prototype-only capabilities are explicitly unavailable.

**Tech Stack:** Vue 3 Composition API, `<script setup lang="ts">`, TypeScript strict, Vite, Vue Router, Pinia, Tailwind CSS, CSS variables, Lucide Vue, Vue Transition/TransitionGroup, Vitest, Vue Test Utils, Playwright and ESLint.

**Spec:** User-supplied “Mission: Rebuild legos-factory Frontend from Approved Prototype”; repository `index.html`, `PRD.md`, `ADR.md`, `UX-SPEC.md`; discovered contracts in `docs/frontend-workflow-inventory.md`.

## Global Constraints

- Do not rebuild the backend or modify database migrations.
- Do not redesign the approved product experience or introduce another frontend framework.
- Preserve user changes, existing endpoint contracts, cookie sessions, authorization, loading/error feedback and SSE cleanup.
- No demo data in production stores; unavailable backend functionality stays unavailable.
- No heavyweight graph library; use native SVG plus Vue/HTML controls unless a demonstrated limitation requires approval.
- No remote push or merge without explicit authorization.
- Existing checkpoint: `301232d` on `codex/checkpoint-before-frontend-rebuild`. Work on `codex/rebuild-approved-frontend`.

## Review Focus

- Route changes during in-flight detail requests or SSE reconnects must never display the previous order's data.
- Viewer sessions, expired sessions and nonowner clarification attempts must not gain mutation controls.
- Empty workspaces, failed API calls and successful creation followed by failed planning must retain a recoverable path.
- Wide dependency graphs, missing dependencies and unexpected cycles must remain inspectable without freezing or page overflow.
- Historical passing evidence and scripted mock harness output must never imply current-revision real execution success.

---

### Task 1: Establish the active app and preserve the integration boundary

**Files:** Create `frontend/package.json`, lockfile, `tsconfig.json`, `tsconfig.e2e.json`, `vite.config.ts`, `eslint.config.js`, `index.html`, `src/main.ts`, `src/types/api.ts`, `src/services/api/client.ts`, `src/services/events/sse.ts`, `src/stores/factory.ts`, `src/stores/session.ts`, `src/app/App.vue`, `src/app/router/index.ts`; update `.gitignore` only for generated active-app artifacts.

**Interfaces:** Preserve `api.get<T>(path)`, `api.post<T>(path, body?)`, `api.put<T>(path, body?)`, `ApiError`, `useFactory()`, `useSession()`, and `subscribe(path, onEvent, onError?): () => void`. Add abort-aware fetching where route ownership requires it without changing HTTP contracts.

- [ ] Port the 8 existing tests without weakening their assertions. Add tests that logout closes the global stream, a detail A response cannot overwrite detail B, named SSE events refresh state, and malformed frames do not crash the UI.
- [ ] Run new behavior tests and record expected failures before changing behavior.
- [ ] Reuse the archived API contracts/client and stores; repair lifecycle ownership in the active copy. Have one global event subscription, a recoverable login screen and per-order cancellation/cleanup.
- [ ] Add pinned-compatible ESLint/Vue/TypeScript tooling and a `lint` script. Exclude `node_modules`, `dist`, `.vite`, test results and build metadata from version control.
- [ ] Verify `npm run typecheck`, `npm run lint`, and `npm test`; all baseline assertions and new lifecycle tests pass.

### Task 2: Reproduce tokens, characters and application shell

**Files:** Create `frontend/src/styles/tokens.css`, `src/styles/global.css`, `src/components/agents/BrickBot.vue`, `src/components/ui/StatusBadge.vue`, `src/components/ui/AppDialog.vue`, `src/app/layouts/AppShell.vue`, `src/composables/useTheme.ts`; tests in `frontend/tests/unit/shell.spec.ts` and `dialogs.spec.ts`.

**Interfaces:** `BrickBot` accepts the existing five persona names and size. `AppDialog` accepts open/title/drawer state, emits close, and owns focus restoration, Escape handling and background inertness. Theme changes set the same `data-theme` selector as the prototype and persist only presentation preference.

- [ ] Add failing component tests for all four navigation destinations, active route identity, search opening with Cmd/Ctrl+K, dialog focus trapping/restoration, Escape dismissal and theme persistence.
- [ ] Port original tokens, original full-body SVG characters, sidebar/project list/budget/profile, desktop top bar, search/notifications dialogs and footer. Match prototype dimensions and responsive breakpoints.
- [ ] Keep search and notifications backed by real projects and work orders. Link project entries to Factory query filters and role-sensitive settings destinations.
- [ ] Use purposeful Vue/CSS transitions and the prototype's reduced-motion behavior.
- [ ] Verify tests and inspect shell captures at 1440×1110 and 390×844 against the prototype, including the mobile menu and dark theme.

### Task 3: Restore Overview hierarchy with truthful monitoring

**Files:** Create `frontend/src/features/overview/OverviewView.vue`; tests in `frontend/tests/unit/overview.spec.ts`.

**Interfaces:** Consume `Overview`, projects, work orders and observed live events through `useFactory()`. Derive human-decision groups and project counts from authoritative states, not prototype sample totals.

- [ ] Add failing tests for zero usage displaying `$0.00`, unset budget, `NEEDS_HUMAN_REVIEW` counted as a decision, empty projects, failed loading and pending jobs labeled accurately.
- [ ] Reproduce the serif greeting, dotted crew floor/conveyor, monitoring strip, human-decision/project sections and resource-strip position. Include active work, failed jobs and recent real activity without fabricating metrics.
- [ ] Show observed agent activity or “availability not reported” as appropriate; Cataloger remains a future persona. Keep resource capacity unavailable where the API does not report it.
- [ ] Verify tests, navigation from decisions/project rows, and paired desktop/mobile/light/dark screenshots.

### Task 4: Rebuild Factory and the three-step creation drawer

**Files:** Create `frontend/src/features/factory/FactoryView.vue`, `work-orders/NewWorkOrderDrawer.vue`, `work-orders/NewProjectDialog.vue`; tests in `frontend/tests/unit/factory.spec.ts` and `creation.spec.ts`.

**Interfaces:** Consume the unchanged create-project and create-order payloads; preserve create order → generate plan → open order. URL query stores project/status filters; board/list and search are local presentation state.

- [ ] Add failing tests for combined project/status/search filters, board/list parity, blank acceptance rejection, retained values when stepping back, viewer restrictions and creation succeeding while plan generation fails.
- [ ] Reproduce project chips, contextual project profile, toolbar, four prototype board groups and corresponding list. Map every backend state explicitly; blocked and pending-review orders must not look completed.
- [ ] Port Intent → Crew → Review drawer flow, real repository/profile display, discovered model choices, governance context and API feedback. Preserve project creation.
- [ ] Keep an order created before a planning failure accessible with a retry action. Do not create a duplicate order on retry.
- [ ] Verify component tests and a live-backend E2E for project creation, project selection, drawer validation and work-order creation.

### Task 5: Rebuild Blueprint and Task Graph

**Files:** Create `frontend/src/features/factory/work-orders/WorkOrderView.vue`, `BlueprintPanel.vue`, `WorkOrderSidebar.vue`, `src/features/factory/task-graph/TaskGraph.vue`, `layout.ts`; tests in `frontend/tests/unit/work-order.spec.ts`, `graph-layout.spec.ts`, and `task-graph.spec.ts`.

**Interfaces:** `layoutTasks(tasks: Task[])` returns positioned nodes and directional edges plus canvas bounds and any invalid-dependency diagnostics. `TaskGraph` consumes tasks/runs, emits selected-task identity, and keeps zoom/pan local. Selected order and tab follow the current route/query.

- [ ] Add failing graph tests: unsorted dependencies still put ancestors before descendants; siblings do not overlap; each valid dependency has an arrow; graph bounds contain all nodes; missing dependencies/cycles produce a readable state rather than an unbounded loop.
- [ ] Add failing component tests for switching order IDs without component remount, clarification permission, approval/revision payloads and task selection via pointer/keyboard.
- [ ] Reproduce order heading, status/meta, contextual tabs, intent/contract/blueprint, crew/configuration sidebar, revision/clarification dialogs and Markdown export. Preserve backend state gates and collect human notes.
- [ ] Implement lightweight layered DAG layout and prototype brick-node treatment with role attribution and real run-derived status. Add selected-task details, fit/reset and bounded zoom/pan for larger graphs.
- [ ] Keep pause/resume, post-creation model changes and manual next-step execution visibly unavailable. Preserve actual cancellation.
- [ ] Verify tests, long-title/wide-graph mobile behavior and paired Blueprint/graph/selected-task screenshots.

### Task 6: Rebuild Activity, Quality, Changes and PR decisions

**Files:** Create `frontend/src/features/factory/runs/ActivityPanel.vue`, `src/features/factory/reviews/QualityPanel.vue`, `ChangesPanel.vue`; tests in `frontend/tests/unit/reviews.spec.ts`.

**Interfaces:** Consume `WOEvent[]`, `Run[]`, `QualityCheck[]`, `Finding[]` and current work-order revision. Keep `prDecision(id, decision, reason)` and check/finding fields unchanged.

- [ ] Add failing tests for event attribution, harness labels, older passing checks alongside current failures, evidence drawer output/exit codes, forbidden viewer decisions and reason payloads for request changes/decline.
- [ ] Reproduce persona timeline, run-output presentation, evidence inspection, findings and final decision controls with transitions keyed to stable IDs.
- [ ] Separate historical evidence from current revision. Present real Builder output as output; do not invent a Git diff. Preserve local-only approval semantics and all existing decision actions.
- [ ] Verify tests plus a live E2E through plan review → execution → evidence → finding resolution → final PR disposition. Add separate clarification/revision/cancel paths using isolated fixture data.
- [ ] Capture and compare Activity, Quality evidence and Changes & PR states.

### Task 7: Rebuild Settings and Knowledge preview

**Files:** Create `frontend/src/features/settings/SettingsView.vue`, `src/features/knowledge/CatalogerView.vue`; tests in `frontend/tests/unit/settings.spec.ts` and `knowledge.spec.ts`.

**Interfaces:** Preserve `Settings` supported fields, model IDs and admin-only saves. Preserve nullable allowlists and values not actively edited. Settings tab selection may use a URL query; theme is local presentation state.

- [ ] Add failing tests for nonadmin read-only governance, model catalog selection, allowed-model serialization, budget conversion, save failure retaining inputs and unavailable integrations issuing no mutation requests.
- [ ] Reproduce the prototype settings navigation and section treatment. Connect Architect/Builder/Inspector defaults, allowlist and budget; show remaining policy/team/resource/integration controls as unavailable with a clear reason.
- [ ] Recreate Cataloger's approved visual preview and opt-in interaction without introducing ingestion, invented authoritative memory or backend APIs.
- [ ] Verify settings persistence against the isolated backend, keyboard navigation and paired Settings/Knowledge screenshots.

### Task 8: Complete verification, visual comparison and delivery report

**Files:** Create/update `frontend/playwright.config.ts`, `frontend/tests/e2e/*.spec.ts`, `frontend/scripts/capture-screens.mjs`, `docs/frontend-rebuild-report.md`, and screenshot evidence under `docs/screenshots/frontend-rebuild/`.

**Interfaces:** E2E starts the unchanged backend with temporary data and a disposable local fixture repository, using a known free port and the required GOROOT override. The visual runner serves the original prototype separately and captures paired states at identical viewport/theme. Production data is never populated with reference fixtures.

- [ ] Extend E2E assertions for all required flows: navigation/project selection, creation, planning review, clarification, graph interaction, models, quality, PR readiness/decisions, Settings, errors and role gates. Test backend integration separately from visual fixtures.
- [ ] Run `npm run typecheck`, `npm run lint`, `npm test`, `npm run build`, and `npm run e2e`. Record command output and actual counts. Do not claim runtime tests pass if a required service is unavailable.
- [ ] Capture corresponding prototype/rebuild screenshots at 1440×1110, 1024×900 and 390×844. Include dark theme, creation steps, selected graph node and evidence dialog. Keep any visual-only API fixtures inside tests.
- [ ] Inspect paired screenshots, record meaningful differences and repair regressions. Check page overflow, visible focus, modal semantics and reduced motion.
- [ ] Inspect the complete diff: no backend/migration/archive modifications; no caches, credentials or generated build metadata staged. Verify production build stays compatible with the existing build script.
- [ ] Write the final report: components rebuilt, resolved design differences, preserved workflows, exact test results, visual evidence and explicit remaining API/UI gaps. Update this checklist only from observed evidence.

## Execution proposal

Use inline execution in this chat, because presentation work and the shared integration boundary are tightly coupled. The approved prototype fixes design choices; the inventory fixes unsupported-capability boundaries. Plan review is the next gate; no product code has been changed by this plan.
