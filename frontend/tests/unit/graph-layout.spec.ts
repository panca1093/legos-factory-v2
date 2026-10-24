import { expect, it } from "vitest";
import { layoutTasks } from "../../src/features/factory/task-graph/layout";
import { tasks } from "../fixtures/workspace";
it("lays out unsorted DAGs with directional edges and no sibling overlaps", () => {
  const g = layoutTasks([...tasks].reverse());
  const byID = new Map(g.nodes.map((n) => [n.id, n]));
  expect(g.edges).toHaveLength(4);
  for (const edge of g.edges)
    expect(byID.get(edge.from)!.x + 210).toBeLessThan(byID.get(edge.to)!.x);
  expect(byID.get("implementation")!.y).not.toBe(byID.get("tests")!.y);
  for (const n of g.nodes) {
    expect(n.x + 210).toBeLessThanOrEqual(g.width);
    expect(n.y + 100).toBeLessThanOrEqual(g.height);
  }
  expect(g.diagnostics).toEqual([]);
});
it("reports missing dependencies and cycles without losing task inspection", () => {
  const g = layoutTasks([
    { ...tasks[0], deps: ["missing", "inspection"] },
    ...tasks.slice(1),
  ]);
  expect(g.nodes).toHaveLength(4);
  expect(g.diagnostics.join(" ")).toContain("missing");
  expect(g.diagnostics.join(" ")).toContain("cycle");
  expect(Number.isFinite(g.width)).toBe(true);
});
it("handles large and empty graphs", () => {
  expect(layoutTasks([]).nodes).toEqual([]);
  const input = Array.from({ length: 100 }, (_, i) => ({
    ...tasks[0],
    id: `t${i}`,
    deps: i ? [`t${i - 1}`] : [],
  }));
  const g = layoutTasks(input);
  expect(g.nodes).toHaveLength(100);
  expect(g.edges).toHaveLength(99);
  expect(g.diagnostics).toEqual([]);
});
