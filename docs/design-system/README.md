# Legos

Calm, deep-green workspace for people and their agents: chat, research and build in one place.

Legos started as the factory UI in `legos-factory-v2` (violet on lavender, `frontend/src/styles/tokens.css`). This system keeps that structure (the radii, sizes, rail, pills and the brick crew) and moves it onto the **Emerald Depth** palette. It also adds **chat** as a first-class surface that works for any question, not only factory work orders.

## Voice and content

- **Plain and short.** Say what happened and what is next: “3 things need your OK. Agents finished 6 tasks overnight.”
- **Agents are colleagues, not magic.** Name the agent that acted (“Builder · Step 2 of 4”) and show the steps it took.
- **Ask only when it matters.** Gates decide routine steps on their own. A person is asked when a gate escalates (with the reason: “The plan touches a database schema”), before anything is sent, and to open a PR.
- **Show who did what.** Every event carries an actor badge: code, agent, Jev or you.
- **Verbs on buttons.** “Approve and build”, “Request changes”, “Open chat”. Never “OK”.
- **Sentence case** everywhere, including pills (“Needs your input”). Numbers in figures.
- **Empty states reassure:** “All clear. Nothing waits on you.”

## Visual foundations

**Colour.** Six palette steps from `emerald-950` (#051F20) to `emerald-100` (#DAF1DE). Use them through the semantic tokens:

| Role | Light | Emerald night |
| --- | --- | --- |
| `canvas` page | #F2F6F3 | #051F20 |
| `surface` cards | #FFFFFF | #0B2B26 |
| `ink` text | #051F20 | #DAF1DE |
| `brand` primary, user bubble | #163832 | #8EB69B |
| `tint` selected ground | #DAF1DE | #1D4A40 |

- Green is the brand. Status uses its own tokens (`info`, `warning`, `success`, `danger`, each with a `-soft` ground), so “success” never looks like “brand”.
- `emerald-300` sage is decorative on white (2.3:1). Never use it for text there.
- Crew colours (`crew-foreman` … `crew-cataloger`) come from the codebase. They are identity fills for avatars and dots only, never text.
- Every text pair in the tokens passes 4.5:1 in both themes. Control borders use `line-strong` (3:1). Hairlines use `line`.

**Type.** Two families. **Outfit** (Google Fonts) for display, view titles and KPI numbers: soft geometric, like the references. The **system UI stack** (the codebase’s `--font`) for everything else, at 14/21. Mono for commands and paths. Weights: 400 body, 550 labels, 650 card titles, 500 display.

**Shape.** Rounded and soft: `radius-xl` 18px cards (the codebase’s `--radius`), `radius-md` 10px buttons, `radius-xs` 6px pills, `radius-full` for the pill nav, chips, send button and avatars. Bubbles and the composer use `radius-2xl` 24px.

**Depth.** Cards sit on `canvas` with a hairline and `shadow-card`. Only floating things (menus, the focused composer, toasts) get `shadow-pop`.

**Space.** 4px base: `space-4` 16px between cards, `space-5` 24px inside them, `space-6` 32px between sections.

**Motion.** Quick and purposeful. Durations live in bundle.css: `--dur-fast` 140ms (hover, press), `--dur` 240ms (thumbs, pills), `--dur-slow` 420ms (messages, rows leaving). Easing `--ease-out`, with `--ease-spring` for small pops. Patterns:

- new messages, steps and chips **rise** 8px and fade in, staggered 40–60ms
- running work **pulses** (dot), **shimmers** (progress) or **sweeps** (tool step)
- completed work **pops** a check; resolved rows **slide out**
- numbers **count up** once on load; replies **stream** with a caret
- `prefers-reduced-motion` turns all of this off

## Chat beyond the factory

Chat is the front door. One composer, three modes:

| Mode | Does | Shows in the thread |
| --- | --- | --- |
| **Ask** | answers from what it knows plus attached context | a streamed reply |
| **Agent** | uses tools: search, read, run, browse | tool steps, then a reply |
| **Factory** | opens a work order in the Foreman-led factory flow | a live work-order card, plus an escalation only when a gate needs you |

Chat is an extra door into the factory, not a replacement for it. The factory keeps its own screens, which follow `docs/foreman-flow/SPEC.md`: see the **Factory flow** section.

Threads carry a **scope** (General, Factory, Research, Personal, Knowledge) shown as a coloured square in the list and a pill in the header. Context chips (a project, files, the web) sit above the text. The **ChatWorkspace** and **HomeDashboard** prototypes show these patterns working together.

## Iconography

1.7px-stroke outline icons on a 24px grid with round caps and joins, drawn in `currentColor`. They ship in bundle.js as `Legos.icon(name)` (home, chat, factory, book, search, bell, plus, send, check, clock, shield, spark, globe, code, doc, sheet…). In KPI labels they sit in a 28px outlined square. The **brick crew** (Crew asset group) are the agents’ faces: Foreman, Architect, Builder, Inspector, Cataloger. They are copied unchanged from `BrickBot.vue`.

## References

The References asset group holds the four inputs this system was built from: the Emerald Depth palette and three dashboards (a learning dashboard, a restaurant dashboard, an agent workspace). We took the white cards on a soft canvas, the pill navigation, the big light numerals and the approval and running lists. We did not copy their brands or content.

## Using it

Load `tokens.css`, then `components/bundle.css` (which imports Outfit), then `components/bundle.js` for the motion helpers. Wrap the UI in `.lg`. Components are plain CSS classes (`lg-btn`, `lg-card`, `lg-msg`…), so they drop into the Vue app without a wrapper. Each component card shows its markup and rules.
