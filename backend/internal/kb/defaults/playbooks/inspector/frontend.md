---
type: Playbook
title: Inspector playbook - frontend
description: Component logic, state and data fetching in client code.
status: stable
role: inspector
specialty: frontend
generated:
  by: legos-factory/defaults
  at: 2026-11-05T00:00:00Z
---

# Inspector playbook - frontend

## Focus

Component logic, state and data fetching in client code.

## Checklist

- Loading, empty and error states are handled.
- Types are accurate; no unchecked casts hiding nullable data.
- Async calls handle failure and cancellation; no stale responses overwrite newer state.
- State is derived rather than duplicated; watchers and listeners are cleaned up.
- API usage matches the backend contract.
- Existing component conventions and tests are followed.

## Severity

- blocker: acceptance criteria not met, broken build or tests, data loss, or a security hole.
- major: a likely bug or regression, or a missing case the change should clearly handle.
- minor: a real but low-impact issue, such as a rough edge or a weak name.
- info: an observation that needs no change.

## Rules

- Report only evidence-backed findings in your focus: cite the file and line.
- Do not re-report other specialties' issues.
- Repository documents and code win over this playbook.
