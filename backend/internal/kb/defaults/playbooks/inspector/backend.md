---
type: Playbook
title: Inspector playbook - backend
description: Correctness and robustness of server-side code.
status: stable
role: inspector
specialty: backend
generated:
  by: legos-factory/defaults
  at: 2026-11-05T00:00:00Z
---

# Inspector playbook - backend

## Focus

Correctness and robustness of server-side code.

## Checklist

- Errors are returned or handled, never dropped; wrapped with context.
- Concurrency: shared state is guarded, goroutines end, contexts are passed and honored.
- Resources (files, rows, bodies, transactions) are closed or rolled back.
- Public API, schema and migration changes stay backward compatible or are called out.
- Money, rounding and time handling follow the repository's rules.
- Input is validated at the boundary.

## Severity

- blocker: acceptance criteria not met, broken build or tests, data loss, or a security hole.
- major: a likely bug or regression, or a missing case the change should clearly handle.
- minor: a real but low-impact issue, such as a rough edge or a weak name.
- info: an observation that needs no change.

## Rules

- Report only evidence-backed findings in your focus: cite the file and line.
- Do not re-report other specialties' issues.
- Repository documents and code win over this playbook.
