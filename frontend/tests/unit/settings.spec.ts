import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createRouter, createMemoryHistory } from "vue-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import SettingsView from "../../src/features/settings/SettingsView.vue";
import AddProjectDialog from "../../src/features/factory/work-orders/AddProjectDialog.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import { settings, models, user, overview } from "../fixtures/workspace";
import type { Connection, McpServer } from "../../src/types/api";

type Handler = (body: unknown, url: string) => Response | Promise<Response>;
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status });
const fail = (status: number, code: string, message: string) =>
  json({ error: { code, message } }, status);

const gh: Connection = {
  id: "c1",
  provider: "github",
  name: "Acme GitHub",
  scope: "shared",
  owner_id: "someone",
  base_url: "https://api.github.com",
  status: "active",
  verified_at: "2026-10-24T00:00:00Z",
  has_credential: true,
  created_at: "",
};
const server = (over: Partial<McpServer> = {}): McpServer => ({
  id: "m1",
  name: "docs_search",
  transport: "http",
  command: "",
  args: [],
  url: "https://mcp.example.com/mcp",
  auth: "",
  roles: ["builder"],
  status: "active",
  last_error: "",
  created_at: "",
  updated_at: "",
  secret_keys: ["Authorization"],
  approx_tokens: 0,
  discovered_at: undefined,
  tools: [],
  ...over,
});
const discovered = (roles: string[]) =>
  server({
    roles,
    discovered_at: "2026-11-11T00:00:00Z",
    approx_tokens: 120,
    tools: [
      {
        name: "search",
        description: "find docs",
        read_only: true,
        enabled: false,
        size: 40,
        unusable: false,
      },
      {
        name: "write_page",
        description: "edit a page",
        read_only: false,
        enabled: false,
        size: 80,
        unusable: false,
      },
      {
        name: "bad name",
        description: "",
        read_only: true,
        enabled: false,
        size: 10,
        unusable: true,
        unusable_reason: "name has a space",
      },
    ],
  });

let routes: Record<string, Handler>;
let calls: { method: string; path: string; body: unknown }[];
let scrolled: string[];

beforeEach(() => {
  setActivePinia(createPinia());
  const f = useFactory();
  f.models = models;
  f.overview = overview;
  f.projects = [
    {
      id: "pos",
      name: "POS",
      repo_url: "https://github.com/acme/pos.git",
      language: "go",
      description: "",
      created_at: "",
    },
  ];
  useSession().user = user;
  calls = [];
  scrolled = [];
  Element.prototype.scrollIntoView = function (this: Element) {
    scrolled.push(this.id);
  };
  routes = {
    "GET /api/v1/settings": () => json(structuredClone(settings)),
    "GET /api/v1/settings/runtime": () =>
      json({
        harness: "acp",
        acp_command: "opencode acp",
        jev: { enabled: true, model: "jev-1.14.0" },
        credentials_configured: true,
        max_concurrent_runs: 3,
      }),
    "GET /api/v1/models": () => json({ models }),
    "GET /api/v1/overview": () => json(overview),
    "GET /api/v1/projects": () => json({ projects: f.projects }),
    "GET /api/v1/connections": () =>
      json({ connections: [gh], credentials_configured: true }),
    "GET /api/v1/mcp-servers": () =>
      json({ servers: [], credentials_configured: true }),
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      const path = input.split("?")[0];
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, path, body });
      const h = routes[`${method} ${path}`];
      return h
        ? h(body, input)
        : fail(404, "NOT_FOUND", `no route ${method} ${path}`);
    }),
  );
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});

async function render(url = "/settings") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/settings", component: SettingsView }],
  });
  await router.push(url);
  await router.isReady();
  const w = mount(SettingsView, {
    global: { plugins: [router] },
    attachTo: document.body,
  });
  await flushPromises();
  return { w, router };
}
const sent = (method: string, path: string) =>
  calls.filter((c) => c.method === method && c.path === path);

