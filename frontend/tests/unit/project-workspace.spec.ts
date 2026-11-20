import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createMemoryHistory, createRouter } from "vue-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import AppShell from "../../src/app/layouts/AppShell.vue";
import ProjectRail from "../../src/features/projects/ProjectRail.vue";
import ProjectWorkspace from "../../src/features/projects/ProjectWorkspace.vue";
import { router as appRouter } from "../../src/app/router";
import {
  RAIL_CAP,
  capRail,
  filterOrders,
  isOpenOrder,
  legacyTabPath,
  readFlags,
  railDot,
  repoLabel,
  sortRail,
} from "../../src/features/projects/rail";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import { orders, projects, user } from "../fixtures/workspace";
import type { Project, RailWorkOrder, WorkOrder } from "../../src/types/api";

const row = (
  id: string,
  patch: Partial<RailWorkOrder> = {},
): RailWorkOrder => ({
  id,
  title: `Title ${id}`,
  state: "IMPLEMENTING",
  stage: "build",
  waiting_on_you: false,
  updated_at: "2026-11-01T10:00:00Z",
  ...patch,
});

describe("rail helpers", () => {
  it("sorts the waiting row first, then newest first", () => {
    const sorted = sortRail([
      row("old", { updated_at: "2026-11-01T08:00:00Z" }),
      row("new", { updated_at: "2026-11-01T12:00:00Z" }),
      row("you", { waiting_on_you: true, updated_at: "2026-11-01T01:00:00Z" }),
    ]);
    expect(sorted.map((r) => r.id)).toEqual(["you", "new", "old"]);
  });
  it("caps a section at 8 rows and keeps the selected row visible", () => {
    const rows = Array.from({ length: 12 }, (_, i) => ({ id: `r${i}` }));
    expect(RAIL_CAP).toBe(8);
    const capped = capRail(rows, false);
    expect(capped.shown).toHaveLength(8);
    expect(capped.hidden).toBe(4);
    const picked = capRail(rows, false, "r10");
    expect(picked.shown.map((r) => r.id)).toContain("r10");
    expect(picked.shown).toHaveLength(9);
    expect(picked.hidden).toBe(3);
    expect(capRail(rows, true).shown).toHaveLength(12);
    expect(capRail(rows.slice(0, 8), false).hidden).toBe(0);
  });
  it("maps rows to status dots", () => {
    expect(railDot(row("a", { waiting_on_you: true }))).toBe("waiting");
    expect(railDot(row("b", { state: "BLOCKED", waiting_on_you: true }))).toBe(
      "waiting",
    );
    expect(railDot(row("c", { state: "QUEUED" }))).toBe("queued");
    expect(railDot(row("d"))).toBe("running");
  });
  it("splits open from done", () => {
    const base = orders[0] as WorkOrder;
    const list: WorkOrder[] = [
      { ...base, id: "a", state: "IMPLEMENTING" },
      { ...base, id: "b", state: "CANCELLED" },
      { ...base, id: "c", state: "READY", pr_merged_at: "t" },
      { ...base, id: "d", state: "READY", pr_url: "u", pr_closed_at: "t" },
    ];
    expect(list.map(isOpenOrder)).toEqual([true, false, false, true]);
    expect(filterOrders(list, "done").map((w) => w.id)).toEqual(["b", "c"]);
    expect(filterOrders(list, "all")).toHaveLength(4);
  });
  it("labels repos and legacy tabs", () => {
    const p = (patch: Partial<Project>) => ({ ...projects[0], ...patch });
    expect(repoLabel(p({ github_repo: "acme/pay" }))).toBe(
      "github.com/acme/pay",
    );
    expect(repoLabel(p({ github_repo: "", remote_repo: "o/r" }))).toBe("o/r");
    expect(
      repoLabel(
        p({ github_repo: "", remote_repo: "", repo_url: "/Users/x/legos" }),
      ),
    ).toBe("legos");
    expect(legacyTabPath("p", "factory")).toBe("/projects/p");
    expect(legacyTabPath("p", "pull-requests")).toBe("/projects/p/pulls");
    expect(legacyTabPath("p", "code")).toBe("/projects/p/code");
    expect(legacyTabPath("p", "nope")).toBeNull();
  });
});

