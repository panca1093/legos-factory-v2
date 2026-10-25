import type { ElkNode, ElkPoint } from "elkjs/lib/elk-api";
import type { Task } from "../../../types/api";

export type Direction = "RIGHT" | "DOWN";
export interface LayoutEngine {
  layout(graph: ElkNode): Promise<ElkNode>;
}
export interface Point {
  x: number;
  y: number;
}
export interface GraphNode extends Task {
  x: number;
  y: number;
  width: number;
  height: number;
}
export interface GraphEdge {
  id: string;
  from: string;
  to: string;
  path: string;
}
export interface GraphLayout {
  nodes: GraphNode[];
  edges: GraphEdge[];
  width: number;
  height: number;
  diagnostics: string[];
}
export interface TaskAnalysis {
  edges: { from: string; to: string }[];
  depth: Map<string, number>;
  diagnostics: string[];
}

export const NODE_WIDTH = 232;
export const NODE_HEIGHT = 112;
const PADDING = 32;
const edgeID = (from: string, to: string) => `${from}->${to}`;

// Validates the Architect's dependency lists: drops unknown and duplicate
// dependencies, reports cycles, and assigns a longest-path depth that the
// fallback layout uses when the ELK engine is unavailable.
export function analyzeTasks(tasks: Task[]): TaskAnalysis {
  const byID = new Map(tasks.map((t) => [t.id, t])),
    depth = new Map<string, number>(),
    pending = new Set(byID.keys()),
    diagnostics: string[] = [],
    edges: TaskAnalysis["edges"] = [],
    seen = new Set<string>();
  const deps = (t: Task) =>
    [...new Set(t.deps ?? [])].filter((d) => byID.has(d));
  for (const task of tasks)
    for (const dep of task.deps ?? []) {
      if (!byID.has(dep))
        diagnostics.push(
          `Task ${task.title} references missing dependency ${dep}.`,
        );
      else if (dep !== task.id && !seen.has(edgeID(dep, task.id))) {
        seen.add(edgeID(dep, task.id));
        edges.push({ from: dep, to: task.id });
      }
    }
  while (pending.size) {
    let progressed = false;
    for (const id of pending) {
      const d = deps(byID.get(id)!);
      if (d.every((dep) => depth.has(dep))) {
        depth.set(
          id,
          d.length ? Math.max(...d.map((x) => depth.get(x)!)) + 1 : 0,
        );
        pending.delete(id);
        progressed = true;
      }
    }
    if (!progressed) {
      diagnostics.push(
        `Dependency cycle detected between ${[...pending].map((id) => byID.get(id)!.title).join(", ")}. Inspect the blueprint before execution.`,
      );
      const fallback = Math.max(-1, ...depth.values()) + 1;
      for (const id of pending) depth.set(id, fallback);
      break;
    }
  }
  return { edges, depth, diagnostics };
}

export function toElkGraph(
  tasks: Task[],
  edges: TaskAnalysis["edges"],
  direction: Direction,
): ElkNode {
  return {
    id: "root",
    layoutOptions: {
      "elk.algorithm": "layered",
      "elk.direction": direction,
      "elk.edgeRouting": "ORTHOGONAL",
      "elk.padding": `[top=${PADDING},left=${PADDING},bottom=${PADDING},right=${PADDING}]`,
      "elk.spacing.nodeNode": direction === "RIGHT" ? "28" : "36",
      "elk.spacing.edgeEdge": "12",
      "elk.layered.spacing.nodeNodeBetweenLayers": "72",
      "elk.layered.spacing.edgeNodeBetweenLayers": "20",
      "elk.layered.crossingMinimization.strategy": "LAYER_SWEEP",
      "elk.layered.nodePlacement.strategy": "BRANDES_KOEPF",
      // Keep the Architect's order as a tie-breaker so the graph is stable.
      "elk.layered.considerModelOrder.strategy": "NODES_AND_EDGES",
    },
    children: tasks.map((t) => ({
      id: t.id,
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
    })),
    edges: edges.map((e) => ({
      id: edgeID(e.from, e.to),
      sources: [e.from],
      targets: [e.to],
    })),
  };
}

export function fromElkGraph(
  result: ElkNode,
  tasks: Task[],
  diagnostics: string[],
): GraphLayout {
  const placed = new Map((result.children ?? []).map((c) => [c.id, c]));
  const nodes = tasks.map((t) => {
    const c = placed.get(t.id);
    return {
      ...t,
      x: c?.x ?? 0,
      y: c?.y ?? 0,
      width: c?.width ?? NODE_WIDTH,
      height: c?.height ?? NODE_HEIGHT,
    };
  });
  const edges = (result.edges ?? []).flatMap((e) => {
    const section = e.sections?.[0];
    if (!section) return [];
    return [
      {
        id: e.id,
        from: e.sources[0],
        to: e.targets[0],
        path: edgePath([
          section.startPoint,
          ...(section.bendPoints ?? []),
          section.endPoint,
        ]),
      },
    ];
  });
  return {
    nodes,
    edges,
    width: result.width ?? 0,
    height: result.height ?? 0,
    diagnostics,
  };
}