it("renders one page with four sections in order, no tabs and no global save", async () => {
  const { w } = await render();
  const ids = w
    .findAll("section.settings-section")
    .map((s) => s.attributes("id"));
  expect(ids).toEqual(["agents", "connections", "runtime", "budget"]);
  expect(w.find('[role="tablist"]').exists()).toBe(false);
  expect(w.text()).not.toContain("Save settings");
  expect(w.text()).not.toContain("Not reported");
  expect(w.text()).not.toContain("My preferences");
  expect(w.text()).not.toContain("Team & access");
  expect(w.text()).not.toContain("OpenCode discovery");
  expect(w.text()).toContain(
    "work order choice → workspace default → first available model",
  );
  w.unmount();
});

it("labels each default with what it resolves to", async () => {
  const { w } = await render();
  const opts = w.findAll("#set-chat option").map((o) => o.text());
  expect(opts[0]).toBe("Default (Architect model)");
  w.unmount();
});

it("saves each model change on its own with a PATCH of one key", async () => {
  routes["PATCH /api/v1/settings"] = (body) =>
    json({ ...settings, ...(body as object) });
  const { w } = await render();
  await w.find("#set-architect").setValue("test/inspector");
  await flushPromises();
  await w.find("#set-chat").setValue("test/builder");
  await flushPromises();
  const patches = sent("PATCH", "/api/v1/settings").map((c) => c.body);
  expect(patches).toEqual([
    { architect_model: "test/inspector" },
    { chat_model: "test/builder" },
  ]);
  expect(sent("PUT", "/api/v1/settings")).toHaveLength(0);
  w.unmount();
});

it("shows the server error and restores the previous model when a save fails", async () => {
  routes["PATCH /api/v1/settings"] = () =>
    fail(500, "INTERNAL", "could not save settings");
  const { w } = await render();
  await w.find("#set-architect").setValue("test/builder");
  await flushPromises();
  expect(w.text()).toContain("could not save settings");
  expect((w.find("#set-architect").element as HTMLSelectElement).value).toBe(
    "test/architect",
  );
  w.unmount();
});

it("keeps saved values when a model and budget response arrive out of order", async () => {
  let finishModel!: (response: Response) => void;
  routes["PATCH /api/v1/settings"] = (body) => {
    if ("architect_model" in (body as object))
      return new Promise((resolve) => {
        finishModel = resolve;
      });
    return json({ ...settings, ...(body as object) });
  };
  const { w } = await render();
  await w.find("#set-architect").setValue("test/inspector");
  await w.find("#set-budget").setValue("12.34");
  await flushPromises();
  finishModel(json({ ...settings, architect_model: "test/inspector" }));
  await flushPromises();
  expect((w.find("#set-architect").element as HTMLSelectElement).value).toBe(
    "test/inspector",
  );
  expect((w.find("#set-budget").element as HTMLInputElement).value).toBe(
    "12.34",
  );
  w.unmount();
});

it("saves the budget cap in micros when the field changes", async () => {
  routes["PATCH /api/v1/settings"] = (body) =>
    json({ ...settings, ...(body as object) });
  const { w } = await render();
  await w.find("#set-budget").setValue("12.34");
  await w.find("#set-budget").trigger("change");
  await flushPromises();
  expect(sent("PATCH", "/api/v1/settings")[0].body).toEqual({
    budget_limit_micros: 12340000,
  });
  w.unmount();
});

it("shows everything read-only with a note to a non-admin", async () => {
  useSession().user = { ...user, role: "VIEWER" };
  const { w } = await render();
  expect(w.find('[data-test="read-only-note"]').exists()).toBe(true);
  expect(w.find("#set-architect").attributes("disabled")).toBeDefined();
  expect(w.find("#set-budget").attributes("disabled")).toBeDefined();
  expect(w.find('[data-test="connection-form"]').exists()).toBe(false);
  expect(w.find("[data-connection] button").exists()).toBe(false);
  expect(sent("GET", "/api/v1/mcp-servers")).toHaveLength(0);
  w.unmount();
});

it("reports the runtime read-only with where each value is set", async () => {
  const { w } = await render();
  const text = w.find('[data-test="runtime"]').text();
  expect(text).toContain("ACP agent · opencode acp");
  expect(text).toContain("FACTORY_ACP_COMMAND");
  expect(text).toContain("On · jev-1.14.0");
  expect(text).toContain("TYPESAFE_API_KEY");
  expect(text).toContain("FACTORY_SECRET_KEY");
  expect(text).toContain("FACTORY_MAX_CONCURRENT_RUNS");
  expect(w.find('[data-test="runtime"] input').exists()).toBe(false);
  w.unmount();
});

