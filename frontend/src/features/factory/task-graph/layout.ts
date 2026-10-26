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
  // How many tasks wait on this one.
  dependents: number;
}
export interface GraphEdge {
  id: string;
  from: string;
  to: string;
  path: string;
  // Points against the flow: the edge ELK reversed to break a cycle.
  cycle: boolean;
}
export interface GraphRank {
  label: string;
  x: number;
  y: number;
}
export interface GraphLayout {
  nodes: GraphNode[];
  edges: GraphEdge[];
  ranks: GraphRank[];
  width: number;
  height: number;
  diagnostics: string[];
}
interface Route {
  id: string;
  from: string;
  to: string;
  points: Point[];
}
export interface TaskAnalysis {
  edges: { from: string; to: string }[];
  depth: Map<string, number>;
  diagnostics: string[];
}

export const NODE_WIDTH = 232;
export const NODE_HEIGHT = 112;
const PADDING = 32;
// Room for the stage labels above columns (left → right) or beside rows.
const RANK_GUTTER = { RIGHT: 20, DOWN: 64 };
const HOP = 5;
// ELK coordinates are floats; treat sub-pixel differences as aligned.
const same = (a: number, b: number) => Math.abs(a - b) < 0.5;
const padding = (d: Direction) => ({
  top: PADDING + (d === "RIGHT" ? RANK_GUTTER.RIGHT : 0),
  left: PADDING + (d === "DOWN" ? RANK_GUTTER.DOWN : 0),
});
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
      // Tasks merely downstream of a cycle are also unresolved; name only the
      // ones that can reach themselves.
      const reaches = (from: string, target: string) => {
        const seen = new Set<string>(),
          queue = deps(byID.get(from)!);
        while (queue.length) {
          const id = queue.pop()!;
          if (id === target) return true;
          if (seen.has(id) || !pending.has(id)) continue;
          seen.add(id);
          queue.push(...deps(byID.get(id)!));
        }
        return false;
      };
      const looped = [...pending].filter((id) => reaches(id, id));
      diagnostics.push(
        `Dependency cycle detected between ${looped.map((id) => byID.get(id)!.title).join(", ")}. Inspect the blueprint before execution.`,
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
      "elk.padding": `[top=${padding(direction).top},left=${padding(direction).left},bottom=${PADDING},right=${PADDING}]`,
      "elk.spacing.nodeNode": direction === "RIGHT" ? "28" : "36",
      "elk.spacing.edgeEdge": "12",
      "elk.layered.spacing.edgeEdgeBetweenLayers": "12",
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
  direction: Direction = "RIGHT",
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
  const routes = (result.edges ?? []).flatMap((e) => {
    const section = e.sections?.[0];
    if (!section) return [];
    return [
      {
        id: e.id,
        from: e.sources[0],
        to: e.targets[0],
        points: [
          section.startPoint,
          ...(section.bendPoints ?? []),
          section.endPoint,
        ],
      },
    ];
  });
  return finishLayout(
    nodes,
    routes,
    result.width ?? 0,
    result.height ?? 0,
    diagnostics,
    direction,
  );
}

// Simple layered placement used only if the ELK engine fails to load, so the
// tasks stay inspectable.
export function fallbackLayout(
  tasks: Task[],
  analysis: TaskAnalysis,
  direction: Direction,
): GraphLayout {
  const index = new Map<number, number>(),
    pad = padding(direction);
  const gapMain = 72,
    gapCross = direction === "RIGHT" ? 28 : 36;
  const nodes = tasks.map((t) => {
    const layer = analysis.depth.get(t.id) ?? 0,
      slot = index.get(layer) ?? 0;
    index.set(layer, slot + 1);
    const main =
        layer * ((direction === "RIGHT" ? NODE_WIDTH : NODE_HEIGHT) + gapMain),
      cross =
        slot * ((direction === "RIGHT" ? NODE_HEIGHT : NODE_WIDTH) + gapCross);
    return {
      ...t,
      x: pad.left + (direction === "RIGHT" ? main : cross),
      y: pad.top + (direction === "RIGHT" ? cross : main),
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
    };
  });
  const byID = new Map(nodes.map((n) => [n.id, n]));
  const routes = analysis.edges.map(({ from, to }) => {
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
    return { id: edgeID(from, to), from, to, points };
  });
  return finishLayout(
    nodes,
    routes,
    Math.max(0, ...nodes.map((n) => n.x + n.width)) + PADDING,
    Math.max(0, ...nodes.map((n) => n.y + n.height)) + PADDING,
    analysis.diagnostics,
    direction,
  );
}

