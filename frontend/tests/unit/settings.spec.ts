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
  scope: "personal",
  owner_id: "usr-test",
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
  auth: "token",
  roles: ["builder"],
  status: "active",
  last_error: "",
  created_at: "",
  updated_at: "",
  secret_keys: ["Authorization"],
  signed_in: false,
  oauth_client: "",
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

const baseRoleAgents = {
  foreman: "claude",
  architect: "claude",
  builder: "claude",
  inspector: "claude",
  chat: "claude",
};
const appSettings: {
  role_agents: Record<string, string>;
  role_permissions: { chat: string };
  max_concurrent_runs: number;
  jev_enabled: boolean;
} = {
  role_agents: { ...baseRoleAgents },
  role_permissions: { chat: "ask" },
  max_concurrent_runs: 4,
  jev_enabled: true,
};
const codexModel = {
  provider_id: "openai",
  model_id: "gpt-6.1-sol",
  name: "GPT 6.1 Sol",
  harness: "codex",
};
const acct = (over: object = {}) => ({
  id: "sys",
  label: "System default",
  system: true,
  active: true,
  status: "signed_in",
  who: "you@example.com",
  plan: "max_5x",
  expires_at: "2027-01-05T00:00:00Z",
  ...over,
});
type Agent = { accounts: object[]; installed?: boolean };
let agentsBody: { agents: Agent[] };
let providersBody: { providers: object[] };
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
  appSettings.role_agents = { ...baseRoleAgents };
  agentsBody = {
    agents: [
      {
        id: "claude",
        name: "Claude",
        command: "claude-agent-acp",
        installed: true,
        enabled: true,
        roles_allowed: ["foreman", "architect", "builder", "inspector", "chat"],
        accounts: [acct()],
      },
      {
        id: "codex",
        name: "Codex",
        command: "codex-acp",
        installed: true,
        enabled: true,
        roles_allowed: ["builder"],
        accounts: [acct({ id: "cx", who: "me@x.com", plan: "plus" })],
      },
      {
        id: "opencode",
        name: "OpenCode",
        command: "opencode acp",
        installed: false,
        enabled: false,
        install_hint: "npm install -g opencode-ai",
        roles_allowed: ["foreman", "architect", "builder", "inspector", "chat"],
        accounts: [],
      },
    ] as unknown as Agent[],
  };
  providersBody = {
    providers: [
      {
        id: "github",
        group: "code",
        accounts: [{ ...gh, used_by: [{ project_id: "pos", name: "POS" }] }],
      },
      { id: "gitlab", group: "code", accounts: [] },
      { id: "bitbucket", group: "code", accounts: [] },
      { id: "jira", group: "work", accounts: [] },
    ],
  };
  calls = [];
  scrolled = [];
  Element.prototype.scrollIntoView = function (this: Element) {
    scrolled.push(this.id);
  };
  routes = {
    "GET /api/v1/settings": () =>
      json(structuredClone({ ...settings, ...appSettings })),
    "GET /api/v1/settings/runtime": () =>
      json({ credentials_configured: true, jev_key_from_env: false }),
    "GET /api/v1/agents": () => json(structuredClone(agentsBody)),
    "GET /api/v1/models": (_b, url) =>
      json({
        models: url.includes("agent=codex")
          ? [codexModel, { ...codexModel, model_id: "gpt-6", name: "GPT 6" }]
          : models,
      }),
    "GET /api/v1/overview": () => json(overview),
    "GET /api/v1/projects": () => json({ projects: f.projects }),
    "GET /api/v1/connections": () => json(structuredClone(providersBody)),
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

async function openMcp(w: ReturnType<typeof mount>) {
  await w
    .findAll("button")
    .find((b) => b.text() === "Add MCP server")!
    .trigger("click");
}
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

type W = ReturnType<typeof mount>;
const click = async (w: W, label: string) => {
  await w.find(`[aria-label="${label}"]`).trigger("click");
  await flushPromises();
};
const buttonByText = (w: { findAll: W["findAll"] }, text: string) =>
  w.findAll("button").find((b) => b.text() === text)!;
const patchEcho: Handler = (body) =>
  json({ ...settings, ...appSettings, ...(body as object) });

it("renders Agents, Connections and Advanced with a section nav and none of the removed UI", async () => {
  const { w } = await render();
  expect(
    w.findAll("section.settings-section").map((s) => s.attributes("id")),
  ).toEqual(["agents", "connections", "advanced"]);
  expect(w.findAll("nav a").map((a) => a.text())).toEqual([
    "Agents",
    "Connections",
    "Advanced",
  ]);
  const text = w.text();
  for (const gone of [
    "Budget",
    "budget",
    "Runtime",
    "Contract tests",
    "FACTORY_MAX_CONCURRENT_RUNS",
  ])
    expect(text).not.toContain(gone);
  expect(w.find("#set-budget").exists()).toBe(false);
  expect(w.find("[data-project-link]").exists()).toBe(false);
  expect(w.find('[data-test="secret-banner"]').exists()).toBe(false);
  w.unmount();
});

it("highlights the nav item of the section scrolled into view", async () => {
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    queueMicrotask(() => cb(0));
    return 1;
  });
  const { w } = await render();
  vi.spyOn(document.documentElement, "scrollHeight", "get").mockReturnValue(
    5000,
  );
  const tops: Record<string, number> = {
    agents: -900,
    connections: 100,
    advanced: 700,
  };
  for (const id of Object.keys(tops))
    vi.spyOn(
      document.getElementById(id)!,
      "getBoundingClientRect",
    ).mockImplementation(() => ({ top: tops[id] }) as DOMRect);
  const current = () => w.find('nav a[aria-current="true"]').text();

  window.dispatchEvent(new Event("scroll"));
  await flushPromises();
  expect(current()).toBe("Connections");

  tops.connections = 900;
  window.dispatchEvent(new Event("scroll"));
  await flushPromises();
  expect(current()).toBe("Agents");

  vi.spyOn(window, "scrollY", "get").mockReturnValue(5000);
  window.dispatchEvent(new Event("scroll"));
  await flushPromises();
  expect(current()).toBe("Advanced");
});

