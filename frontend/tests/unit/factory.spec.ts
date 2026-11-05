import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createMemoryHistory, createRouter } from "vue-router";
import { beforeEach, expect, it, vi } from "vitest";
import FactoryView from "../../src/features/factory/FactoryView.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import { orders, projects, user } from "../fixtures/workspace";
beforeEach(() => {
  setActivePinia(createPinia());
  const f = useFactory();
  f.projects = projects;
  f.workOrders = structuredClone(orders);
  useSession().user = user;
  vi.spyOn(f, "refreshProjects").mockResolvedValue();
  vi.spyOn(f, "refreshWorkOrders").mockResolvedValue();
});
async function render(url: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/factory", component: FactoryView },
      { path: "/factory/:id", component: { render: () => null } },
    ],
  });
  await router.push(url);
  await router.isReady();
  const w = mount(FactoryView, { global: { plugins: [router] } });
  await flushPromises();
  return w;
}
it("combines project, status and text filters", async () => {
  const w = await render("/factory?project=atlas&status=attention");
  expect(w.findAll('[data-test="order-row"]')).toHaveLength(1);
  expect(w.find('[data-test="order-row"]').text()).toContain(
    "Ship team invitation flow",
  );
  await w.find('[aria-label="Filter work orders"]').setValue("unmatched");
  expect(w.text()).toContain("No work orders match");
  w.unmount();
});
it("groups orders by who holds them and counts only merged, closed or blocked as finished", async () => {
  const f = useFactory();
  f.workOrders.find((o) => o.id === "LF-121")!.pr_url = "https://x/pr/1";
  const w = await render("/factory");
  const group = (name: string) =>
    w.findAll(".fx-group").find((g) => g.text().startsWith(name))!;
  expect(group("Needs you").findAll('[data-test="order-row"]')).toHaveLength(2);
  expect(group("Running").text()).toContain("Validate settlement timestamps");
  expect(group("Running").text()).toContain("PR open · in review");
  expect(group("Finished").findAll('[data-test="order-row"]')).toHaveLength(0);
  f.workOrders.find((o) => o.id === "LF-121")!.pr_merged_at =
    "2026-10-25T00:00:00Z";
  await flushPromises();
  expect(group("Finished").text()).toContain("Validate settlement timestamps");
  w.unmount();
});
it("draws five stage segments per row and explains badges only behind the help popover", async () => {
  const w = await render("/factory");
  expect(w.find(".fx-segs").findAll("i")).toHaveLength(5);
  expect(w.text()).not.toContain("Who is acting");
  await w.find(".fx-help").trigger("click");
  expect(w.text()).toContain("Who is acting");
  w.unmount();
});
