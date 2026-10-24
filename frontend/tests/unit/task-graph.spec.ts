import { mount } from "@vue/test-utils";
import { expect, it } from "vitest";
import TaskGraph from "../../src/features/factory/task-graph/TaskGraph.vue";
import { tasks, runs } from "../fixtures/workspace";
it("exposes clickable task nodes, directional connections, status and contextual details", async () => {
  const w = mount(TaskGraph, {
    props: { tasks, runs },
    attachTo: document.body,
  });
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