it("gives every checkbox and select an accessible name", async () => {
  const { w } = await render();
  const controls = w.findAll("input[type=checkbox], select");
  expect(controls.length).toBeGreaterThan(5);
  for (const el of controls) {
    const id = el.attributes("id");
    const named =
      el.attributes("aria-label") ||
      (id && w.find(`label[for="${id}"]`).exists()) ||
      el.element.closest("label")?.textContent?.trim();
    expect(named, el.html()).toBeTruthy();
  }
  w.unmount();
});

it("shows the secret-key banner only when credentials are not configured", async () => {
  routes["GET /api/v1/settings/runtime"] = () =>
    json({ credentials_configured: false, jev_key_from_env: false });
  const { w } = await render();
  expect(w.find('[data-test="secret-banner"]').text()).toContain(
    "FACTORY_SECRET_KEY",
  );
  w.unmount();
});

it("lands on the matching section for old ?tab= links and hashes", async () => {
  for (const [url, id] of [
    ["/settings?tab=governance", "agents"],
    ["/settings?tab=integrations", "connections"],
    ["/settings?tab=usage", "agents"],
    ["/settings#advanced", "advanced"],
  ]) {
    const r = await render(url);
    expect(scrolled.at(-1)).toBe(id);
    r.w.unmount();
  }
});

it("lists agents with plan and login expiry while usage is off, with no bars", async () => {
  const { w } = await render();
  const claude = w.find('[data-agent="claude"]');
  expect(claude.text()).toContain("you@example.com");
  const plan = claude.find('[data-test="plan-line"]').text();
  expect(plan).toContain("max_5x");
  expect(plan).toContain("login expires");
  expect(claude.find("[data-meter]").exists()).toBe(false);
  expect(w.find(".install").text()).toContain("npm install -g opencode-ai");
  w.unmount();
});

it("shows Claude usage bars by default with no toggle; usage_error shows Usage unavailable", async () => {
  agentsBody.agents[0].accounts = [
    acct({
      usage: {
        session: {
          used_pct: 58,
          resets_at: new Date(Date.now() + 3 * 3600e3).toISOString(),
        },
        week: { used_pct: 91 },
      },
    }),
  ];
  const { w } = await render();
  expect(w.find('input[aria-label="Show Claude usage"]').exists()).toBe(false);
  const session = w.find('[data-meter="session"]');
  expect(session.text()).toContain("58%");
  expect(session.find("[role=progressbar]").attributes("aria-valuenow")).toBe(
    "58",
  );
  expect(w.find('[data-meter="week"] i').classes()).toContain("warn");
  expect(w.find('[data-test="usage-error"]').exists()).toBe(false);

  agentsBody.agents[0].accounts = [acct({ usage_error: "unavailable" })];
  await buttonByText(w, "Refresh").trigger("click");
  await flushPromises();
  expect(w.find('[data-test="usage-error"]').text()).toBe("Usage unavailable");
  expect(w.find("[data-meter]").exists()).toBe(false);
  w.unmount();
});