describe("stored flags", () => {
  it("ignores stored JSON that is not a plain object", () => {
    for (const v of ["null", "[1]", "3", "oops"]) {
      vi.stubGlobal("localStorage", { getItem: () => v });
      expect(readFlags("k")).toEqual({});
    }
    vi.unstubAllGlobals();
  });
});

describe("redirects", () => {
  let wo: { work_order: { project_id: string } } | null = null;
  beforeEach(async () => {
    setActivePinia(createPinia());
    wo = null;
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        wo
          ? { ok: true, status: 200, text: async () => JSON.stringify(wo) }
          : {
              ok: false,
              status: 404,
              text: async () =>
                JSON.stringify({
                  error: { code: "NOT_FOUND", message: "gone" },
                }),
            },
      ),
    );
    await appRouter.push("/");
  });
  afterEach(() => vi.unstubAllGlobals());
  const go = async (to: string) => {
    await appRouter.push(to);
    return appRouter.currentRoute.value.fullPath;
  };
  it("sends /factory to /projects and keeps the project", async () => {
    expect(await go("/factory")).toBe("/projects");
    expect(await go("/factory?project=p1")).toBe("/projects/p1");
    expect(await go("/factory?project=p1&new=1")).toBe("/projects/p1?new=1");
    expect(await go("/factory?new=1")).toBe("/projects?new=1");
  });
  it("resolves a work order to its own project, or falls back to /projects", async () => {
    wo = { work_order: { project_id: "p9" } };
    expect(await go("/factory/wo_1?tab=review")).toBe(
      "/projects/p9/wo/wo_1?tab=review",
    );
    wo = null;
    expect(await go("/factory/missing")).toBe("/projects");
  });
  it("maps the old hub tabs to the workspace paths", async () => {
    expect(await go("/projects/p1?tab=code")).toBe("/projects/p1/code");
    expect(await go("/projects/p1?tab=pull-requests")).toBe(
      "/projects/p1/pulls",
    );
    expect(await go("/projects/p1?tab=factory&filter=done")).toBe(
      "/projects/p1?filter=done",
    );
    expect(await go("/projects/p1?tab=other")).toBe("/projects/p1?tab=other");
  });
});

function railJSON(rows: RailWorkOrder[], id = "p1") {
  return {
    project: {
      id,
      name: `Name ${id}`,
      repo: `github.com/o/${id}`,
      needs_you: 0,
    },
    work_orders: { open: rows.length, rows },
    pulls: {
      identity: null,
      count: 0,
      rows: [],
      stale: false,
      error: "no_identity",
    },
  };
}

