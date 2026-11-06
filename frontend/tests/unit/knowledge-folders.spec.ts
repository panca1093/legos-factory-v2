import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import KnowledgeView from "../../src/features/knowledge/KnowledgeView.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import type { KBDoc, KBDocDetail } from "../../src/types/api";

vi.mock("vue-router", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useRoute: () => ({ query: {} }),
}));

const mk = (over: Partial<KBDoc>): KBDoc => ({
  id: "factory:prj_1:x.md",
  source: "factory",
  project_id: "prj_1",
  path: "x.md",
  commit_sha: "0123456789abcdef",
  content_hash: "h",
  type: "Note",
  status: "stable",
  trust: "human",
  title: "X",
  description: "",
  meta: {},
  indexed_at: 100,
  ...over,
});
const brief = mk({
  id: "factory:prj_1:brief/b.md",
  type: "Brief",
  title: "Brief doc",
  read_only: true,
  meta: { origin: "work_order" },
  indexed_at: 500,
});
const spec = mk({
  id: "factory:prj_1:spec/s.md",
  type: "Spec",
  title: "Spec doc",
  read_only: true,
  meta: { origin: "work_order" },
  indexed_at: 400,
});
const dag = mk({
  id: "factory:prj_1:task/d.md",
  type: "Task DAG",
  title: "Dag doc",
  read_only: true,
  meta: { origin: "work_order" },
  indexed_at: 300,
});
const summary = mk({
  id: "factory:prj_1:task/s.md",
  type: "Task Summary",
  title: "Summary doc",
  read_only: true,
  meta: { origin: "chat" },
  indexed_at: 350,
});
const note = mk({
  id: "factory:prj_1:notes/n.md",
  title: "Note doc",
  meta: { origin: "manual" },
  indexed_at: 200,
});
const repo = mk({
  id: "repo:prj_1:docs/r.md",
  source: "repo",
  type: "Guide",
  title: "Repo doc",
  indexed_at: 50,
});
const wide = mk({
  id: "factory::playbooks/p.md",
  project_id: undefined,
  type: "Playbook",
  title: "Wide doc",
  indexed_at: 10,
});
const draft = mk({
  id: "factory:prj_1:drafts/d.md",
  type: "Reviewer Routing",
  status: "draft",
  title: "Draft doc",
  indexed_at: 600,
});
const old = mk({
  id: "factory:prj_1:notes/o.md",
  title: "Old doc",
  status: "deprecated",
  indexed_at: 20,
});
const untyped = mk({
  id: "repo:prj_1:x.md",
  source: "repo",
  type: "",
  title: "Untyped doc",
  indexed_at: 40,
});
const wideDraft = mk({
  id: "factory::drafts/w.md",
  project_id: undefined,
  type: "Playbook",
  status: "draft",
  title: "Wide draft",
  indexed_at: 700,
});
let served: KBDoc[];
const all = [
  brief,
  spec,
  dag,
  summary,
  note,
  repo,
  wide,
  draft,
  old,
  untyped,
  wideDraft,
];

