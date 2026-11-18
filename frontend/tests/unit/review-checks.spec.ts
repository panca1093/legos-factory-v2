import { mount } from "@vue/test-utils";
import { expect, it } from "vitest";
import ChecksGrid from "../../src/features/factory/reviews/ChecksGrid.vue";
import { baselineFailures } from "../../src/features/factory/reviews/checks";
import type { QualityCheck } from "../../src/types/api";

const q = (
  id: string,
  name: string,
  outcome: QualityCheck["outcome"],
  revision = 1,
): QualityCheck => ({
  id,
  name,
  command: `run ${name}`,
  outcome,
  exit_code: outcome === "PASS" ? 0 : 1,
  output: "",
  revision,
  created_at: "2026-11-18T00:00:00Z",
});

it("tags a check whose baseline run also failed", () => {
  const set = baselineFailures([
    q("qc_base_1", "api test", "FAIL"),
    q("qc_2", "api test", "FAIL"),
  ]);
  expect(set.has("api test")).toBe(true);
});
it("does not tag a check whose baseline passed", () => {
  expect(baselineFailures([q("qc_base_1", "api test", "PASS")]).size).toBe(0);
});
it("does not tag a check without a baseline row", () => {
  expect(baselineFailures([q("qc_2", "api test", "FAIL")]).size).toBe(0);
});
it("draws no baseline tiles and keeps them out of the totals", () => {
  const w = mount(ChecksGrid, {
    props: {
      revision: 1,
      checks: [
        q("qc_base_1", "api test", "FAIL", 0),
        q("qc_base_2", "api vet", "PASS", 0),
        q("qc_1", "api test", "FAIL"),
        q("qc_2", "api vet", "PASS"),
        q("qc_3", "api build", "FAIL"),
      ],
    },
  });
  expect(w.findAll(".ck")).toHaveLength(3);
  expect(w.find('[data-check="qc_base_1"]').exists()).toBe(false);
  expect(w.text()).toContain("1 passed · 2 failed");
  expect(w.text()).toContain("1 failed on main too");
  expect(w.findAll('[data-test="main-fail-tag"]')).toHaveLength(1);
  expect(w.find('[data-check="qc_1"]').classes()).toContain("base");
  expect(w.find('[data-check="qc_3"]').classes()).not.toContain("base");
});
it("shows the output of the clicked tile below the grid", async () => {
  const w = mount(ChecksGrid, {
    props: {
      revision: 1,
      checks: [{ ...q("qc_1", "api vet", "PASS"), output: "all ok" }],
    },
  });
  await w.find('[data-check="qc_1"]').trigger("click");
  expect(w.find('[data-check="qc_1"]').attributes("aria-pressed")).toBe("true");
  expect(w.find('[data-test="check-detail"]').text()).toContain("all ok");
});