describe("project rail", () => {
  let calls: string[];
  let rail: Record<string, ReturnType<typeof railJSON> | number>;
  let store: Record<string, string>;
  beforeEach(() => {
    setActivePinia(createPinia());
    calls = [];
    store = {};
    rail = {};
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store[k] ?? null,
      setItem: (k: string, v: string) => (store[k] = v),
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        calls.push(url);
        const m = /\/projects\/([^/]+)\/rail/.exec(url);
        const body = m ? rail[m[1]] : {};
        if (typeof body === "number")
          return { ok: false, status: body, text: async () => "{}" };
        return {
          ok: true,
          status: 200,
          text: async () => JSON.stringify(body ?? {}),
        };
      }),
    );
    useSession().user = user;
    const f = useFactory();
    f.projects = projects;
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });
  async function mountRail(path: string) {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/projects", component: { render: () => null } },
        {
          path: "/projects/:id",
          component: { render: () => null },
          children: [
            { path: "", name: "project", component: { render: () => null } },
            {
              path: "wo/:wo",
              name: "project-wo",
              component: { render: () => null },
            },
          ],
        },
      ],
    });
    await router.push(path);
    await router.isReady();
    const w = mount(ProjectRail, {
      global: { plugins: [router], stubs: { UsageCard: true } },
    });
    await flushPromises();
    return { w, router };
  }
  const titles = (w: Awaited<ReturnType<typeof mountRail>>["w"]) =>
    w.findAll('[data-test="rail-wo"]').map((r) => r.text());

  it("shows the project, puts the you row first and links rows into the workspace", async () => {
    rail.p1 = railJSON([
      row("a", { updated_at: "2026-11-01T12:00:00Z" }),
      row("b", {
        waiting_on_you: true,
        state: "WAITING_FOR_HUMAN",
        updated_at: "2026-11-01T01:00:00Z",
      }),
    ]);
    const { w } = await mountRail("/projects/p1");
    expect(w.text()).toContain("Name p1");
    expect(w.text()).toContain("github.com/o/p1");
    expect(w.find('[data-test="rail-factory"]').text()).toBe("Factory");
    expect(w.find(".pr-count").text()).toBe("2 open work orders");
    const rows = w.findAll('[data-test="rail-wo"]');
    expect(rows[0].text()).toContain("Title b");
    expect(rows[0].text()).toContain("you");
    expect(rows[0].attributes("href")).toBe("/projects/p1/wo/b");
    expect(rows[1].text()).not.toContain("you");
    expect(w.find('[data-test="rail-pulls"]').text()).toBe(
      "Connect an account",
    );
    expect(w.find('a[href="/projects"]').text()).toContain("All projects");
    w.unmount();
  });

  it("caps the Factory section at 8 and expands in place", async () => {
    rail.p1 = railJSON(
      Array.from({ length: 11 }, (_, i) =>
        row(`w${i}`, {
          updated_at: `2026-11-01T${String(10 + i).padStart(2, "0")}:00:00Z`,
        }),
      ),
    );
    const { w } = await mountRail("/projects/p1");
    expect(titles(w)).toHaveLength(8);
    await w.find(".rt-more").trigger("click");
    expect(titles(w)).toHaveLength(11);
    expect(w.find(".rt-more").text()).toBe("Show less");
    w.unmount();
  });

  it("remembers a collapsed section", async () => {
    rail.p1 = railJSON([row("a")]);
    const first = await mountRail("/projects/p1");
    await first.w
      .find('[aria-label="Toggle Factory section"]')
      .trigger("click");
    expect(titles(first.w)).toHaveLength(0);
    expect(JSON.parse(store["legos-project-rail-sections"])).toEqual({
      factory: true,
    });
    first.w.unmount();
    const second = await mountRail("/projects/p1");
    expect(titles(second.w)).toHaveLength(0);
    second.w.unmount();
  });

  it("marks the selected work order and steps through selections with back and forward", async () => {
    rail.p1 = railJSON([row("a"), row("b")]);
    const { w, router } = await mountRail("/projects/p1/wo/a");
    expect(
      w.findAll(".pr-row.active").map((r) => r.attributes("href")),
    ).toEqual(["/projects/p1/wo/a"]);
    await w.findAll('[data-test="rail-wo"]')[1].trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.path).toBe(
      w.findAll('[data-test="rail-wo"]')[1].attributes("href"),
    );
    expect(router.currentRoute.value.path).toBe("/projects/p1/wo/b");
    router.back();
    await flushPromises();
    expect(router.currentRoute.value.path).toBe("/projects/p1/wo/a");
    expect(
      w.findAll(".pr-row.active").map((r) => r.attributes("href")),
    ).toEqual(["/projects/p1/wo/a"]);
    router.forward();
    await flushPromises();
    expect(router.currentRoute.value.path).toBe("/projects/p1/wo/b");
    w.unmount();
  });

  it("drops a deleted work order from the rail", async () => {
    rail.p1 = railJSON([row("a"), row("b")]);
    const f = useFactory();
    f.workOrders = [
      { ...(orders[1] as WorkOrder), id: "a", project_id: "p1" },
      { ...(orders[1] as WorkOrder), id: "b", project_id: "p1" },
    ];
    const { w } = await mountRail("/projects/p1");
    expect(titles(w)).toEqual(["Title a", "Title b"]);
    rail.p1 = railJSON([row("a")]);
    await f.deleteWorkOrder("b");
    await flushPromises();
    expect(titles(w)).toEqual(["Title a"]);
    w.unmount();
  });

  it("reloads when the project in the route changes", async () => {
    rail.p1 = railJSON([row("a")], "p1");
    rail.p2 = railJSON([row("z")], "p2");
    const { w, router } = await mountRail("/projects/p1");
    expect(titles(w)).toEqual(["Title a"]);
    await router.push("/projects/p2");
    await flushPromises();
    expect(titles(w)).toEqual(["Title z"]);
    expect(calls.filter((c) => c.includes("/rail"))).toHaveLength(2);
    w.unmount();
  });

  it("debounces live events for this project into one wo-part refetch", async () => {
    rail.p1 = railJSON([row("a")]);
    const f = useFactory();
    f.workOrders = [
      { ...(orders[1] as WorkOrder), id: "x1", project_id: "p1" },
      { ...(orders[1] as WorkOrder), id: "y1", project_id: "other" },
    ];
    const { w } = await mountRail("/projects/p1");
    vi.useFakeTimers();
    const ev = (id: number, wo: string) => ({
      id,
      work_order_id: wo,
      actor: "a",
      kind: "k",
      message: "m",
    });
    f.liveEvents = [ev(1, "y1")];
    await flushPromises();
    f.liveEvents = [ev(2, "x1"), ev(1, "y1")];
    await flushPromises();
    f.liveEvents = [ev(3, "x1"), ev(2, "x1")];
    await flushPromises();
    await vi.advanceTimersByTimeAsync(999);
    expect(calls.filter((c) => c.includes("part=wo"))).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(2);
    expect(calls.filter((c) => c.includes("part=wo"))).toHaveLength(1);
    w.unmount();
    f.liveEvents = [ev(4, "x1")];
    await vi.advanceTimersByTimeAsync(2000);
    expect(calls.filter((c) => c.includes("part=wo"))).toHaveLength(1);
  });

  it("shows a not-found rail for an unknown project", async () => {
    rail.nope = 404;
    const { w } = await mountRail("/projects/nope");
    expect(w.text()).toContain("Project not found");
    expect(w.find('a[href="/projects"]').exists()).toBe(true);
    w.unmount();
  });
});

