import { mount } from "@vue/test-utils";
import { expect, it } from "vitest";
import BlueprintPanel from "../../src/features/factory/work-orders/BlueprintPanel.vue";
import { orders, tasks } from "../fixtures/workspace";

function render(order: object, taskList = tasks) {
  return mount(BlueprintPanel, {
    props: {
      order: { ...structuredClone(orders[0]!), ...order },
      tasks: taskList,
      canWrite: true,
      canClarify: true,
      busy: false,
    },
  });
}

it("shows the Foreman's brief, the Architect's spec and each task's scope", () => {
  const w = render(
    { brief: "Round money in rupiah", spec: "Greet() returns Hello" },
    [{ ...tasks[0]!, scope: ["greeter/", "cmd/app"] }],
  );
  expect(w.get("[data-test=brief]").text()).toContain("Round money in rupiah");
  expect(w.get("[data-test=spec]").text()).toContain("Greet() returns Hello");
  expect(w.text()).toContain("Scope: greeter/, cmd/app");
});

it("hides the brief and spec sections until they exist", () => {
  const w = render({ brief: undefined, spec: undefined });
  expect(w.find("[data-test=brief]").exists()).toBe(false);
  expect(w.find("[data-test=spec]").exists()).toBe(false);
  expect(w.text()).not.toContain("Scope:");
});
