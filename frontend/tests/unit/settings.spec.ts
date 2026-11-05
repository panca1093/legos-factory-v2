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
it("saves model IDs and budget in micros", async () => {
  const f = useFactory();
  const save = vi.spyOn(f, "saveSettings").mockResolvedValue();
  const { w, router } = await render();
  await w.find("#set-architect").setValue("test/inspector");
  await w.find('[data-test="save-settings"]').trigger("click");
  await flushPromises();
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({ architect_model: "test/inspector" }),
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
it("keeps failed edits after a save error", async () => {
  const f = useFactory();
  const save = vi
    .spyOn(f, "saveSettings")
    .mockRejectedValue(new Error("Save unavailable"));
  const { w } = await render();
  await w.find("#set-architect").setValue("test/builder");
  await w.find('[data-test="save-settings"]').trigger("click");
  await flushPromises();
  expect(w.text()).toContain("Save unavailable");
  expect((w.find("#set-architect").element as HTMLSelectElement).value).toBe(
    "test/builder",
  );
  expect(save).toHaveBeenCalledOnce();
  w.unmount();
});

it("does not edit or save default configuration after an initialization failure and exposes retry", async () => {
  const f = useFactory();
  vi.mocked(f.loadSettings).mockRejectedValue(new Error("Settings offline"));
  const save = vi.spyOn(f, "saveSettings").mockResolvedValue();
  const { w } = await render();
  expect(
    w.find('[data-test="save-settings"]').attributes("disabled"),
  ).toBeDefined();
  expect(w.find("#set-architect").attributes("disabled")).toBeDefined();
  expect(w.text()).toContain("Try again");
  await w.find('[data-test="save-settings"]').trigger("click");
  expect(save).not.toHaveBeenCalled();
  w.unmount();
});
it("retains authoritative settings when ancillary model discovery fails", async () => {
  const f = useFactory();
  vi.mocked(f.refreshModels).mockRejectedValue(
    new Error("Model discovery offline"),
  );
  const save = vi.spyOn(f, "saveSettings").mockResolvedValue();
  const { w } = await render();
  expect((w.find("#set-architect").element as HTMLSelectElement).value).toBe(
    "test/architect",
  );
  await w.find('[data-test="save-settings"]').trigger("click");
  await flushPromises();
  expect(save).toHaveBeenCalledWith(
    expect.objectContaining({
      budget_limit_micros: 40000000,
      architect_model: "test/architect",
    }),
  );
  w.unmount();
});
it("round-trips the Cataloger summaries toggle", async () => {
  const f = useFactory();
  const save = vi.spyOn(f, "saveSettings").mockResolvedValue();
  const { w } = await render();
  await w.find('[data-test="cataloger-summaries"]').setValue(true);
  await w.find('[data-test="save-settings"]').trigger("click");
  await flushPromises();
  expect(save).toHaveBeenLastCalledWith(
    expect.objectContaining({ cataloger_summaries: true }),
  );
  w.unmount();
});
it("shows whether Jev is on under Integrations", async () => {
  const f = useFactory();
  vi.spyOn(f, "loadSettings").mockResolvedValue({
    ...structuredClone(settings),
    jev_enabled: false,
  });
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/settings", component: SettingsView }],
  });
  await router.push("/settings?tab=integrations");
  await router.isReady();
  const stubs = { IntegrationsPanel: true };
  const w = mount(SettingsView, { global: { plugins: [router], stubs } });
  await flushPromises();
  expect(w.find('[data-test="jev-state"]').text()).toBe("Off");
  expect(w.find('[data-test="jev-status"]').text()).toContain(
    "TYPESAFE_API_KEY",
  );
  w.unmount();

  vi.spyOn(f, "loadSettings").mockResolvedValue({
    ...structuredClone(settings),
    jev_enabled: true,
  });
  const on = mount(SettingsView, { global: { plugins: [router], stubs } });
  await flushPromises();
  expect(on.find('[data-test="jev-state"]').text()).toBe("On");
  on.unmount();
});
it("no longer offers autonomy levels", async () => {
  const { w } = await render();
  expect(w.text()).not.toContain("Progressive autonomy");
  w.unmount();
});