it("lands on the matching section for old ?tab= links and hashes", async () => {
  let r = await render("/settings?tab=usage");
  expect(scrolled.at(-1)).toBe("budget");
  r.w.unmount();
  r = await render("/settings?tab=integrations");
  expect(scrolled.at(-1)).toBe("connections");
  r.w.unmount();
  r = await render("/settings#runtime");
  expect(scrolled.at(-1)).toBe("runtime");
  r.w.unmount();
});

it("adds an MCP server with a secret and shows a server validation error", async () => {
  routes["POST /api/v1/mcp-servers"] = () =>
    fail(
      400,
      "VALIDATION_FAILED",
      "name must be 1-40 characters of a-z and 0-9",
    );
  const { w } = await render();
  await w.find("#conn-provider").setValue("mcp");
  await w.find("#mcp-transport").setValue("http");
  await w.find("#conn-name").setValue("Bad Name");
  await w.find("#mcp-url").setValue(" https://mcp.example.com/mcp ");
  await w.find('input[aria-label="Secret name"]').setValue("Authorization");
  await w.find('input[aria-label="Secret value"]').setValue("Bearer t0k");
  await w.find('[data-test="connection-form"]').trigger("submit");
  await flushPromises();
  expect(sent("POST", "/api/v1/mcp-servers")[0].body).toEqual({
    name: "Bad Name",
    transport: "http",
    command: "",
    args: [],
    url: "https://mcp.example.com/mcp",
    secret: { Authorization: "Bearer t0k" },
  });
  expect(w.text()).toContain("name must be 1-40 characters");
  w.unmount();
});

it("adds a stdio MCP server, then discovers it and lists the tools", async () => {
  let current: McpServer[] = [];
  routes["GET /api/v1/mcp-servers"] = () =>
    json({ servers: current, credentials_configured: true });
  routes["POST /api/v1/mcp-servers"] = () => {
    current = [
      server({
        transport: "stdio",
        command: "/bin/srv",
        url: "",
        secret_keys: [],
      }),
    ];
    return json(current[0], 201);
  };
  routes["POST /api/v1/mcp-servers/m1/discover"] = () => {
    current = [discovered(["builder"])];
    return json(current[0]);
  };
  const { w } = await render();
  await w.find("#conn-provider").setValue("mcp");
  await w.find("#conn-name").setValue("docs_search");
  await w.find("#mcp-command").setValue("/bin/srv");
  await w.find("#mcp-args").setValue("--serve\n--quiet");
  await w.find('[data-test="connection-form"]').trigger("submit");
  await flushPromises();
  expect(sent("POST", "/api/v1/mcp-servers")[0].body).toMatchObject({
    transport: "stdio",
    command: "/bin/srv",
    args: ["--serve", "--quiet"],
  });
  const row = () => w.find('[data-mcp="docs_search"]');
  expect(row().text()).toContain("Not discovered");
  const discover = row()
    .findAll("button")
    .find((b) => b.text() === "Discover")!;
  await discover.trigger("click");
  await flushPromises();
  expect(row().text()).toContain("0 of 3 tools on");
  expect(row().find('[data-test="mcp-tokens"]').text()).toBe("~120 tokens");
  w.unmount();
});

