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
  };
  vi.mocked(f.loadDetail).mockImplementation(async () => {
    f.detail = { work_order: review, plan: tasks, runs: [] };
  });
  const decision = vi.spyOn(f, "planDecision").mockResolvedValue();
  const { w } = await render();
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

it("keeps unsupported prototype execution controls visibly disabled", async () => {
  const { w } = await render("LF-127");
  const router = w.vm.$router;
  await router.replace({ query: { tab: "graph" } });
  await flushPromises();
  expect(
    w.find('[data-test="execution-pause"]').attributes("disabled"),
  ).toBeDefined();
  expect(
    w.find('[data-test="execution-step"]').attributes("disabled"),
  ).toBeDefined();
  expect(w.text()).toContain("Manual execution controls are unavailable");
  await router.replace({ query: { tab: "activity" } });
  await flushPromises();
  expect(
    w.find('[data-test="execution-pause"]').attributes("disabled"),
  ).toBeDefined();
  w.unmount();
});
