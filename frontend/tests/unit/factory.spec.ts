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
  f.workOrders = orders;
  useSession().user = user;
  vi.spyOn(f, "refreshProjects").mockResolvedValue();
  vi.spyOn(f, "refreshWorkOrders").mockResolvedValue();
});
it("combines project, status and text filters with board/list parity", async () => {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/factory", component: FactoryView }],
  });
  await router.push("/factory?project=atlas&status=attention");
  await router.isReady();
  const w = mount(FactoryView, { global: { plugins: [router] } });
  await flushPromises();
  expect(w.findAll(".order-card")).toHaveLength(1);
  expect(w.find(".order-card").text()).toContain("Ship team invitation flow");
  await w.find('[aria-label="List view"]').trigger("click");
  expect(w.findAll("tbody tr")).toHaveLength(1);
  await w.find('[aria-label="Filter work orders"]').setValue("unmatched");
  expect(w.text()).toContain("No work orders match");
  w.unmount();
});
