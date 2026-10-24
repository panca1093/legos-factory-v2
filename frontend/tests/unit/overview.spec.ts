import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, expect, it, vi } from "vitest";
import OverviewView from "../../src/features/overview/OverviewView.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import { overview, orders, projects, user } from "../fixtures/workspace";
beforeEach(() => {
  setActivePinia(createPinia());
  const f = useFactory();
  f.overview = structuredClone(overview);
  f.workOrders = structuredClone(orders);
  f.projects = projects;
  useSession().user = user;
  vi.spyOn(f, "refreshOverview").mockResolvedValue();
  vi.spyOn(f, "refreshProjects").mockResolvedValue();
  vi.spyOn(f, "refreshWorkOrders").mockResolvedValue();
});
const render = () =>
  mount(OverviewView, {
    global: { stubs: { RouterLink: { template: "<a><slot/></a>" } } },
  });
it("uses the approved crew floor and includes the pending PR decision", async () => {
  const w = render();
  await flushPromises();
  expect(w.find(".floor .crew-track").exists()).toBe(true);
  expect(w.findAll(".crew-station")).toHaveLength(5);
  expect(w.findAll(".attention-card")).toHaveLength(2);
  expect(w.text()).toContain("Ship team invitation flow");
  w.unmount();
});
it("shows zero dollars, an unset cap, and accurate pending-job language", async () => {
  const f = useFactory();
  f.overview!.usage.cost_micros = 0;
  f.overview!.budget.limit_micros = 0;
  const w = render();
  await flushPromises();
  expect(w.text()).toContain("$0.00");
  expect(w.text()).toContain("No budget cap");
  expect(w.text()).toContain("Pending / running jobs");
  expect(w.text()).toContain("availability not reported");
  w.unmount();
});
it("offers recovery for empty and failed workspaces", async () => {
  const f = useFactory();
  f.projects = [];
  f.workOrders = [];
  vi.mocked(f.refreshOverview).mockRejectedValue(
    new Error("Workspace unavailable"),
  );
  const w = render();
  await flushPromises();
  expect(w.text()).toContain("Workspace unavailable");
  expect(w.find('[role="alert"]').exists()).toBe(true);
  expect(w.text()).toContain("No projects yet");
  w.unmount();
});
