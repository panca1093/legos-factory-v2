import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createMemoryHistory, createRouter } from "vue-router";
import { beforeEach, expect, it, vi } from "vitest";
import FactoryOverview from "../../src/features/projects/FactoryOverview.vue";
import ProjectsView from "../../src/features/projects/ProjectsView.vue";
import NewWorkOrderDrawer from "../../src/features/factory/work-orders/NewWorkOrderDrawer.vue";
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
async function render(url: string, component = FactoryOverview) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/projects", component: ProjectsView },
      { path: "/projects/:id", component: FactoryOverview },
      { path: "/projects/:id/wo/:wo", component: { render: () => null } },
    ],
  });
  await router.push(url);
  await router.isReady();
  const w = mount(component, { global: { plugins: [router] } });
  await flushPromises();
  return { w, router };
}
it("lists open work orders of the project by default, needs-you first", async () => {
  const { w } = await render("/projects/atlas");
  const rows = w.findAll('[data-test="order-row"]');
  expect(rows.map((r) => r.attributes("href"))).toEqual([
    "/projects/atlas/wo/LF-125",
    "/projects/atlas/wo/LF-127",
  ]);
  expect(w.find('[data-test="chip-open"]').attributes("aria-pressed")).toBe(
    "true",
  );
  w.unmount();
});
it("splits open from done: cancelled and merged are done, everything else stays open", async () => {
  const f = useFactory();
  f.workOrders.find((o) => o.id === "LF-125")!.state = "CANCELLED";
  const merged = f.workOrders.find((o) => o.id === "LF-121")!;
  merged.pr_url = "https://x/pr/1";
  const open = await render("/projects/loan");
  expect(open.w.findAll('[data-test="order-row"]')).toHaveLength(2);
  open.w.unmount();
  merged.pr_merged_at = "2026-10-25T00:00:00Z";
  const loanOpen = await render("/projects/loan");
  expect(loanOpen.w.findAll('[data-test="order-row"]')).toHaveLength(1);
  loanOpen.w.unmount();
  const done = await render("/projects/loan?filter=done");
  expect(done.w.findAll('[data-test="order-row"]')).toHaveLength(1);
  expect(done.w.text()).toContain("Validate settlement timestamps");
  done.w.unmount();
  const all = await render("/projects/atlas?filter=all");
  expect(all.w.findAll('[data-test="order-row"]')).toHaveLength(2);
  all.w.unmount();
});
it("keeps a ready order whose PR was closed unmerged open", async () => {
  const f = useFactory();
  const wo = f.workOrders.find((o) => o.id === "LF-121")!;
  wo.pr_url = "https://x/pr/1";
  wo.pr_closed_at = "2026-10-25T00:00:00Z";
  const { w } = await render("/projects/loan");
  expect(w.text()).toContain("Validate settlement timestamps");
  w.unmount();
});
it("filter chips replace the query and keep other params", async () => {
  const { w, router } = await render("/projects/atlas?x=1");
  await w.find('[data-test="chip-done"]').trigger("click");
  await flushPromises();
  expect(router.currentRoute.value.query).toEqual({ x: "1", filter: "done" });
  await w.find('[data-test="chip-open"]').trigger("click");
  await flushPromises();
  expect(router.currentRoute.value.query).toEqual({ x: "1" });
  w.unmount();
});
it("draws five stage segments per row", async () => {
  const { w } = await render("/projects/atlas");
  expect(w.find(".fx-segs").findAll("i")).toHaveLength(5);
  w.unmount();
});
it("New work order sets ?new=1 on the current route", async () => {
  const { w, router } = await render("/projects/atlas");
  await w.find('[data-test="overview-new"]').trigger("click");
  await flushPromises();
  expect(router.currentRoute.value.query.new).toBe("1");
  w.unmount();
});
it("project cards put the project that needs you first", async () => {
  const { w } = await render("/projects", ProjectsView);
  const cards = w.findAll('[data-test="project-card"]');
  expect(cards).toHaveLength(3);
  expect(cards[0].text()).toContain("1 needs you");
  expect(cards[1].text()).toContain("1 needs you");
  expect(cards[2].attributes("href")).toBe("/projects/orbit");
  expect(cards[2].text()).not.toContain("needs you");
  w.unmount();
});
it("filters project cards from the home links", async () => {
  const f = useFactory();
  f.workOrders.find((o) => o.id === "LF-121")!.pr_url = "https://x/pr/1";
  const attention = await render("/projects?filter=attention", ProjectsView);
  expect(attention.w.findAll('[data-test="project-card"]')).toHaveLength(2);
  expect(attention.w.find('[data-test="projects-filter"]').exists()).toBe(true);
  attention.w.unmount();
  const shipped = await render("/projects?filter=shipped", ProjectsView);
  expect(
    shipped.w
      .findAll('[data-test="project-card"]')
      .map((c) => c.attributes("href")),
  ).toEqual(["/projects/loan"]);
  shipped.w.unmount();
});
it("opens the new work order drawer from the projects page", async () => {
  const { w } = await render("/projects?new=1", ProjectsView);
  expect(w.findComponent(NewWorkOrderDrawer).exists()).toBe(true);
  w.unmount();
});
