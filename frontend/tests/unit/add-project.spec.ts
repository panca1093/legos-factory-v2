import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import AddProjectDialog from "../../src/features/factory/work-orders/AddProjectDialog.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import { projects, user } from "../fixtures/workspace";
import type { LocalGh, LocalInspect } from "../../src/types/api";
type Handler = (
  url: string,
  init?: RequestInit,
) => Response | Promise<Response>;
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status });
const err = (status: number, code: string) =>
  json({ error: { code, message: code } }, status);
const repo = (over: Partial<LocalInspect> = {}): LocalInspect => ({
  path: "/code/app",
  name: "app",
  is_git: true,
  github_repo: "acme/app",
  default_branch: "main",
  language: "Go",
  repos: [],
  ...over,
});
const gh: LocalGh = { installed: true, authenticated: true, account: "panca" };
let routes: Record<string, Handler>;
let calls: string[];
beforeEach(() => {
  setActivePinia(createPinia());
  const f = useFactory();
  f.projects = [];
  vi.spyOn(f, "refreshConnections").mockResolvedValue();
  vi.spyOn(f, "refreshProjects").mockResolvedValue();
  useSession().user = user;
  calls = [];
  routes = { "/api/v1/local/gh": () => json(gh) };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const path = input.split("?")[0];
      calls.push(`${init?.method ?? "GET"} ${path}`);
      const h = routes[path];
      return h ? h(input, init) : err(404, "NOT_FOUND");
    }),
  );
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.body.innerHTML = "";
});
async function render() {
  const w = mount(AddProjectDialog, {
    props: { open: true },
    attachTo: document.body,
  });
  await flushPromises();
  return w;
}
const body = () => document.body.textContent ?? "";
const click = async (label: string) => {
  const b = [...document.body.querySelectorAll("button")].find((x) =>
    x.textContent?.includes(label),
  );
  if (!b) throw new Error(`no button ${label}`);
  b.click();
  await flushPromises();
};
const set = async (sel: string, v: string) => {
  const el = document.querySelector(sel) as HTMLInputElement;
  el.value = v;
  el.dispatchEvent(new Event("input", { bubbles: true }));
  await flushPromises();
};
const browse = async (inspect: LocalInspect, path = "/code/app") => {
  routes["/api/v1/local/pick-folder"] = () => json({ path });
  routes["/api/v1/local/inspect"] = () => json(inspect);
  await click("Browse folder");
};
it("shows the picker home when the local API is available", async () => {
  const w = await render();
  expect(body()).toContain("This computer");
  expect(body()).toContain("Browse folder");
  expect(body()).toContain("Clone from URL");
  expect(body()).toContain("Create new project");
  w.unmount();
});
it("falls back to the path-entry form when local APIs are NOT_AVAILABLE", async () => {
  routes["/api/v1/local/gh"] = () => err(404, "NOT_AVAILABLE");
  const w = await render();
  expect(document.querySelector("#prj-repo")).toBeTruthy();
  expect(body()).not.toContain("Browse folder");
  const create = vi
    .spyOn(useFactory(), "createProject")
    .mockResolvedValue(projects[0]);
  await set("#prj-name", "X");
  await set("#prj-repo", "/r");
  await click("Create project");
  expect(create).toHaveBeenCalledWith({ name: "X", repo_url: "/r" });
  expect(w.emitted("created")?.[0]).toEqual([projects[0].id]);
  w.unmount();
});
it("picks a git repo and shows the gh publishing line, then adds it", async () => {
  const w = await render();
  await browse(repo());
  expect(body()).toContain("acme/app");
  expect(body()).toContain("via your gh login");
  expect(body()).toContain("panca");
  const create = vi
    .spyOn(useFactory(), "createProject")
    .mockResolvedValue(projects[0]);
  await click("Add project");
  expect(create).toHaveBeenCalledWith({ name: "app", repo_url: "/code/app" });
  expect(w.emitted("created")?.[0]).toEqual([projects[0].id]);
  w.unmount();
});
it("returns home without inspecting when the picker is cancelled", async () => {
  const w = await render();
  routes["/api/v1/local/pick-folder"] = () => json({ cancelled: true });
  await click("Browse folder");
  expect(body()).toContain("Browse folder");
  expect(calls.some((c) => c.includes("inspect"))).toBe(false);
  w.unmount();
});
it("aborts the pending picker request on Cancel", async () => {
  const w = await render();
  routes["/api/v1/local/pick-folder"] = (_u, init) =>
    new Promise((_res, rej) =>
      init!.signal!.addEventListener("abort", () =>
        rej(new DOMException("aborted", "AbortError")),
      ),
    );
  void click("Browse folder");
  await flushPromises();
  expect(body()).toContain("Choose a folder in the window that opened");
  await click("Cancel");
  expect(body()).toContain("Browse folder");
  expect(body()).not.toContain("Choose a folder");
  w.unmount();
});
it("asks for a pasted path when the picker is unsupported (501)", async () => {
  const w = await render();
  routes["/api/v1/local/pick-folder"] = () => err(501, "NOT_SUPPORTED");
  await click("Browse folder");
  expect(document.querySelector("#ap-path")).toBeTruthy();
  routes["/api/v1/local/inspect"] = () => json(repo());
  await set("#ap-path", "/code/app");
  await click("Continue");
  expect(body()).toContain("acme/app");
  w.unmount();
});
it("lists child repos checked by default and adds only the selected ones", async () => {
  const w = await render();
  const a = repo({ path: "/code/a", name: "a" });
  const b = repo({ path: "/code/b", name: "b", github_repo: "" });
  await browse(
    repo({ is_git: false, name: "code", path: "/code", repos: [a, b] }),
    "/code",
  );
  expect(body()).toContain("Add 2 projects");
  await click("None");
  expect(body()).toContain("Add 0 projects");
  await click("Select all");
  document
    .querySelectorAll<HTMLInputElement>('.ap-repo input[type="checkbox"]')[1]
    .click();
  await flushPromises();
  const create = vi
    .spyOn(useFactory(), "createProject")
    .mockResolvedValue(projects[0]);
  await click("Add 1 project");
  expect(create).toHaveBeenCalledTimes(1);
  expect(create).toHaveBeenCalledWith(
    { name: "a", repo_url: "/code/a" },
    { refresh: false },
  );
  w.unmount();
});
it("explains a plain folder and offers no add button", async () => {
  const w = await render();
  await browse(repo({ is_git: false, github_repo: "", repos: [] }));
  expect(body()).toContain("is not a Git repository");
  expect(body()).not.toContain("Add project");
  w.unmount();
});
it("blocks a path that is already a project", async () => {
  useFactory().projects = [{ ...projects[0], repo_url: "/code/app/" }];
  const w = await render();
  await browse(repo());
  expect(body()).toContain("Already added");
  const add = [...document.querySelectorAll("button")].find(
    (x) => x.textContent?.trim() === "Add project",
  ) as HTMLButtonElement;
  expect(add.disabled).toBe(true);
  w.unmount();
});
it("warns with a copyable fix when gh is not signed in", async () => {
  routes["/api/v1/local/gh"] = () =>
    json({ installed: true, authenticated: false, account: "" });
  const w = await render();
  await browse(repo());
  expect(body()).toContain("not signed in");
  expect(document.querySelector(".ap-code")?.textContent).toContain(
    "gh auth login",
  );
  w.unmount();
});
it("says local only when the repo has no remote", async () => {
  const w = await render();
  await browse(repo({ github_repo: "" }));
  expect(body()).toContain("Local only — no remote, pull requests are off");
  w.unmount();
});
it("clones then lands on the confirm step", async () => {
  const w = await render();
  await click("Clone from URL");
  await set("#ap-url", "https://github.com/acme/app.git");
  await set("#ap-parent", "/code");
  let sent = "";
  routes["/api/v1/local/clone"] = (_u, init) => {
    sent = String(init?.body);
    return json(repo());
  };
  await click("Clone");
  expect(JSON.parse(sent)).toEqual({
    url: "https://github.com/acme/app.git",
    parent_dir: "/code",
  });
  expect(body()).toContain("acme/app");
  w.unmount();
});
it("creates a new project via init", async () => {
  const w = await render();
  await click("Create new project");
  await set("#ap-new-name", "fresh");
  await set("#ap-new-parent", "/code");
  routes["/api/v1/local/init"] = () =>
    json(repo({ name: "fresh", path: "/code/fresh", github_repo: "" }));
  await click("Create");
  expect(body()).toContain("/code/fresh");
  expect(body()).toContain("Local only");
  w.unmount();
});
const submitForm = async () => {
  document
    .querySelector("#project-form")!
    .dispatchEvent(new Event("submit", { cancelable: true }));
  await flushPromises();
};
it("wires every form step to a submit button so Enter is the primary action", async () => {
  const w = await render();
  await click("Clone from URL");
  const primary = () =>
    [
      ...document.querySelectorAll<HTMLButtonElement>(".modal-footer button"),
    ].at(-1)!;
  expect(primary().type).toBe("submit");
  expect(primary().getAttribute("form")).toBe("project-form");
  await set("#ap-url", "https://github.com/acme/app.git");
  await set("#ap-parent", "/code");
  routes["/api/v1/local/clone"] = () => json(repo());
  await submitForm();
  expect(body()).toContain("acme/app");
  const create = vi
    .spyOn(useFactory(), "createProject")
    .mockResolvedValue(projects[0]);
  expect(primary().getAttribute("form")).toBe("project-form");
  await submitForm();
  expect(create).toHaveBeenCalledWith({ name: "app", repo_url: "/code/app" });
  w.unmount();
});
it("keeps the failed repos selected when adding several fails midway, and refreshes once", async () => {
  const w = await render();
  const a = repo({ path: "/code/a", name: "a" });
  const b = repo({ path: "/code/b", name: "b" });
  await browse(
    repo({ is_git: false, name: "code", path: "/code", repos: [a, b] }),
    "/code",
  );
  const f = useFactory();
  const create = vi
    .spyOn(f, "createProject")
    .mockResolvedValueOnce(projects[0])
    .mockRejectedValueOnce(new Error("disk full"));
  await click("Add 2 projects");
  expect(create).toHaveBeenCalledTimes(2);
  expect(body()).toContain("disk full");
  expect(body()).toContain("Add 1 project");
  expect(w.emitted("created")).toBeUndefined();
  expect(f.refreshProjects).toHaveBeenCalledTimes(1);
  w.unmount();
});
it("reports a bind failure, then Close finishes with the created project", async () => {
  const f = useFactory();
  f.connections = [
    {
      id: "c1",
      name: "Atlas",
      provider: "github",
      scope: "shared",
      status: "active",
    },
  ] as never;
  const w = await render();
  await browse(repo());
  const select = document.querySelector("#prj-conn") as HTMLSelectElement;
  select.value = "c1";
  select.dispatchEvent(new Event("change", { bubbles: true }));
  await flushPromises();
  expect(body()).toContain("via connection Atlas");
  expect(body()).not.toContain("via your gh login");
  await click("Add project");
  expect(body()).toContain("Remote repository must look like owner/repo");
  await set("#prj-remote", "acme/app");
  vi.spyOn(f, "createProject").mockResolvedValue(projects[0]);
  vi.spyOn(f, "bindProject").mockRejectedValue(new Error("bad token"));
  await click("Add project");
  expect(body()).toContain("Project created without a repository connection");
  expect(w.emitted("created")).toBeUndefined();
  await click("Close");
  expect(w.emitted("created")?.[0]).toEqual([projects[0].id]);
  w.unmount();
});
it("shows an inspect error on the current step instead of the paste form", async () => {
  const w = await render();
  routes["/api/v1/local/pick-folder"] = () => json({ path: "/root" });
  routes["/api/v1/local/inspect"] = () => err(403, "PERMISSION_DENIED");
  await click("Browse folder");
  expect(body()).toContain("PERMISSION_DENIED");
  expect(body()).toContain("Browse folder");
  expect(document.querySelector("#ap-path")).toBeNull();
  w.unmount();
});
it("ignores a picker result that lands after Cancel", async () => {
  const w = await render();
  let release!: () => void;
  routes["/api/v1/local/pick-folder"] = () =>
    new Promise((res) => {
      release = () => res(json({ path: "/code/app" }));
    });
  routes["/api/v1/local/inspect"] = () => json(repo());
  void click("Browse folder");
  await flushPromises();
  await click("Cancel");
  release();
  await flushPromises();
  expect(body()).toContain("Browse folder");
  expect(calls.some((c) => c.includes("inspect"))).toBe(false);
  w.unmount();
});
it("lets a slow clone be cancelled", async () => {
  const w = await render();
  await click("Clone from URL");
  await set("#ap-url", "https://github.com/acme/app.git");
  await set("#ap-parent", "/code");
  let signal!: AbortSignal;
  routes["/api/v1/local/clone"] = (_u, init) => {
    signal = init!.signal!;
    return new Promise((_r, rej) =>
      signal.addEventListener("abort", () =>
        rej(new DOMException("aborted", "AbortError")),
      ),
    );
  };
  void click("Clone");
  await flushPromises();
  expect(body()).toContain("Working on it");
  await click("Cancel");
  expect(signal.aborted).toBe(true);
  expect(document.querySelector("#ap-url")).toBeTruthy();
  w.unmount();
});
const pending = () => {
  routes["/api/v1/local/pick-folder"] = (_u, init) =>
    new Promise((_res, rej) =>
      init!.signal!.addEventListener("abort", () =>
        rej(new DOMException("aborted", "AbortError")),
      ),
    );
  routes["/api/v1/local/pick-folder/cancel"] = () =>
    new Response(null, { status: 204 });
};
it("tells the backend to close the native chooser on Cancel and refocuses Browse", async () => {
  const w = await render();
  pending();
  void click("Browse folder");
  await flushPromises();
  await click("Cancel");
  await flushPromises();
  expect(calls).toContain("POST /api/v1/local/pick-folder/cancel");
  expect(document.activeElement?.textContent).toContain("Browse folder");
  w.unmount();
});
it("cancels a pending pick when the dialog closes, ignoring a 404", async () => {
  const w = await render();
  pending();
  delete routes["/api/v1/local/pick-folder/cancel"];
  void click("Browse folder");
  await flushPromises();
  await w.setProps({ open: false });
  await flushPromises();
  expect(calls).toContain("POST /api/v1/local/pick-folder/cancel");
  w.unmount();
});
it("does not call cancel when no pick is in flight", async () => {
  const w = await render();
  await w.setProps({ open: false });
  await flushPromises();
  expect(calls.some((c) => c.includes("pick-folder/cancel"))).toBe(false);
  w.unmount();
});
