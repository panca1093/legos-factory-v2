import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createRouter, createMemoryHistory } from "vue-router";
import { beforeEach, expect, it, vi } from "vitest";
import SettingsView from "../../src/features/settings/SettingsView.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import { settings, models, user, overview } from "../fixtures/workspace";
beforeEach(() => {
  setActivePinia(createPinia());
  const f = useFactory();
  f.models = models;
  f.overview = overview;
  useSession().user = user;
  vi.spyOn(f, "loadSettings").mockResolvedValue(structuredClone(settings));
  vi.spyOn(f, "refreshModels").mockResolvedValue();
  vi.spyOn(f, "refreshOverview").mockResolvedValue();
});
async function render(tab = "governance") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/settings", component: SettingsView }],
  });
  await router.push(`/settings?tab=${tab}`);
  await router.isReady();
  const w = mount(SettingsView, { global: { plugins: [router] } });
  await flushPromises();
  return { w, router };
}
it("saves model IDs, allowed-model choices and budget in micros", async () => {
  const f = useFactory();
  const save = vi.spyOn(f, "saveSettings").mockResolvedValue();
  const { w, router } = await render();
  await w.find("#set-architect").setValue("test/inspector");
  await w.find('[data-model="test/builder"]').setValue(false);
  await w.find('[data-test="save-settings"]').trigger("click");
  await flushPromises();
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({
      architect_model: "test/inspector",
      allowed_models: ["test/architect", "test/inspector"],
    }),
  );
  await router.push("/settings?tab=usage");
  await flushPromises();
  await w.find("#set-budget").setValue("12.34");
  await w.find('[data-test="save-settings"]').trigger("click");
  await flushPromises();
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({ budget_limit_micros: 12340000 }),
  );
  w.unmount();
});
it("shows read-only governance to a nonadmin", async () => {
  useSession().user = { ...user, role: "ENGINEER" };
  const { w } = await render();
  expect(w.find("#set-architect").attributes("disabled")).toBeDefined();
  expect(
    w.find('[data-test="save-settings"]').attributes("disabled"),
  ).toBeDefined();
  w.unmount();
});
it("keeps failed edits and marks unsupported integrations unavailable without writes", async () => {
  const f = useFactory();
  const save = vi
    .spyOn(f, "saveSettings")
    .mockRejectedValue(new Error("Save unavailable"));
  const { w, router } = await render();
  await w.find("#set-architect").setValue("test/builder");
  await w.find('[data-test="save-settings"]').trigger("click");
  await flushPromises();
  expect(w.text()).toContain("Save unavailable");
  expect((w.find("#set-architect").element as HTMLSelectElement).value).toBe(
    "test/builder",
  );
  await router.push("/settings?tab=integrations");
  await flushPromises();
  expect(w.text()).toContain("Connection management is unavailable");
  expect(w.findAll("button:disabled").length).toBeGreaterThan(0);
  expect(save).toHaveBeenCalledOnce();
  w.unmount();
});
