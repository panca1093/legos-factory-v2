import type { Project, RailWorkOrder, WorkOrder } from "../../types/api";

export const RAIL_CAP = 8;

export type RailDot = "running" | "waiting" | "failed" | "queued";

export function railDot(r: RailWorkOrder): RailDot {
  if (r.waiting_on_you) return "waiting";
  if (r.state === "BLOCKED") return "failed";
  if (r.state === "CREATED" || r.state === "QUEUED") return "queued";
  return "running";
}

export function sortRail(rows: RailWorkOrder[]): RailWorkOrder[] {
  return [...rows].sort(
    (a, b) =>
      Number(b.waiting_on_you) - Number(a.waiting_on_you) ||
      Date.parse(b.updated_at) - Date.parse(a.updated_at),
  );
}

// The selected row stays visible even when it falls past the cap.
export function capRail<T extends { id: string }>(
  rows: T[],
  expanded: boolean,
  selected = "",
  cap = RAIL_CAP,
): { shown: T[]; hidden: number } {
  if (expanded || rows.length <= cap) return { shown: rows, hidden: 0 };
  const head = rows.slice(0, cap);
  const pick = rows.find((r) => r.id === selected);
  const shown = pick && !head.includes(pick) ? [...head, pick] : head;
  return { shown, hidden: rows.length - shown.length };
}

export const isOpenOrder = (w: WorkOrder) =>
  w.state !== "CANCELLED" && !w.pr_merged_at;

export type OrderFilter = "open" | "done" | "all";
export const orderFilter = (q: unknown): OrderFilter =>
  q === "done" || q === "all" ? q : "open";
export const filterOrders = (orders: WorkOrder[], f: OrderFilter) =>
  f === "all"
    ? orders
    : orders.filter((w) => (f === "open" ? isOpenOrder(w) : !isOpenOrder(w)));

export function repoLabel(p: Project): string {
  if (p.github_repo) return `github.com/${p.github_repo}`;
  if (p.remote_repo) return p.remote_repo;
  const parts = p.repo_url.split(/[\\/]/).filter(Boolean);
  return parts.at(-1) ?? p.repo_url;
}

const tabPaths: Record<string, string> = {
  factory: "",
  "pull-requests": "/pulls",
  code: "/code",
};
export const legacyTabPath = (id: string, tab: unknown) =>
  typeof tab === "string" && tab in tabPaths
    ? `/projects/${id}${tabPaths[tab]}`
    : null;

export const woPath = (projectID: string, id: string) =>
  `/projects/${projectID}/wo/${id}`;

export function woPathFor(orders: WorkOrder[], id: string) {
  const w = orders.find((o) => o.id === id);
  return w ? woPath(w.project_id, id) : `/factory/${id}`;
}

export function readStored(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writeStored(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* Browser storage can be disabled. */
  }
}

export function readFlags(key: string): Record<string, boolean> {
  try {
    const v = JSON.parse(readStored(key) ?? "{}");
    return v && typeof v === "object" ? v : {};
  } catch {
    return {};
  }
}
