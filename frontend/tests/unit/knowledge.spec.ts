import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { expect, it } from "vitest";
import CatalogerView from "../../src/features/knowledge/CatalogerView.vue";
it("presents the approved optional visual preview without implying ingestion", async () => {
  setActivePinia(createPinia());
  const w = mount(CatalogerView);
  expect(w.find(".catalog-hero").exists()).toBe(true);
  await w.find('[data-test="knowledge-preview"]').setValue(true);
  expect(w.text()).toContain("UI-only preview");
  expect(w.text()).toContain("Nothing is ingested");
  w.unmount();
});
