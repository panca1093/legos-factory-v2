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
const all = [brief, spec, dag, summary, note, repo, wide];

beforeEach(() => {
  setActivePinia(createPinia());
  const f = useFactory();
  f.projects = [
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
        return new Response(JSON.stringify({ docs: all }));
      const doc = all.find((d) => d.id === u.searchParams.get("id"))!;
      const detail: KBDocDetail = {
        doc,
        frontmatter: {},
        body: "Body.",
        links: [],
        backlinks: [],
        editable: !doc.read_only && doc.source === "factory",
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
async function openProject(w: Awaited<ReturnType<typeof render>>) {
  const book = w
    .findAll(".nb-book")
    .find((b) => b.text().startsWith("Greeter"))!;
  await book.trigger("click");
  await flushPromises();
}
const chip = async (k: string) => {
  (q(`kb-chip-${k}`) as HTMLElement).click();
  await flushPromises();
};

it("shows type chips only inside a project notebook", async () => {
  const w = await render();
  expect(q("kb-chip-all")).toBeNull();
  await openProject(w);
  expect(
    ["all", "brief", "spec", "task", "notes"].map((k) => !!q(`kb-chip-${k}`)),
  ).toEqual([true, true, true, true, true]);
  await w
    .findAll(".nb-book")
    .find((b) => b.text().startsWith("Factory-wide"))!
    .trigger("click");
  expect(q("kb-chip-all")).toBeNull();
  w.unmount();
});

it("filters by type, groups Task DAG and Task Summary, keeps repo docs under All", async () => {
  const w = await render();
  await openProject(w);
  expect(titles(w)).toEqual([
    "Brief doc",
    "Spec doc",
    "Summary doc",
    "Dag doc",
    "Note doc",
    "Repo doc",
  ]);
  await chip("task");
  expect(titles(w)).toEqual(["Summary doc", "Dag doc"]);
  await chip("brief");
  expect(titles(w)).toEqual(["Brief doc"]);
  await chip("spec");
  expect(titles(w)).toEqual(["Spec doc"]);
  await chip("notes");
  expect(titles(w)).toEqual(["Note doc"]);
  await chip("all");
  expect(titles(w)).toContain("Repo doc");
  w.unmount();
});

it("labels each card with its origin when present", async () => {
  const w = await render();
  await openProject(w);
  const row = (t: string) =>
    w.findAll('[data-test="kb-doc"]').find((r) => r.text().includes(t))!;
  expect(row("Brief doc").find('[data-test="kb-origin"]').text()).toBe(
    "Work order",
  );
  expect(row("Summary doc").find('[data-test="kb-origin"]').text()).toBe(
    "Chat",
  );
  expect(row("Note doc").find('[data-test="kb-origin"]').text()).toBe("Manual");
  expect(row("Repo doc").find('[data-test="kb-origin"]').exists()).toBe(false);
  expect(row("Brief doc").text()).toContain("Brief");
  w.unmount();
});

it("opens a work-order record read-only, and an editable note with Edit", async () => {
  const w = await render();
  await openProject(w);
  expect(q("record-readonly")?.textContent).toContain("read-only");
  expect(q("kb-edit")).toBeNull();
  expect(q("kb-delete")).toBeNull();
  expect(q("kb-approve")).toBeNull();
  await w
    .findAll('[data-test="kb-doc"]')
    .find((r) => r.text().includes("Note doc"))!
    .trigger("click");
  await flushPromises();
  expect(q("record-readonly")).toBeNull();
  expect(q("kb-edit")).not.toBeNull();
  w.unmount();
});
