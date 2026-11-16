import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import ReasonText from "../../src/features/factory/ReasonText.vue";

describe("ReasonText", () => {
  it("lists a multi-line reason by line, not by semicolon", async () => {
    const text =
      "The plan needs a human decision: 1. open question\nOpen findings:\n- R1-F1 [high, requirement] a; b Fix: c";
    const w = mount(ReasonText, { props: { text } });
    await w.find("button").trigger("click");
    const items = w.findAll("li").map((li) => li.text());
    expect(items).toHaveLength(3);
    expect(items[2]).toBe("- R1-F1 [high, requirement] a; b Fix: c");
  });

  it("splits a single-line reason on semicolons as before", async () => {
    const w = mount(ReasonText, { props: { text: "one; two" } });
    await w.find("button").trigger("click");
    expect(w.findAll("li").map((li) => li.text())).toEqual(["one", "two"]);
  });
});
