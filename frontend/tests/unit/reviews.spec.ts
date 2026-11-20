import { mount, flushPromises } from "@vue/test-utils";
import { expect, it } from "vitest";
import ReviewPanel from "../../src/features/factory/reviews/ReviewPanel.vue";
import NowCard from "../../src/features/factory/work-orders/NowCard.vue";
import ActivityPanel from "../../src/features/factory/runs/ActivityPanel.vue";
import {
  checks,
  findings,
  runs,
  events,
  orders,
  tasks,
} from "../fixtures/workspace";
it("separates historical passing evidence from current failures and opens real evidence", async () => {
  const evidence = [
    { ...checks[0], outcome: "PASS" as const },
    { ...checks[1], outcome: "FAIL" as const, exit_code: 1 },
  ];
  const w = mount(ReviewPanel, {
    props: { checks: evidence, findings, revision: 1 },
    attachTo: document.body,
  });
  expect(w.text()).toContain("Historical evidence");
  expect(w.text()).toContain("Current revision 1");
  await w.find('[data-check="check-1"]').trigger("click");
  await flushPromises();
  const detail = w.find('[data-test="check-detail"]').text();
  expect(detail).toContain("Exit code: 1");
  expect(detail).toContain("go test ./...");
  w.unmount();
});
const nowCard = (canWrite: boolean, state = "NEEDS_HUMAN_REVIEW") =>
  mount(NowCard, {
    props: {
      order: { ...orders[0], state } as never,
      tasks,
      runs,
      events,
      canWrite,
      canClarify: canWrite,
      busy: false,
    },
  });
it("does not permit viewer PR decisions", () => {
  const w = nowCard(false);
  expect(
    w.find('[data-test="pr-approve"]').attributes("disabled"),
  ).toBeDefined();
  w.unmount();
});
it("offers decisions only at NEEDS_HUMAN_REVIEW", () => {
  expect(
    nowCard(true, "BLOCKED").find('[data-test="pr-decline"]').exists(),
  ).toBe(false);
});
it("emits all supported dispositions for an authorized reviewer", async () => {
  const w = nowCard(true);
  for (const [id, decision] of [
    ["pr-approve", "approve"],
    ["pr-changes", "request_changes"],
    ["pr-decline", "decline"],
  ]) {
    await w.find(`[data-test="${id}"]`).trigger("click");
    expect(w.emitted("decision")?.at(-1)).toEqual([decision]);
  }
  w.unmount();
});
it("preserves actor attribution, events and run errors", () => {
  const w = mount(ActivityPanel, {
    props: {
      events,
      runs: [
        ...runs,
        {
          ...runs[0],
          id: "failure",
          state: "failed",
          error: "Runner unavailable",
        },
      ],
    },
  });
  expect(w.text()).toContain("Architect");
  expect(w.text()).toContain("Recorded a failing baseline");
  expect(w.text()).toContain("Runner unavailable");
  expect(w.text()).toContain("mock");
  w.unmount();
});
