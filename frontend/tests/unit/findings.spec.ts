import { mount } from "@vue/test-utils";
import { afterEach, expect, it, vi } from "vitest";
import ChangesPanel from "../../src/features/factory/reviews/ChangesPanel.vue";
import type { Finding } from "../../src/types/api";

const finding = (id: string, status: Finding["status"]): Finding => ({
  id,
  run_id: "run",
  severity: "blocker",
  title: `Finding ${id}`,
  detail: "detail",
  location: "a.go:1",
  status,
  specialty: "testing",
  created_at: "",
});

const render = (state = "IMPLEMENTING", findings = [finding("f1", "open")]) =>
  mount(ChangesPanel, {
    props: { runs: [], findings, state, canWrite: true, busy: false },
  });

afterEach(() => vi.unstubAllGlobals());

it("requires a reason to dispute and passes it with escalations", async () => {
  const prompt = vi
    .fn()
    .mockReturnValueOnce("   ")
    .mockReturnValueOnce("needs PM");
  vi.stubGlobal("prompt", prompt);
  const w = render();
  expect(w.text()).toContain("testing");
  const button = (label: string) =>
    w.findAll("button").find((b) => b.text() === label)!;
  await button("Dispute").trigger("click");
  expect(w.emitted("finding")).toBeUndefined();
  await button("Escalate").trigger("click");
  await button("Confirm").trigger("click");
  expect(w.emitted("finding")).toEqual([
    ["f1", "escalate", "needs PM"],
    ["f1", "confirm", ""],
  ]);
});

it("offers no dispositions for resolved findings or finished orders", () => {
  expect(
    render("IMPLEMENTING", [finding("f2", "resolved")]).text(),
  ).not.toContain("Dispute");
  expect(render("READY").text()).not.toContain("Dispute");
});
