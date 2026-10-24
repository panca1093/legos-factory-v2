import { mount, flushPromises } from "@vue/test-utils";
import { expect, it } from "vitest";
import QualityPanel from "../../src/features/factory/reviews/QualityPanel.vue";
import ChangesPanel from "../../src/features/factory/reviews/ChangesPanel.vue";
import ActivityPanel from "../../src/features/factory/runs/ActivityPanel.vue";
import { checks, findings, runs, events } from "../fixtures/workspace";
it("separates historical passing evidence from current failures and opens real evidence", async () => {
  const evidence = [
    { ...checks[0], outcome: "PASS" as const },
    { ...checks[1], outcome: "FAIL" as const, exit_code: 1 },
  ];
  const w = mount(QualityPanel, {
    props: { checks: evidence, findings, revision: 1 },
    attachTo: document.body,
  });
  expect(w.text()).toContain("Historical evidence");
  expect(w.text()).toContain("Current revision 1");
  await w.find('[data-check="check-1"]').trigger("click");
  await flushPromises();
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain(
    "Exit code: 1",
  );
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain(
    "go test ./...",
  );
  w.unmount();
});
it("labels mock output and does not permit viewer PR decisions", () => {
  const w = mount(ChangesPanel, {
    props: {
      runs,
      findings,
      state: "NEEDS_HUMAN_REVIEW",
      canWrite: false,
      busy: false,
    },
  });
  expect(w.text()).toContain("mock");
  expect(w.text()).toContain("Builder output");
  expect(
    w.find('[data-test="pr-approve"]').attributes("disabled"),
  ).toBeDefined();
  w.unmount();
});
it("emits all supported dispositions for an authorized reviewer", async () => {
  const w = mount(ChangesPanel, {
    props: {
      runs,
      findings,
      state: "NEEDS_HUMAN_REVIEW",
      canWrite: true,
      busy: false,
    },
  });
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
