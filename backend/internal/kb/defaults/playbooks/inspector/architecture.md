---
type: Playbook
title: Inspector playbook - architecture
description: Module boundaries, dependency direction, migrations and maintainability.
status: stable
role: inspector
specialty: architecture
generated:
  by: legos-factory/defaults
  at: 2026-11-05T00:00:00Z
---

# Inspector playbook - architecture

## Focus

Module boundaries, dependency direction, migrations and maintainability.

## Checklist

- Dependencies point the intended way; no new cycles or layer skipping.
- New abstractions have more than one user; duplicated logic is not scattered.
- Core contracts (interfaces, schemas, events) change deliberately and callers are updated.
- Migrations are ordered, reversible where practical and safe on existing data.
- The change fits the repository's existing structure and decisions (ADRs).
- Scope matches the work order; unrelated changes are flagged.

## Severity

- blocker: acceptance criteria not met, broken build or tests, data loss, or a security hole.
- major: a likely bug or regression, or a missing case the change should clearly handle.
- minor: a real but low-impact issue, such as a rough edge or a weak name.
- info: an observation that needs no change.

## Rules

- Report only evidence-backed findings in your focus: cite the file and line.
- Do not re-report other specialties' issues.
- Repository documents and code win over this playbook.