beforeEach(() => {
  served = all;
  setActivePinia(createPinia());
  const f = useFactory();
  f.projects = [
    {
      id: "prj_2",
      name: "Empty",
      description: "",
      repo_url: "",
      language: "go",
      created_at: "",
    },
    {
      id: "prj_1",
      name: "Greeter",
      description: "",
      repo_url: "",
      language: "go",
      created_at: "",
    },
  ];
  vi.spyOn(f, "refreshProjects").mockResolvedValue();
  useSession().user = { id: "u", name: "U", email: "u@x", role: "ENGINEER" };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      const u = new URL(url, "http://x");
      if (u.pathname === "/api/v1/kb/docs")
        return new Response(JSON.stringify({ docs: served }));
      const doc = all.find((d) => d.id === u.searchParams.get("id"))!;
      const detail: KBDocDetail = {
        doc,
        frontmatter: {},
        body: "Body.",
        links: [],
        backlinks: [],
        editable: doc.source === "factory",
        default_available: false,
      };
      return new Response(JSON.stringify(detail));
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

async function render() {
  const w = mount(KnowledgeView, { attachTo: document.body });
  await flushPromises();
  return w;
}
const q = (t: string) => document.body.querySelector(`[data-test="${t}"]`);
const titles = (w: Awaited<ReturnType<typeof render>>) =>
  w.findAll('[data-test="kb-doc"]').map((r) => r.find(".t").text());
const folder = (k: string) => q(`kb-folder-${k}`) as HTMLButtonElement;
const inFolder = (w: Awaited<ReturnType<typeof render>>, k: string) =>
  w
    .find(`#kb-folder-${k}`)
    .findAll('[data-test="kb-doc"]')
    .map((r) => r.find(".t").text());
const book = (w: Awaited<ReturnType<typeof render>>, name: string) =>
  w.findAll(".nb-book").find((b) => b.text().startsWith(name))!;
const open = async (w: Awaited<ReturnType<typeof render>>, name: string) => {
  await book(w, name).trigger("click");
  await flushPromises();
};
const row = (w: Awaited<ReturnType<typeof render>>, t: string) =>
  w.findAll('[data-test="kb-doc"]').find((r) => r.text().includes(t))!;

it("has no status dropdown or drafts notebook, and loads every status once", async () => {
  const w = await render();
  expect(q("kb-status")).toBeNull();
  expect(q("drafts-shortcut")).toBeNull();
  expect(w.find(".nb-books").text()).not.toContain("Drafts to review");
  expect(w.find("select").exists()).toBe(false);
  const urls = vi.mocked(fetch).mock.calls.map((c) => String(c[0]));
  expect(urls.filter((u) => u.includes("/kb/docs"))).toEqual([
    "/api/v1/kb/docs?status=all",
  ]);
  w.unmount();
});

it("lists All notes, Factory-wide and every project, even with zero documents", async () => {
  const w = await render();
  expect(w.findAll(".nb-book").map((b) => b.text())).toEqual([
    "All notes211",
    "Factory-wide12",
    "Empty0",
    "Greeter19",
  ]);
  expect(
    w.findAll('[data-test="kb-draft-count"]').map((b) => b.text()),
  ).toEqual(["2", "1", "1"]);
  w.unmount();
});

it("shows project folders with counts in the documented order", async () => {
  const w = await render();
  await open(w, "Greeter");
  expect(w.findAll(".nb-folder").map((f) => f.text())).toEqual([
    "Brief1",
    "Spec1",
    "Task2",
    "Notes2",
    "Repository2",
    "Needs review1",
  ]);
  expect(inFolder(w, "brief")).toEqual(["Brief doc"]);
  expect(inFolder(w, "task")).toEqual(["Summary doc", "Dag doc"]);
  expect(inFolder(w, "notes")).toEqual(["Note doc", "Old doc"]);
  expect(inFolder(w, "review")).toEqual(["Draft doc"]);
  expect(inFolder(w, "repo")).toEqual(["Repo doc", "Untyped doc"]);
  w.unmount();
});

it("expands folders with documents, collapses Repository, and toggles aria state", async () => {
  const w = await render();
  await open(w, "Greeter");
  expect(folder("brief").getAttribute("aria-expanded")).toBe("true");
  expect(folder("repo").getAttribute("aria-expanded")).toBe("false");
  expect(folder("repo").getAttribute("aria-controls")).toBe("kb-folder-repo");
  expect(w.find("#kb-folder-repo").isVisible()).toBe(false);
  folder("repo").click();
  await flushPromises();
  expect(folder("repo").getAttribute("aria-expanded")).toBe("true");
  expect(w.find("#kb-folder-repo").isVisible()).toBe(true);
  folder("brief").click();
  await flushPromises();
  expect(folder("brief").getAttribute("aria-expanded")).toBe("false");
  expect(w.find("#kb-folder-brief").isVisible()).toBe(false);
  w.unmount();
});

it("shows empty folders muted and not expandable", async () => {
  const w = await render();
  await open(w, "Empty");
  const folders = w.findAll(".nb-folder");
  expect(folders).toHaveLength(6);
  for (const f of folders) {
    expect(f.attributes("disabled")).toBeDefined();
    expect(f.attributes("aria-expanded")).toBe("false");
  }
  expect(folders[0].attributes("aria-label")).toBe("Brief, 0 documents");
  expect(titles(w)).toEqual([]);
  w.unmount();
});

it("groups Factory-wide into Playbooks and Needs review", async () => {
  const w = await render();
  await open(w, "Factory-wide");
  expect(w.findAll(".nb-folder").map((f) => f.text())).toEqual([
    "Playbooks1",
    "Needs review1",
  ]);
  expect(inFolder(w, "playbooks")).toEqual(["Wide doc"]);
  expect(inFolder(w, "review")).toEqual(["Wide draft"]);
  w.unmount();
});

it("reviews a draft from its Needs review folder with Approve and Discard", async () => {
  const w = await render();
  await open(w, "Greeter");
  await row(w, "Draft doc").trigger("click");
  await flushPromises();
  expect(q("kb-approve")).not.toBeNull();
  expect(q("kb-discard")).not.toBeNull();
  w.unmount();
});

it("shows All notes flat, newest first, with draft and deprecated badges", async () => {
  const w = await render();
  expect(w.find(".nb-folder").exists()).toBe(false);
  expect(titles(w)).toHaveLength(11);
  expect(titles(w)[0]).toBe("Wide draft");
  expect(row(w, "Draft doc").find(".nb-pill").text()).toBe("draft");
  expect(row(w, "Old doc").find(".nb-pill").text()).toBe("deprecated");
  expect(row(w, "Old doc").classes()).toContain("dep");
  expect(row(w, "Brief doc").classes()).not.toContain("dep");
  w.unmount();
});

it("shows a flat list in server order while searching", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const w = await render();
  await open(w, "Greeter");
  expect(w.find(".nb-folder").exists()).toBe(true);
  await w.find("input[type=search]").setValue("doc");
  await vi.advanceTimersByTimeAsync(300);
  await flushPromises();
  vi.useRealTimers();
  expect(w.find(".nb-folder").exists()).toBe(false);
  expect(titles(w)).toHaveLength(9);
  expect(titles(w)[0]).toBe("Brief doc");
  w.unmount();
});

it("labels cards with type or source, never untyped", async () => {
  const w = await render();
  expect(row(w, "Untyped doc").find(".tag").text()).toBe("repo");
  expect(row(w, "Brief doc").find(".tag").text()).toBe("Brief");
  expect(w.text()).not.toContain("untyped");
  w.unmount();
});

it("labels each card with its origin when present", async () => {
  const w = await render();
  await open(w, "Greeter");
  const origin = (t: string) => row(w, t).find('[data-test="kb-origin"]');
  expect(origin("Brief doc").text()).toBe("Work order");
  expect(origin("Summary doc").text()).toBe("Chat");
  expect(origin("Note doc").text()).toBe("Manual");
  expect(origin("Repo doc").exists()).toBe(false);
  w.unmount();
});

it("opens a work-order record read-only, and an editable note with Edit", async () => {
  const w = await render();
  await open(w, "Greeter");
  await row(w, "Brief doc").trigger("click");
  await flushPromises();
  expect(q("record-readonly")?.textContent).toContain("read-only");
  expect(q("kb-edit")).toBeNull();
  expect(q("kb-delete")).toBeNull();
  expect(q("kb-approve")).toBeNull();
  await row(w, "Note doc").trigger("click");
  await flushPromises();
  expect(q("record-readonly")).toBeNull();
  expect(q("kb-edit")).not.toBeNull();
  w.unmount();
});

it("labels folder buttons with a count a screen reader can read", async () => {
  const w = await render();
  await open(w, "Greeter");
  expect(folder("brief").getAttribute("aria-label")).toBe("Brief, 1 document");
  expect(folder("task").getAttribute("aria-label")).toBe("Task, 2 documents");
  w.unmount();
});

it("hints when the list hit the 500 cap", async () => {
  const w = await render();
  expect(q("kb-capped")).toBeNull();
  w.unmount();
  served = Array.from({ length: 500 }, (_, i) =>
    mk({ id: `factory:prj_1:n${i}.md`, title: `N${i}`, indexed_at: i }),
  );
  const capped = await render();
  expect(q("kb-capped")?.textContent).toContain("Showing the first 500");
  capped.unmount();
});

it("closes a folder whose last document disappeared", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const w = await render();
  await open(w, "Greeter");
  folder("repo").click();
  await flushPromises();
  expect(folder("repo").getAttribute("aria-expanded")).toBe("true");
  served = all.filter((d) => d.source !== "repo");
  const input = w.find("input[type=search]");
  for (const v of ["x", ""]) {
    await input.setValue(v);
    await vi.advanceTimersByTimeAsync(300);
    await flushPromises();
  }
  vi.useRealTimers();
  expect(folder("repo").disabled).toBe(true);
  expect(folder("repo").getAttribute("aria-expanded")).toBe("false");
  w.unmount();
});

