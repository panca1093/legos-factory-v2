import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createMemoryHistory, createRouter } from "vue-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import FactoryView from "../../src/features/factory/FactoryView.vue";
import WorkOrderView from "../../src/features/factory/work-orders/WorkOrderView.vue";
import { isDeletable } from "../../src/composables/presentation";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import { orders, projects, tasks, user } from "../fixtures/workspace";
import type { WorkOrder } from "../../src/types/api";

const fetchMock = vi.fn();
let queued: Response[] = [];
const reply = (status: number, body = "") =>
  queued.push(new Response(status === 204 ? null : body, { status }));
const deletes = () =>
  fetchMock.mock.calls.filter((c) => c[1]?.method === "DELETE");
const conflict = (message: string) =>
  reply(409, JSON.stringify({ error: { code: "CONFLICT", message } }));
const dom = <T extends Element>(sel: string) => document.querySelector<T>(sel)!;

beforeEach(() => {
  setActivePinia(createPinia());
  queued = [];
  fetchMock.mockReset();
  fetchMock.mockImplementation(async (_url: string, init?: RequestInit) =>
    init?.method === "DELETE" ? queued.shift()! : new Response("{}"),
  );
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal(
    "EventSource",
    class {
      addEventListener() {}
      close() {}
    },
  );
  const f = useFactory();
  f.projects = structuredClone(projects);
  f.workOrders = structuredClone(orders);
  useSession().user = user;
  vi.spyOn(f, "refreshProjects").mockResolvedValue();
  vi.spyOn(f, "refreshWorkOrders").mockResolvedValue();
});
afterEach(() => {
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
});

describe("isDeletable", () => {
  const o = (patch: Partial<WorkOrder>) => ({ ...orders[0], ...patch });
  it("allows cancelled and READY with a finished or absent PR only", () => {
    expect(isDeletable(o({ state: "CANCELLED" }))).toBe(true);
    expect(isDeletable(o({ state: "READY" }))).toBe(true);
    expect(isDeletable(o({ state: "READY", pr_ref: "7" }))).toBe(false);
    expect(
      isDeletable(o({ state: "READY", pr_ref: "7", pr_merged_at: "t" })),
    ).toBe(true);
    expect(
      isDeletable(o({ state: "READY", pr_ref: "7", pr_closed_at: "t" })),
    ).toBe(true);
    expect(
      isDeletable(o({ state: "READY", pr_ref: "7", pr_track_failed_at: "t" })),
    ).toBe(true);
    expect(isDeletable(o({ state: "IMPLEMENTING" }))).toBe(false);
    expect(isDeletable(o({ state: "BLOCKED" }))).toBe(true);
    expect(isDeletable(o({ state: "NEEDS_HUMAN_REVIEW" }))).toBe(false);
  });
});

describe("store actions", () => {
  it("deleteWorkOrder removes the order and clears its open detail", async () => {
    const f = useFactory();
    f.selectedID = "LF-121";
    reply(204);
    await f.deleteWorkOrder("LF-121");
    expect(deletes()[0][0]).toBe("/api/v1/work-orders/LF-121");
    expect(deletes()[0][1].method).toBe("DELETE");
    expect(f.workOrders.map((w) => w.id)).not.toContain("LF-121");
    expect(f.selectedID).toBe("");
  });
  it("deleteProject removes the project and all its work orders", async () => {
    const f = useFactory();
    reply(204);
    await f.deleteProject("loan");
    expect(f.projects.map((p) => p.id)).toEqual(["atlas", "orbit"]);
    expect(f.workOrders.map((w) => w.id)).toEqual([
      "LF-127",
      "LF-125",
      "LF-124",
    ]);
  });
  it("treats 404 as already deleted for both actions", async () => {
    const f = useFactory();
    f.selectedID = "LF-121";
    reply(
      404,
      JSON.stringify({ error: { code: "NOT_FOUND", message: "gone" } }),
    );
    await f.deleteWorkOrder("LF-121");
    expect(f.workOrders.map((w) => w.id)).not.toContain("LF-121");
    expect(f.selectedID).toBe("");
    reply(
      404,
      JSON.stringify({ error: { code: "NOT_FOUND", message: "gone" } }),
    );
    await f.deleteProject("loan");
    expect(f.projects.map((p) => p.id)).toEqual(["atlas", "orbit"]);
    expect(f.workOrders.some((w) => w.project_id === "loan")).toBe(false);
  });
  it("keeps state and throws the server message on 409", async () => {
    const f = useFactory();
    conflict("2 work orders are still active");
    await expect(f.deleteProject("loan")).rejects.toThrow(
      "2 work orders are still active",
    );
    expect(f.projects).toHaveLength(3);
    expect(f.workOrders).toHaveLength(5);
    conflict("work order is not terminal");
    await expect(f.deleteWorkOrder("LF-128")).rejects.toThrow("not terminal");
    expect(f.workOrders).toHaveLength(5);
  });
});

