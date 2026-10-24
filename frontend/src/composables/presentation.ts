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
export const isActive = (w: WorkOrder) =>
  !["READY", "CANCELLED"].includes(w.state);
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
    NEEDS_HUMAN_REVIEW: ["PR decision", "green"],
    READY: ["Assembled", "green"],
    BLOCKED: ["Blocked", "red"],
    CANCELLED: ["Closed", ""],
  };
  const [label, tone] = states[state] ?? [
    state.toLowerCase().replaceAll("_", " "),
    "",
  ];
  return { label, tone };
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
