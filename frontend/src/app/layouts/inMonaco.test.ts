import { describe, expect, it } from "vitest";
import { inMonaco } from "./inMonaco";

function seenFrom(id: string): boolean {
  let seen = false;
  const el = document.getElementById(id)!;
  el.addEventListener("keydown", (x) => (seen = inMonaco(x)));
  el.dispatchEvent(
    new KeyboardEvent("keydown", { key: "k", metaKey: true, bubbles: true }),
  );
  return seen;
}

describe("inMonaco", () => {
  it("is true only for events from inside a monaco editor", () => {
    document.body.innerHTML =
      '<div class="monaco-editor"><textarea id="a"></textarea></div><input id="b">';
    expect(seenFrom("a")).toBe(true);
    expect(seenFrom("b")).toBe(false);
  });
});
