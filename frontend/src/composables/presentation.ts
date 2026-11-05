import type { WorkOrder, WorkOrderState } from "../types/api";
export const crew = [
  { id: "foreman", name: "Foreman", job: "Orchestration", color: "#F2AA6B" },
  {
    id: "architect",
    name: "Architect",
    job: "Planning & spec review",
    color: "#A89CEC",
  },
  { id: "builder", name: "Builder", job: "Implementation", color: "#8BBCC9" },
  {
    id: "inspector",
    name: "Inspector",
    job: "Code & testing review",
    color: "#A0CFB7",
  },
  {
    id: "cataloger",
    name: "Cataloger",
    job: "Second brain · future",
    color: "#D7A8C5",
  },
] as const;
export type Persona = (typeof crew)[number]["id"];
export function persona(role: string): Persona {
  return crew.find((c) => c.id === role.toLowerCase())?.id ?? "foreman";
}
export const isCrew = (actor: string) =>
  crew.some((c) => c.id === actor.toLowerCase());
export const money = (micros: number = 0) =>
  new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(
    micros / 1_000_000,
  );
export const projectColor = (index: number) =>
  ["#8B7AD8", "#5D9FAC", "#C58D66", "#6DA58A"][Math.max(0, index) % 4];
export const attentionStates: WorkOrderState[] = [
  "WAITING_FOR_HUMAN",
  "ARCHITECT_REVIEW",
  "NEEDS_HUMAN_REVIEW",
  "BLOCKED",
];
export const runningStates: WorkOrderState[] = [
  "QUEUED",
  "IMPLEMENTING",
  "VERIFYING",
  "SPECIALIST_REVIEW",
  "PR_PREPARATION",
  "MERGE_READINESS",
];
export const isFinished = (w: WorkOrder) =>
  !!w.pr_merged_at || ["CANCELLED", "BLOCKED"].includes(w.state);
export const isActive = (w: WorkOrder) => !isFinished(w);

export const stages = ["Brief", "Plan", "Build", "Review", "Ship"] as const;
export const stageTabs = ["plan", "plan", "progress", "review", "changes"];
const stageByState: Partial<Record<WorkOrderState, number>> = {
  CREATED: 0,
  REQUIREMENT_ANALYSIS: 0,
  TASK_DECOMPOSITION: 1,
  SPEC_AND_PLAN_GENERATION: 1,
  PLANNING: 1,
  ARCHITECT_REVIEW: 1,
  WAITING_FOR_HUMAN: 1,
  QUEUED: 2,
  IMPLEMENTING: 2,
  VERIFYING: 2,
  SPECIALIST_REVIEW: 3,
  PR_PREPARATION: 3,
  MERGE_READINESS: 3,
  NEEDS_HUMAN_REVIEW: 4,
  READY: 4,
};
const runStage: Record<string, number> = {
  foreman: 0,
  architect: 1,
  builder: 2,
  inspector: 3,
};
// BLOCKED and CANCELLED carry no stage of their own; the furthest run shows where they stopped.
export function stageIndex(w: WorkOrder, runs?: { kind: string }[]): number {
  return (
    stageByState[w.state] ??
    (runs ? Math.max(0, ...runs.map((r) => runStage[r.kind] ?? 0)) : -1)
  );
}
export const waitingOnHuman = (w: WorkOrder) =>
  w.state === "WAITING_FOR_HUMAN" ||
  w.state === "NEEDS_HUMAN_REVIEW" ||
  (w.state === "ARCHITECT_REVIEW" && w.plan_review === "approved");
const actorByState: Partial<Record<WorkOrderState, string>> = {
  CREATED: "foreman",
  REQUIREMENT_ANALYSIS: "foreman",
  TASK_DECOMPOSITION: "architect",
  SPEC_AND_PLAN_GENERATION: "architect",
  PLANNING: "architect",
  ARCHITECT_REVIEW: "foreman",
  QUEUED: "builder",
  IMPLEMENTING: "builder",
  SPECIALIST_REVIEW: "inspector",
};
// Who holds the order right now: a crew member, the factory's code, or you.
export const actorOf = (w: WorkOrder) =>
  waitingOnHuman(w) ? "you" : (actorByState[w.state] ?? "factory");
export type StageStatus = "done" | "cur" | "wait" | "stop" | "todo";
export function stageStatuses(
  w: WorkOrder,
  runs?: { kind: string }[],
): StageStatus[] {
  const at = stageIndex(w, runs);
  return stages.map((_, i) =>
    w.pr_merged_at || i < at
      ? "done"
      : i > at
        ? "todo"
        : ["BLOCKED", "CANCELLED"].includes(w.state)
          ? "stop"
          : waitingOnHuman(w)
            ? "wait"
            : "cur",
  );
}

export type ShipKey =
  "approval" | "open" | "closed" | "tracking_stopped" | "merged" | "local";
export function shipState(
  w: WorkOrder,
): { key: ShipKey; label: string; tone: string } | null {
  if (w.state === "NEEDS_HUMAN_REVIEW")
    return {
      key: "approval",
      label: "Waiting for your approval",
      tone: "orange",
    };
  if (w.state !== "READY") return null;
  if (w.pr_merged_at) return { key: "merged", label: "Merged", tone: "green" };
  if (w.pr_closed_at)
    return { key: "closed", label: "PR closed without merge", tone: "red" };
  if (w.pr_track_failed_at)
    return {
      key: "tracking_stopped",
      label: "PR tracking stopped",
      tone: "orange",
    };
  if (w.pr_url)
    return { key: "open", label: "PR open · in review", tone: "green" };
  return { key: "local", label: "Approved · no pull request", tone: "green" };
}
export function stateInfo(state: string): { label: string; tone: string } {
  const states: Record<string, [string, string]> = {
    CREATED: ["Draft", ""],
    REQUIREMENT_ANALYSIS: ["Shaping the outcome", "purple"],
    TASK_DECOMPOSITION: ["Planning tasks", "purple"],
    SPEC_AND_PLAN_GENERATION: ["Drafting blueprint", "purple"],
    PLANNING: ["Planning", "purple"],
    ARCHITECT_REVIEW: ["Architect review", "purple"],
    WAITING_FOR_HUMAN: ["Needs your input", "orange"],
    QUEUED: ["Queued", ""],
    IMPLEMENTING: ["Building", "purple"],
    VERIFYING: ["Verifying", "purple"],
    SPECIALIST_REVIEW: ["Inspector review", "purple"],
    PR_PREPARATION: ["Preparing handoff", "purple"],
    MERGE_READINESS: ["Checking readiness", "purple"],
    NEEDS_HUMAN_REVIEW: ["Waiting for your approval", "orange"],
    READY: ["Ship", "green"],
    BLOCKED: ["Blocked", "red"],
    CANCELLED: ["Closed", ""],
  };
  const [label, tone] = states[state] ?? [
    state.toLowerCase().replaceAll("_", " "),
    "",
  ];
  return { label, tone };
}
export function orderStatus(w: WorkOrder): { label: string; tone: string } {
  const ship = shipState(w);
  return ship && w.state === "READY" ? ship : stateInfo(w.state);
}
export function taskState(
  taskID: string,
  runs: { task_id: string | null; state: string }[],
) {
  return (
    runs
      .filter((r) => r.task_id === taskID)
      .at(-1)
      ?.state.toLowerCase() ?? "waiting"
  );
}
