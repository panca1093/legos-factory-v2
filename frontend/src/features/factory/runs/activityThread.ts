import type { Run, Task, WOEvent, WorkOrder } from "../../../types/api";
import { stages, stageOfState } from "../../../composables/presentation";

export type DocSection =
  "outcome" | "brief" | "acceptance" | "spec" | "tasks" | "scope" | "decision";
export interface ThreadDoc {
  title: string;
  section: DocSection;
  text?: string;
}
export interface ThreadFinding {
  severity: string;
  title: string;
  detail?: string;
}
export interface ThreadPill {
  label: string;
  tone: "" | "green" | "orange" | "red";
}
export interface ThreadItem {
  id: string;
  kind: "message" | "system" | "divider";
  from: string;
  to: string;
  type: string;
  text: string;
  at: string;
  docs?: ThreadDoc[];
  quotes?: string[];
  findings?: ThreadFinding[];
  pill?: ThreadPill;
}

const str = (v: unknown) => (typeof v === "string" ? v : "");
const strings = (v: unknown): string[] =>
  Array.isArray(v) ? v.map((x) => str(x)).filter(Boolean) : [];
const objects = (v: unknown): Record<string, unknown>[] =>
  Array.isArray(v)
    ? v.filter(
        (x): x is Record<string, unknown> => !!x && typeof x === "object",
      )
    : [];
const firstLine = (s: string) => s.split("\n", 1)[0]!.trim();
const pad = (n: number) => String(n).padStart(2, "0");

const gateTarget = (gate: string) =>
  gate === "plan" ? "architect" : "builder";

// Check events are folded into one summary line per verification pass.
function checkOutcome(kind: string) {
  return kind.slice("check_".length);
}
const checkLabels: Record<string, string> = {
  pass: "passed",
  fail: "failed",
  failed_before_change: "failed before the change",
};

