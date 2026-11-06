import type { Run, WOEvent, WorkOrder } from "../../../types/api";

export interface AgentTool {
  name: string;
  target?: string;
  failed?: boolean;
}
export interface AgentStep {
  n: number;
  tools: AgentTool[];
  at: string;
  message?: string; // a human message steered into the run
}
export type AgentRunStatus =
  "running" | "stalled" | "finished" | "asked" | "failed";
export interface AgentRunGroup {
  type: "run";
  id: string;
  actor: string;
  taskID: string;
  status: AgentRunStatus;
  startedAt: string;
  lastAt: string;
  steps: AgentStep[];
  tokensOut: number;
  outcome: string;
}
export type ActivityItem = AgentRunGroup | { type: "event"; event: WOEvent };

const runKinds = new Set([
  "agent_started",
  "agent_step",
  "agent_stalled",
  "agent_completed",
  "agent_failed",
  "agent_steered",
]);

// Folds agent_* events sharing a run_id into one group, oldest first; other events pass through.
export function groupActivity(events: WOEvent[]): ActivityItem[] {
  const items: ActivityItem[] = [];
  const runs = new Map<string, AgentRunGroup>();
  for (const e of events) {
    const runID = e.payload?.run_id;
    if (!runKinds.has(e.kind) || typeof runID !== "string") {
      items.push({ type: "event", event: e });
      continue;
    }
    let run = runs.get(runID);
    if (!run) {
      run = {
        type: "run",
        id: runID,
        actor: e.actor,
        taskID: String(e.payload?.task_id ?? ""),
        status: "running",
        startedAt: e.created_at,
        lastAt: e.created_at,
        steps: [],
        tokensOut: 0,
        outcome: "",
      };
      runs.set(runID, run);
      items.push(run);
    }
    run.lastAt = e.created_at;
    if (e.kind === "agent_step") {
      run.status = "running";
      run.steps.push({
        n: Number(e.payload?.step) || run.steps.length + 1,
        tools: (e.payload?.tools as AgentTool[] | undefined) ?? [],
        at: e.created_at,
      });
      run.tokensOut += Number(e.payload?.tokens_out) || 0;
    } else if (e.kind === "agent_steered")
      run.steps.push({
        n: run.steps.length + 1,
        tools: [],
        at: e.created_at,
        message: e.message,
      });
    else if (e.kind === "agent_stalled") run.status = "stalled";
    else if (e.kind === "agent_completed")
      run.status = e.payload?.needs_context ? "asked" : "finished";
    else if (e.kind === "agent_failed") {
      run.status = "failed";
      run.outcome = e.message;
    }
  }
  return items;
}

export interface GateSummary {
  gate: string;
  outcome: string;
  row: number;
  note: string;
  tone: string;
}
const gateTones: Record<string, string> = {
  approve: "green",
  converged: "green",
  revise: "orange",
  append: "orange",
  escalate: "red",
  loop: "",
};
// A Jev gate event's outcome, truth-table row and code-built note; null for any other event.
export function gateSummary(e: WOEvent): GateSummary | null {
  if (e.kind !== "plan_gate" && e.kind !== "converge_gate") return null;
  const outcome = String(e.payload?.outcome ?? "");
  return {
    gate: e.kind === "plan_gate" ? "Plan gate" : "Converge gate",
    outcome,
    row: Number(e.payload?.row) || 0,
    note: String(e.payload?.note ?? e.payload?.error ?? ""),
    tone: gateTones[outcome] ?? "",
  };
}

// Why the flow asked a human: the latest plan escalation or escalating converge gate.
export function escalationReason(events: WOEvent[]): string {
  for (const e of [...events].reverse()) {
    if (e.kind === "plan_escalated") return e.message;
    const g = gateSummary(e);
    if (g?.outcome === "escalate" && g.gate === "Converge gate")
      return g.note || e.message;
  }
  return "";
}

const reasonKinds = (o: WorkOrder): string[] => {
  switch (o.state) {
    case "NEEDS_HUMAN_REVIEW":
      return ["readiness_needs_human_review", "converge_gate"];
    case "ARCHITECT_REVIEW":
      return o.plan_review === "approved" ? ["plan_escalated"] : [];
    case "WAITING_FOR_HUMAN":
      return [
        "clarification_requested",
        "clarification_escalated",
        "escalated",
      ];
    case "BLOCKED":
      return ["readiness_blocked", "escalated"];
    case "READY":
      return o.pr_closed_at && !o.pr_merged_at
        ? ["pr_closed"]
        : o.pr_track_failed_at
          ? ["pr_track_failed"]
          : [];
    default:
      return [];
  }
};

// Why the order waits right now. The causing event can be logged just before its state_changed, so the window opens after the transition preceding the current state's entry.
export function currentReason(
  o: WorkOrder,
  events: WOEvent[],
  hasMore = false,
): string {
  return reasonOf(o, events, hasMore).text;
}

