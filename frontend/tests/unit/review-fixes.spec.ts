import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createRouter, createMemoryHistory } from "vue-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import WorkOrderView from "../../src/features/factory/work-orders/WorkOrderView.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import type { WOEvent, WorkOrder } from "../../src/types/api";
import { orders, projects, runs, tasks, user } from "../fixtures/workspace";

describe("Now card and page", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.stubGlobal(
      "EventSource",
      class {
        addEventListener() {}
        close() {}
      },
    );
    useSession().user = user;
  });
  async function render(
    state: WorkOrder["state"],
    extra: Partial<WorkOrder>,
    evs: WOEvent[] = [],
  ) {
    const f = useFactory();
    f.projects = projects;
    vi.spyOn(f, "loadDetail").mockImplementation(async (id) => {
      f.selectedID = id;
      f.detail = {
        work_order: { ...orders[0], state, clarif_question: "", ...extra },
        plan: tasks,
        runs,
      };
      f.events = evs;
    });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/factory", component: { render: () => null } },
        { path: "/factory/:id", component: WorkOrderView },
      ],
    });
    await router.push("/factory/LF-128");
    await router.isReady();
    const w = mount(WorkOrderView, { global: { plugins: [router] } });
    await flushPromises();
    return w;
  }
  const ev = (kind: string, message: string): WOEvent => ({
    id: Math.random(),
    work_order_id: "LF-128",
    actor: "factory",
    kind,
    message,
    created_at: "2026-10-25T00:00:00Z",
  });

  it("BLOCKED hides Pause and Cancel and shows the readiness reason", async () => {
    const w = await render("BLOCKED", {}, [
      ev("readiness_blocked", "2 blocker findings are open"),
    ]);
    expect(w.find('[data-test="blocked-reason"]').text()).toContain(
      "2 blocker findings are open",
    );
    expect(w.find('[data-test="execution-pause"]').exists()).toBe(false);
    expect(w.find('[data-test="cancel-order"]').exists()).toBe(false);
    expect(w.find('[role="tab"][aria-selected="true"]').text()).toBe("Review");
    w.unmount();
  });
  it("hides Cancel at NEEDS_HUMAN_REVIEW", async () => {
    const w = await render("NEEDS_HUMAN_REVIEW", {});
    expect(w.find('[data-test="cancel-order"]').exists()).toBe(false);
    w.unmount();
  });
  it("sends an Inspector escalation (no open question) to Review", async () => {
    const w = await render("WAITING_FOR_HUMAN", {}, [
      ev("escalated", "Blocker findings remain after max revision cycles"),
    ]);
    expect(w.find('[data-test="now-card"]').text()).toContain(
      "The Inspector escalated",
    );
    expect(w.find('[data-test="now-card"]').text()).toContain(
      "Blocker findings remain",
    );
    expect(w.find('[role="tab"][aria-selected="true"]').text()).toBe("Review");
    expect(w.find('[data-test="clarify"]').exists()).toBe(false);
    w.unmount();
  });
  it("shows the answered clarification on the Activity tab and the crew models", async () => {
    const w = await render("QUEUED", {
      clarif_question: "How long?",
      clarif_answer: "24 hours",
      builder_model: "mock/builder",
    });
    await w.findAll('[role="tab"]')[0].trigger("click");
    await flushPromises();
    expect(w.find('[data-doc="decision"]').text()).toContain("24 hours");
    expect(w.find('[data-test="crew-models"]').text()).toContain(
      "Builder: mock/builder",
    );
    w.unmount();
  });
});