// Simple layered placement used only if the ELK engine fails to load, so the
// tasks stay inspectable.
export function fallbackLayout(
  tasks: Task[],
  analysis: TaskAnalysis,
  direction: Direction,
): GraphLayout {
  const index = new Map<number, number>();
  const gapMain = 72,
    gapCross = direction === "RIGHT" ? 28 : 36;
  const nodes = tasks.map((t) => {
    const layer = analysis.depth.get(t.id) ?? 0,
      slot = index.get(layer) ?? 0;
    index.set(layer, slot + 1);
    const main =
        PADDING +
        layer * ((direction === "RIGHT" ? NODE_WIDTH : NODE_HEIGHT) + gapMain),
      cross =
        PADDING +
        slot * ((direction === "RIGHT" ? NODE_HEIGHT : NODE_WIDTH) + gapCross);
    return {
      ...t,
      x: direction === "RIGHT" ? main : cross,
      y: direction === "RIGHT" ? cross : main,
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
    };
  });
  const byID = new Map(nodes.map((n) => [n.id, n]));
  const edges = analysis.edges.map(({ from, to }) => {
    const a = byID.get(from)!,
      b = byID.get(to)!;
    const points: Point[] =
      direction === "RIGHT"
        ? [
            { x: a.x + a.width, y: a.y + a.height / 2 },
            { x: b.x - gapMain / 2, y: a.y + a.height / 2 },
            { x: b.x - gapMain / 2, y: b.y + b.height / 2 },
            { x: b.x, y: b.y + b.height / 2 },
          ]
        : [
            { x: a.x + a.width / 2, y: a.y + a.height },
            { x: a.x + a.width / 2, y: b.y - gapMain / 2 },
            { x: b.x + b.width / 2, y: b.y - gapMain / 2 },
            { x: b.x + b.width / 2, y: b.y },
          ];
    return { id: edgeID(from, to), from, to, path: edgePath(points) };
  });
  return {
    nodes,
    edges,
    width: Math.max(0, ...nodes.map((n) => n.x + n.width)) + PADDING,
    height: Math.max(0, ...nodes.map((n) => n.y + n.height)) + PADDING,
    diagnostics: analysis.diagnostics,
  };
}

// Orthogonal polyline with softly rounded corners.
export function edgePath(points: ElkPoint[], radius = 8): string {
  if (!points.length) return "";
  const r = (n: number) => Math.round(n * 10) / 10;
  let d = `M ${r(points[0].x)} ${r(points[0].y)}`;
  for (let i = 1; i < points.length - 1; i++) {
    const [p, c, n] = [points[i - 1], points[i], points[i + 1]];
    const inLen = Math.hypot(c.x - p.x, c.y - p.y),
      outLen = Math.hypot(n.x - c.x, n.y - c.y),
      k = Math.min(radius, inLen / 2, outLen / 2);
    if (!k) {
      d += ` L ${r(c.x)} ${r(c.y)}`;
      continue;
    }
    const a = {
        x: c.x - ((c.x - p.x) / inLen) * k,
        y: c.y - ((c.y - p.y) / inLen) * k,
      },
      b = {
        x: c.x + ((n.x - c.x) / outLen) * k,
        y: c.y + ((n.y - c.y) / outLen) * k,
      };
    d += ` L ${r(a.x)} ${r(a.y)} Q ${r(c.x)} ${r(c.y)} ${r(b.x)} ${r(b.y)}`;
  }
  const last = points[points.length - 1];
  return `${d} L ${r(last.x)} ${r(last.y)}`;
}

export async function layoutTasks(
  tasks: Task[],
  direction: Direction,
  engine: () => Promise<LayoutEngine>,
): Promise<GraphLayout> {
  const analysis = analyzeTasks(tasks);
  if (!tasks.length)
    return {
      nodes: [],
      edges: [],
      width: 0,
      height: 0,
      diagnostics: analysis.diagnostics,
    };
  try {
    const elk = await engine();
    const result = await elk.layout(
      toElkGraph(tasks, analysis.edges, direction),
    );
    return fromElkGraph(result, tasks, analysis.diagnostics);
  } catch {
    const layout = fallbackLayout(tasks, analysis, direction);
    layout.diagnostics = [
      ...layout.diagnostics,
      "The graph layout engine could not load, so a simplified layout is shown.",
    ];
    return layout;
  }
}

// The task itself plus every ancestor and descendant.
export function lineage(id: string, edges: { from: string; to: string }[]) {
  const walk = (key: "from" | "to", next: "from" | "to") => {
    const found = new Set([id]),
      queue = [id];
    while (queue.length) {
      const current = queue.shift()!;
      for (const e of edges)
        if (e[key] === current && !found.has(e[next])) {
          found.add(e[next]);
          queue.push(e[next]);
        }
    }
    return found;
  };
  return new Set([...walk("to", "from"), ...walk("from", "to")]);
}
