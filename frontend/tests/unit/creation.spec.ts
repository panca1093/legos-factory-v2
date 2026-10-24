import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, expect, it, vi } from "vitest";
import NewWorkOrderDrawer from "../../src/features/factory/work-orders/NewWorkOrderDrawer.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import {
  projects,
  user,
  models,
  settings,
  orders,
} from "../fixtures/workspace";
beforeEach(() => {
  setActivePinia(createPinia());
  const f = useFactory();
  f.projects = projects;
  f.models = models;
  useSession().user = user;
  vi.spyOn(f, "refreshModels").mockResolvedValue();
  vi.spyOn(f, "loadSettings").mockResolvedValue(settings);
});
const render = (projectID = "loan") =>
  mount(NewWorkOrderDrawer, {
    props: { open: true, projectID },
    global: {
      stubs: {
        AppDialog: { template: '<div><slot/><slot name="footer"/></div>' },
      },
    },
  });
async function intent(w: ReturnType<typeof render>) {
  await w.find("#wo-title").setValue("Retry safely");
  await w.find("#wo-objective").setValue("One ledger entry");
  await w
    .find("#wo-acceptance")
    .setValue("Repeated keys return the original result");
}
it("rejects blank acceptance and preserves values when stepping back", async () => {
  const w = render();
  await flushPromises();
  await w.find("#wo-title").setValue("Retry safely");
  await w.find("#wo-objective").setValue("One ledger entry");
  await w.find('[data-test="drawer-next"]').trigger("click");
  expect(w.text()).toContain("Observable acceptance criteria are required");
  await intent(w);
  await w.find('[data-test="drawer-next"]').trigger("click");
  expect(w.text()).toContain("Choose the hands. Choose the eyes.");
  await w.find('[data-test="drawer-back"]').trigger("click");
  expect((w.find("#wo-title").element as HTMLInputElement).value).toBe(
    "Retry safely",
  );
  w.unmount();
});
it("retains the created order identity when planning fails and never submits twice", async () => {
  const f = useFactory();
  const create = vi.spyOn(f, "createWorkOrder").mockResolvedValue(orders[0]);
  vi.spyOn(f, "planGenerate").mockRejectedValue(new Error("Planning offline"));
  const w = render();
  await flushPromises();
  await intent(w);
  await w.find('[data-test="drawer-next"]').trigger("click");
  await w.find('[data-test="drawer-next"]').trigger("click");
  await w.find('[data-test="drawer-create"]').trigger("click");
  await flushPromises();
  expect(create).toHaveBeenCalledOnce();
  expect(w.emitted("created")?.[0]).toEqual([
    { id: "LF-128", planningError: "Planning offline" },
  ]);
  w.unmount();
});
it("does not offer creation to a viewer", async () => {
  useSession().user = { ...user, role: "VIEWER" };
  const w = render();
  await flushPromises();
  expect(
    w.find('[data-test="drawer-next"]').attributes("disabled"),
  ).toBeDefined();
  w.unmount();
});
it("selects a real project when a cold drawer opens before project loading finishes", async () => {
  const f = useFactory();
  f.projects = [];
  const w = render("");
  await flushPromises();
  f.projects = projects;
  await flushPromises();
  expect((w.find("#wo-project").element as HTMLSelectElement).value).toBe(
    "atlas",
  );
  w.unmount();
});
it("imports a Jira issue into the intent and keeps its source reference", async () => {
  const f = useFactory();
  vi.spyOn(f, "refreshConnections").mockImplementation(async () => {
    f.connections = [
      {
        id: "jira1",
        provider: "jira",
        name: "Team Jira",
        scope: "personal",
        owner_id: user.id,
        base_url: "https://acme.atlassian.net",
        status: "active",
        has_credential: true,
        created_at: "",
      },
    ];
  });
  const lookup = vi.spyOn(f, "lookupJiraIssue").mockResolvedValue({
    source_ref: "jira:PAY-7",
    title: "Idempotent retries",
    objective: "Retries must not double charge.",
    acceptance: "Repeated keys return the original result",
    url: "https://acme.atlassian.net/browse/PAY-7",
  });
  const create = vi.spyOn(f, "createWorkOrder").mockResolvedValue(orders[0]);
  vi.spyOn(f, "planGenerate").mockResolvedValue();
  const w = render();
  await flushPromises();
  await w.find("#wo-jira-key").setValue(" pay-7 ");
  await w.find('[data-test="jira-import"] button').trigger("click");
  await flushPromises();
  expect(lookup).toHaveBeenCalledWith("jira1", "pay-7");
  expect((w.find("#wo-title").element as HTMLInputElement).value).toBe(
    "Idempotent retries",
  );
  expect(w.find('[data-test="source-ref"]').text()).toContain("PAY-7");
  await w.find('[data-test="drawer-next"]').trigger("click");
  await w.find('[data-test="drawer-next"]').trigger("click");
  await w.find('[data-test="drawer-create"]').trigger("click");
  await flushPromises();
  expect(create).toHaveBeenCalledWith(
    expect.objectContaining({
      source_ref: "jira:PAY-7",
      acceptance: "Repeated keys return the original result",
    }),
  );
  w.unmount();
});
