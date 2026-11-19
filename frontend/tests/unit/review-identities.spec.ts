import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { createRouter, createMemoryHistory } from "vue-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import ReviewIdentities from "../../src/features/settings/ReviewIdentities.vue";
import ProjectsView from "../../src/features/projects/ProjectsView.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import { user } from "../fixtures/workspace";
import { readinessLabel } from "../../src/types/reviewIdentity";

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status });
let calls: { method: string; path: string; body: unknown }[];
let routes: Record<string, (body: unknown) => Response>;

const identities = () => [
  {
    provider: "github",
    host: "github.com",
    pinned: null,
    connections: [
      {
        id: "c1",
        name: "Acme GitHub",
        login: "octo",
        readiness: "unknown",
      },
    ],
    cli: { tool: "gh", installed: true, users: ["octo-cli"] },
  },
  {
    provider: "gitlab",
    host: "gitlab.com",
    pinned: null,
    connections: [],
    cli: null,
  },
];

beforeEach(() => {
  setActivePinia(createPinia());
  useSession().user = user;
  calls = [];
  routes = { "GET /api/v1/review-identities": () => json(identities()) };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      const path = input.split("?")[0];
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, path, body });
      const h = routes[`${method} ${path}`];
      return h
        ? h(body)
        : json({ error: { code: "NOT_FOUND", message: "no route" } }, 404);
    }),
  );
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

it("labels readiness and warns within 7 days of expiry", () => {
  const now = Date.parse("2026-11-19T00:00:00Z");
  expect(readinessLabel("ready").text).toBe("Ready");
  expect(readinessLabel("read_only").text).toBe("Read only, cannot post");
  expect(readinessLabel("unknown").text).toBe("Unknown, write not probed");
  expect(readinessLabel("expired").tone).toBe("red");
  expect(readinessLabel("invalid").text).toBe("Invalid");
  expect(readinessLabel("ready", "2026-11-22T00:00:00Z", now)).toEqual({
    tone: "orange",
    text: "Expires in 3 days",
  });
  expect(readinessLabel("ready", "2027-01-01T00:00:00Z", now).text).toBe(
    "Ready",
  );
});

it("lists identities and pins a connection or a gh user", async () => {
  const w = mount(ReviewIdentities, { attachTo: document.body });
  await flushPromises();
  const gh = w.find('[data-identity="github@github.com"]');
  expect(gh.find('[data-test="pinned"]').text()).toBe("Not set");
  expect(gh.find('[data-connection-row="c1"]').text()).toContain(
    "Unknown, write not probed",
  );
  expect(gh.find('[data-test="cli-row"]').exists()).toBe(true);
  expect(w.find('[data-identity="gitlab@gitlab.com"]').text()).toContain(
    "token connection",
  );

  await gh.find('[data-connection-row="c1"] button').trigger("click");
  routes["PUT /api/v1/review-identities"] = () => json({});
  await flushPromises();
  const put = calls.filter((c) => c.method === "PUT");
  expect(put[0].body).toEqual({
    provider: "github",
    host: "github.com",
    source: "connection",
    connection_id: "c1",
  });

  await gh.find('[data-cli-user="octo-cli"] button').trigger("click");
  await flushPromises();
  expect(calls.filter((c) => c.method === "PUT")[1].body).toEqual({
    provider: "github",
    host: "github.com",
    source: "gh",
    login: "octo-cli",
  });
});

it("shows the code and url from live events, then falls back", async () => {
  routes["POST /api/v1/review-identities/cli/login"] = () =>
    json({ login_id: "l1" });
  const w = mount(ReviewIdentities, { attachTo: document.body });
  await flushPromises();
  const loginBtn = w
    .find('[data-test="cli-row"]')
    .findAll("button")
    .find((b) => b.text() === "Login with gh")!;
  await loginBtn.trigger("click");
  await flushPromises();
  expect(calls.find((c) => c.path.endsWith("/cli/login"))?.body).toEqual({
    tool: "gh",
    host: "github.com",
  });

  const f = useFactory();
  f.liveEvents = [
    {
      id: 0,
      work_order_id: "",
      actor: "",
      kind: "cli_login.url",
      message: "",
      payload: {
        login_id: "l1",
        kind: "url",
        url: "https://github.com/login/device",
      },
    },
    {
      id: 0,
      work_order_id: "",
      actor: "",
      kind: "cli_login.code",
      message: "",
      payload: { login_id: "l1", kind: "code", code: "ABCD-1234" },
    },
    {
      id: 0,
      work_order_id: "",
      actor: "",
      kind: "cli_login.code",
      message: "",
      payload: { login_id: "other", kind: "code", code: "NOPE" },
    },
  ];
  await flushPromises();
  expect(
    document.body.querySelector('[data-test="cli-code"]')?.textContent,
  ).toBe("ABCD-1234");
  expect(
    document.body.querySelector('[data-test="cli-url"]')?.getAttribute("href"),
  ).toBe("https://github.com/login/device");

  f.liveEvents = [
    {
      id: 0,
      work_order_id: "",
      actor: "",
      kind: "cli_login.fallback",
      message: "",
      payload: { login_id: "l1", kind: "fallback" },
    },
    ...f.liveEvents,
  ];
  await flushPromises();
  const fb = document.body.querySelector('[data-test="cli-fallback"]');
  expect(fb?.textContent).toContain("gh auth login -h github.com");
  const before = calls.filter(
    (c) => c.path === "/api/v1/review-identities",
  ).length;
  const refresh = [...document.body.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === "Refresh" && b.closest('[role="dialog"]'),
  ) as HTMLButtonElement;
  refresh.click();
  await flushPromises();
  expect(
    calls.filter((c) => c.path === "/api/v1/review-identities").length,
  ).toBe(before + 1);
});

it("fills PRs for you from the summary and shows a dash for null", async () => {
  routes["GET /api/v1/projects"] = () =>
    json({
      projects: [
        {
          id: "a",
          name: "Alpha",
          repo_url: "",
          language: "go",
          description: "",
          created_at: "",
        },
        {
          id: "b",
          name: "Beta",
          repo_url: "",
          language: "go",
          description: "",
          created_at: "",
        },
      ],
    });
  routes["GET /api/v1/work-orders"] = () => json({ work_orders: [] });
  routes["GET /api/v1/projects/summary"] = () =>
    json([
      {
        project_id: "a",
        needs_you: 0,
        wo_running: 0,
        prs_for_me: 3,
        shipped_30d: 0,
        last_activity: "",
        identity: null,
      },
      {
        project_id: "b",
        needs_you: 0,
        wo_running: 0,
        prs_for_me: null,
        shipped_30d: 0,
        last_activity: "",
        identity: null,
      },
    ]);
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/projects", component: ProjectsView },
      { path: "/projects/:id", component: { template: "<div/>" } },
    ],
  });
  await router.push("/projects");
  const w = mount(ProjectsView, { global: { plugins: [router] } });
  await flushPromises();
  const vals = w.findAll('[data-test="prs-for-you"]').map((e) => e.text());
  expect(vals.sort()).toEqual(["3", "—"]);
});
