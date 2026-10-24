# legos-factory — proposed decisions and open architecture questions

Status: proposed handoff decisions; prototype only. 24 October 2026.

## ADR-001: Use a hybrid deterministic workflow with bounded agent judgments

**Decision:** model lifecycle transitions, policy, budgets, dependency scheduling and evidence gates as explicit workflow rules. Use agents for drafting, implementation, analysis, specialist review and ambiguity detection within those rules.

**Why:** prompts alone cannot reliably enforce authorization, resource accounting, lifecycle invariants or reproducible handoffs. A deterministic shell makes failures and recovery states visible without pretending code quality is deterministic.

**Production consequence:** durable events, idempotent transitions, revision identities, leases and interruption recovery are required. This prototype has only local state and event samples; it is not that orchestration engine.

## ADR-002: Consolidate work beneath projects and contextual order tabs

**Decision:** one Factory entry handles project filtering, orders, runs, QA and PR decisions. Overview monitors; Settings holds integrations. Use contextual tabs rather than competing top-level modules.

**Trade-off:** fewer navigation destinations, but the order must preserve clear stage identity and deep links. The prototype does not implement URL routing/deep links; production should.

## ADR-003: Separate model policy, personal preference and task selection

**Decision:** admins set allowed models/providers and workspace Foreman/Architect defaults. Personal governance preferences must be allowed. Each order chooses its Builder/Inspector and records the resolved governance models.

**Why:** teams need stable governance and accountable cost while allowing per-task optimization. Model defaults/preferences affect new work. Tightened policy still gates active execution; it does not silently replace a running model.

**Open:** profile migration, specialist reviewer multiplicity, provider outages/fallback, execution identity, authorization revocation and recording exact model versions. No fallback silently crosses connection ownership.

## ADR-004: Verification contracts are repository-specific; evidence is revision-specific

**Decision:** compose universal acceptance/scope requirements with language/repository checks. Final evidence must identify source revision, test inputs/commands, outputs, exit statuses and reviewer identity. New implementation revisions invalidate quality approval.

**Why:** Go, Java, Rust, Node, TS and Vue do not share one meaningful universal test command. Superficial coverage counts cannot establish that assertions verify the intended behavior.

**Open:** profile onboarding, untrusted repository commands, sandbox security, deterministic check runners, artifact retention, flaky-check policy and human override accountability. Java/Node standalone profiles are not included in this sample.

## ADR-005: Progressive autonomy never bypasses evidence and final human decisions

**Decision:** policy caps the level a task may request. Guided requires each explicit step; assisted runs after plan approval; delegated may start after Architect review under policy. Ambiguity, cost/resource violations and unresolved findings stop progress. Final PR decision is human in every level.

**Prototype boundary:** guided uses manual Next step; assisted/delegated have explicit Auto-play. It runs only while that order is open, not as background scheduling. The demo QA fix is a single atomic simulated fix-and-recheck action rather than a real worker job.

## ADR-006: Make provider ownership and spending identities explicit

**Decision:** separate personal and shared connections. Account each execution to a user and workspace. Enforce concurrency/resource ceilings and per-user budgets before additional work.

**Open:** authenticated provider support, subscription licensing/terms, OpenCode APIs, credential storage, per-user isolation, reservations for concurrent in-flight usage, reconciliation, price drift, external rate limits and emergency stops. No credential sharing or production auth approach is approved by this prototype.

## ADR-007: Keep Cataloger exploratory and provenance-led

**Decision:** expose an opt-in sample preview. A future system could propose source-linked decisions, patterns and change records for human curation. Do not assume every event deserves permanent memory.

**Open:** purpose, accepted record types, tenant/repository ACL inheritance, redaction, retention, freshness, supersession, corrections, deletion and retrieval quality. No ingestion, vector storage, or agent-memory architecture is chosen here.

## ADR-008: Ship this prototype as one offline HTML artifact

**Decision:** inline original SVGs, CSS, JavaScript and sample data in `index.html`, with no third-party dependency or real adapters.

**Why:** the user can open or hand off the prototype without installing a toolchain or connecting accounts. Local storage makes interactions persistent and resettable.

**Trade-off:** this is deliberately not a production frontend architecture. Subsequent production work should choose a framework, routing, typed state model, backend contracts and test boundaries from the validated product flow. Do not treat this file as proof of backend feasibility.