// The converge gate's reasons one by one, as the backend listed them; empty for events logged before it did.
export function currentIssues(
  o: WorkOrder,
  events: WOEvent[],
  hasMore = false,
): string[] {
  const issues = reasonOf(o, events, hasMore).event?.payload?.issues;
  return Array.isArray(issues) ? issues.map(String) : [];
}

function reasonOf(
  o: WorkOrder,
  events: WOEvent[],
  hasMore: boolean,
): { text: string; event?: WOEvent } {
  const kinds = reasonKinds(o);
  if (!kinds.length) return { text: "" };
  const lastTransition = (end: number, to?: string) => {
    for (let i = end - 1; i >= 0; i--)
      if (
        events[i].kind === "state_changed" &&
        (to === undefined || events[i].payload?.to === to)
      )
        return i;
    return -1;
  };
  const entry = lastTransition(events.length, o.state);
  if (entry < 0 && hasMore) return { text: "Needs your decision" };
  const from = entry < 0 ? 0 : lastTransition(entry) + 1;
  for (let i = events.length - 1; i >= from; i--) {
    const e = events[i];
    if (!kinds.includes(e.kind)) continue;
    if (e.kind !== "converge_gate") return { text: e.message, event: e };
    const g = gateSummary(e);
    if (g?.outcome === "escalate")
      return { text: g.note || e.message, event: e };
  }
  return { text: "" };
}

// The latest message of the given kinds, e.g. why a readiness check blocked or an Inspector escalated.
export function lastMessage(events: WOEvent[], ...kinds: string[]): string {
  return (
    [...events].reverse().find((e) => kinds.includes(e.kind))?.message ?? ""
  );
}

export interface ConvergeGap {
  id: string;
  type: string;
  layer: string;
  cites: string;
  evidence: string;
  fix: string;
}
export interface ArchitectReport {
  revision: number;
  round: number;
  gaps: ConvergeGap[];
  converged: boolean;
}
export function architectReport(events: WOEvent[]): ArchitectReport | null {
  const e = [...events].reverse().find((x) => x.kind === "converge_report");
  if (!e) return null;
  return {
    revision: Number(e.payload?.revision) || 0,
    round: Number(e.payload?.round) || 0,
    gaps: (e.payload?.gaps as ConvergeGap[]) ?? [],
    converged: Boolean(e.payload?.converged),
  };
}

// Prefills Request changes with the Architect's fixes for the gaps it found.
export function changeRequestDraft(r: ArchitectReport | null): string {
  return (r?.gaps ?? [])
    .filter((g) => g.fix)
    .map((g) => `${g.cites || g.id}: ${g.fix}`)
    .join("\n\n");
}

// Parallel plan tasks run at once, so more than one run can be live.
export function liveRuns(items: ActivityItem[]): AgentRunGroup[] {
  return items.filter(
    (it): it is AgentRunGroup =>
      it.type === "run" && (it.status === "running" || it.status === "stalled"),
  );
}

const verbs: Record<string, string> = {
  read: "Read",
  grep: "Searched for",
  glob: "Listed files matching",
  list: "Listed",
  bash: "Ran",
  shell: "Ran",
  edit: "Edited",
  write: "Wrote",
  webfetch: "Fetched",
};
export function toolLine(t: AgentTool): string {
  const verb = verbs[t.name] ?? t.name;
  return t.target ? `${verb} ${t.target}` : verb;
}

const working: Record<string, string> = {
  architect: "is drafting the plan",
  foreman: "is reviewing",
  builder: "is building",
  inspector: "is inspecting",
  cataloger: "is cataloging",
};
export function liveVerb(run: AgentRunGroup): string {
  const name = run.actor.charAt(0).toUpperCase() + run.actor.slice(1);
  return run.status === "stalled"
    ? `${name} is waiting on the model…`
    : `${name} ${working[run.actor] ?? "is working"}…`;
}

export function duration(fromISO: string, toMs: number): string {
  const s = Math.max(0, Math.round((toMs - Date.parse(fromISO)) / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  return m < 60 ? `${m}m ${s % 60}s` : `${Math.floor(m / 60)}h ${m % 60}m`;
}

// Price weights relative to fresh input (Anthropic: cache read 0.1x, 5-minute cache write 1.25x, output 5x).
const cacheWeights = { fresh: 1, read: 0.1, write: 1.25, out: 5 };

export interface RunCacheSplit {
  fresh: number;
  read: number;
  write: number;
  readMicros: number; // estimated share of cost_micros
  writeMicros: number;
}

// Splits a run's measured cost across cache read/write by price weight; the harness reports only the total.
export function runCacheSplit(r: Run): RunCacheSplit | null {
  const read = r.cache_read_tokens ?? 0;
  const write = r.cache_write_tokens ?? 0;
  if (read + write === 0) return null;
  const fresh = Math.max(0, r.tokens_in - read - write);
  const w = cacheWeights;
  const units =
    fresh * w.fresh + read * w.read + write * w.write + r.tokens_out * w.out;
  const share = (n: number) =>
    units > 0 ? Math.round((r.cost_micros * n) / units) : 0;
  return {
    fresh,
    read,
    write,
    readMicros: share(read * w.read),
    writeMicros: share(write * w.write),
  };
}
