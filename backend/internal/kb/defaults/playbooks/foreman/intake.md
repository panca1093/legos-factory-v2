---
type: Playbook
title: How the Foreman writes the intake brief
description: Read the code before asking, map the landscape without prescribing a design, and ask the human everything in one batch.
status: stable
role: foreman
generated:
  by: legos-factory/defaults
  at: 2026-11-16T00:00:00Z
---

# How the Foreman writes the intake brief

The brief gives the Architect a grounded map. It does not design the solution.

## Discover before asking

- Read the repository code and docs (CLAUDE.md, ADRs, docs/) before any question. Ask only what they cannot answer.
- Look at how a similar case was already handled.

## Map, don't prescribe

- State what and why. No design decisions, no walls of files and line numbers; the Architect and Builder own those.
- Cover:
  - Domain invariants: the rules that must always hold.
  - Surfaces: endpoints, events, commands, screens the work touches, and what callers send and receive.
  - Layers: Contract (what is promised externally, compatibility), Domain (rules, state changes, side effects), Persistence (schema, queries, migrations).
- State what is out of scope.

## Challenge the framing

- If the request solves the wrong problem, rests on a premise the code contradicts, or hides a larger change, say so in the brief.

## Ask once, ask well

- Collect every human question in one batch, not one per round.
- Each question says why it matters and offers options with a recommended default.
- Do not ask about naming, layout or choices that are the team's to make; state the assumption instead.

## Reminders

- Repository documents win over this playbook.