describe("project workspace", () => {
  let store: Record<string, string>;
  beforeEach(() => {
    setActivePinia(createPinia());
    store = {};
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store[k] ?? null,
      setItem: (k: string, v: string) => (store[k] = v),
    });
    vi.stubGlobal("innerWidth", 1440);
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, status: 200, text: async () => "{}" })),
    );
    useSession().user = user;
    const f = useFactory();
    f.projects = projects;
    f.workOrders = structuredClone(orders);
    vi.spyOn(f, "refreshProjects").mockResolvedValue();
    vi.spyOn(f, "refreshWorkOrders").mockResolvedValue();
  });
  afterEach(() => vi.unstubAllGlobals());
  async function mountWorkspace(path: string) {
    const blank = { render: () => null };
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/projects", component: blank },
        {
          path: "/projects/:id",
          component: ProjectWorkspace,
          children: [
            { path: "", name: "project", component: blank },
            { path: "wo/:wo", name: "project-wo", component: blank },
            { path: "pulls", name: "project-pulls", component: blank },
            { path: "code", name: "project-code", component: blank },
          ],
        },
      ],
    });
    await router.push(path);
    await router.isReady();
    const w = mount(
      { template: "<RouterView />" },
      { global: { plugins: [router], stubs: { ProjectChecks: true } } },
    );
    await flushPromises();
    return { w, router };
  }

  it("shows the panel by default and remembers Hide and Show", async () => {
    const { w } = await mountWorkspace("/projects/atlas");
    expect(w.find("#pw-panel").exists()).toBe(true);
    const toggle = w.find('[data-test="panel-toggle"]');
    expect(toggle.text()).toBe("Hide panel");
    await toggle.trigger("click");
    expect(w.find("#pw-panel").exists()).toBe(false);
    expect(store["legos-project-panel"]).toBe("0");
    expect(w.find('[data-test="panel-toggle"]').text()).toBe("Show panel");
    await w.find('[data-test="panel-toggle"]').trigger("click");
    expect(store["legos-project-panel"]).toBe("1");
    expect(w.find("#pw-panel").exists()).toBe(true);
    w.unmount();
  });

  it("starts hidden when the choice was Hide, and below 1280px", async () => {
    store["legos-project-panel"] = "0";
    const hidden = await mountWorkspace("/projects/atlas");
    expect(hidden.w.find("#pw-panel").exists()).toBe(false);
    hidden.w.unmount();
    store["legos-project-panel"] = "1";
    vi.stubGlobal("innerWidth", 1100);
    const narrow = await mountWorkspace("/projects/atlas");
    expect(narrow.w.find("#pw-panel").exists()).toBe(false);
    await narrow.w.find('[data-test="panel-toggle"]').trigger("click");
    expect(narrow.w.find("#pw-panel").exists()).toBe(true);
    expect(store["legos-project-panel"]).toBe("1");
    narrow.w.unmount();
  });

  it("disables the toggle on the pull request list only; the code route has its Explorer panel", async () => {
    const list = await mountWorkspace("/projects/atlas/pulls");
    expect(
      list.w.find('[data-test="panel-toggle"]').attributes("disabled"),
    ).toBeDefined();
    expect(list.w.find("#pw-panel").exists()).toBe(false);
    list.w.unmount();
    const code = await mountWorkspace("/projects/atlas/code");
    expect(
      code.w.find('[data-test="panel-toggle"]').attributes("disabled"),
    ).toBeUndefined();
    expect(code.w.find("#pw-panel").exists()).toBe(true);
    code.w.unmount();
  });

  it("shows Details with the project on the overview", async () => {
    const { w } = await mountWorkspace("/projects/atlas");
    expect(w.find('[data-card="details"]').exists()).toBe(true);
    expect(w.find('[data-card="crew"]').exists()).toBe(false);
    w.unmount();
  });

  it("shows Crew with the active role and Details on a work order", async () => {
    const f = useFactory();
    f.detail = {
      work_order: structuredClone(orders[1]),
      plan: [],
      runs: [
        { id: "r1", task_id: null, kind: "builder", state: "running" },
        { id: "r2", task_id: null, kind: "architect", state: "succeeded" },
      ] as never,
    };
    const { w } = await mountWorkspace("/projects/atlas/wo/LF-127");
    const state = (role: string) =>
      w.find(`[data-role="${role}"] .pc-state`).text();
    expect(state("builder")).toBe("working");
    expect(state("architect")).toBe("finished");
    expect(state("foreman")).toBe("idle");
    expect(w.findAll("[data-role]")).toHaveLength(4);
    expect(w.text()).toContain("legos/LF-127");
    expect(
      w
        .findAll("button")
        .find((b) => b.text() === "Open in Code")!
        .attributes("disabled"),
    ).toBeUndefined();
    w.unmount();
  });

  it("work order panel: Details, Plan Progress and What's Happening, the same on every work order tab", async () => {
    const f = useFactory();
    f.detail = {
      work_order: structuredClone(orders[1]),
      plan: [{ id: "t1", seq: 1, title: "Do it" }] as never,
      runs: [
        { id: "r1", task_id: "t1", kind: "builder", state: "succeeded" },
      ] as never,
    };
    for (const tab of ["activity", "progress", "review"]) {
      const { w, router } = await mountWorkspace(
        `/projects/atlas/wo/LF-127?tab=${tab}`,
      );
      const tabs = w.findAll('#pw-panel [role="tab"]').map((t) => t.text());
      expect(tabs).toEqual([
        "Details",
        "Plan Progress 0/1",
        "What’s Happening",
      ]);
      expect(
        w.find('[data-test="tab-happening"]').attributes("aria-selected"),
      ).toBe("true");
      expect(w.findAll("#pw-panel [data-card='crew']")).toHaveLength(1);
      expect(w.findAll("#pw-panel [data-card='handoffs']")).toHaveLength(1);
      expect(w.findAll("[data-card='handoffs']")).toHaveLength(1);
      await w.find('[data-test="tab-details"]').trigger("click");
      await flushPromises();
      expect(router.currentRoute.value.query).toMatchObject({
        tab,
        side: "details",
      });
      w.unmount();
    }
  });

  it("Open in Code opens the work order's worktree folder", async () => {
    const f = useFactory();
    f.detail = { work_order: structuredClone(orders[1]), plan: [], runs: [] };
    const fetchMock = vi.fn(async (url: string) => ({
      ok: true,
      status: 200,
      text: async () =>
        String(url).endsWith("/editor/folders")
          ? JSON.stringify({
              folder: { id: "w9", path: "/d/wt", kind: "worktree" },
            })
          : "{}",
    }));
    vi.stubGlobal("fetch", fetchMock);
    const { w } = await mountWorkspace("/projects/atlas/wo/LF-127");
    await w.find('[data-test="wo-open-code"]').trigger("click");
    await flushPromises();
    const call = fetchMock.mock.calls.find((c) =>
      String(c[0]).endsWith("/api/v1/editor/folders"),
    ) as unknown as [string, { method: string; body: string }];
    expect(call[1].method).toBe("POST");
    expect(JSON.parse(call[1].body)).toEqual({ wo_id: "LF-127" });
    w.unmount();
  });

  it("redirects a work order that belongs to another project", async () => {
    const f = useFactory();
    f.detail = { work_order: structuredClone(orders[1]), plan: [], runs: [] };
    const { w, router } = await mountWorkspace(
      "/projects/loan/wo/LF-127?tab=review",
    );
    f.detail = { work_order: structuredClone(orders[1]), plan: [], runs: [] };
    await flushPromises();
    expect(router.currentRoute.value.fullPath).toBe(
      "/projects/atlas/wo/LF-127?tab=review",
    );
    w.unmount();
  });

  const stubApi = (handler: (url: string) => unknown) =>
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        const body = handler(url);
        return typeof body === "number"
          ? { ok: false, status: body, text: async () => "{}" }
          : { ok: true, status: 200, text: async () => JSON.stringify(body) };
      }),
    );

  it("redirects a work order deep link to its own project when the project is missing", async () => {
    useFactory().projects = [];
    stubApi((url) =>
      url.includes("/work-orders/LF-127")
        ? { work_order: { id: "LF-127", project_id: "atlas" } }
        : {},
    );
    const { w, router } = await mountWorkspace(
      "/projects/ghost/wo/LF-127?tab=review",
    );
    expect(router.currentRoute.value.fullPath).toBe(
      "/projects/atlas/wo/LF-127?tab=review",
    );
    w.unmount();
  });

  it("redirects the same way when the projects fetch fails", async () => {
    const f = useFactory();
    f.projects = [];
    vi.mocked(f.refreshProjects).mockRejectedValue(new Error("boom"));
    stubApi(() => ({ work_order: { id: "LF-127", project_id: "atlas" } }));
    const { w, router } = await mountWorkspace("/projects/ghost/wo/LF-127");
    expect(router.currentRoute.value.path).toBe("/projects/atlas/wo/LF-127");
    w.unmount();
  });

  it("shows a missing project when the work order is not found either", async () => {
    useFactory().projects = [];
    stubApi(() => 404);
    const { w, router } = await mountWorkspace("/projects/ghost/wo/nope");
    expect(router.currentRoute.value.path).toBe("/projects/ghost/wo/nope");
    expect(w.find('[data-test="project-missing"]').exists()).toBe(true);
    w.unmount();
  });

  it("shows a load error with Retry instead of the missing state", async () => {
    const f = useFactory();
    f.projects = [];
    vi.mocked(f.refreshProjects).mockRejectedValueOnce(new Error("db down"));
    const { w } = await mountWorkspace("/projects/atlas");
    expect(w.find('[data-test="project-missing"]').exists()).toBe(false);
    expect(w.find('[data-test="project-error"]').text()).toContain("db down");
    vi.mocked(f.refreshProjects).mockImplementation(async () => {
      f.projects = projects;
    });
    await w.find('[data-test="project-retry"]').trigger("click");
    await flushPromises();
    expect(w.find('[data-test="project-error"]').exists()).toBe(false);
    expect(w.find(".pw").exists()).toBe(true);
    w.unmount();
  });

  it("?checks=1 opens a hidden panel on the Details tab", async () => {
    store["legos-project-panel"] = "0";
    vi.stubGlobal("innerWidth", 1100);
    const { w } = await mountWorkspace("/projects/atlas?checks=1");
    expect(w.find("#pw-panel").exists()).toBe(true);
    expect(w.find("#sp-details").attributes("style") ?? "").not.toContain(
      "display: none",
    );
    w.unmount();
  });

  it("shows a friendly empty state for an unknown project", async () => {
    const { w } = await mountWorkspace("/projects/ghost");
    expect(w.find('[data-test="project-missing"]').exists()).toBe(true);
    expect(w.find('a[href="/projects"]').exists()).toBe(true);
    w.unmount();
  });
});

