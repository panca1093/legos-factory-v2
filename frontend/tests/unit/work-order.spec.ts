import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createRouter, createMemoryHistory } from "vue-router";
import { beforeEach, expect, it, vi } from "vitest";
import WorkOrderView from "../../src/features/factory/work-orders/WorkOrderView.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import { orders, projects, user, tasks } from "../fixtures/workspace";
let closed = 0;
beforeEach(() => {
  closed = 0;
  setActivePinia(createPinia());
  vi.stubGlobal(
    "EventSource",
    class {
      onmessage = null;
      addEventListener() {}
      close() {
        closed++;
      }
    },
  );
  const f = useFactory();
  f.projects = projects;
  useSession().user = user;
  vi.spyOn(f, "loadDetail").mockImplementation(async (id) => {
    f.selectedID = id;
    f.detail = {
      work_order: structuredClone(orders.find((w) => w.id === id)!),
      plan: tasks,
      runs: [],
    };
  });
});
async function render(id = "LF-128") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/factory/:id", component: WorkOrderView }],
  });
  await router.push(`/factory/${id}`);
  await router.isReady();
  const w = mount(WorkOrderView, { global: { plugins: [router] } });
  await flushPromises();
  return { w, router };
}
it("changes the order identity without retaining the old stream or data", async () => {
  const { w, router } = await render();
  await router.push("/factory/LF-127");
  await flushPromises();
  expect(w.find("h1").text()).toBe("Add accessible product search");
  expect(closed).toBe(1);
  w.unmount();
  expect(closed).toBe(2);
});
it("only allows the creator or admin to answer a clarification", async () => {
  useSession().user = { ...user, id: "other", role: "ENGINEER" };
  const { w } = await render();
  expect(w.find('[data-test="clarify"]').exists()).toBe(false);
  w.unmount();
});
it("submits human revision notes and approval through existing contracts", async () => {
  const f = useFactory();
  const review = {
    ...orders[0],
    state: "ARCHITECT_REVIEW" as const,
    clarif_question: "",
    plan_review: "pending" as const,
  };
  vi.mocked(f.loadDetail).mockImplementation(async () => {
    f.detail = { work_order: review, plan: tasks, runs: [] };
  });
  const decision = vi.spyOn(f, "planDecision").mockResolvedValue();
  const { w } = await render();
  expect(w.find('[data-test="foreman-reviewing"]').exists()).toBe(true);
  expect(
    w.find('[data-test="approve-plan"]').attributes("disabled"),
  ).toBeDefined();
  review.plan_review = "approved" as never;
  f.detail = { work_order: { ...review }, plan: tasks, runs: [] };
  await flushPromises();
  await w.find('[data-test="approve-plan"]').trigger("click");
  await flushPromises();
  expect(decision).toHaveBeenCalledWith("LF-128", "approve");
  await w.find('[data-test="revise-plan"]').trigger("click");
  await flushPromises();
  const input = document.querySelector<HTMLTextAreaElement>("#decision-notes")!;
  input.value = "Preserve the old contract";
  input.dispatchEvent(new Event("input", { bubbles: true }));
  document
    .querySelector<HTMLButtonElement>('[data-test="save-decision"]')!
    .click();
  await flushPromises();
  expect(decision).toHaveBeenCalledWith(
    "LF-128",
    "revise",
    "Preserve the old contract",
  );
  w.unmount();
});

it("pauses a running order and resumes a guided step from the run banner", async () => {
  const f = useFactory();
  const pause = vi.spyOn(f, "pause").mockResolvedValue();
  const resume = vi.spyOn(f, "resume").mockResolvedValue();
  const { w, router } = await render("LF-127");
  await router.replace({ query: { tab: "graph" } });
  await flushPromises();
  await w.find('[data-test="execution-pause"]').trigger("click");
  await flushPromises();
  expect(pause).toHaveBeenCalledWith("LF-127");
  f.detail = {
    work_order: {
      ...f.detail!.work_order,
      autonomy: "guided",
      paused_at: "2026-10-24T00:00:00Z",
      pause_reason:
        "Guided autonomy: build step finished; resume to run the next step",
    },
    plan: tasks,
    runs: [],
  };
  await flushPromises();
  expect(w.find('[data-test="pause-reason"]').text()).toContain("Guided");
  const step = w.find('[data-test="execution-step"]');
  expect(step.text()).toContain("Next step");
  await step.trigger("click");
  await flushPromises();
  expect(resume).toHaveBeenCalledWith("LF-127");
  w.unmount();
});
