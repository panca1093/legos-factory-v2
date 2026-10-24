import type { Task } from "../../../types/api";
export interface GraphNode extends Task {
  x: number;
  y: number;
}
export interface GraphEdge {
  from: string;
  to: string;
  path: string;
}
export function layoutTasks(tasks: Task[]): {
  nodes: GraphNode[];
  edges: GraphEdge[];
  width: number;
  height: number;
  diagnostics: string[];
} {
  const byID = new Map(tasks.map((t) => [t.id, t])),
    depth = new Map<string, number>(),
    pending = new Set(tasks.map((t) => t.id)),
    diagnostics: string[] = [];
  for (const task of tasks)
    for (const dep of task.deps ?? [])
      if (!byID.has(dep))
        diagnostics.push(
          `Task ${task.title} references missing dependency ${dep}.`,
        );
  while (pending.size) {
    let progressed = false;
    for (const id of pending) {
      const t = byID.get(id)!;
      const deps = (t.deps ?? []).filter((d) => byID.has(d));
      if (deps.every((d) => depth.has(d))) {
        depth.set(
          id,
          deps.length ? Math.max(...deps.map((d) => depth.get(d)!)) + 1 : 0,
        );
        pending.delete(id);
        progressed = true;
      }
    }
    if (!progressed) {
      diagnostics.push(
        "Dependency cycle detected. Inspect the blueprint before execution.",
      );
      const fallback = Math.max(0, ...depth.values()) + 1;
      for (const id of pending) depth.set(id, fallback);
      break;
    }
  }
  const layers = new Map<number, Task[]>();
  for (const t of tasks) {
    const d = depth.get(t.id) ?? 0;
    layers.set(d, [...(layers.get(d) ?? []), t]);
  }
  const rows = Math.max(1, ...[...layers.values()].map((l) => l.length));
  const nodes = tasks.map((t) => {
    const d = depth.get(t.id) ?? 0;
    const layer = layers.get(d)!;
    return {
      ...t,
      x: 32 + d * 280,
      y:
        42 +
        (rows - layer.length) * 75 +
        layer.findIndex((n) => n.id === t.id) * 150,
    };
  });
  const positions = new Map(nodes.map((n) => [n.id, n]));
  const edges: GraphEdge[] = [];
  for (const n of nodes)
    for (const dep of n.deps ?? []) {
      const from = positions.get(dep);
      if (from) {
        const x = from.x + 210,
          y = from.y + 50,
          x2 = n.x,
          y2 = n.y + 50;
        edges.push({
          from: dep,
          to: n.id,
          path: `M ${x} ${y} C ${x + 35} ${y}, ${x2 - 35} ${y2}, ${x2 - 7} ${y2}`,
        });
      }
    }
  return {
    nodes,
    edges,
    width: Math.max(520, ...nodes.map((n) => n.x + 242)),
    height: Math.max(260, rows * 150 + 35),
    diagnostics,
  };
}
