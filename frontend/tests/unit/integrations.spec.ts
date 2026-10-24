import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, expect, it, vi } from "vitest";
import IntegrationsPanel from "../../src/features/settings/IntegrationsPanel.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import type { Connection } from "../../src/types/api";
import { user } from "../fixtures/workspace";

const base = {
  base_url: "https://api.github.com",
  has_credential: true,
  created_at: "",
} as const;
const connections: Connection[] = [
  {
    ...base,
    id: "c1",
    provider: "github",
    name: "Acme GitHub",
    scope: "shared",
    owner_id: "someone-else",
    status: "active",
    account: "factory-bot",
    verified_at: "2026-10-24T00:00:00Z",
  },
  {
    ...base,
    id: "c2",
    provider: "jira",
    name: "My Jira",
    scope: "personal",
    owner_id: user.id,
    status: "active",
    base_url: "https://acme.atlassian.net",
    last_error: "jira API returned 401: Unauthorized",
  },
];

beforeEach(() => {
  setActivePinia(createPinia());
  useSession().user = user;
  const f = useFactory();
  vi.spyOn(f, "refreshConnections").mockImplementation(async () => {
    f.connections = structuredClone(connections);
  });
});

async function render() {
  const w = mount(IntegrationsPanel);
  await flushPromises();
  return w;
}

it("lists connections with scope, verification and the last provider error", async () => {
  const w = await render();
  const rows = w.findAll("[data-connection]");
  expect(rows).toHaveLength(2);
  expect(rows[0].text()).toContain("Shared");
  expect(rows[0].text()).toContain("Verified");
  expect(rows[0].text()).toContain("factory-bot");
  expect(rows[1].text()).toContain("Personal");
  expect(rows[1].text()).toContain("Unverified");
  expect(rows[1].text()).toContain("401");
  expect(w.find("#conn-token").attributes("type")).toBe("password");
  w.unmount();
});

it("only offers management where the backend allows it", async () => {
  useSession().user = { ...user, role: "ENGINEER" };
  const w = await render();
  const [shared, personal] = w.findAll("[data-connection]");
  expect(shared.findAll("button")).toHaveLength(0);
  expect(personal.text()).toContain("Test");
  expect(personal.text()).toContain("Revoke");
  const sharedOption = w.find('#conn-scope option[value="shared"]');
  expect(sharedOption.attributes("disabled")).toBeDefined();
  w.unmount();
});

it("submits a trimmed connection and clears the token field", async () => {
  const f = useFactory();
  const create = vi.spyOn(f, "createConnection").mockResolvedValue();
  const w = await render();
  await w.find("#conn-provider").setValue("jira");
  await w.find("#conn-name").setValue("Team Jira");
  await w.find("#conn-base").setValue(" https://acme.atlassian.net ");
  await w.find("#conn-user").setValue(" pm@acme.io ");
  await w.find("#conn-token").setValue(" secret ");
  await w.find('[data-test="connection-form"]').trigger("submit");
  await flushPromises();
  expect(create).toHaveBeenCalledWith({
    provider: "jira",
    name: "Team Jira",
    scope: "personal",
    base_url: "https://acme.atlassian.net",
    username: "pm@acme.io",
    token: "secret",
  });
  expect((w.find("#conn-token").element as HTMLInputElement).value).toBe("");
  w.unmount();
});

it("explains when the server cannot store credentials", async () => {
  const f = useFactory();
  vi.mocked(f.refreshConnections).mockImplementation(async () => {
    f.connections = [];
    f.credentialsConfigured = false;
  });
  const w = await render();
  expect(w.text()).toContain("FACTORY_SECRET_KEY");
  expect(w.find('[data-test="connection-form"]').exists()).toBe(false);
  w.unmount();
});
