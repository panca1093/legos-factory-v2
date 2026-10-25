import type { WOEvent } from "../../../types/api";

export interface AgentTool {
  name: string;
  target?: string;
  failed?: boolean;
}
export interface AgentStep {
  n: number;
  tools: AgentTool[];
  at: string;
}
export type AgentRunStatus = "running" | "stalled" | "finished" | "failed";
export interface AgentRunGroup {
  type: "run";
  id: string;
  actor: string;
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
    } else if (e.kind === "agent_stalled") run.status = "stalled";
    else if (e.kind === "agent_completed") run.status = "finished";
    else if (e.kind === "agent_failed") {
      run.status = "failed";
      run.outcome = e.message;
    }
  }
  return items;
}

export function liveRun(items: ActivityItem[]): AgentRunGroup | undefined {
  for (let i = items.length - 1; i >= 0; i--) {
    const it = items[i];
    if (
      it.type === "run" &&
      (it.status === "running" || it.status === "stalled")
    )
      return it;
  }
  return undefined;
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
