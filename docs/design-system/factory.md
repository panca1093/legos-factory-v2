# Factory flow

The factory screens follow the Foreman-led work-order flow in `docs/foreman-flow/SPEC.md`. That flow is in implementation, and the design takes its rules as given.

## What the design must make obvious

1. **Who acted.** Every event, stage and decision carries an **actor badge** in one of four kinds. They differ in shape and fill, not just hue:

| Kind | Badge | Who | Means |
| --- | --- | --- | --- |
| Code | `{ }` on `surface-2`, square | `factory` (Go) | Deterministic: state machine, checks, gates, truth tables, PRs |
| Agent | Brick character on its `crew-*` fill, round | Foreman, Architect, Builder, Inspector | Wrote something: brief, spec, plan, code, findings, notes |
| Judgment | **J** on `ink`, square | Jev (TypeSafe System One) | Answered typed questions with probabilities. Never decides alone |
| Human | Initials on `tint`, round | You | Answered an escalation, or opened the PR |

2. **Jev judges, code decides.** A gate always shows three parts in order: the Foreman's *notes* (no decision), Jev's *answers* (each question, its probability, and yes / no / unsettled at 0.7 and 0.3), and the *truth-table row* that code matched, with its outcome. Never show an outcome without its row.

3. **People are asked only when needed.** There is one mode: an approving gate starts the build with no click. The **escalation banner** (warning) is the only place the flow asks for a decision. It names the reason (for example "the plan touches a database schema") and offers exactly the choices the SPEC allows:

| Escalation | State | Choices |
| --- | --- | --- |
| Plan gate escalated | `WAITING_FOR_HUMAN` | Approve plan and build · Request changes (resets the revision counter) |
| Architect question the Foreman can't answer | `WAITING_FOR_HUMAN` | Answer (required text) |
| Done gate escalated | `NEEDS_HUMAN_REVIEW` | Accept result · Send back to Builder |
| Ready | `READY` | Open PR · Request a fix first |

4. **Budgets are visible.** Plan revisions and fix rounds each show *n of 2*. Fix rounds count failed checks and done-gate fixes together. When a budget is spent, revise or fix becomes escalate, and the UI says so in the decision row.

5. **Pause and Cancel stay manual.** Both appear on every running order. Cancel asks for a second click.

## The nine stages

`Legos.stages` holds them in order, with who acts in each:

Prepare (code) → Intake (Foreman) → Spec & plan (Architect, with Foreman-answered questions) → Plan gate (Foreman notes, Jev, code) → Build (Builder, one task at a time, in dependency order, within scope) → Checks (code; failures go back to the Builder) → Review (Inspector specialists ∥ Architect verification) → Done gate (Foreman notes, Jev, code) → PR (you).

The **stage stepper** shows them as nine cells: done (`chart-1` bar), current (`info`, pulsing), waiting on you (`warning` cell), and not reached (`chart-3`). Lists use the compact **dots** version.

## Work order page

The page has a stepper, then the banner (current activity, or an escalation), then five tabs with a side panel:

- **Blueprint**: Foreman brief (constraints, risks, suggested task shape), Architect spec with a revision tag, and the plan's tasks, each with its `scope` and dependencies.
- **Task graph**: tasks in dependency columns, with live status.
- **Activity**: the event timeline, filterable by actor kind. Jev events expand to show the full gate.
- **Quality**: checks (runs and baseline), Architect verification per criterion (met / not met / unclear, with evidence) and deviations, and Inspector findings.
- **Changes & PR**: files with + / −, and the PR decision.
- **Side panel**: acceptance criteria with their verdicts, budgets, Jev status (model `jev-1.14.0`, gate runs, cost), and the Foreman session (open from intake to the plan gate, closed afterwards).

**Jev off.** When there is no `TYPESAFE_API_KEY`, every plan escalates and the done gate uses the deterministic rules. Say this wherever a gate would appear, never leave it silent.

## Chat is a door, not a replacement

Chat in **Factory** mode, the **New work order** form, and the Agent tool `create_work_order` all open the same work order. In chat it appears as a live work-order card (dots, current activity, and inline escalation choices) that links to the full page.
