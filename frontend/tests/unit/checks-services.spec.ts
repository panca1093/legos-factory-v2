import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import ProjectChecks from "../../src/features/factory/ProjectChecks.vue";
import WorkOrderView from "../../src/features/factory/work-orders/WorkOrderView.vue";
import { useFactory } from "../../src/stores/factory";
import { useChecks } from "../../src/stores/checks";
import { useSession } from "../../src/stores/session";
import { orders, projects, tasks, user } from "../fixtures/workspace";
import type { CheckProposal, ChecksStatus } from "../../src/types/api";

const migrate = { name: "migrate", dir: "api", command: "make migrate-up", per_run: true };
const proposal = (results: CheckProposal["results"] = []): CheckProposal => ({
  profile: {
    version: 2,
    services: { compose: "docker-compose.yml", names: ["postgres"], ports: { postgres: 5432 } },
    setup: [{ name: "web deps", dir: "web", command: "npm ci" }, structuredClone(migrate)],
    checks: [{ name: "api test", dir: "api", command: "go test ./..." }],
  },
  results,
  base_sha: "abc1234def",
  model: "m",
  created_at: "2026-11-17T10:00:00Z",
});
const idle: ChecksStatus = { profile: null, proposal: null, run: { status: "idle" }, stale: false };

let status: ChecksStatus;
const fetchMock = vi.fn();
const calls = (method: string) => fetchMock.mock.calls.filter((c) => (c[1]?.method ?? "GET") === method);

beforeEach(() => {
  setActivePinia(createPinia());
  status = structuredClone(idle);
  fetchMock.mockReset();
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    if ((init?.method ?? "GET") === "POST" && String(url).endsWith("/checks/stop"))
      return new Response(null, { status: 204 });
    return new Response(JSON.stringify(status));
  });
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

const mountIt = async () => {
  useSession().user = user;
  const w = mount(ProjectChecks, { props: { projectId: "p1" }, attachTo: document.body });
  await flushPromises();
  return w;
};

describe("per-run setup steps", () => {
  it("shows an editable per-run step in the proposal and sends the edit on approve", async () => {
    status = { ...idle, proposal: proposal(), run: { status: "ready" } };
    const w = await mountIt();
    const row = w.get('[data-test="per-run-migrate"]');
    expect(row.text()).toContain("per run");
    await row.get("input").setValue("make migrate-up ENV=test");
    expect(useChecks().draft?.setup?.[1].command).toBe("make migrate-up ENV=test");
  });

  it("lists the approved per-run step", async () => {
    status = { ...idle, profile: proposal().profile, run: { status: "idle" } };
    const w = await mountIt();
    expect(w.get('[data-test="approved"] [data-test="per-run-migrate"]').text()).toContain("per run");
    expect(w.get('[data-test="approved"] [data-test="setup-web deps"]').text()).toContain("npm ci");
  });

  it("shows the services-unavailable text next to the per-run step", async () => {
    const msg = "check services unavailable: docker compose is not available";
    status = {
      ...idle,
      proposal: proposal([
        { name: "api test", kind: "check", command: "go test ./...", outcome: "UNVERIFIED", exit_code: null, output_tail: msg },
      ]),
      run: { status: "ready" },
    };
    const w = await mountIt();
    expect(w.get('[data-test="per-run-migrate"] [data-test="services-unavailable"]').text()).toContain(msg);
    expect(w.findAll('[data-test="services-unavailable"]')).toHaveLength(1);
  });
});

describe("stop check services", () => {
  it("posts to the stop endpoint and confirms", async () => {
    status = { ...idle, profile: proposal().profile, run: { status: "idle" } };
    const w = await mountIt();
    await w.get('[data-test="stop-services"]').trigger("click");
    await flushPromises();
    expect(calls("POST")[0][0]).toBe("/api/v1/projects/p1/checks/stop");
    expect(w.find('[data-test="services-stopped"]').exists()).toBe(true);
  });

  it("is absent for a project without services", async () => {
    status = { ...idle, profile: { checks: [{ name: "vet", command: "go vet ./..." }] }, run: { status: "idle" } };
    const w = await mountIt();
    expect(w.find('[data-test="stop-services"]').exists()).toBe(false);
  });
});

describe("work order fallback banner", () => {
  it("shows when the checks were detected, not approved", async () => {
    useSession().user = user;
    const f = useFactory();
    f.projects = projects;
    vi.spyOn(f, "loadDetail").mockImplementation(async (oid) => {
      f.selectedID = oid;
      f.detail = { work_order: structuredClone(orders[0]), plan: tasks, runs: [] };
      f.events = [{ id: 1, work_order_id: oid, actor: "factory", kind: "checks_fallback", message: "m", created_at: "t" }];
    });
    vi.spyOn(f, "reloadDetail").mockResolvedValue();
    vi.stubGlobal("EventSource", class { addEventListener() {} close() {} });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/projects/:id", component: { render: () => null } },
        { path: "/projects/:id/wo/:wo", component: WorkOrderView },
      ],
    });
    await router.push(`/projects/atlas/wo/${orders[0].id}`);
    await router.isReady();
    const w = mount(WorkOrderView, { global: { plugins: [router] }, attachTo: document.body });
    await flushPromises();
    expect(w.get('[data-test="checks-banner"] [data-test="checks-fallback"]').text()).toContain("not approved");
    useChecks().stop();
    w.unmount();
  });
});