describe("app shell", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: true,
        status: 200,
        text: async () =>
          JSON.stringify({ projects: [], work_orders: [], chats: [] }),
      })),
    );
  });
  afterEach(() => vi.unstubAllGlobals());
  async function shell(path: string) {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/:pathMatch(.*)*", component: { render: () => null } }],
    });
    await router.push(path);
    await router.isReady();
    useSession().user = user;
    const f = useFactory();
    vi.spyOn(f, "refreshProjects").mockResolvedValue();
    vi.spyOn(f, "refreshWorkOrders").mockResolvedValue();
    vi.spyOn(f, "refreshOverview").mockResolvedValue();
    const w = mount(AppShell, {
      global: { plugins: [router], stubs: { UsageCard: true } },
    });
    await flushPromises();
    return w;
  }
  it("swaps the chat rail for the project rail on project routes", async () => {
    const project = await shell("/projects/p1/wo/x");
    expect(project.find("#rail").attributes("aria-label")).toBe(
      "Project sidebar",
    );
    expect(project.find(".pr").exists()).toBe(true);
    expect(project.find(".rail-tree").exists()).toBe(false);
    expect(project.findAll("usage-card-stub")).toHaveLength(1);
    project.unmount();
    const chats = await shell("/chats");
    expect(chats.find(".pr").exists()).toBe(false);
    expect(chats.find(".rail-tree").exists()).toBe(true);
    chats.unmount();
  });
  it("puts the attention dot on Projects and has Projects and Reviews in the nav", async () => {
    const w = await shell("/projects");
    const labels = w.findAll(".topnav a").map((a) => a.text());
    expect(labels).toEqual([
      "Home",
      "Chats",
      "Projects",
      "Reviews",
      "Knowledge",
    ]);
    w.unmount();
  });
});
