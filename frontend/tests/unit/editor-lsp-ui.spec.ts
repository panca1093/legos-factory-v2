import { mount, flushPromises } from "@vue/test-utils";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import ProblemsCard from "../../src/features/editor/ProblemsCard.vue";
import ServerCard from "../../src/features/editor/ServerCard.vue";
import ServersPanel from "../../src/features/editor/ServersPanel.vue";
import { lspState, servers } from "../../src/features/editor/lsp";

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status });
let calls: { method: string; path: string; body: unknown }[];
const list = (state: string, extra = {}) => [
  {
    id: "gopls",
    languages: ["go"],
    state,
    version: "v0.16.0",
    path: "/opt/gopls",
    install: "go install golang.org/x/tools/gopls@latest",
    ...extra,
  },
];

beforeEach(() => {
  calls = [];
  servers.value = [];
  for (const k of Object.keys(lspState)) delete lspState[k];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, path, body });
      if (method === "PUT") return json({ ...list("found")[0], path: body.path });
      return json(list("not_found"));
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

const stubs = { global: { stubs: { RouterLink: true } } };

it("ProblemsCard groups by file and emits open at line", async () => {
  const d = (line: number, message: string, severity: 1 | 2 = 1) => ({
    range: { start: { line, character: 3 }, end: { line, character: 4 } },
    message,
    severity,
  });
  const w = mount(ProblemsCard, {
    props: {
      folderPath: "/work",
      diagnostics: new Map([
        ["file:///work/b.go", [d(4, "second")]],
        ["file:///work/a.go", [d(9, "warn", 2), d(1, "first")]],
      ]),
    },
  });
  const files = w.findAll("[data-test=prob-file]");
  expect(files.map((f) => f.find(".prob-name").text().split(" ")[0])).toEqual([
    "a.go",
    "b.go",
  ]);
  expect(files[0].findAll(".prob-msg").map((m) => m.text())).toEqual([
    "first",
    "warn",
  ]);
  await files[0].find(".prob-row").trigger("click");
  expect(w.emitted("open")![0][0]).toEqual({
    uri: "file:///work/a.go",
    line: 2,
    column: 4,
  });
});

it("ProblemsCard shows an empty state", () => {
  const w = mount(ProblemsCard, { props: { diagnostics: new Map() } });
  expect(w.find("[data-test=no-problems]").exists()).toBe(true);
});

it("ServerCard shows not_found with install command and Check again", async () => {
  const w = mount(ServerCard, { props: { language: "go" }, ...stubs });
  await flushPromises();
  expect(w.find("[data-test=server-state]").attributes("data-state")).toBe(
    "not_found",
  );
  expect(w.find("[data-test=install]").text()).toContain("go install");
  await w.findAll("button").find((b) => b.text() === "Check again")!.trigger("click");
  await flushPromises();
  expect(calls.at(-1)).toMatchObject({
    method: "POST",
    path: "/api/v1/editor/servers/check",
  });
});

it("ServerCard shows running with version and crashed with Restart", async () => {
  servers.value = list("found") as never;
  lspState.gopls = { conn: "running", progress: { title: "Indexing" } };
  const w = mount(ServerCard, { props: { language: "go" }, ...stubs });
  expect(w.find("[data-test=server-state]").text()).toContain("running");
  expect(w.text()).toContain("v0.16.0");
  expect(w.find("[data-test=install]").exists()).toBe(false);
  lspState.gopls.conn = "crashed";
  await flushPromises();
  expect(w.find("[data-test=server-state]").attributes("data-state")).toBe(
    "crashed",
  );
  expect(w.findAll("button").some((b) => b.text() === "Restart")).toBe(true);
});

it("ServerCard shows the takeover state with a reconnect action", async () => {
  servers.value = list("found") as never;
  lspState.gopls = { conn: "taken_over", progress: null };
  const w = mount(ServerCard, { props: { language: "go" }, ...stubs });
  expect(w.find("[data-test=taken-over]").text()).toContain(
    "Opened in another tab",
  );
  expect(w.find("[data-test=taken-over] button").text()).toBe("Use here");
});

it("ServersPanel needs a confirm step before PUT with the full path", async () => {
  const w = mount(ServersPanel);
  await flushPromises();
  expect(w.find("[data-test=row-state]").text()).toContain("not found");
  await w.findAll("button").find((b) => b.text().startsWith("Use a different"))!.trigger("click");
  await w.find("input").setValue("/home/me/bin/gopls");
  await w.find("form").trigger("submit");
  expect(w.find("[data-test=confirm]").text()).toContain(
    "/home/me/bin/gopls version",
  );
  expect(calls.some((c) => c.method === "PUT")).toBe(false);
  await w.find("[data-test=confirm-btn]").trigger("click");
  await flushPromises();
  expect(calls.at(-1)).toMatchObject({
    method: "PUT",
    path: "/api/v1/editor/servers/gopls",
    body: { path: "/home/me/bin/gopls", confirm: true },
  });
  expect(w.find("[data-test=row-state]").text()).toContain("found");
});

it("ServersPanel rejects relative paths", async () => {
  const w = mount(ServersPanel);
  await flushPromises();
  await w.findAll("button").find((b) => b.text().startsWith("Use a different"))!.trigger("click");
  await w.find("input").setValue("gopls");
  await w.find("form").trigger("submit");
  expect(w.find("[data-test=confirm]").exists()).toBe(false);
  expect(w.find("[role=alert]").text()).toContain("absolute path");
});