export function activityThread(
  events: WOEvent[],
  order: WorkOrder,
  tasks: Task[],
  runs: Run[] = [],
): ThreadItem[] {
  const sorted = [...events].sort((a, b) => a.id - b.id);
  const taskByID = new Map(tasks.map((t, i) => [t.id, { t, n: i + 1 }]));
  const runRole = new Map(runs.map((r) => [r.id, r.kind]));
  for (const e of sorted) {
    const id = e.payload?.run_id;
    if (e.kind === "agent_started" && typeof id === "string")
      runRole.set(id, e.actor);
  }

  const out: ThreadItem[] = [];
  let stage = 0,
    topStage = -1;
  let at = order.created_at;
  const divide = (id: string) => {
    if (stage <= topStage) return;
    topStage = stage;
    out.push({
      id: `divider-${stage}-${id}`,
      kind: "divider",
      from: "",
      to: "",
      type: "Stage",
      text: stages[stage] ?? "",
      at,
    });
  };
  const push = (
    item: Omit<ThreadItem, "id" | "at"> & { id: string; at?: string },
  ) => {
    divide(item.id);
    out.push({ ...item, at: item.at ?? at });
  };

  let revision = 0,
    pendingRevision = 0,
    pending: string[] = [],
    lastQuestion = "",
    lastAsker = "architect",
    checks: Record<string, number> = {},
    checksAt = "",
    checksID = "",
    group: { id: string; at: string; rows: ThreadFinding[] } | null = null,
    lastGroup: string[] = [];

  const lastHumanAnswer = [...sorted]
    .reverse()
    .find(
      (e) => e.kind === "clarification_answered" && e.actor === "human",
    )?.id;
  const latestPlan = [...sorted]
    .reverse()
    .find((e) => e.kind === "plan_ready")?.id;
  const buildersTasked = new Set<string>();
  push({
    id: "request",
    kind: "message",
    from: "you",
    to: "foreman",
    type: "Request",
    text: order.title || order.objective,
    at: order.created_at,
    docs: [
      ...(order.objective
        ? [{ title: "The outcome", section: "outcome" as const }]
        : []),
      ...(order.clarif_answer && lastHumanAnswer === undefined
        ? [{ title: "Decision recorded", section: "decision" as const }]
        : []),
    ],
  });

  const flushChecks = () => {
    const keys = Object.keys(checks);
    if (!keys.length) return;
    const bad = "fail" in checks;
    push({
      id: checksID,
      kind: "system",
      from: "inspector",
      to: "",
      type: "Checks",
      at: checksAt,
      text: keys
        .map((k) => `${checks[k]} ${checkLabels[k] ?? k.replaceAll("_", " ")}`)
        .join(", "),
      pill: { label: "Checks", tone: bad ? "red" : "green" },
    });
    checks = {};
  };
  const flushFindings = () => {
    const open = group;
    group = null;
    if (!open) return;
    lastGroup = open.rows.map((r) => r.title);
    if (!open.rows.length) return;
    push({
      id: open.id,
      kind: "message",
      from: "inspector",
      to: "builder",
      type: "Feedback",
      text: `${open.rows.length} finding${open.rows.length === 1 ? "" : "s"} to fix`,
      at: open.at,
      findings: open.rows,
    });
  };
  const system = (
    e: WOEvent,
    label: string,
    tone: ThreadPill["tone"],
    text = e.message,
  ) =>
    push({
      id: String(e.id),
      kind: "system",
      from: e.actor,
      to: "",
      type: label,
      text,
      pill: { label, tone },
    });

  for (const e of sorted) {
    at = e.created_at;
    const p = e.payload ?? {};
    const id = String(e.id);
    const k = e.kind;
    if (k === "state_changed") {
      flushChecks();
      const s = stageOfState(str(p.to));
      if (s !== undefined) stage = s;
      continue;
    }
    if (k === "inspection_specialists") flushFindings();
    if (!k.startsWith("check_")) flushChecks();

    if (k === "brief_ready" || k === "brief_amended") {
      const brief = str(p.brief) || strings(p.amendments).join("\n");
      push({
        id,
        kind: "message",
        from: "foreman",
        to: "architect",
        type: "Task",
        text: k === "brief_ready" ? "Brief ready for the Architect" : e.message,
        docs: brief
          ? [{ title: "Foreman's brief", section: "brief", text: brief }]
          : undefined,
      });
    } else if (k === "plan_ready") {
      const reply = pendingRevision > 0;
      const current = e.id === latestPlan;
      push({
        id,
        kind: "message",
        from: "architect",
        to: "foreman",
        type: reply ? `Reply · revision ${pendingRevision}` : "Spec",
        text: str(p.summary) || e.message,
        quotes: reply && pending.length ? pending : undefined,
        docs: current
          ? [
              { title: "Acceptance contract", section: "acceptance" },
              ...(order.spec
                ? [{ title: "Architect's spec", section: "spec" as const }]
                : []),
              { title: "How the crew will build it", section: "tasks" },
              { title: "Scope boundaries", section: "scope" },
            ]
          : undefined,
      });
      pendingRevision = 0;
      pending = [];
    } else if (k === "plan_reviewed") {
      const findings = objects(p.findings).map((f) => ({
        severity: str(f.severity) || "info",
        title: str(f.issue) || str(f.title) || str(f.cites),
        detail: str(f.fix) || undefined,
      }));
      const raw = objects(p.findings);
      pending.push(
        ...findings
          .filter(
            (f, i) =>
              f.title &&
              ["critical", "high"].includes(f.severity) &&
              str(raw[i]?.layer) === "contract",
          )
          .map((f) => f.title),
      );
      push({
        id,
        kind: "message",
        from: "foreman",
        to: "architect",
        type: "Feedback",
        text: findings.length
          ? `${findings.length} finding${findings.length === 1 ? "" : "s"} on the plan`
          : "Plan reviewed, no findings",
        findings: findings.length ? findings : undefined,
      });
    } else if (e.actor === "jev" && k.endsWith("_gate")) {
      const gate = str(p.gate) || k.slice(0, -"_gate".length);
      const outcome = str(p.outcome);
      const name = gate.charAt(0).toUpperCase() + gate.slice(1);
      if (outcome === "approve" || outcome === "converged") {
        pending = [];
        system(e, "Gate passed", "green", `${name} gate passed`);
      } else if (["revise", "escalate", "append"].includes(outcome)) {
        const issues = strings(p.issues);
        const note = str(p.note) || str(p.error);
        const sev = outcome === "escalate" ? "blocker" : "major";
        if (gate === "plan" && outcome === "revise")
          pending.push(...(note ? [firstLine(note)] : issues));
        push({
          id,
          kind: "message",
          from: "jev",
          to: gateTarget(gate),
          type: "Feedback",
          text: note || `${name} gate: ${outcome}`,
          findings: issues.map((title) => ({ severity: sev, title })),
        });
      }
    } else if (k === "plan_revision_requested") {
      revision = Number(p.revision) || revision + 1;
      pendingRevision = revision;
      system(e, "Revision", "orange", `Revision ${revision} requested`);
    } else if (k === "plan_revision_human" || k === "revision_requested") {
      const toPlan = k === "plan_revision_human";
      if (toPlan) {
        revision = Number(p.revision) || revision + 1;
        pendingRevision = revision;
        pending = [firstLine(e.message)];
      }
      push({
        id,
        kind: "message",
        from: "you",
        to: toPlan ? "architect" : "builder",
        type: "Feedback",
        text: e.message,
      });
    } else if (k === "clarification_asked" || k === "clarification_requested") {
      lastQuestion = e.message;
      lastAsker = e.actor;
      push({
        id,
        kind: "message",
        from: e.actor,
        to: k === "clarification_asked" ? "foreman" : "you",
        type: "Question",
        text: e.message,
      });
    } else if (k === "clarification_answered") {
      const q = str(p.question) || lastQuestion;
      if (p.repeated === true) {
        system(e, "Answer reused", "");
        continue;
      }
      push({
        id,
        kind: "message",
        from: e.actor === "human" ? "you" : e.actor,
        to: lastAsker,
        type: "Reply",
        text: e.message,
        quotes: q ? [firstLine(q)] : undefined,
        docs:
          e.id === lastHumanAnswer && order.clarif_answer
            ? [{ title: "Decision recorded", section: "decision" }]
            : undefined,
      });
    } else if (k === "agent_steered") {
      push({
        id,
        kind: "message",
        from: "you",
        to: runRole.get(str(p.run_id)) ?? "builder",
        type: "Steer",
        text: e.message,
      });
    } else if (k === "agent_started") {
      const entry = taskByID.get(str(p.task_id));
      if (e.actor === "builder" && entry && !buildersTasked.has(entry.t.id)) {
        buildersTasked.add(entry.t.id);
        push({
          id,
          kind: "message",
          from: "foreman",
          to: "builder",
          type: `Task #${pad(entry.n)}`,
          text: entry.t.title,
        });
      }
    } else if (k === "finding" || k === "inspection_specialists") {
      const m = /^\[[^\]]*\]\s*(.*)$/.exec(e.message);
      group ??= { id, at: e.created_at, rows: [] };
      group.at = e.created_at;
      if (k === "finding")
        group.rows.push({
          severity: str(p.severity) || "info",
          title: m?.[1] || e.message,
          detail: str(p.location) || undefined,
        });
    } else if (k === "inspection_finished") {
      flushFindings();
    } else if (k === "fix_requested") {
      const items = strings(p.items).map(firstLine);
      push({
        id,
        kind: "message",
        from: "foreman",
        to: "builder",
        type: `Task "Revision ${Number(p.revision) || "?"} fix"`,
        text: e.message,
        quotes: items.length ? items : lastGroup.length ? lastGroup : undefined,
      });
    } else if (k.startsWith("check_")) {
      const o = checkOutcome(k);
      if (!Object.keys(checks).length) {
        checksAt = e.created_at;
        checksID = id;
      }
      if (o === "failed_before_change" && checks.fail) {
        checks.fail--;
        if (!checks.fail) delete checks.fail;
      }
      checks[o] = (checks[o] ?? 0) + 1;
    } else if (k === "commit_recorded") system(e, "Committed", "green");
    else if (k === "task_merged") system(e, "Merged", "green");
    else if (k === "verification_passed") system(e, "Verified", "green");
    else if (k === "verification_skipped" || k === "verification_unavailable")
      system(e, "Not verified", "orange");
    else if (k === "pr_opened") system(e, "PR opened", "green");
    else if (k === "escalated" || k === "plan_escalated")
      system(e, "Needs you", "orange");
    else if (k === "paused") system(e, "Paused", "orange");
    else if (k === "resumed") system(e, "Resumed", "");
    else if (k === "cancelled") system(e, "Cancelled", "red");
  }
  flushFindings();
  flushChecks();
  return out;
}

export interface Handoff {
  from: string;
  to: string;
  tasks: number;
  feedback: number;
}
export function handoffs(items: ThreadItem[]): Handoff[] {
  const map = new Map<string, Handoff>();
  for (const it of items) {
    if (it.kind !== "message" || !it.to) continue;
    const task = it.type.startsWith("Task");
    if (!task && it.type !== "Feedback") continue;
    const key = `${it.from}>${it.to}`;
    const h = map.get(key) ?? {
      from: it.from,
      to: it.to,
      tasks: 0,
      feedback: 0,
    };
    if (task) h.tasks++;
    else h.feedback++;
    map.set(key, h);
  }
  return [...map.values()];
}