it("add-account flow: create, show the login command, copy, then refresh on I've signed in", async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  routes["POST /api/v1/agents/claude/accounts"] = () =>
    json({
      account: acct({
        id: "a2",
        label: "Work",
        system: false,
        active: false,
        status: "signed_out",
      }),
      login_command: "CLAUDE_CONFIG_DIR=/d claude auth login",
      env_hint: "Run it in a terminal.",
    });
  routes["POST /api/v1/agents/claude/accounts/a2/refresh"] = () =>
    json(acct({ id: "a2" }));
  const { w } = await render();
  await click(w, "More for Claude");
  await buttonByText(w, "Add account").trigger("click");
  await w.find("#acct-label-claude").setValue("Work");
  await w.find('[data-test="add-account"]').trigger("submit");
  await flushPromises();
  expect(sent("POST", "/api/v1/agents/claude/accounts")[0].body).toEqual({
    label: "Work",
  });
  expect(w.find('[data-test="login-steps"]').text()).toContain(
    "CLAUDE_CONFIG_DIR=/d claude auth login",
  );
  await click(w, "Copy login command");
  expect(writeText).toHaveBeenCalledWith(
    "CLAUDE_CONFIG_DIR=/d claude auth login",
  );
  await buttonByText(w, "I've signed in").trigger("click");
  await flushPromises();
  expect(
    sent("POST", "/api/v1/agents/claude/accounts/a2/refresh"),
  ).toHaveLength(1);
  expect(w.find('[data-test="login-steps"]').exists()).toBe(false);
  w.unmount();
});

it("a signed-out account shows Not signed in and a Sign in action", async () => {
  agentsBody.agents[1].accounts = [acct({ id: "cx", status: "signed_out" })];
  const { w } = await render();
  const codex = w.find('[data-agent="codex"]');
  expect(codex.text()).toContain("Not signed in");
  await buttonByText(codex, "Sign in").trigger("click");
  expect(codex.find('[data-test="login-steps"]').text()).toContain(
    "codex login",
  );
  w.unmount();
});

it("roles table: changing an agent reloads its models and saves the first one", async () => {
  routes["PATCH /api/v1/settings"] = patchEcho;
  const { w } = await render();
  expect(w.findAll("#model-builder option").map((o) => o.text())).toContain(
    "Builder model",
  );
  await w.find("#agent-builder").setValue("codex");
  await flushPromises();
  expect(sent("PATCH", "/api/v1/settings")[0].body).toEqual({
    role_agents: { ...baseRoleAgents, builder: "codex" },
    builder_model: "openai/gpt-6.1-sol",
  });
  expect(
    calls.some((c) => c.path === "/api/v1/models" && c.method === "GET"),
  ).toBe(true);
  expect(w.findAll("#model-builder option").map((o) => o.text())).toEqual([
    "GPT 6.1 Sol",
    "GPT 6",
  ]);
  w.unmount();
});

it("roles table: Codex is disabled for Inspector, Builder permission is locked, Chat has a choice", async () => {
  routes["PATCH /api/v1/settings"] = patchEcho;
  const { w } = await render();
  for (const role of ["foreman", "architect", "inspector", "chat"]) {
    const o = w.find(`#agent-${role} option[value="codex"]`);
    expect(o.attributes("disabled"), role).toBeDefined();
    expect(o.text()).toContain("Builder only");
  }
  expect(
    w.find('#agent-builder option[value="codex"]').attributes("disabled"),
  ).toBeUndefined();
  const builder = w.find('[data-role="builder"]');
  expect(builder.text()).toContain("Edit its worktree");
  expect(builder.find('select[aria-label$="permission"]').exists()).toBe(false);
  expect(
    w.findAll("#perm-chat option").map((o) => o.attributes("value")),
  ).toEqual(["ask", "read"]);
  await w.find("#perm-chat").setValue("read");
  await flushPromises();
  expect(sent("PATCH", "/api/v1/settings")[0].body).toEqual({
    role_permissions: { chat: "read" },
  });
  w.unmount();
});

it("roles table hints in red when a role's agent is signed out or off", async () => {
  appSettings.role_agents = {
    ...baseRoleAgents,
    builder: "codex",
    chat: "opencode",
  };
  agentsBody.agents[1].accounts = [];
  agentsBody.agents[2].installed = true;
  const { w } = await render();
  expect(w.find('[data-test="hint-builder"]').text()).toBe(
    "Codex isn't signed in",
  );
  expect(w.find('[data-test="hint-chat"]').text()).toBe("OpenCode is off");
  expect(w.find('[data-test="hint-foreman"]').exists()).toBe(false);
  w.unmount();
});

