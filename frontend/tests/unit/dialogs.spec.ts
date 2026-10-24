import { mount } from "@vue/test-utils";
import { afterEach, expect, it } from "vitest";
import { nextTick } from "vue";
import AppDialog from "../../src/components/ui/AppDialog.vue";
afterEach(() => {
  document.body.innerHTML = "";
});
it("traps focus, closes on Escape and restores the trigger", async () => {
  const trigger = document.createElement("button");
  document.body.append(trigger);
  trigger.focus();
  const wrapper = mount(AppDialog, {
    props: { open: true, title: "Inspect evidence" },
    slots: { default: '<input aria-label="Decision"><button>Last</button>' },
    attachTo: document.body,
  });
  await nextTick();
  const dialog = document.querySelector('[role="dialog"]')!;
  expect(dialog.getAttribute("aria-modal")).toBe("true");
  const buttons = dialog.querySelectorAll<HTMLButtonElement>("button");
  buttons[buttons.length - 1].focus();
  document.dispatchEvent(
    new KeyboardEvent("keydown", { key: "Tab", bubbles: true }),
  );
  expect(document.activeElement).toBe(buttons[0]);
  document.dispatchEvent(
    new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
  );
  expect(wrapper.emitted("close")).toHaveLength(1);
  await wrapper.setProps({ open: false });
  await nextTick();
  expect(document.activeElement).toBe(trigger);
  wrapper.unmount();
});
