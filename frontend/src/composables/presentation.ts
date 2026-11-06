import type { Project, WorkOrder, WorkOrderState } from "../types/api";
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
// A PR that was closed or can no longer be tracked needs a human, since nothing will merge it on its own.
const prStalled = (w: WorkOrder) =>
  w.state === "READY" &&
  !w.pr_merged_at &&
  !!(w.pr_closed_at || w.pr_track_failed_at);
export const isNeedsYou = (w: WorkOrder) =>
  ["WAITING_FOR_HUMAN", "NEEDS_HUMAN_REVIEW", "BLOCKED"].includes(w.state) ||
  (w.state === "ARCHITECT_REVIEW" && w.plan_review === "approved") ||
  prStalled(w);
export const needsYou = (orders: WorkOrder[]) => orders.filter(isNeedsYou);
export const runningStates: WorkOrderState[] = [
  "QUEUED",
  "IMPLEMENTING",
  "VERIFYING",
  "SPECIALIST_REVIEW",
  "PR_PREPARATION",
  "MERGE_READINESS",
];
// Approved with no pull request to track: nothing can merge, so it is done.
const approvedLocally = (w: WorkOrder) =>
  w.state === "READY" && !w.pr_url && !w.pr_closed_at && !w.pr_track_failed_at;
export const isFinished = (w: WorkOrder) =>
  !!w.pr_merged_at || w.state === "CANCELLED" || approvedLocally(w);
// Not yet picked up by an agent: just created, or approved and waiting for a builder.
export const isTodo = (w: WorkOrder) =>
  (w.state === "CREATED" || w.state === "QUEUED") &&
  !isFinished(w) &&
  !isNeedsYou(w);
export const isRunning = (w: WorkOrder) =>
  !isFinished(w) && !isNeedsYou(w) && !isTodo(w);
export const isActive = (w: WorkOrder) => !isFinished(w);
// Mirrors the server rule for DELETE /work-orders/:id (409 otherwise).
export const isDeletable = (w: WorkOrder) =>
  w.state === "CANCELLED" ||
  w.state === "BLOCKED" ||
  (w.state === "READY" &&
    (!w.pr_ref ||
      !!w.pr_merged_at ||
      !!w.pr_closed_at ||
      !!w.pr_track_failed_at));

export const stages = ["Brief", "Plan", "Build", "Review", "Ship"] as const;
export const stageTabs = ["plan", "plan", "progress", "review", "changes"];
const stageByState: Partial<Record<WorkOrderState, number>> = {
  CREATED: 0,
  REQUIREMENT_ANALYSIS: 0,
  TASK_DECOMPOSITION: 1,
  SPEC_AND_PLAN_GENERATION: 1,
  PLANNING: 1,
  ARCHITECT_REVIEW: 1,
  QUEUED: 2,
  IMPLEMENTING: 2,
  VERIFYING: 2,
  SPECIALIST_REVIEW: 3,
  PR_PREPARATION: 3,
  MERGE_READINESS: 3,
  BLOCKED: 3,
  NEEDS_HUMAN_REVIEW: 4,
  READY: 4,
};
const runStage: Record<string, number> = {
  foreman: 0,
  architect: 1,
  builder: 2,
  inspector: 3,
};
// CANCELLED has no stage of its own: a declined PR stopped at Ship, otherwise the furthest run shows where it stopped.
export function stageIndex(
  w: WorkOrder,
  runs?: { kind: string }[],
  events?: { kind: string }[],
): number {
  if (w.state === "WAITING_FOR_HUMAN")
    return w.clarif_question && !w.clarif_answer ? 1 : 3;
  if (w.state === "CANCELLED" && events?.some((e) => e.kind === "pr_declined"))
    return 4;
  return (
    stageByState[w.state] ??
    (runs ? Math.max(0, ...runs.map((r) => runStage[r.kind] ?? 0)) : -1)
  );
}
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
  isNeedsYou(w) ? "you" : (actorByState[w.state] ?? "factory");
export type StageStatus = "done" | "cur" | "wait" | "stop" | "todo";
export function stageStatuses(
  w: WorkOrder,
  runs?: { kind: string }[],
  events?: { kind: string }[],
): StageStatus[] {
  const at = stageIndex(w, runs, events);
  return stages.map((_, i) =>
    (isFinished(w) && w.state !== "CANCELLED") || i < at
      ? "done"
      : i > at
        ? "todo"
        : w.state === "CANCELLED" || w.state === "BLOCKED"
          ? "stop"
          : isNeedsYou(w)
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
export const publishLabel = (p: {
  publish_via?: string;
  github_repo?: string;
  remote_repo?: string;
}) =>
  p.publish_via === "gh"
    ? p.github_repo
      ? `gh · ${p.github_repo}`
      : "GitHub"
    : p.publish_via === "connection" || (!p.publish_via && p.remote_repo)
      ? p.remote_repo || p.github_repo
        ? `Connection · ${p.remote_repo || p.github_repo}`
        : "Connection"
      : "Local only";

// What Approve does for a project, by how it publishes.
export function publishNote(
  p?: Pick<
    Project,
    "publish_via" | "github_repo" | "remote_repo" | "git_connection_id"
  >,
): { text: string; remote: boolean } {
  const via = p?.publish_via ?? (p?.git_connection_id ? "connection" : "none");
  if (via === "gh")
    return {
      text: `Approving pushes the branch and opens a pull request on GitHub${p?.github_repo ? ` (${p.github_repo})` : ""} with your gh login.`,
      remote: true,
    };
  if (via === "connection") {
    const repo = p?.remote_repo || p?.github_repo;
    return {
      text: `Approving pushes the branch and opens a pull request${repo ? ` on ${repo}` : ""} through the project's connection.`,
      remote: true,
    };
  }
  return {
    text: p
      ? "This records a local decision. No pull request is created for this project."
      : "Approving records your decision for this work order.",
    remote: false,
  };
}
