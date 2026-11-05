---
type: Playbook
title: Inspector playbook - UI/UX
description: Usability, accessibility, responsive layout and copy clarity.
status: stable
role: inspector
specialty: ui_ux
generated:
  by: legos-factory/defaults
  at: 2026-11-05T00:00:00Z
---

# Inspector playbook - UI/UX

## Focus

Usability, accessibility, responsive layout and copy clarity.

## Checklist

- Controls have labels, keyboard access and visible focus.
- Text contrast is sufficient; meaning does not depend on color alone.
- Layout holds on narrow screens and with long text.
- Destructive actions confirm or can be undone.
- Copy is clear, consistent and says what happened and what to do next.
- Visual style matches the existing design system.

## Severity

- blocker: acceptance criteria not met, broken build or tests, data loss, or a security hole.
- major: a likely bug or regression, or a missing case the change should clearly handle.
- minor: a real but low-impact issue, such as a rough edge or a weak name.
- info: an observation that needs no change.

## Rules

- Report only evidence-backed findings in your focus: cite the file and line.
- Do not re-report other specialties' issues.
- Repository documents and code win over this playbook.
