import { flushPromises, mount } from "@vue/test-utils";
import { expect, it, vi } from "vitest";
import ELK from "elkjs/lib/elk.bundled.js";
import type { ElkNode } from "elkjs/lib/elk-api";
import TaskGraph from "../../src/features/factory/task-graph/TaskGraph.vue";
import type { LayoutEngine } from "../../src/features/factory/task-graph/layout";
import { tasks, runs } from "../fixtures/workspace";

const mountGraph = async (
  engine: () => Promise<LayoutEngine> = () => Promise.resolve(new ELK()),
) => {
  const w = mount(TaskGraph, {
    props: { tasks, runs, engine },
    attachTo: document.body,
  });
  await settled(w);
  return w;
};
// The bundled ELK engine resolves on a later tick than flushPromises.
const settled = (w: { findAll: (s: string) => unknown[] }) =>
  vi.waitFor(() => expect(w.findAll(".tg-edge")).toHaveLength(4));

it("exposes clickable task nodes, directional connections, status and contextual details", async () => {
  const w = await mountGraph();
  expect(w.findAll(".task-brick")).toHaveLength(4);
  expect(w.findAll("path[marker-end]")).toHaveLength(4);
  expect(w.find('[data-node="implementation"]').classes()).toContain("running");
  await w.find('[data-node="implementation"]').trigger("click");
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain(
    "Implement the approved contract.",
  );
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain(
    "Record failing baseline",
  );
  w.unmount();
});

it("renders role, sequence, status and progress like the reference design", async () => {
  const w = await mountGraph();
  const baseline = w.find('[data-node="baseline"]');
  expect(baseline.text()).toContain("Builder");
  expect(baseline.text()).toContain("#01");
  expect(baseline.text()).toContain("Completed");
  expect(w.find('[data-node="implementation"]').text()).toContain("Running");
  expect(w.find('[data-node="inspection"]').text()).toContain("Waiting");
  expect(w.find('[data-node="inspection"]').text()).toContain("Pending");
  expect(w.find(".tg-summary").text()).toMatch(
    /1 of 4 completed\s*·\s*1 in progress\s*·\s*2 queued/,
  );
  expect(w.find('[role="progressbar"]').attributes("aria-valuenow")).toBe("25");
  expect(w.find(".tg-footer").text()).toMatch(
    /4 tasks · 4 dependencies · \d+% zoom/,
  );
  expect(w.text()).toContain("Drag to pan · Scroll to zoom · Click to inspect");
  w.unmount();
});

it("shows the run duration for completed tasks when the run has finished", async () => {
  const w = mount(TaskGraph, {
    props: {
      tasks,
      runs: [{ ...runs[0], finished_at: "2026-10-24T01:24:12Z" }, runs[1]],
      engine: () => Promise.resolve(new ELK()),
    },
  });
  await settled(w);
  expect(w.find('[data-node="baseline"]').text()).toContain("4m 12s");
});

it("focuses the selected task's ancestors and descendants", async () => {
  const w = await mountGraph();
  const focusButton = w.find(".tg-focus");
  expect(focusButton.attributes("disabled")).toBeDefined();
  await w.find('[data-node="tests"]').trigger("click");
  await focusButton.trigger("click");
  expect(focusButton.attributes("aria-pressed")).toBe("true");
  expect(w.find('[data-node="implementation"]').classes()).toContain("dim");
  for (const id of ["baseline", "tests", "inspection"])
    expect(w.find(`[data-node="${id}"]`).classes()).not.toContain("dim");
  expect(w.findAll(".tg-edge.active")).toHaveLength(2);
  expect(w.findAll(".tg-edge.dim")).toHaveLength(2);
  w.unmount();
});

it("re-runs the layout when the direction changes", async () => {
  const layout = vi.fn((g: ElkNode) => new ELK().layout(g));
  const w = await mountGraph(() => Promise.resolve({ layout }));
  expect(layout.mock.calls[0][0].layoutOptions?.["elk.direction"]).toBe(
    "RIGHT",
  );
  await w.find(".tg-segment button:last-child").trigger("click");
  await vi.waitFor(() => expect(layout).toHaveBeenCalledTimes(2));
  expect(layout.mock.calls.at(-1)![0].layoutOptions?.["elk.direction"]).toBe(
    "DOWN",
  );
  await w.setProps({ runs: [] });
  await flushPromises();
  expect(layout).toHaveBeenCalledTimes(2);
  w.unmount();
});

it("shows the empty state without loading the layout engine", async () => {
  const engine = vi.fn();
  const w = mount(TaskGraph, { props: { tasks: [], runs: [], engine } });
  await flushPromises();
  expect(w.text()).toContain("The task graph will appear");
  expect(engine).not.toHaveBeenCalled();
});