it("shows roles and Advanced read-only to a non-admin", async () => {
  useSession().user = { ...user, role: "VIEWER" };
  const { w } = await render();
  expect(w.find('[data-test="read-only-note"]').exists()).toBe(true);
  expect(w.find("#agent-builder").attributes("disabled")).toBeDefined();
  expect(w.find("#max-runs").attributes("disabled")).toBeDefined();
  expect(sent("GET", "/api/v1/mcp-servers")).toHaveLength(0);
  w.unmount();
});

it("connections are grouped by provider with accounts, used-by and inline Connect", async () => {
  routes["POST /api/v1/connections"] = () => json({ ...gh, id: "c2" }, 201);
  const { w } = await render();
  const groups = w.findAll(".conn-group > h3").map((h) => h.text());
  expect(groups[0]).toContain("Code hosting");
  expect(groups[1]).toContain("Work tools");
  expect(groups[2]).toContain("MCP servers");
  const github = w.find('[data-connection="github"]');
  expect(github.text()).toContain("Used by POS");
  expect(w.find('[data-connection="jira"]').text()).toContain("Connect");

  await click(w, "Connect Jira");
  await w.find("#f-site-jira").setValue("acme.atlassian.net");
  await w.find("#f-user-jira").setValue("me@acme.com");
  await w.find("#f-token-jira").setValue("tok");
  await w.find('[data-test="connect-jira"]').trigger("submit");
  await flushPromises();
  expect(sent("POST", "/api/v1/connections")[0].body).toEqual({
    provider: "jira",
    name: "Jira",
    base_url: "https://acme.atlassian.net",
    username: "me@acme.com",
    token: "tok",
  });
  w.unmount();
});

it("connection menu tests, turns off and disconnects an account", async () => {
  routes["POST /api/v1/connections/c1/test"] = () => json(gh);
  routes["PATCH /api/v1/connections/c1"] = () => json(gh);
  routes["DELETE /api/v1/connections/c1"] = () =>
    new Response(null, { status: 204 });
  vi.spyOn(window, "confirm").mockReturnValue(true);
  const { w } = await render();
  for (const item of ["Test connection", "Turn off", "Disconnect"]) {
    await click(w, "More for Acme GitHub");
    await buttonByText(w, item).trigger("click");
    await flushPromises();
  }
  expect(sent("POST", "/api/v1/connections/c1/test")).toHaveLength(1);
  expect(sent("PATCH", "/api/v1/connections/c1")[0].body).toEqual({
    status: "disabled",
  });
  expect(sent("DELETE", "/api/v1/connections/c1")).toHaveLength(1);
  w.unmount();
});

it("Advanced saves max runs (1..16) and rejects out-of-range values", async () => {
  routes["PATCH /api/v1/settings"] = patchEcho;
  const { w } = await render();
  expect((w.find("#max-runs").element as HTMLInputElement).value).toBe("4");
  await w.find("#max-runs").setValue("8");
  await w.find("#max-runs").trigger("change");
  await flushPromises();
  expect(sent("PATCH", "/api/v1/settings")[0].body).toEqual({
    max_concurrent_runs: 8,
  });
  await w.find("#max-runs").setValue("40");
  await w.find("#max-runs").trigger("change");
  await flushPromises();
  expect(sent("PATCH", "/api/v1/settings")).toHaveLength(1);
  expect(w.text()).toContain("from 1 to 16");
  w.unmount();
});

it("Jev toggle patches jev_enabled and the key is write-only", async () => {
  routes["PATCH /api/v1/settings"] = patchEcho;
  const { w } = await render();
  await w.find('input[aria-label="Jev plan checks"]').setValue(false);
  await flushPromises();
  expect(sent("PATCH", "/api/v1/settings")[0].body).toEqual({
    jev_enabled: false,
  });
  expect(w.find('[data-test="jev-status"]').text()).toContain("Off");
  expect(w.find("#jev-key").exists()).toBe(false);
  expect(w.find('[data-test="jev-key-state"]').text()).toBe(
    "No key yet. Add one to turn checks on.",
  );
  await buttonByText(w, "Add key").trigger("click");
  expect((w.find("#jev-key").element as HTMLInputElement).type).toBe(
    "password",
  );
  await w.find("#jev-key").setValue("sk-secret");
  await buttonByText(w, "Save key").trigger("click");
  await flushPromises();
  expect(sent("PATCH", "/api/v1/settings")[1].body).toEqual({
    jev_key: "sk-secret",
  });
  expect(w.html()).not.toContain("sk-secret");
  expect(w.find("#jev-key").exists()).toBe(false);
  w.unmount();
});

