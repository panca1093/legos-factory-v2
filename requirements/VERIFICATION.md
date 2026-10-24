# Prototype verification

Verified on 24 October 2026 in the Codex in-app browser, using the delivered HTML over a local HTTP server. This records frontend verification, not verification of the simulated project code.

## Browser-exercised behavior

| Scenario | Observed result |
|---|---|
| Existing payment work order | Clarification → plan ready → human approval → graph → pause/resume → QA finding → fix/recheck → demo PR approval completed |
| Revision identity | Fix/recheck incremented the demo revision and attached passing sample evidence to the new revision |
| Fresh work order | Entered title/outcome/criteria, selected Orbit SDK and its repository, chose a distinct Inspector model and assisted autonomy, created order, drafted plan, ran Architect review, reached clarification |
| Budget exhaustion | Lowering cap below current usage paused active work; resume remained blocked with a specific budget message |
| Model allowlist | Removing Gemini removed it from personal preference options |
| Member permissions | Governance defaults, allowlist, autonomy controls and save action became disabled |
| Autonomy recovery | Lowering ceiling paused assisted work; paused-order configuration allowed changing to Guided; resume succeeded |
| Concurrency | At one occupied slot, the next approved order entered Queued with a retry action |
| Assisted Auto-play | Automatically advanced to an Inspector finding and stopped there |
| Connection preview | OpenCode consent preview explicitly explained no real auth; enabling changed the local demo flag |
| Cataloger | Opt-in preview, knowledge search, provenance detail and source-order drill-down worked |
| Views and search | Project selection, work-order filtering, board/list switching and contextual tabs were exercised |
| Theme | Light and dark views rendered; saved state survived reload |
| Reset | Restored original work orders, usage and settings |
| Download | Blueprint button produced `LF-128-blueprint.md`; the browser returned an actual local download path |
| Runtime console | No captured console errors in the final served prototype |

## Visual / responsive inspection

- Wide desktop at 1440 × 1000: labeled rail, four-column Factory board, overview, and contextual order layout.
- Tablet at approximately 718px: compact rail, narrower layouts and configuration stacking.
- Mobile at 390 × 844: greeting/monitoring, menu navigation, filtered board, order tabs, graph and run controls.
- Read-only DOM measurement confirmed `documentElement.scrollWidth === innerWidth === 390` for the mobile Factory board and task graph page. The graph intentionally scrolls inside its canvas.
- Fixed issues observed during inspection: header search text wrapping, hidden mobile navigation remaining in accessibility navigation, and narrow run-control layout.
- Fixed a code-inspected recovery gap: users can lower autonomy for paused orders after policy tightens.
- Saved screenshots: `preview-overview.png`, `preview-factory-dark.png`.

## Static checks

- JavaScript syntax checked successfully with Node.
- 64 statically enumerated UI actions map to defined handlers (70 handler cases total, including dynamically selected actions).
- No external asset URLs, remote fonts or provider endpoints in the HTML.
- Reduced-motion CSS, visible-focus rules, native form labels, dialog semantics, background inertness, focus trap and Escape/focus-restoration code inspected.
- Local HTTP server returned status 200.
- ZIP archive integrity checked after packaging.

## Limits of this verification

- This is a focused browser walkthrough plus static inspection, not an exhaustive automated regression suite or WCAG audit.
- Native reduced-motion emulation, every team-edit branch, every decline/revision branch, and every provider-disable combination were not exhaustively exercised.
- Direct `file://` navigation is blocked by the in-app browser's protocol policy, so direct-file launch was not verified there. The delivered HTML is fully self-contained, with a documented local-server option. No attempt was made to bypass that browser restriction.
- There is no actual provider connection, backend, sandbox allocation, repository checkout, test runner, authoritative billing, email invitation or PR action. Passing QA labels are scripted sample data.
- All local preview work-order/settings mutations were reset to the original demo data before handoff. Saved screenshots reflect that original data.