async function renderFactory(url: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/factory", component: FactoryView }],
  });
  await router.push(url);
  await router.isReady();
  const w = mount(FactoryView, {
    global: { plugins: [router] },
    attachTo: document.body,
  });
  await flushPromises();
  return { w, router };
}
async function openProjectDelete(
  w: Awaited<ReturnType<typeof renderFactory>>["w"],
) {
  await w.find('[aria-label="Project actions"]').trigger("click");
  await w.find(".danger-text").trigger("click");
  await flushPromises();
}

describe("project delete dialog", () => {
  it("shows the count, blocks confirm while orders are active, focuses Cancel and closes on Escape", async () => {
    const { w } = await renderFactory("/factory?project=loan");
    await openProjectDelete(w);
    const dialog = dom('[role="dialog"]');
    expect(dialog.textContent).toContain("Loan Platform");
    expect(dom('[data-test="delete-count"]').textContent).toBe("2 work orders");
    expect(dialog.textContent).toContain("1 work order is still active");
    expect(dialog.textContent).toContain(
      "GitHub pull requests or branches are not",
    );
    expect(
      dom<HTMLButtonElement>('[data-test="confirm-delete"]').disabled,
    ).toBe(true);
    expect(document.activeElement?.textContent?.trim()).toBe("Cancel");
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
    );
    await flushPromises();
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(deletes()).toHaveLength(0);
    w.unmount();
  });
  it("deletes an all-terminal project and returns to All projects", async () => {
    useFactory().workOrders.find((o) => o.id === "LF-124")!.state = "CANCELLED";
    const { w, router } = await renderFactory("/factory?project=orbit");
    await openProjectDelete(w);
    const confirm = dom<HTMLButtonElement>('[data-test="confirm-delete"]');
    expect(confirm.disabled).toBe(false);
    expect(confirm.textContent).toContain("Delete project");
    reply(204);
    confirm.click();
    await flushPromises();
    expect(deletes()[0][0]).toBe("/api/v1/projects/orbit");
    expect(useFactory().projects.map((p) => p.id)).toEqual(["atlas", "loan"]);
    expect(router.currentRoute.value.query.project).toBeUndefined();
    w.unmount();
  });
  it("shows the server message when the delete is refused", async () => {
    useFactory().workOrders.find((o) => o.id === "LF-124")!.state = "CANCELLED";
    const { w } = await renderFactory("/factory?project=orbit");
    await openProjectDelete(w);
    conflict("1 work order is still active");
    dom<HTMLButtonElement>('[data-test="confirm-delete"]').click();
    await flushPromises();
    expect(dom('[role="dialog"]').textContent).toContain(
      "1 work order is still active",
    );
    expect(useFactory().projects).toHaveLength(3);
    w.unmount();
  });
});

describe("work order delete", () => {
  async function renderOrder(id: string) {
    const f = useFactory();
    f.projects = projects;
    vi.spyOn(f, "loadDetail").mockImplementation(async (oid) => {
      f.selectedID = oid;
      f.detail = {
        work_order: structuredClone(orders.find((x) => x.id === oid)!),
        plan: tasks,
        runs: [],
      };
    });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/factory", component: { render: () => null } },
        { path: "/factory/:id", component: WorkOrderView },
      ],
    });
    await router.push(`/factory/${id}`);
    await router.isReady();
    const w = mount(WorkOrderView, {
      global: { plugins: [router] },
      attachTo: document.body,
    });
    await flushPromises();
    return { w, router };
  }
  it("hides Delete for a non-terminal order", async () => {
    const { w } = await renderOrder("LF-128");
    expect(w.find('[data-test="delete-order"]').exists()).toBe(false);
    w.unmount();
  });
  it("confirms with the title, deletes and leaves for the factory floor", async () => {
    const { w, router } = await renderOrder("LF-121");
    await w.find('[data-test="delete-order"]').trigger("click");
    await flushPromises();
    expect(dom('[role="dialog"]').textContent).toContain(
      "Validate settlement timestamps",
    );
    expect(dom('[role="dialog"]').textContent).toContain("history");
    expect(document.activeElement?.textContent?.trim()).toBe("Cancel");
    reply(204);
    dom<HTMLButtonElement>('[data-test="confirm-delete"]').click();
    await flushPromises();
    expect(deletes()[0][0]).toBe("/api/v1/work-orders/LF-121");
    expect(router.currentRoute.value.path).toBe("/factory");
    expect(useFactory().workOrders.map((o) => o.id)).not.toContain("LF-121");
    w.unmount();
  });
});