it("toggles tools and roles, enforcing the writing-tool rule in the UI and showing the server error", async () => {
  let current = discovered(["builder"]);
  routes["GET /api/v1/mcp-servers"] = () =>
    json({ servers: [current], credentials_configured: true });
  routes["PUT /api/v1/mcp-servers/m1/tools"] = (body) => {
    const [t] = body as { name: string; enabled: boolean }[];
    current = {
      ...current,
      tools: current.tools.map((x) =>
        x.name === t.name ? { ...x, enabled: t.enabled } : x,
      ),
    };
    return json(current);
  };
  routes["PUT /api/v1/mcp-servers/m1/roles"] = (body) => {
    const roles = (body as { roles: string[] }).roles;
    if (roles.length > 1 && roles.includes("chat"))
      return fail(
        400,
        "VALIDATION_FAILED",
        "chat sessions cannot be offered this server",
      );
    current = { ...current, roles };
    return json(current);
  };
  const { w } = await render();
  await w
    .find('[data-mcp="docs_search"]')
    .findAll("button")
    .find((b) => b.text() === "Tools and roles")!
    .trigger("click");

  const writing = w.find('[data-test="mcp-tool-write_page"]');
  const unusable = w.find('[data-test="mcp-tool-bad name"]');
  expect(w.find('[data-tool="write_page"]').text()).toContain("Writing");
  expect(w.find('[data-tool="search"]').text()).toContain("Read-only");
  expect(unusable.attributes("disabled")).toBeDefined();
  expect(w.find('[data-tool="bad name"]').text()).toContain("Unusable");
  expect(writing.attributes("disabled")).toBeUndefined();

  await w.find('[data-test="mcp-tool-search"]').setValue(true);
  await flushPromises();
  expect(sent("PUT", "/api/v1/mcp-servers/m1/tools")[0].body).toEqual([
    { name: "search", enabled: true },
  ]);

  await w.find('[data-test="mcp-role-architect"]').setValue(true);
  await flushPromises();
  expect(sent("PUT", "/api/v1/mcp-servers/m1/roles")[0].body).toEqual({
    roles: ["architect", "builder"],
  });
  expect(
    w.find('[data-test="mcp-tool-write_page"]').attributes("disabled"),
  ).toBeDefined();
  expect(w.find('[data-tool="write_page"]').text()).toContain("Builder alone");

  await w.find('[data-test="mcp-role-chat"]').setValue(true);
  await flushPromises();
  expect(w.text()).toContain("chat sessions cannot be offered this server");
  expect(
    (w.find('[data-test="mcp-role-chat"]').element as HTMLInputElement).checked,
  ).toBe(false);
  w.unmount();
});

it("shows a failed discovery's last_error on the server", async () => {
  routes["GET /api/v1/mcp-servers"] = () =>
    json({
      servers: [server({ last_error: "connection refused" })],
      credentials_configured: true,
    });
  const { w } = await render();
  expect(w.find('[data-test="mcp-last-error"]').text()).toBe(
    "connection refused",
  );
  w.unmount();
});

it("binds a project from its row in Connections and toggles contract tests", async () => {
  routes["PUT /api/v1/projects/pos/binding"] = (body) =>
    json({ ...useFactory().projects[0], ...(body as object) });
  routes["PUT /api/v1/projects/pos/contract-tests"] = (body) =>
    json({ ...useFactory().projects[0], ...(body as object) });
  const { w } = await render();
  const row = w.find('[data-project-link="pos"]');
  expect(row.text()).not.toContain("work orders cannot start");
  await row.find("select").setValue("c1");
  await flushPromises();
  expect(
    (row.find("input[placeholder='owner/repo']").element as HTMLInputElement)
      .value,
  ).toBe("acme/pos");
  expect(sent("PUT", "/api/v1/projects/pos/binding")[0].body).toEqual({
    git_connection_id: "c1",
    remote_repo: "acme/pos",
  });
  await row.find('[data-test="contract-tests-proj-pos"]').setValue(true);
  await flushPromises();
  expect(sent("PUT", "/api/v1/projects/pos/contract-tests")[0].body).toEqual({
    contract_tests: "on",
  });
  w.unmount();
});

it("uses the same project row and wording in the Add project dialog", async () => {
  const f = useFactory();
  f.connections = [gh];
  const { w } = await render();
  const settingsHelp = w.find('[data-project-link="pos"] .help').text();
  w.unmount();
  const d = mount(AddProjectDialog, {
    props: { open: true },
    attachTo: document.body,
  });
  await flushPromises();
  const dialogText = document.body.textContent ?? "";
  expect(document.querySelector("#prj-conn")).toBeTruthy();
  expect(dialogText).toContain(settingsHelp.slice(0, 60));
  expect(dialogText).not.toContain("work orders cannot start");
  d.unmount();
});