it("hides the Jev key field when the server sets the key", async () => {
  routes["GET /api/v1/settings/runtime"] = () =>
    json({ credentials_configured: true, jev_key_from_env: true });
  const { w } = await render();
  expect(w.find("#jev-key").exists()).toBe(false);
  expect(w.find('[data-test="jev-key-state"]').text()).toBe(
    "Key set by the server.",
  );
  expect(w.text()).not.toContain("Change key");
  expect(w.text()).not.toContain("Add key");
  w.unmount();
});

it("offers Change key once a key is saved", async () => {
  routes["GET /api/v1/settings"] = () =>
    json({ ...settings, ...appSettings, jev_key_set: true });
  const { w } = await render();
  expect(w.find('[data-test="jev-key-state"]').text()).toBe("Key saved.");
  expect(w.find("#jev-key").exists()).toBe(false);
  await buttonByText(w, "Change key").trigger("click");
  expect(w.find("#jev-key").exists()).toBe(true);
  w.unmount();
});

it("overlapping role agent changes are serialized and neither is lost", async () => {
  const bodies: unknown[] = [];
  let release!: () => void;
  const gate = new Promise<void>((r) => (release = r));
  let saved = { ...baseRoleAgents };
  routes["PATCH /api/v1/settings"] = async (body) => {
    bodies.push(body);
    if (bodies.length === 1) await gate;
    saved = { ...saved, ...(body as { role_agents: object }).role_agents };
    return json({ ...settings, ...appSettings, role_agents: saved });
  };
  const { w } = await render();
  await w.find("#agent-builder").setValue("codex");
  await w.find("#agent-chat").setValue("codex");
  await flushPromises();
  expect(bodies).toHaveLength(1);
  release();
  await flushPromises();
  expect(bodies).toHaveLength(2);
  expect((bodies[1] as { role_agents: object }).role_agents).toEqual({
    ...baseRoleAgents,
    builder: "codex",
    chat: "codex",
  });
  w.unmount();
});

it("a failed model load aborts the agent change, reverts the select and retries next time", async () => {
  routes["PATCH /api/v1/settings"] = patchEcho;
  let failing = true;
  routes["GET /api/v1/models"] = (_b, url) =>
    url.includes("agent=codex")
      ? failing
        ? fail(500, "INTERNAL", "models unavailable")
        : json({ models: [codexModel] })
      : json({ models });
  const { w } = await render();
  await w.find("#agent-builder").setValue("codex");
  await flushPromises();
  expect(sent("PATCH", "/api/v1/settings")).toHaveLength(0);
  expect((w.find("#agent-builder").element as HTMLSelectElement).value).toBe(
    "claude",
  );
  expect(w.text()).toContain("models unavailable");
  failing = false;
  await w.find("#agent-builder").setValue("codex");
  await flushPromises();
  expect(sent("PATCH", "/api/v1/settings")[0].body).toMatchObject({
    builder_model: "openai/gpt-6.1-sol",
  });
  w.unmount();
});

it("signed-out accounts use the server login_command and Sign in shows even with no active account", async () => {
  agentsBody.agents[1].accounts = [
    acct({
      id: "cx",
      active: false,
      status: "signed_out",
      system: false,
      login_command: "CODEX_HOME=/h codex login",
    }),
  ];
  const { w } = await render();
  const codex = w.find('[data-agent="codex"]');
  await buttonByText(codex, "Sign in").trigger("click");
  expect(codex.find('[data-test="login-steps"]').text()).toContain(
    "CODEX_HOME=/h codex login",
  );
  w.unmount();
});

it("Add account closes the menu and focuses the label field", async () => {
  const { w } = await render();
  await click(w, "More for Claude");
  await buttonByText(w, "Add account").trigger("click");
  await flushPromises();
  expect(w.find('[role="menuitem"]').exists()).toBe(false);
  expect(document.activeElement?.id).toBe("acct-label-claude");
  w.unmount();
});

it("Cancel clears the Jev key input", async () => {
  const { w } = await render();
  await buttonByText(w, "Add key").trigger("click");
  await w.find("#jev-key").setValue("abc");
  await buttonByText(w, "Cancel").trigger("click");
  await buttonByText(w, "Add key").trigger("click");
  expect((w.find("#jev-key").element as HTMLInputElement).value).toBe("");
  w.unmount();
});

