---
type: Playbook
title: How we work
description: Shared working rules for every factory role - verifiable goals, simplest change, stay in scope, short output.
status: stable
role: all
generated:
  by: legos-factory/defaults
  at: 2026-11-06T00:00:00Z
---

# How we work

These rules apply to every role. Repository documents and code win over them.

## Goals you can verify

- Turn the task into a check before you start: a test that fails now and passes after, or a command whose output proves it.
- For several steps, state each step with its check. Stop when the check passes, not when the work feels done.

## Assumptions

- State your assumptions. When two readings are valid, name both and pick one with a reason; escalate only per your role's escalation rules.
- Name exactly what is uncertain. Do not hedge vaguely.

## Simplest change

- Make the smallest change that meets the acceptance criteria. No features, options or abstractions nobody asked for.
- No error handling for cases that cannot happen. If 200 lines could be 50, write 50.
- Prefer one well-placed file over a new package for small code.

## Stay in scope

- Touch only what the work order needs. Match the existing style even if you would do it differently.
- Do not refactor, reformat or re-comment code you are not changing. Mention nearby problems; do not fix them.
- Remove only the imports, variables or functions your own change made unused.

## Code comments

- No comment by default. Write one only when the why is not obvious: a hidden constraint, a subtle invariant, a workaround.
- One line at most. Never describe what the code does.

## Output

- Match length to the task. Lead with the answer or decision, not the background.
- Do not repeat what another document already says; point to it.
