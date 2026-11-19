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
  await router.push("/projects");
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
  for (const label of ["Home", "Chats", "Projects", "Reviews", "Knowledge"])
    expect(
      w
        .find(
          `.topnav a[href="${label === "Home" ? "/" : "/" + label.toLowerCase()}"]`,
        )
        .text(),
    ).toContain(label);
  expect(w.find(".topbar .kbtn").exists()).toBe(true);
  expect(w.find(".avatar-btn").exists()).toBe(true);
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
it("shows compact agent limits and opens the detail panel", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => ({
      ok: true,
      text: async () =>
        JSON.stringify(
          url.endsWith("/agents")
            ? {
                agents: [
                  {
                    id: "claude",
                    name: "Claude",
                    installed: true,
                    enabled: true,
                    accounts: [
                      {
                        id: "a",
                        label: "System default",
                        active: true,
                        status: "signed_in",
                        usage: {
                          session: { used_pct: 42 },
                          week: { used_pct: 93 },
                        },
                      },
                    ],
                  },
                  {
                    id: "codex",
                    name: "Codex",
                    installed: true,
                    enabled: false,
                    accounts: [],
                  },
                ],
              }
            : { projects: [], work_orders: [] },
        ),
    })),
  );
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/:pathMatch(.*)*", component: { template: "<div />" } }],
  });
  await router.push("/");
  await router.isReady();
  const f = useFactory();
  vi.spyOn(f, "refreshProjects").mockResolvedValue();
  vi.spyOn(f, "refreshWorkOrders").mockResolvedValue();
  vi.spyOn(f, "refreshOverview").mockResolvedValue();
  vi.spyOn(f, "refreshUsage").mockResolvedValue();
  const w = mount(AppShell, {
    global: { plugins: [router] },
    attachTo: document.body,
  });
  await flushPromises();
  const rows = w.findAll(".uc-row");
  expect(rows.map((r) => r.attributes("data-agent"))).toEqual(["claude"]);
  expect(rows[0].text()).toContain("5h");
  expect(rows[0].text()).toContain("42%");
  expect(rows[0].text()).toContain("93%");
  expect(w.find(".usage-card").text()).not.toContain("$");
  await rows[0].trigger("click");
  const panel = document.body.querySelector(".uc-panel")!;
  expect(panel.textContent).toContain("System default");
  expect(
    [...panel.querySelectorAll('[role="progressbar"]')].map((b) =>
      b.getAttribute("aria-label"),
    ),
  ).toEqual(["Claude Session limit used", "Claude Weekly limit used"]);
  expect(panel.querySelectorAll("i.high")).toHaveLength(1);
  document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
  await flushPromises();
  expect(document.body.querySelector(".uc-panel")).toBeNull();
  w.unmount();
});
