import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import ProjectChecks from "../../src/features/factory/ProjectChecks.vue";
import WorkOrderView from "../../src/features/factory/work-orders/WorkOrderView.vue";
import { useFactory } from "../../src/stores/factory";
import { dryRunOf, failsOnMain, mapToText, textToMap, useChecks } from "../../src/stores/checks";
import { useSession } from "../../src/stores/session";
import { orders, projects, tasks, user } from "../fixtures/workspace";
import type {
  CheckDryRun,
  CheckProposal,
  CheckStep,
  ChecksStatus,
} from "../../src/types/api";

const apiTest: CheckStep = {
  name: "api test",
  dir: "api",
  command: "go test ./...",
  source: ".github/workflows/ci.yml:68",
};
const webLint: CheckStep = { name: "web lint", dir: "web", command: "npm run lint" };
const results: CheckDryRun[] = [
  { name: "web deps", kind: "setup", command: "npm ci", outcome: "PASS", exit_code: 0, output_tail: "" },
  { name: "api test", kind: "check", command: "go test ./...", outcome: "FAIL", exit_code: 1, output_tail: "emoji assertion failed" },
  { name: "web lint", kind: "check", command: "npm run lint", outcome: "PASS", exit_code: 0, output_tail: "" },
];
const proposal = (): CheckProposal => ({
  profile: {
    version: 2,
    services: { compose: "docker-compose.yml", names: ["postgres"] },
    env: { DATABASE_URL: "postgres://u:p@{{postgres}}/{{db}}" },
    setup: [{ name: "web deps", dir: "web", command: "npm ci" }],
    checks: [structuredClone(apiTest), structuredClone(webLint)],
  },
  results,
  notes: { "web lint": "from package.json" },
  base_sha: "abc1234def",
  model: "anthropic/sonnet",
  created_at: "2026-11-17T10:00:00Z",
});
const idle: ChecksStatus = { profile: null, proposal: null, run: { status: "idle" }, stale: false };

let status: ChecksStatus;
const fetchMock = vi.fn();
const calls = (method: string) =>
  fetchMock.mock.calls.filter((c) => (c[1]?.method ?? "GET") === method);

