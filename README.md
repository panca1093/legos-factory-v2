# legos-factory — interactive frontend prototype

A standalone, dependency-free browser demo of a multi-project autonomous software engineering factory. Open `index.html` directly in a modern browser. All visuals, styles, sample data, and behavior are embedded in that file; no install or internet access is needed.

For consistent browser storage across sessions, optionally serve this directory:

```sh
python3 -m http.server 8765 --bind 127.0.0.1
```

Then open `http://127.0.0.1:8765`. Stop the server with Ctrl+C.

## A useful five-minute walkthrough

1. **Overview:** meet the five original brick characters and inspect monitoring across three sample projects.
2. Open **Make payment retries idempotent**. Answer Architect’s clarification. Approve the blueprint.
3. In **Task graph**, inspect a task brick, pause/resume the run, then use **Next step** three times.
4. Inspector surfaces a deliberately failing acceptance check. Inspect the evidence, then **Ask Builder to fix & recheck**.
5. Continue to **Changes & PR**, inspect the illustrative diff, and approve, request changes, or decline the demo PR.
6. Create a **New work order**. Select a project/repository, describe acceptance criteria, and choose Builder/Inspector models and autonomy. Draft the blueprint and run Architect review.
7. Explore **Settings**: admin governance, policy-constrained personal preferences, team roles and per-user limits, shared/personal demo connections, budgets, concurrency and sandbox resources.
8. Turn on **Cataloger’s optional preview** to inspect sample knowledge records and source links.

**Auto-play** advances assisted/delegated demo runs every 2.5 seconds while the order is open. Guided runs require each step to be advanced explicitly. Auto-play stops when navigating away, pausing, encountering a finding, or reaching a policy limit. Hidden tabs and open dialogs do not advance.

## Boundaries

- No backend, real agent execution, repository operations, OAuth, provider calls, emails, real tests, PR creation, or merges.
- All costs, usage, model catalog entries, resources, evidence, checks, and results are illustrative.
- The three model families are a demo catalog, not a current pricing/capability recommendation or a live OpenCode listing.
- State persists in this browser with `localStorage`. Direct-file storage behavior varies by browser; if storage is unavailable, the page still runs in memory. Use **Settings → My preferences → Reset demo** to restore samples.
- The role selector previews UI restrictions; it is not production authorization. Other sample members have editable limits but do not have executable user sessions.
- Every project has one sample repository. The picker updates the repository when the project changes.
- Cataloger is experimental and does not ingest or automatically promote any data.
- The source chat’s user messages and attached screenshot were available. Earlier assistant messages were opaque content references, so these documents describe this prototype and proposed decisions, not recovered approved documents.

## Contents

- `index.html`: complete runnable prototype.
- `PRD.md`: proposed product scope, user journeys and acceptance criteria.
- `ADR.md`: proposed architectural and governance decisions.
- `UX-SPEC.md`: screen map, transition model and interaction/accessibility notes.
- `VERIFICATION.md`: actual verification performed and limits.
- `preview-*.png`: verified browser views.

No external assets, trademark logos, telemetry, or remote fonts are used. Character artwork is original inline SVG. Copyright/trademark affiliation is not implied.