it("moves with j and k over expanded folders only", async () => {
  const w = await render();
  await open(w, "Greeter");
  folder("repo").click();
  folder("brief").click();
  await flushPromises();
  const sel = () => w.find(".nb-item.on .t").text();
  await row(w, "Spec doc").trigger("click");
  await flushPromises();
  const key = async (k: string) => {
    document.body.dispatchEvent(
      new KeyboardEvent("keydown", { key: k, bubbles: true }),
    );
    await flushPromises();
  };
  await key("k");
  expect(sel()).toBe("Spec doc");
  await key("j");
  expect(sel()).toBe("Summary doc");
  folder("repo").click();
  await flushPromises();
  for (let i = 0; i < 8; i++) await key("j");
  expect(sel()).toBe("Draft doc");
  w.unmount();
});

it("keeps the open document when its folder is collapsed", async () => {
  const w = await render();
  await open(w, "Greeter");
  await row(w, "Spec doc").trigger("click");
  await flushPromises();
  folder("spec").click();
  await flushPromises();
  expect(w.find("#kb-folder-spec").isVisible()).toBe(false);
  expect(document.body.querySelector(".nb-h")!.textContent).toBe("Spec doc");
  w.unmount();
});

it("resets folder state when switching notebooks", async () => {
  const w = await render();
  await open(w, "Greeter");
  folder("brief").click();
  folder("repo").click();
  await flushPromises();
  await open(w, "Factory-wide");
  await open(w, "Greeter");
  expect(folder("brief").getAttribute("aria-expanded")).toBe("true");
  expect(folder("repo").getAttribute("aria-expanded")).toBe("false");
  w.unmount();
});

it("labels a notebook for an unknown project by its id", async () => {
  served = [mk({ id: "factory:prj_9:a.md", project_id: "prj_9", title: "A" })];
  const w = await render();
  expect(w.findAll(".nb-book").map((b) => b.text())).toContain("prj_91");
  w.unmount();
});
