import { describe, expect, it, vi } from "vitest";
import ELK from "elkjs/lib/elk.bundled.js";
import type { ElkNode } from "elkjs/lib/elk-api";
import {
  analyzeTasks,
  edgePath,
  findHops,
  fromElkGraph,
  layoutTasks,
  lineage,
  toElkGraph,
  NODE_HEIGHT,
  NODE_WIDTH,
  type Direction,
} from "../../src/features/factory/task-graph/layout";
import { tasks } from "../fixtures/workspace";

const realElk = () => Promise.resolve(new ELK());

describe("analyzeTasks", () => {
  it("keeps valid edges once and reports missing dependencies and cycles", () => {
    const a = analyzeTasks([
      { ...tasks[0], deps: ["missing", "inspection"] },
      { ...tasks[1], deps: ["baseline", "baseline"] },
      ...tasks.slice(2),
    ]);
    expect(a.edges).toContainEqual({ from: "inspection", to: "baseline" });
    expect(
      a.edges.filter((e) => e.from === "baseline" && e.to === "implementation"),
    ).toHaveLength(1);
    expect(a.diagnostics.join(" ")).toContain("missing dependency missing");
    expect(a.diagnostics.join(" ")).toContain("cycle");
    expect(a.depth.size).toBe(4);
  });
  it("drops self dependencies from drawn edges but reports the cycle", () => {
    const a = analyzeTasks([{ ...tasks[0], deps: ["baseline"] }]);
    expect(a.edges).toEqual([]);
    expect(a.diagnostics.join(" ")).toContain("cycle");
  });
  it("assigns longest-path depths for an acyclic plan", () => {
    const a = analyzeTasks([...tasks].reverse());
    expect(Object.fromEntries(a.depth)).toEqual({
      baseline: 0,
      implementation: 1,
      tests: 1,
      inspection: 2,
    });
    expect(a.diagnostics).toEqual([]);
  });
});

describe("ELK graph shaping", () => {
  it("builds a layered orthogonal ELK graph in the requested direction", () => {
    const { edges } = analyzeTasks(tasks);
    for (const direction of ["RIGHT", "DOWN"] as Direction[]) {
      const g = toElkGraph(tasks, edges, direction);
      expect(g.layoutOptions).toMatchObject({
        "elk.algorithm": "layered",
        "elk.direction": direction,
        "elk.edgeRouting": "ORTHOGONAL",
      });
      expect(g.children).toEqual(
        tasks.map((t) => ({
          id: t.id,
          width: NODE_WIDTH,
          height: NODE_HEIGHT,
        })),
      );
      expect(g.edges).toContainEqual({
        id: "baseline->tests",
        sources: ["baseline"],
        targets: ["tests"],
      });
    }
  });
  it("maps ELK positions and edge sections back onto tasks", () => {
    const result: ElkNode = {
      id: "root",
      width: 600,
      height: 300,
      children: [
        { id: "baseline", x: 32, y: 40, width: 232, height: 108 },
        { id: "tests", x: 336, y: 40, width: 232, height: 108 },
      ],
      edges: [
        {
          id: "baseline->tests",
          sources: ["baseline"],
          targets: ["tests"],
          sections: [
            {
              id: "s0",
              startPoint: { x: 264, y: 94 },
              bendPoints: [
                { x: 300, y: 94 },
                { x: 300, y: 120 },
              ],
              endPoint: { x: 336, y: 120 },
            },
          ],
        },
        { id: "unrouted", sources: ["a"], targets: ["b"] },
      ],
    };
    const g = fromElkGraph(result, [tasks[0], tasks[2]], ["note"]);
    expect(g.width).toBe(600);
    expect(g.height).toBe(300);
    expect(g.nodes[1]).toMatchObject({
      id: "tests",
      title: tasks[2].title,
      x: 336,
      y: 40,
    });
    expect(g.edges).toEqual([
      {
        id: "baseline->tests",
        from: "baseline",
        to: "tests",
        path: "M 264 94 L 292 94 Q 300 94 300 102 L 300 112 Q 300 120 308 120 L 336 120",
        cycle: false,
      },
    ]);
    expect(g.nodes.map((n) => n.dependents)).toEqual([1, 0]);
    expect(g.ranks).toEqual([
      { label: "Stage 1", x: 32, y: 38 },
      { label: "Stage 2", x: 336, y: 38 },
    ]);
    expect(g.diagnostics).toEqual(["note"]);
  });
  it("flags edges that run against the flow as the cycle", () => {
    const result: ElkNode = {
      id: "root",
      children: [
        { id: "baseline", x: 32, y: 40, width: 232, height: 108 },
        { id: "tests", x: 336, y: 40, width: 232, height: 108 },
      ],
      edges: [
        {
          id: "tests->baseline",
          sources: ["tests"],
          targets: ["baseline"],
          sections: [
            {
              id: "s0",
              startPoint: { x: 336, y: 60 },
              endPoint: { x: 264, y: 60 },
            },
          ],
        },
      ],
    };
    const g = fromElkGraph(result, [tasks[0], tasks[2]], [], "RIGHT");
    expect(g.edges[0].cycle).toBe(true);
  });
  it("hops horizontal segments over vertical segments they cross", () => {
    const horizontal = [
        { x: 0, y: 50 },
        { x: 100, y: 50 },
      ],
      vertical = [
        { x: 40, y: 0 },
        { x: 40, y: 100 },
      ],
      touching = [
        { x: 100, y: 0 },
        { x: 100, y: 100 },
      ];
    const hops = findHops([horizontal, vertical, touching]);
    expect(hops[0]).toEqual([[40]]);
    expect(hops[1]).toEqual([[]]);
    // ELK can emit sub-pixel noise on a straight segment.
    expect(
      findHops([
        [
          { x: 0, y: 50 },
          { x: 100, y: 50.0000001 },
        ],
        vertical,
      ])[0],
    ).toEqual([[40]]);
    expect(edgePath(horizontal, 8, hops[0])).toBe(
      "M 0 50 L 35 50 A 5 5 0 0 1 45 50 L 100 50",
    );
    expect(edgePath([...horizontal].reverse(), 8, [[40]])).toBe(
      "M 100 50 L 45 50 A 5 5 0 0 0 35 50 L 0 50",
    );
  });
  it("rounds corners no further than half of the shorter segment", () => {
    expect(
      edgePath([
        { x: 0, y: 0 },
        { x: 4, y: 0 },
        { x: 4, y: 20 },
      ]),
    ).toBe("M 0 0 L 2 0 Q 4 0 4 2 L 4 20");
    expect(
      edgePath([
        { x: 0, y: 0 },
        { x: 10, y: 0 },
      ]),
    ).toBe("M 0 0 L 10 0");
  });
});

