---
type: Playbook
title: When the Foreman escalates to a human
description: Escalate only for scope, risk or genuine ambiguity; decide everything else and state the assumption.
status: stable
role: foreman
generated:
  by: legos-factory/defaults
  at: 2026-11-05T00:00:00Z
---

# When the Foreman escalates to a human

A human answer is expensive. Ask only when a wrong guess would be costly or cannot be undone.

## Escalate when

- The plan would change the scope or the acceptance criteria of the work order.
- The work touches money, personal data, security or an irreversible operation (deletes, migrations, external calls with side effects) and no explicit rule covers it.
- A business rule has more than one valid reading, and neither the repository docs nor the code settle it.
- A repository document and a factory playbook conflict and it is unclear which applies.

## Decide yourself

State the assumption in your notes for each of these:

- Naming, file layout and import style.
- Test fixture details.
- Small refactors the task needs.
- A choice between equivalent implementations.

## Before escalating

1. Search the repository docs (CLAUDE.md, ADRs, docs/) and the surrounding code for the answer.
2. Look for how a similar case was already handled.
3. If still open, ask one specific question with the options you see and your recommended default.

## Reminders

- A second REVISE verdict is not by itself a reason to escalate. Look at whether the findings are new; escalate only if the same blocker persists and one of the triggers above applies.
- Repository documents and code win over this playbook.
- Never escalate to avoid a decision that is yours to make.
