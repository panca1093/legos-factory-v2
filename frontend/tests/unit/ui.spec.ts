import { mount } from "@vue/test-utils";
import { afterEach, expect, it, vi } from "vitest";
import Popover from "../../src/components/ui/Popover.vue";
import Toast from "../../src/components/ui/Toast.vue";
import { useToast } from "../../src/composables/useToast";
afterEach(() => vi.useRealTimers());
it("shows a toast, runs its undo action once and expires on its own", async () => {
  vi.useFakeTimers();
  const undo = vi.fn();
  const w = mount(Toast);
  useToast().toast("Archived", undo);
  useToast().toast("Saved");
  await w.vm.$nextTick();
  expect(w.findAll(".lg-toast")).toHaveLength(2);
  await w.find(".lg-toast button").trigger("click");
  expect(undo).toHaveBeenCalledTimes(1);
  expect(w.findAll(".lg-toast")).toHaveLength(1);
  vi.advanceTimersByTime(4000);
  await w.vm.$nextTick();
  expect(w.findAll(".lg-toast")).toHaveLength(0);
});
it("pauses toast expiry while hovered", async () => {
  vi.useFakeTimers();
  const w = mount(Toast);
  useToast().toast("Hold on");
  await w.vm.$nextTick();
  vi.advanceTimersByTime(3000);
  await w.find(".lg-toast").trigger("mouseenter");
  vi.advanceTimersByTime(10000);
  await w.vm.$nextTick();
  expect(w.findAll(".lg-toast")).toHaveLength(1);
  await w.find(".lg-toast").trigger("mouseleave");
  vi.advanceTimersByTime(600);
  await w.vm.$nextTick();
  expect(w.findAll(".lg-toast")).toHaveLength(0);
});
it("closes the popover on Escape and returns focus to the trigger", async () => {
  const w = mount(Popover, {
    props: {
      open: true,
      "onUpdate:open": (v: boolean) => w.setProps({ open: v }),
    },
    slots: {
      trigger: '<button id="t">menu</button>',
      default: "<a href='#'>x</a>",
    },
    attachTo: document.body,
  });
  document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
  await w.vm.$nextTick();
  expect(w.find(".lg-menu").exists()).toBe(false);
  expect(document.activeElement?.id).toBe("t");
  w.unmount();
});