describe("layoutTasks", () => {
  it("passes the analysed graph to the engine and returns its layout", async () => {
    const layout = vi.fn(async (g: ElkNode) => ({
      ...g,
      width: 100,
      height: 50,
      children: g.children!.map((c, i) => ({ ...c, x: i * 10, y: 0 })),
    }));
    const g = await layoutTasks(tasks, "DOWN", async () => ({ layout }));
    expect(layout).toHaveBeenCalledOnce();
    expect(layout.mock.calls[0][0].layoutOptions?.["elk.direction"]).toBe(
      "DOWN",
    );
    expect(layout.mock.calls[0][0].edges).toHaveLength(4);
    expect(g.nodes.map((n) => n.x)).toEqual([0, 10, 20, 30]);
  });
  it("falls back to a simple layered layout when the engine fails", async () => {
    const g = await layoutTasks(tasks, "RIGHT", () =>
      Promise.reject(new Error("worker blocked")),
    );
    expect(g.nodes).toHaveLength(4);
    expect(g.edges).toHaveLength(4);
    expect(g.diagnostics.join(" ")).toContain("simplified layout");
    const byID = new Map(g.nodes.map((n) => [n.id, n]));
    expect(byID.get("inspection")!.x).toBeGreaterThan(byID.get("tests")!.x);
  });
  it("skips the engine for empty plans", async () => {
    const engine = vi.fn();
    expect((await layoutTasks([], "RIGHT", engine)).nodes).toEqual([]);
    expect(engine).not.toHaveBeenCalled();
  });

  for (const direction of ["RIGHT", "DOWN"] as Direction[])
    it(`lays out an unsorted DAG with forward edges and no overlaps (${direction})`, async () => {
      const g = await layoutTasks([...tasks].reverse(), direction, realElk);
      const byID = new Map(g.nodes.map((n) => [n.id, n]));
      expect(g.edges).toHaveLength(4);
      for (const e of g.edges) {
        const a = byID.get(e.from)!,
          b = byID.get(e.to)!;
        if (direction === "RIGHT") expect(a.x + a.width).toBeLessThan(b.x);
        else expect(a.y + a.height).toBeLessThan(b.y);
        expect(e.path).toMatch(/^M [\d.]+ [\d.]+/);
      }
      for (const a of g.nodes) {
        expect(a.x + a.width).toBeLessThanOrEqual(g.width);
        expect(a.y + a.height).toBeLessThanOrEqual(g.height);
        for (const b of g.nodes)
          if (a !== b)
            expect(
              a.x + a.width <= b.x ||
                b.x + b.width <= a.x ||
                a.y + a.height <= b.y ||
                b.y + b.height <= a.y,
            ).toBe(true);
      }
      expect(g.diagnostics).toEqual([]);
    });

  it("still lays out cyclic plans and large chains", async () => {
    const cyclic = await layoutTasks(
      [{ ...tasks[0], deps: ["inspection"] }, ...tasks.slice(1)],
      "RIGHT",
      realElk,
    );
    expect(cyclic.nodes).toHaveLength(4);
    expect(cyclic.diagnostics.join(" ")).toContain("cycle");
    // inspection -> baseline closes the loop; only tasks on it are named.
    expect(cyclic.diagnostics.join(" ")).toContain(tasks[0].title);
    const extra = await layoutTasks(
      [
        { ...tasks[0], deps: ["implementation"] },
        { ...tasks[1], deps: ["baseline"] },
        { ...tasks[2], deps: ["implementation"] },
      ],
      "RIGHT",
      realElk,
    );
    expect(extra.diagnostics.join(" ")).not.toContain(tasks[2].title);
    const chain = Array.from({ length: 100 }, (_, i) => ({
      ...tasks[0],
      id: `t${i}`,
      deps: i ? [`t${i - 1}`] : [],
    }));
    const g = await layoutTasks(chain, "RIGHT", realElk);
    expect(g.nodes).toHaveLength(100);
    expect(g.edges).toHaveLength(99);
    expect(g.diagnostics).toEqual([]);
  });
});

it("collects ancestors and descendants for path focus", () => {
  const { edges } = analyzeTasks([
    ...tasks,
    { ...tasks[0], id: "docs", deps: ["tests"] },
  ]);
  expect([...lineage("tests", edges)].sort()).toEqual([
    "baseline",
    "docs",
    "inspection",
    "tests",
  ]);
  expect([...lineage("implementation", edges)].sort()).toEqual([
    "baseline",
    "implementation",
    "inspection",
  ]);
});
