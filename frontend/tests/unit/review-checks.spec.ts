import { mount } from "@vue/test-utils";
import { expect, it } from "vitest";
import ChecksGrid from "../../src/features/factory/reviews/ChecksGrid.vue";
import { failsOnMain } from "../../src/features/factory/reviews/checks";
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

const base = (o: Partial<QualityCheck> = {}) => ({
  ...q("qc_base_1", "api test", "FAIL", 0),
  ...o,
});
const cur = (o: Partial<QualityCheck> = {}) => ({
  ...q("qc_2", "api test", "FAIL"),
  ...o,
});
const fail = (...names: string[]) =>
  names.map((n) => `--- FAIL: ${n} (0.00s)`).join("\n");
const tagged = (b: QualityCheck, c: QualityCheck) => failsOnMain([b, c], c);

it("tags a check whose baseline run also failed", () => {
  expect(tagged(base(), cur())).toBe(true);
});
it("does not tag a check whose baseline passed", () => {
  expect(tagged(base({ outcome: "PASS" }), cur())).toBe(false);
});
it("does not tag a check without a baseline row", () => {
  const c = cur();
  expect(failsOnMain([c], c)).toBe(false);
});
it("does not tag a new failing Go test", () => {
  expect(
    tagged(
      base({ output: fail("TestA") }),
      cur({ output: fail("TestA", "TestB") }),
    ),
  ).toBe(false);
  expect(
    tagged(
      base({ output: fail("TestA", "TestB") }),
      cur({ output: fail("TestA") }),
    ),
  ).toBe(true);
});
it("does not tag when the command changed", () => {
  expect(tagged(base({ command: "run old" }), cur())).toBe(false);
});
it("tags an UNVERIFIED check whose baseline failed, not a passing or optional one", () => {
  expect(tagged(base(), cur({ outcome: "UNVERIFIED" }))).toBe(true);
  expect(tagged(base(), cur({ outcome: "PASS" }))).toBe(false);
  expect(tagged(base(), cur({ optional: true }))).toBe(false);
});
it("compares against the first baseline row of that name", () => {
  const first = base({ outcome: "PASS" });
  const later = base({ id: "qc_base_2", outcome: "FAIL" });
  const c = cur();
  expect(failsOnMain([first, later, c], c)).toBe(false);
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
it("drops a picked historical check when the revision changes", async () => {
  const w = mount(ChecksGrid, {
    props: {
      revision: 1,
      checks: [
        { ...q("qc_0", "api vet", "PASS", 0), output: "old out" },
        q("qc_1", "api vet", "PASS"),
      ],
    },
  });
  await w.find('[data-check="qc_0"]').trigger("click");
  expect(w.find('[data-test="check-detail"]').text()).toContain("old out");
  await w.setProps({ revision: 2 });
  expect(w.find('[data-test="check-detail"]').exists()).toBe(false);
});