// Shared by both layouts: dependents badges, stage labels, cycle edges and
// line hops where edges cross.
function finishLayout(
  placed: Omit<GraphNode, "dependents">[],
  routes: Route[],
  width: number,
  height: number,
  diagnostics: string[],
  direction: Direction,
): GraphLayout {
  const byID = new Map(placed.map((n) => [n.id, n])),
    main = (n: { x: number; y: number }) =>
      Math.round(direction === "RIGHT" ? n.x : n.y);
  const nodes = placed.map((n) => ({
    ...n,
    dependents: routes.filter((r) => r.from === n.id).length,
  }));
  const ranks = [...new Set(nodes.map(main))]
    .sort((a, b) => a - b)
    .map((at, i) => ({
      label: `Stage ${i + 1}`,
      x: direction === "RIGHT" ? at : PADDING,
      y: direction === "RIGHT" ? PADDING + 6 : at + nodes[0].height / 2,
    }));
  const hops = findHops(routes.map((r) => r.points));
  const edges = routes.map((r, i) => {
    const a = byID.get(r.from),
      b = byID.get(r.to);
    return {
      id: r.id,
      from: r.from,
      to: r.to,
      path: edgePath(r.points, 8, hops[i]),
      cycle: !!a && !!b && main(a) >= main(b),
    };
  });
  return { nodes, edges, ranks, width, height, diagnostics };
}

// For every horizontal segment, the x positions where it crosses a vertical
// segment of another edge. Horizontal lines hop; vertical lines stay solid.
export function findHops(lines: Point[][], margin = HOP + 4): number[][][] {
  const inside = (v: number, a: number, b: number) =>
    v > Math.min(a, b) + margin && v < Math.max(a, b) - margin;
  return lines.map((line, i) =>
    line.slice(1).map((end, k) => {
      const start = line[k];
      if (!same(start.y, end.y)) return [];
      const xs: number[] = [];
      lines.forEach((other, j) => {
        if (i === j) return;
        for (let m = 1; m < other.length; m++) {
          const [p, q] = [other[m - 1], other[m]];
          if (
            same(p.x, q.x) &&
            inside(p.x, start.x, end.x) &&
            inside(start.y, p.y, q.y)
          )
            xs.push(p.x);
        }
      });
      return xs;
    }),
  );
}

// Orthogonal polyline with softly rounded corners, and a small arc over each
// crossing listed in hops[segment].
export function edgePath(
  points: ElkPoint[],
  radius = 8,
  hops: number[][] = [],
): string {
  const r = (n: number) => Math.round(n * 10) / 10;
  if (points.length < 2)
    return points.length ? `M ${r(points[0].x)} ${r(points[0].y)}` : "";
  let d = `M ${r(points[0].x)} ${r(points[0].y)}`,
    from = points[0];
  // Draws segment k from `from` up to `to`, hopping over crossings on the way.
  const run = (k: number, to: Point) => {
    const [a, b] = [points[k], points[k + 1]],
      dir = Math.sign(b.x - a.x);
    if (same(a.y, b.y) && dir)
      for (const x of [...(hops[k] ?? [])].sort((m, n) => (m - n) * dir)) {
        const left = x - HOP * dir,
          right = x + HOP * dir;
        if ((left - from.x) * dir < 0 || (to.x - right) * dir < 0) continue;
        d += ` L ${r(left)} ${r(a.y)} A ${HOP} ${HOP} 0 0 ${dir > 0 ? 1 : 0} ${r(right)} ${r(a.y)}`;
        from = { x: right, y: a.y };
      }
    d += ` L ${r(to.x)} ${r(to.y)}`;
  };
  for (let i = 1; i < points.length - 1; i++) {
    const [p, c, n] = [points[i - 1], points[i], points[i + 1]];
    const inLen = Math.hypot(c.x - p.x, c.y - p.y),
      outLen = Math.hypot(n.x - c.x, n.y - c.y),
      k = Math.min(radius, inLen / 2, outLen / 2);
    if (!k) {
      run(i - 1, c);
      from = c;
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
    run(i - 1, a);
    d += ` Q ${r(c.x)} ${r(c.y)} ${r(b.x)} ${r(b.y)}`;
    from = b;
  }
  run(points.length - 2, points[points.length - 1]);
  return d;
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
      ranks: [],
      width: 0,
      height: 0,
      diagnostics: analysis.diagnostics,
    };
  try {
    const elk = await engine();
    const result = await elk.layout(
      toElkGraph(tasks, analysis.edges, direction),
    );
    return fromElkGraph(result, tasks, analysis.diagnostics, direction);
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
