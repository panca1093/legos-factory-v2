# legos-factory — proposed PRD

Status: prototype handoff, 24 October 2026. Product decisions below follow the user’s current brief; backend and integration details remain proposals.

## Problem and intended outcome

Engineering work becomes hard to supervise when projects, work orders, runs, reviews, model selection, cost, and evidence live in separate places. legos-factory offers a single multi-project workspace that turns an outcome into a reviewed blueprint, bounded execution, inspectable evidence, and an explicit human PR decision.

The product should help an engineer answer: What is happening? What needs my decision? What changed? Why should I trust it? How much authority and budget did I delegate?

## Users and roles

- Admin: workspace policy, shared provider connections, allowed models, governance defaults, membership, budgets and execution capacity.
- Member: create orders, select permitted task models, choose autonomy within policy, clarify requirements, inspect evidence and make authorized decisions.
- Viewer: inspect orders, plans, runs and evidence without work mutations.

The prototype uses one active demo identity and a role preview. A production multi-tenant permission model is not implemented.

## Product structure

**Overview** is greetings and monitoring only. It shows a five-persona crew, decisions waiting, active runs, assembled work, usage, projects and sandbox condition. Work management happens in **Factory**.

**Factory** consolidates projects, work orders, runs, reviews and PR decisions. Users filter by project/state, search, switch board/list view, and open an order with Blueprint, Task graph, Activity, Quality, and Changes & PR tabs.

**Settings** contains governance, personal preferences, team/access, integrations, and usage/resources. Integrations do not need a separate primary menu.

**Cataloger** is an optional future/experimental preview. It illustrates provenance, not an agreed ingestion or retrieval scope.

## Agent responsibilities

| Persona | Responsibility | Authority boundary |
|---|---|---|
| Foreman | Orchestration, DAG dependencies, gates, escalation | State changes constrained by workflow and policy |
| Architect | Plan/spec review, acceptance mapping, ambiguity detection | Missing contracts return to a human |
| Builder | Implementation and behavioral checks | Approved scope and isolated execution |
| Inspector | Specialist code/testing review and evidence inspection | Findings retain impact, evidence and disposition |
| Cataloger | Potential curated decision/pattern/change records | Future; no automatic truth promotion |

## Primary journey

1. Create an outcome with project/repository and observable acceptance criteria.
2. Choose Builder and Inspector models for that order. Inherit Foreman and Architect model snapshots from workspace policy plus permitted user preferences.
3. Foreman drafts a blueprint and dependency graph. Architect reviews scope, coverage and missing decisions.
4. Human clarification becomes a recorded contract. Architect re-reviews it.
5. Depending on autonomy, a human or workspace policy authorizes eligible execution.
6. Builder records a failing baseline, implements within scope, and produces tests and change evidence.
7. Inspector checks the final revision. Findings block the PR handoff until resolved.
8. Human reviews the evidence and diff, then approves, requests changes or declines. Production PR creation/merge permissions need separate design.

## Progressive autonomy

- Guided: human approves the blueprint and explicitly advances each build step.
- Assisted: human approves the blueprint; execution and required checks may then continue within scope.
- Delegated: Architect-reviewed, unambiguous plans may proceed under workspace policy; unresolved requirements still require a human.

All levels retain hard stops for policy, budgets, dependencies, unresolved findings and final PR decisions. The demo requires an explicit Auto-play control to advance assisted/delegated simulation automatically; it is not a background execution service.

## Quality strategy

Style instructions and TDD prompts are inputs, not guarantees. Confidence should come from repository-specific quality profiles, acceptance-to-test mapping, meaningful assertions, expected baseline failures, final-revision checks, diff review and unresolved-finding gates. Go, Rust and TypeScript/Vue sample profiles show how this can vary by repository. Real profiles must be discovered and confirmed during onboarding.

Production measurement proposals: verified acceptance coverage, escaped defects, false-positive findings, rework after inspection, human overrides, cost per accepted change and abandoned-run cost. These are not calculated by this prototype.

## Cost and resource behavior

Users choose implementation/review models only from policy-approved providers/models. Admins own the Foreman/Architect defaults and model allowlist; personal preferences cannot expand it. Personal/shared connection ownership must be explicit. Per-user monthly budgets and workspace concurrency gate execution. Resource ceilings bound each sandbox. Exhausted budgets pause work; capacity queues work; stricter policy pauses affected active work.

The prototype has illustrative fixed step costs and sample usage. Production requires authoritative provider accounting, reservations, reconciliation and enforcement across workers. Subscription authentication through OpenCode remains unvalidated and must not imply multi-user account sharing is acceptable or supported.

## Acceptance criteria for the prototype

- Three projects with distinct repositories and language profiles are navigable in one Factory view.
- An order can be created and taken through planning, clarification, execution, QA remediation and a human decision.
- Builder/Inspector choices are per task; Foreman/Architect defaults and preferences obey workspace policy.
- Budget, concurrency, role and provider policy controls affect demo execution.
- Revision requests invalidate quality approval. Previous evidence is not silently reused as current.
- Search, filters, themes, dialogs, source links, downloads and reset have functional results.
- Desktop, tablet and mobile layouts, visible focus, labeled controls, trapped dialogs and reduced-motion styling are included.
- All demo execution and external-adapter boundaries are clear.

## Non-goals and unresolved decisions

No backend, provider authentication, actual agents, repository mutation, real quality evidence, billing, real PR delivery or production multi-tenancy. Open: supported auth/terms, model discovery and pricing, team/repository access matrix, escalation rights, cost reservations, run retry/recovery, evidence retention, merge/publish authority, and Cataloger’s scope, permissions, freshness and corrections.