it("adds an MCP server with a secret and shows a server validation error", async () => {
  routes["POST /api/v1/mcp-servers"] = () =>
    fail(
      400,
      "VALIDATION_FAILED",
      "name must be 1-40 characters of a-z and 0-9",
    );
  const { w } = await render();
  await openMcp(w);
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
  await openMcp(w);
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
  await click(w, "More for docs_search");
  await buttonByText(w, "Tools and roles").trigger("click");

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
  ).toBeUndefined();
  expect(w.find('[data-test="mcp-warn-write_page"]').exists()).toBe(false);
  await w.find('[data-test="mcp-tool-write_page"]').setValue(true);
  await flushPromises();
  expect(w.find('[data-test="mcp-warn-write_page"]').text()).toBe(
    "Agent bisa menjalankan tool ini tanpa persetujuan",
  );
  expect(w.find('[data-test="mcp-warn-search"]').exists()).toBe(false);

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

it("Add project dialog offers no contract tests or connection binding", async () => {
  const f = useFactory();
  f.connections = [gh];
  const d = mount(AddProjectDialog, {
    props: { open: true },
    attachTo: document.body,
  });
  await flushPromises();
  const dialogText = document.body.textContent ?? "";
  expect(document.querySelector("#prj-conn")).toBeNull();
  expect(dialogText).not.toContain("Contract tests");
  expect(document.querySelector('[data-test^="contract-tests"]')).toBeNull();
  d.unmount();
});

const oauthServer = (over: Partial<McpServer> = {}) =>
  server({
    auth: "oauth",
    secret_keys: [],
    status: "needs_sign_in",
    signed_in: false,
    ...over,
  });

it("offers None, Token and OAuth for http servers and sends the OAuth fields", async () => {
  routes["POST /api/v1/mcp-servers"] = () => json(oauthServer(), 201);
  const { w } = await render();
  await openMcp(w);
  expect(w.find("#mcp-auth").exists()).toBe(false);
  await w.find("#mcp-transport").setValue("http");
  const options = w.findAll("#mcp-auth option").map((o) => o.text());
  expect(options).toEqual(["None", "Token or header", "OAuth sign-in"]);
  expect(w.find('input[aria-label="Secret name"]').exists()).toBe(true);
  await w.find("#mcp-auth").setValue("oauth");
  expect(w.find('input[aria-label="Secret name"]').exists()).toBe(false);
  await w.find("#conn-name").setValue("docs_search");
  await w.find("#mcp-url").setValue("https://mcp.example.com/mcp");
  await w.find("#mcp-client-id").setValue(" cid ");
  await w.find("#mcp-client-secret").setValue("csec");
  await w.find("#mcp-scopes").setValue("read write");
  await w.find('[data-test="connection-form"]').trigger("submit");
  await flushPromises();
  expect(sent("POST", "/api/v1/mcp-servers")[0].body).toEqual({
    name: "docs_search",
    transport: "http",
    command: "",
    args: [],
    url: "https://mcp.example.com/mcp",
    secret: {},
    auth: "oauth",
    client_id: "cid",
    client_secret: "csec",
    scopes: "read write",
  });
  w.unmount();
});

it("Sign in starts the flow and opens the authorize URL in a new window", async () => {
  routes["GET /api/v1/mcp-servers"] = () =>
    json({ servers: [oauthServer()], credentials_configured: true });
  routes["POST /api/v1/mcp-servers/m1/oauth/start"] = () =>
    json({ authorize_url: "https://auth.example/authorize?state=s" });
  const win = {
    location: { href: "" },
    close: vi.fn(),
    opener: window as Window | null,
  };
  const open = vi.spyOn(window, "open").mockReturnValue(win as never);
  const { w } = await render();
  const row = w.find('[data-mcp="docs_search"]');
  expect(row.find('[data-test="mcp-oauth-state"]').text()).toContain(
    "sign-in needed",
  );
  expect(row.text()).toContain("Sign-in needed");
  const btn = row.find('[data-test="mcp-sign-in"]');
  expect(btn.text()).toBe("Sign in");
  await btn.trigger("click");
  await flushPromises();
  expect(open).toHaveBeenCalledWith("", "_blank");
  expect(sent("POST", "/api/v1/mcp-servers/m1/oauth/start")).toHaveLength(1);
  expect(win.location.href).toBe("https://auth.example/authorize?state=s");
  expect(win.opener).toBeNull();
  w.unmount();
});

it("closes the opened window and shows the error when start fails", async () => {
  routes["GET /api/v1/mcp-servers"] = () =>
    json({ servers: [oauthServer()], credentials_configured: true });
  routes["POST /api/v1/mcp-servers/m1/oauth/start"] = () =>
    fail(400, "VALIDATION_FAILED", "enter a client ID under Advanced");
  const win = { location: { href: "" }, close: vi.fn() };
  vi.spyOn(window, "open").mockReturnValue(win as never);
  const { w } = await render();
  await w.find('[data-test="mcp-sign-in"]').trigger("click");
  await flushPromises();
  expect(win.close).toHaveBeenCalled();
  expect(win.location.href).toBe("");
  expect(w.text()).toContain("enter a client ID under Advanced");
  w.unmount();
});

it("shows signed-in state with expiry and offers Sign in again", async () => {
  routes["GET /api/v1/mcp-servers"] = () =>
    json({
      servers: [
        oauthServer({
          status: "active",
          signed_in: true,
          expires_at: "2026-11-12T10:00:00Z",
          oauth_client: "dynamic",
        }),
      ],
      credentials_configured: true,
    });
  const { w } = await render();
  const row = w.find('[data-mcp="docs_search"]');
  const state = row.find('[data-test="mcp-oauth-state"]').text();
  expect(state).toContain("signed in");
  expect(state).toContain("access token until");
  expect(state).toContain("dynamic client");
  expect(row.find('[data-test="mcp-sign-in"]').text()).toBe("Sign in again");
  expect(row.text()).not.toContain("Replace secrets");
  w.unmount();
});

it("shows the callback result from the URL once and clears it", async () => {
  let r = await render("/settings?mcp_oauth=ok#connections");
  expect(r.w.find('[data-test="mcp-oauth-flag"]').text()).toContain(
    "Signed in",
  );
  expect(r.router.currentRoute.value.query.mcp_oauth).toBeUndefined();
  r.w.unmount();
  r = await render("/settings?mcp_oauth=error#connections");
  expect(r.w.find('[data-test="mcp-oauth-flag"]').text()).toContain(
    "Sign-in did not complete",
  );
  r.w.unmount();
  r = await render("/settings#connections");
  expect(r.w.find('[data-test="mcp-oauth-flag"]').exists()).toBe(false);
  r.w.unmount();
});

it("MCP row shows one status and one primary action, the rest in the menu", async () => {
  routes["GET /api/v1/mcp-servers"] = () =>
    json({ servers: [discovered(["builder"])], credentials_configured: true });
  const { w } = await render();
  const row = w.find('[data-mcp="docs_search"]');
  expect(row.find('[data-test="mcp-status"]').text()).toBe("0 of 3 tools on");
  expect(row.find(".btn.primary").exists()).toBe(false);
  expect(row.findAll("button").map((b) => b.attributes("aria-label"))).toEqual([
    "More for docs_search",
  ]);
  await click(w, "More for docs_search");
  expect(row.findAll('[role="menuitem"]').map((b) => b.text())).toEqual([
    "Test and discover again",
    "Tools and roles",
    "Rename or rotate",
    "Turn off",
    "Remove",
  ]);
  w.unmount();
});

it("shows usage bars for Codex without any opt-in, and the plan line when there is no usage", async () => {
  agentsBody.agents[1].accounts = [
    acct({
      id: "cx",
      usage: { session: { used_pct: 12 }, week: { used_pct: 40 } },
    }),
  ];
  const { w } = await render();
  const codex = w.find('[data-agent="codex"]');
  expect(codex.find('[data-meter="session"]').text()).toContain("12%");
  expect(codex.find('[data-meter="week"]').text()).toContain("40%");
  expect(codex.find('[data-test="plan-line"]').exists()).toBe(false);
  const claude = w.find('[data-agent="claude"]');
  expect(claude.find("[data-meter]").exists()).toBe(false);
  expect(claude.find('[data-test="plan-line"]').exists()).toBe(true);
  w.unmount();
});

const dsAcct = (over: object = {}) =>
  acct({
    id: "ds",
    label: "DeepSeek",
    system: false,
    active: false,
    who: "",
    plan: "API key · api.deepseek.com",
    expires_at: undefined,
    roles: ["builder"],
    ...over,
  });

it("role chips show only with 2+ accounts and PUT the account's new role list", async () => {
  routes["PUT /api/v1/agents/claude/accounts/ds/roles"] = () =>
    new Response(null, { status: 204 });
  agentsBody.agents[0].accounts = [acct({ roles: [] }), dsAcct()];
  const { w } = await render();
  expect(w.find('[data-agent="codex"] .role-chip').exists()).toBe(false);
  const row = w.find('[data-agent="claude"]');
  const group = row.find('[aria-label="Roles for DeepSeek"]');
  expect(group.findAll(".role-chip").map((c) => c.text())).toEqual([
    "Foreman",
    "Architect",
    "Builder",
    "Inspector",
    "Chat",
  ]);
  const pressed = group
    .findAll(".role-chip")
    .map((c) => c.attributes("aria-pressed"));
  expect(pressed).toEqual(["false", "false", "true", "false", "false"]);
  await group.findAll(".role-chip")[3].trigger("click");
  await flushPromises();
  expect(
    sent("PUT", "/api/v1/agents/claude/accounts/ds/roles")[0].body,
  ).toEqual({ roles: ["builder", "inspector"] });
  w.unmount();
});

it("role chips stay locked until the reload lands, so a second click builds on the first", async () => {
  agentsBody.agents[0].accounts = [acct({ roles: [] }), dsAcct({ roles: [] })];
  routes["PUT /api/v1/agents/claude/accounts/ds/roles"] = (body) => {
    agentsBody.agents[0].accounts[1] = dsAcct({
      roles: (body as { roles: string[] }).roles,
    });
    return new Response(null, { status: 204 });
  };
  const { w } = await render();
  const chips = () =>
    w
      .find('[data-agent="claude"] [aria-label="Roles for DeepSeek"]')
      .findAll(".role-chip");
  const reload = routes["GET /api/v1/agents"];
  let release!: () => void;
  const gate = new Promise<void>((r) => (release = r));
  routes["GET /api/v1/agents"] = async (b, u) => {
    await gate;
    return reload(b, u);
  };
  await chips()[2].trigger("click");
  await flushPromises();
  expect(chips().every((c) => c.attributes("disabled") !== undefined)).toBe(
    true,
  );
  await chips()[3].trigger("click");
  expect(sent("PUT", "/api/v1/agents/claude/accounts/ds/roles")).toHaveLength(
    1,
  );
  release();
  await flushPromises();
  expect(chips()[3].attributes("disabled")).toBeUndefined();
  await chips()[3].trigger("click");
  await flushPromises();
  expect(
    sent("PUT", "/api/v1/agents/claude/accounts/ds/roles").map((c) => c.body),
  ).toEqual([{ roles: ["builder"] }, { roles: ["builder", "inspector"] }]);
  w.unmount();
});

it("a role chip held by another account names it and still moves the role", async () => {
  routes["PUT /api/v1/agents/claude/accounts/ds/roles"] = () =>
    new Response(null, { status: 204 });
  agentsBody.agents[0].accounts = [
    acct({ roles: ["chat"] }),
    dsAcct({ roles: ["builder"] }),
  ];
  const { w } = await render();
  const chats = w
    .find('[data-agent="claude"] [aria-label="Roles for DeepSeek"]')
    .findAll(".role-chip")[4];
  expect(chats.attributes("aria-label")).toBe("Chat · on System default");
  expect(chats.attributes("title")).toBe("Chat · on System default");
  await chats.trigger("click");
  await flushPromises();
  expect(
    sent("PUT", "/api/v1/agents/claude/accounts/ds/roles")[0].body,
  ).toEqual({ roles: ["builder", "chat"] });
  w.unmount();
});

it("an account whose settings.json cannot be read shows why", async () => {
  agentsBody.agents[0].accounts = [
    acct({ active: false }),
    dsAcct({
      active: true,
      status: "unknown",
      status_note: "settings.json is not valid JSON",
    }),
  ];
  const { w } = await render();
  expect(w.find('[data-agent="claude"]').text()).toContain(
    "settings.json is not valid JSON",
  );
  w.unmount();
});

it("roles table checks the account serving the role and shows the API-key host", async () => {
  agentsBody.agents[0].accounts = [
    acct({ roles: [] }),
    dsAcct({ status: "signed_out", plan: "API key · api.deepseek.com" }),
  ];
  const { w } = await render();
  expect(w.find('[data-test="hint-builder"]').text()).toBe(
    "Claude isn't signed in",
  );
  expect(w.find('[data-test="via-builder"]').text()).toBe(
    "via api.deepseek.com",
  );
  expect(w.find('[data-test="hint-foreman"]').exists()).toBe(false);
  expect(w.find('[data-test="via-foreman"]').exists()).toBe(false);
  w.unmount();
});
