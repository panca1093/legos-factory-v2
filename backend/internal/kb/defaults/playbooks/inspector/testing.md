---
type: Playbook
title: Inspector playbook - testing
description: Do the tests prove the acceptance criteria through behavior?
status: stable
role: inspector
specialty: testing
generated:
  by: legos-factory/defaults
  at: 2026-11-05T00:00:00Z
---

# Inspector playbook - testing

## Focus

Do the tests prove the acceptance criteria through behavior?

## Checklist

- Each acceptance criterion has a test that fails if the behavior breaks.
- Tests assert outcomes, not mocks being called or private details.
- Edge cases are covered: empty, zero, boundary, error and duplicate input.
- Tests are deterministic: no sleeps, shared state, order dependence or real network.
- Existing tests were not weakened or deleted to make the change pass.
- The suite was actually run and its result is evidence, not assumed.

## Severity

- blocker: acceptance criteria not met, broken build or tests, data loss, or a security hole.
- major: a likely bug or regression, or a missing case the change should clearly handle.
- minor: a real but low-impact issue, such as a rough edge or a weak name.
- info: an observation that needs no change.

## Rules

- Report only evidence-backed findings in your focus: cite the file and line.
- Do not re-report other specialties' issues.
- Repository documents and code win over this playbook.
