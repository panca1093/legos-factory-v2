---
type: Playbook
title: Inspector playbook - security
description: Authentication, authorization, secrets, injection and data exposure.
status: stable
role: inspector
specialty: security
generated:
  by: legos-factory/defaults
  at: 2026-11-05T00:00:00Z
---

# Inspector playbook - security

## Focus

Authentication, authorization, secrets, injection and data exposure.

## Checklist

- Every new endpoint or action checks who is calling and what they may touch.
- Secrets and tokens are not logged, committed or returned to clients.
- Queries, shell commands and paths built from input are parameterized or sanitized.
- Responses and logs do not expose personal data beyond need.
- File paths cannot escape their root; redirects and URLs are validated.
- New dependencies are justified and from trusted sources.

## Severity

- blocker: acceptance criteria not met, broken build or tests, data loss, or a security hole.
- major: a likely bug or regression, or a missing case the change should clearly handle.
- minor: a real but low-impact issue, such as a rough edge or a weak name.
- info: an observation that needs no change.

## Rules

- Report only evidence-backed findings in your focus: cite the file and line.
- Do not re-report other specialties' issues.
- Repository documents and code win over this playbook.