beforeEach(() => {
  setActivePinia(createPinia());
  status = structuredClone(idle);
  fetchMock.mockReset();
  fetchMock.mockImplementation(async (_url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (method === "POST") {
      status = { ...status, run: { status: "running" } };
      return new Response(JSON.stringify({ run: { status: "running" } }), { status: 202 });
    }
    if (method === "PUT" || method === "DELETE") {
      if (method === "PUT")
        status = { ...status, profile: JSON.parse(String(init?.body)).profile };
      status = { ...status, proposal: null, run: { status: "idle" } };
      return new Response(method === "DELETE" ? null : "{}", {
        status: method === "DELETE" ? 204 : 200,
      });
    }
    return new Response(JSON.stringify(status));
  });
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("fails-on-main logic", () => {
  it("flags a FAIL dry-run result of a check, not of a passing or setup step", () => {
    expect(failsOnMain(results, apiTest)).toBe(true);
    expect(failsOnMain(results, webLint)).toBe(false);
    expect(failsOnMain(results, { name: "web deps", command: "npm ci" })).toBe(false);
    expect(failsOnMain(undefined, apiTest)).toBe(false);
  });

  it("ignores the result once the command was edited", () => {
    const edited = { ...apiTest, command: "go test ./internal/..." };
    expect(dryRunOf(results, "check", edited)).toBeUndefined();
    expect(failsOnMain(results, edited)).toBe(false);
  });
});

describe("checks store", () => {
  it("loads the status and clones the proposal into an editable draft", async () => {
    status = { ...idle, proposal: proposal(), run: { status: "ready" } };
    const c = useChecks();
    await c.load("p1");
    expect(c.draft?.checks).toHaveLength(2);
    expect(c.draft).not.toBe(status.proposal!.profile);
    expect(c.dirty).toBe(false);
    c.setCommand("checks", 0, "go test ./... -short");
    expect(c.dirty).toBe(true);
    await c.load("p1");
    expect(c.draft?.checks[0].command).toBe("go test ./... -short");
  });

  it("makeOptional marks only that check", async () => {
    status = { ...idle, proposal: proposal(), run: { status: "ready" } };
    const c = useChecks();
    await c.load("p1");
    c.makeOptional(0);
    expect(c.draft?.checks.map((s) => !!s.optional)).toEqual([true, false]);
  });

  it("suggest posts, then polls while the run is running and stops when it is ready", async () => {
    vi.useFakeTimers();
    const c = useChecks();
    await c.load("p1");
    await c.suggest("p1");
    expect(calls("POST")[0][0]).toBe("/api/v1/projects/p1/checks/suggest");
    expect(c.running).toBe(true);
    const before = calls("GET").length;
    status = { ...idle, proposal: proposal(), run: { status: "ready" } };
    await vi.advanceTimersByTimeAsync(2100);
    expect(calls("GET").length).toBe(before + 1);
    expect(c.running).toBe(false);
    expect(c.draft?.checks).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(10000);
    expect(calls("GET").length).toBe(before + 1);
  });

  it("approve sends the edited draft and refreshes", async () => {
    status = { ...idle, proposal: proposal(), run: { status: "ready" } };
    const c = useChecks();
    await c.load("p1");
    c.makeOptional(0);
    await c.approve();
    const put = calls("PUT")[0];
    expect(put[0]).toBe("/api/v1/projects/p1/checks");
    expect(JSON.parse(put[1].body).profile.checks[0].optional).toBe(true);
    expect(c.status?.profile?.checks[0].optional).toBe(true);
    expect(c.status?.proposal).toBeNull();
    expect(c.draft).toBeNull();
  });

  it("discard deletes the proposal", async () => {
    status = { ...idle, proposal: proposal(), run: { status: "ready" } };
    const c = useChecks();
    await c.load("p1");
    await c.discard();
    expect(calls("DELETE")[0][0]).toBe("/api/v1/projects/p1/checks/proposal");
    expect(c.status?.proposal).toBeNull();
  });

  it("surfaces an API error and does not poll", async () => {
    fetchMock.mockImplementationOnce(
      async () =>
        new Response(JSON.stringify({ error: { code: "X", message: "boom" } }), { status: 500 }),
    );
    const c = useChecks();
    await c.load("p1");
    expect(c.error).toBe("boom");
    expect(c.timer).toBeNull();
  });
});

describe("ProjectChecks", () => {
  const mountIt = async () => {
    useSession().user = user;
    const w = mount(ProjectChecks, { props: { projectId: "p1" }, attachTo: document.body });
    await flushPromises();
    return w;
  };

  it("offers Suggest checks when nothing is approved", async () => {
    const w = await mountIt();
    const btn = w.get('[data-test="suggest-checks"]');
    expect(btn.text()).toBe("Suggest checks");
    await btn.trigger("click");
    await flushPromises();
    expect(calls("POST")).toHaveLength(1);
    expect(w.find('[data-test="checks-running"]').exists()).toBe(true);
    expect(w.get('[data-test="suggest-checks"]').attributes("disabled")).toBeDefined();
    useChecks().stop();
  });

  it("labels a failing check 'fails on main' and make optional clears the button", async () => {
    status = { ...idle, proposal: proposal(), run: { status: "ready" } };
    const w = await mountIt();
    const row = w.get('[data-test="check-api test"]');
    expect(row.find('[data-test="fails-on-main"]').exists()).toBe(true);
    expect(row.text()).toContain("emoji assertion failed");
    expect(w.get('[data-test="check-web lint"]').find('[data-test="fails-on-main"]').exists()).toBe(false);
    expect(w.text()).toContain("postgres");
    expect(w.text()).toContain("DATABASE_URL=postgres://u:p@{{postgres}}/{{db}}");
    await row.get('[data-test="make-optional"]').trigger("click");
    expect(w.get('[data-test="check-api test"]').find('[data-test="make-optional"]').exists()).toBe(false);
    expect(w.get('[data-test="check-api test"]').text()).toContain("optional");
    await w.get('[data-test="approve-checks"]').trigger("click");
    await flushPromises();
    expect(JSON.parse(calls("PUT")[0][1].body).profile.checks[0].optional).toBe(true);
    expect(w.find('[data-test="approved"]').exists()).toBe(true);
  });

  it("editing a command drops its dry-run verdict", async () => {
    status = { ...idle, proposal: proposal(), run: { status: "ready" } };
    const w = await mountIt();
    const input = w.get('[data-test="check-api test"] input');
    await input.setValue("go test ./internal/...");
    const row = w.get('[data-test="check-api test"]');
    expect(row.find('[data-test="fails-on-main"]').exists()).toBe(false);
    expect(row.text()).toContain("edited · not dry-run");
  });

  it("shows the stale notice and a failed run", async () => {
    status = {
      ...idle,
      profile: { checks: [apiTest] },
      stale: true,
      run: { status: "failed", error: "model unavailable" },
    };
    const w = await mountIt();
    expect(w.get('[data-test="checks-stale"]').text()).toContain("Checks may be stale");
    expect(w.get('[data-test="checks-failed"]').text()).toContain("model unavailable");
    expect(w.get('[data-test="approved"]').text()).toContain("go test ./...");
  });

  it("hides actions from a viewer", async () => {
    status = { ...idle, proposal: proposal(), run: { status: "ready" } };
    useSession().user = { ...user, role: "VIEWER" } as unknown as typeof user;
    const w = mount(ProjectChecks, { props: { projectId: "p1" }, attachTo: document.body });
    await flushPromises();
    expect(w.find('[data-test="suggest-checks"]').exists()).toBe(false);
    expect(w.find('[data-test="approve-checks"]').exists()).toBe(false);
  });
});

describe("smoke section", () => {
  const withSmoke = (): CheckProposal => {
    const p = proposal();
    p.profile.smoke = {
      start: [
        { name: "api", dir: "api", command: "go run .", port_env: "PORT", ready: "/healthz", env: { APP_URL: "http://localhost:{{port.web}}" } },
        { name: "web", dir: "web", command: "npm run dev", port_env: "PORT", ready: "/" },
      ],
      base: "web",
      login: { path: "/login", user_env: "SMOKE_USER", password_env: "SMOKE_PASSWORD" },
      routes_from: "web/app/**/page.tsx",
      params: { id: "sql:SELECT id FROM t" },
      ready_timeout_sec: 90,
      blocking: false,
    };
    return p;
  };
  const mountIt = async () => {
    useSession().user = user;
    const w = mount(ProjectChecks, { props: { projectId: "p1" }, attachTo: document.body });
    await flushPromises();
    return w;
  };

  it("round-trips env text", () => {
    expect(textToMap("A=1\nB=x=y\n\nC")).toEqual({ A: "1", B: "x=y", C: "" });
    expect(mapToText({ A: "1", B: "2" })).toBe("A=1\nB=2");
  });

  it("toggling smoke on adds a non-blocking block, off sets it to null", async () => {
    status = { ...idle, proposal: proposal(), run: { status: "ready" } };
    const w = await mountIt();
    expect(w.find('[data-test="smoke-warning"]').exists()).toBe(false);
    await w.get('[data-test="smoke-toggle"]').setValue(true);
    expect(w.get('[data-test="smoke-warning"]').text()).toContain("test values only");
    expect(useChecks().draft?.smoke?.blocking).toBe(false);
    await w.get('[data-test="smoke-toggle"]').setValue(false);
    expect(useChecks().draft?.smoke).toBeNull();
    await w.get('[data-test="approve-checks"]').trigger("click");
    await flushPromises();
    expect(JSON.parse(calls("PUT")[0][1].body).profile.smoke).toBeNull();
  });

  it("edits flow into the draft Approve sends", async () => {
    status = { ...idle, proposal: withSmoke(), run: { status: "ready" } };
    const w = await mountIt();
    await w.get('[data-test="smoke-blocking"]').setValue(true);
    await w.get('[aria-label="web command"]').setValue("npm run dev -- --turbo");
    await w.get('[aria-label="Routes from"]').setValue("web/src/**/page.tsx");
    await w.get('[aria-label="Login user env"]').setValue("E2E_USER");
    await w.get('[aria-label="Ready timeout"]').setValue("120");
    const env = w.get('[aria-label="api env"]');
    await env.setValue("APP_URL=http://localhost:{{port.web}}\nJWT_SECRET=test-only");
    await env.trigger("change");
    const params = w.get('[aria-label="Route params"]');
    await params.setValue("id=sql:SELECT 1");
    await params.trigger("change");
    await w.get('[data-test="smoke-add-proc"]').trigger("click");
    await w.get('[data-test="approve-checks"]').trigger("click");
    await flushPromises();
    const sent = JSON.parse(calls("PUT")[0][1].body).profile.smoke;
    expect(sent.blocking).toBe(true);
    expect(sent.start[1].command).toBe("npm run dev -- --turbo");
    expect(sent.start[0].env).toEqual({ APP_URL: "http://localhost:{{port.web}}", JWT_SECRET: "test-only" });
    expect(sent.routes_from).toBe("web/src/**/page.tsx");
    expect(sent.login).toEqual({ path: "/login", user_env: "E2E_USER", password_env: "SMOKE_PASSWORD" });
    expect(sent.params).toEqual({ id: "sql:SELECT 1" });
    expect(sent.ready_timeout_sec).toBe(120);
    expect(sent.start).toHaveLength(3);
  });

  it("removing the base process moves base to the first remaining one", async () => {
    status = { ...idle, proposal: withSmoke(), run: { status: "ready" } };
    const w = await mountIt();
    await w.get('[data-test="smoke-proc-1"] [data-test="smoke-remove-proc"]').trigger("click");
    expect(useChecks().draft?.smoke?.base).toBe("api");
    expect(w.get('[data-test="smoke-proc-0"] [data-test="smoke-remove-proc"]').attributes("disabled")).toBeDefined();
  });

  it("shows warning notes that belong to no check", async () => {
    const p = withSmoke();
    p.notes = { ...p.notes, "env.JWT_SECRET": "looks like a random token" };
    status = { ...idle, proposal: p, run: { status: "ready" } };
    const w = await mountIt();
    const box = w.get('[data-test="proposal-warnings"]');
    expect(box.text()).toContain("env.JWT_SECRET");
    expect(box.text()).not.toContain("from package.json");
  });

  it("summarises the approved smoke block", async () => {
    status = { ...idle, profile: withSmoke().profile, run: { status: "idle" } };
    const w = await mountIt();
    const t = w.get('[data-test="approved-smoke"]').text();
    expect(t).toContain("not blocking");
    expect(t).toContain("api: go run .");
    expect(t).toContain("login /login");
  });
});

describe("work order banner", () => {
  async function renderOrder(kinds: string[]) {
    useSession().user = user;
    const f = useFactory();
    f.projects = projects;
    vi.spyOn(f, "loadDetail").mockImplementation(async (oid) => {
      f.selectedID = oid;
      f.detail = { work_order: structuredClone(orders[0]), plan: tasks, runs: [] };
      f.events = kinds.map((kind, i) => ({
        id: i + 1, work_order_id: oid, actor: "factory", kind, message: kind, created_at: "t",
      }));
    });
    vi.spyOn(f, "reloadDetail").mockResolvedValue();
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/factory", component: { render: () => null } },
        { path: "/factory/:id", component: WorkOrderView },
      ],
    });
    await router.push(`/factory/${orders[0].id}`);
    await router.isReady();
    const w = mount(WorkOrderView, { global: { plugins: [router] }, attachTo: document.body });
    await flushPromises();
    return w;
  }
  beforeEach(() => {
    vi.stubGlobal("EventSource", class { addEventListener() {} close() {} });
  });

  it("offers Suggest checks only when verification was skipped", async () => {
    const w = await renderOrder(["verification_skipped"]);
    const btn = w.get('[data-test="checks-banner"] [data-test="suggest-checks"]');
    await btn.trigger("click");
    await flushPromises();
    expect(calls("POST")[0][0]).toBe(`/api/v1/projects/${orders[0].project_id}/checks/suggest`);
    expect(w.get('[data-test="checks-banner"]').text()).toContain("Suggesting checks");
    useChecks().stop();
    w.unmount();
  });

  it("stays hidden when checks ran", async () => {
    const w = await renderOrder(["verification_passed"]);
    expect(w.find('[data-test="checks-banner"]').exists()).toBe(false);
    w.unmount();
  });
});
