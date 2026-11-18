# Work-order tabs: Activity thread, Progress 80/20, Review split

Prototype (approved 2026-11-18): https://claude.ai/artifact/Azb9aBVmgxi18d8ogFMp33 (`docs/ui-proposals/work-order-tabs.html`).

Frontend only. Every piece below is built from data the API already returns (`factory.events`, `detail.plan`, `detail.runs`, `factory.quality`, `factory.findings`, the work order). No backend change is in scope; the two gaps that would need one are listed at the end.

Three PRs, in order, smallest first. Each one stands on its own.

## PR 1: Review, checks on top and a 50/50 split

Files: `reviews/ReviewPanel.vue` (layout only), new `reviews/ChecksGrid.vue`.

- Checks become their own full-width card at the top: a tile per check (name, pass/fail pill, command), 4 columns → 2 under 1080px → 1 under 720px. Clicking a tile shows its output below the grid, the same output the row expander shows today.
- **Baseline marker**: a check whose name matches a `qc_base_*` row that also failed is tagged "Fails on main too", with a dashed border, and the header gets a "N failed on main too" pill. This is the same rule `verify` uses since #82 (`baselineIDPrefix`), worked out in a pure helper `baselineFailures(checks)`; the frontend only shows it, it decides nothing.
- `qc_base_*` rows themselves are not drawn as tiles.
- Architect gaps | Inspector findings sit side by side (`grid 1fr 1fr`, stacking under 720px), with the same header (avatar, label, count pill) and the same empty-state box. Finding actions (accept / dismiss / note) stay as they are.

Done when: the unit test for `baselineFailures` covers same-name fail, base passed, no base row; Playwright sees checks before the two columns and both columns at the same top edge at 1280px.

## PR 2: Progress, 80/20

Files: `work-orders/WorkOrderView.vue` (progress branch), `runs/ActivityPanel.vue` split into `runs/PlanProgressCard.vue` + `runs/HappeningCard.vue`, `task-graph/TaskGraph.vue` (height only).

- `grid-template-columns: minmax(0, 4fr) minmax(240px, 1fr)`. The page caps at 1320px, so a 300px floor never reaches 4:1; 240px gives a true 80/20 with the sidebar closed.
- Left: TaskGraph fills the column height (min 640px). ELK layout, zoom and pan unchanged.
- Right, top: **Plan progress** card, always shown (today it only appears when a retry is possible). Merged count, meter, a compact row per task. Retry and remediation buttons move into this card, unchanged in behaviour.
- Right, bottom: **What's happening**: `AgentLiveCard` (spinner + steer input) first, then the filter chips at small size, then the event list in a compact one-line-plus-time form. Same `groupActivity` data and filters as today.
- Under 1080px the right column drops below the graph.

Done when: existing ActivityPanel tests pass after the split, plus Playwright: at 1440px the graph's width is about 4× the right column, and at 1000px the columns stack.

## PR 3: Activity tab (replaces Plan)

Files: new `runs/activityThread.ts` (pure mapping) + `runs/activityThread.test.ts`, new `work-orders/ActivityThread.vue`, `work-orders/WorkOrderView.vue` (tabs), `work-orders/BlueprintPanel.vue` sections reused as doc-card bodies.

### Tab key

- The tab becomes `{ key: "activity", label: "Activity" }`. `?tab=plan` and `?tab=blueprint` redirect to it.
- **Conflict**: `legacyTabs` today maps `activity → progress` (old links). That entry is removed so `?tab=activity` opens the new tab. Old bookmarks to `?tab=activity` will land on the thread instead of Progress; acceptable, flagged here so it is a choice and not an accident.

### Mapping events to messages

`activityThread(events, order, tasks)` returns a list of `{ kind: "message" | "system" | "divider", from, to, type, text, docs?, quotes?, findings? }`. The table is the whole contract; anything not listed stays out of the thread (it still shows in What's happening).

| Event (actor/kind) | Thread item |
|---|---|
| order created (order.description / `created_from_chat`) | You → Foreman · Request (right side) |
| foreman `brief_ready`, `brief_amended` | Foreman → Architect · Task, doc card "Foreman's brief" (`payload.brief`) |
| architect `plan_ready` | Architect → Foreman · Spec, doc cards: acceptance contract, spec, how the crew will build it, scope boundaries (from `order` + `tasks`, same text BlueprintPanel shows now) |
| foreman `plan_reviewed` | Foreman → Architect · Feedback, `payload.findings` as finding rows |
| jev `*_gate` | outcome revise/escalate → Jev → (Architect for plan, Builder for build) · Feedback with `payload.issues`; pass → system line "Gate passed" |
| factory `plan_revision_requested` | system line "Revision N requested" |
| `plan_ready` that follows a revision request | Reply · revision N, quoting the titles of the feedback it answers |
| human `plan_revision_human`, `revision_requested` | You → Architect / Builder · Feedback (right side) |
| `clarification_asked` | agent → You · Question |
| `clarification_answered` | You (or Foreman) → agent · Reply, quoting the question |
| `agent_steered` | You → that run's role · Steer (right side) |
| builder `agent_started` with a task | Foreman → Builder · Task #NN (task title) |
| inspector `finding` (grouped until `inspection_finished`) | Inspector → Builder · Feedback, one row per finding with severity |
| factory `fix_requested` | Foreman → Builder · Task "Revision N fix", quoting the findings it carries |
| `commit_recorded`, `task_merged`, `check_*`/verification summary, `pr_opened`, `escalated`, `plan_escalated`, `paused`/`resumed`/`cancelled` | system line; escalations use the warning pill and say "needs you" |

Stage dividers (Brief / Plan / Build / Review / Ship) go in when the first event of that stage appears, using the same stage mapping as `StageTrack`.

### Screen

- Filters with counts: All / Tasks / Feedback / You / System.
- Right rail "Handoffs & loops": one row per sender→recipient pair, counting tasks and feedback rounds, derived from the same list. "Crew": each role's latest run state.
- Typing row at the bottom while a run is live (reuses the `liveRuns` helper).
- Composer: **steers the live run only**, using the existing `factory.steer(id, runID, message)`. The recipient picker lists agents with a live run; with none live, the composer is disabled and says so.

Done when: `activityThread.test.ts` covers every row of the table, a revise loop (gate revise → revision request → plan_ready quotes it), and unknown kinds being dropped; Playwright opens `?tab=plan` and lands on Activity with the brief as a doc card.

## Gaps, not in these PRs

1. **No reply to a steer.** When you steer an agent, nothing records the agent's answer as a message, so the prototype's "Builder → You · Reply" can't be built. Fixing it needs a backend event (e.g. the agent's next text step tagged with the steer it follows). Until then the thread shows the steer and the run carries on.
2. **No recipient field on events.** The `to` side comes from the table above, i.e. from the event kind. A new event kind shows up in What's happening but not in the thread until a row is added here.

## Pipeline

Per the usual rule: Opus implements and opens the PR → Fable reviews → Opus fixes → Fable quick re-review → merge → next PR. Each worktree starts from `origin/main`. Check in the browser (`scripts/dev.sh`) on a real work order before each merge.
