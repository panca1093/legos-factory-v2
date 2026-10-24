import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createRouter, createMemoryHistory } from "vue-router";
import { beforeEach, expect, it, vi } from "vitest";
import AppShell from "../../src/app/layouts/AppShell.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
beforeEach(() => {
  setActivePinia(createPinia());
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      text: async () =>
        JSON.stringify({
          projects: [],
          work_orders: [],
          metrics: {},
          budget: {},
          usage: {},
        }),
    }),
  );
});
it("shows navigation, the prototype desktop topbar, search shortcut and theme toggle", async () => {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/:pathMatch(.*)*", component: { template: "<div />" } }],
  });
  await router.push("/factory");
  await router.isReady();
  useSession().user = { id: "u", name: "Panca", email: "p@x", role: "OWNER" };
  const f = useFactory();
  vi.spyOn(f, "refreshProjects").mockResolvedValue();
  vi.spyOn(f, "refreshWorkOrders").mockResolvedValue();
  vi.spyOn(f, "refreshOverview").mockResolvedValue();
  const w = mount(AppShell, {
    global: { plugins: [router] },
    attachTo: document.body,
  });
  await flushPromises();
  for (const label of ["Overview", "Factory", "Knowledge", "Settings"])
    expect(w.find(`a[aria-label="${label}"]`).exists()).toBe(true);
  expect(w.find(".topbar .search-trigger").exists()).toBe(true);
  document.dispatchEvent(
    new KeyboardEvent("keydown", { key: "k", metaKey: true, bubbles: true }),
  );
  await flushPromises();
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain(
    "Find your next piece",
  );
  await w.find('[aria-label="Switch theme"]').trigger("click");
  expect(document.documentElement.dataset.theme).toBe("dark");
  w.unmount();
  document.body.innerHTML = "";
});
