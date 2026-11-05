import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import KnowledgeView from "../../src/features/knowledge/KnowledgeView.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import type { KBDoc, KBDocDetail } from "../../src/types/api";

const mk = (over: Partial<KBDoc>): KBDoc => ({
  id: "factory::playbooks/foreman/escalation.md",
  source: "factory",
  path: "playbooks/foreman/escalation.md",
  commit_sha: "0123456789abcdef0123456789abcdef01234567",
  content_hash: "h",
  type: "Playbook",
  status: "stable",
  trust: "human",
  title: "Escalation",
  description: "When the Foreman escalates",
  meta: {},
  indexed_at: 1,
  ...over,
});
const factoryDoc = mk({});
const repoDoc = mk({
  id: "repo:prj_1:docs/money.md",
  source: "repo",
  project_id: "prj_1",
  path: "docs/money.md",
  type: "Spec",
  trust: "unverified",
  title: "Money",
});
const draftDoc = mk({
  id: "factory:prj_1:projects/prj_1/drafts/d1.md",
  project_id: "prj_1",
  status: "draft",
  trust: "machine",
  title: "Learned rule",
});
const backlink = mk({ id: "factory::b.md", title: "Linked from" });

function detailOf(doc: KBDoc, over: Partial<KBDocDetail> = {}): KBDocDetail {
  return {
    doc,
    frontmatter: { type: doc.type, role: "foreman" },
    body: "# Heading\n\nSome paragraph.\n\n```\ncode <b>x</b>\n```",
    links: [repoDoc],
    backlinks: [backlink],
    editable: doc.source === "factory",
    default_available: false,
    ...over,
  };
}

let calls: { url: string; method: string; body?: unknown }[];
let details: Record<string, KBDocDetail>;
let fetchMock: ReturnType<typeof vi.fn>;

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
  calls = [];
  details = {
    [factoryDoc.id]: detailOf(factoryDoc),
    [repoDoc.id]: detailOf(repoDoc),
    [draftDoc.id]: detailOf(draftDoc),
  };
  fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    calls.push({
      url,
      method,
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    const u = new URL(url, "http://x");
    if (u.pathname === "/api/v1/kb/docs") {
      const list =
        u.searchParams.get("status") === "draft"
          ? [draftDoc]
          : [factoryDoc, repoDoc];
      return new Response(JSON.stringify({ docs: list }), { status: 200 });
    }
    if (u.pathname === "/api/v1/kb/doc" && method === "GET")
      return new Response(JSON.stringify(details[u.searchParams.get("id")!]));
    if (u.pathname.endsWith("/discard"))
      return new Response(null, { status: 204 });
    return new Response(JSON.stringify({ doc: factoryDoc }), { status: 200 });
  });
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => vi.unstubAllGlobals());

async function render() {
  const w = mount(KnowledgeView, { attachTo: document.body });
  await flushPromises();
  return w;
}
const body = () => document.body.textContent ?? "";
const btn = (t: string) => document.body.querySelector(`[data-test="${t}"]`);
async function openDoc(w: Awaited<ReturnType<typeof render>>, i: number) {
  await w.findAll('[data-test="kb-doc"]')[i].trigger("click");
  await flushPromises();
}
const click = async (t: string) => {
  (btn(t) as HTMLElement).click();
  await flushPromises();
};

it("lists documents with source, type, status and trust pills", async () => {
  const w = await render();
  expect(calls[0].url).toBe("/api/v1/kb/docs?status=stable");
  const cards = w.findAll('[data-test="kb-doc"]');
  expect(cards).toHaveLength(2);
  expect(cards[0].text()).toContain("Escalation");
  expect(cards[0].text()).toContain("playbooks/foreman/escalation.md");
  expect(cards[0].text()).toContain("factory");
  expect(cards[0].text()).toContain("Playbook");
  expect(cards[0].text()).toContain("stable");
  expect(cards[0].text()).toContain("human");
  expect(cards[1].text()).toContain("Greeter");
  w.unmount();
});

it("builds the query from project, source, type, status and search", async () => {
  const w = await render();
  await w.find("#kb-project").setValue("prj_1");
  await w.find("#kb-source").setValue("repo");
  await w.find("#kb-type").setValue("Spec");
  await w.find("#kb-status").setValue("all");
  await w.find("#kb-search").setValue("  rounding ");
  await w.find('form[role="search"]').trigger("submit");
  await flushPromises();
  expect(calls.at(-1)!.url).toBe(
    "/api/v1/kb/docs?project_id=prj_1&source=repo&type=Spec&status=all&q=rounding",
  );
  w.unmount();
});

it("drafts shortcut shows the count and filters to factory drafts", async () => {
  const w = await render();
  expect(calls.some((c) => c.url.includes("source=factory&status=draft"))).toBe(
    true,
  );
  const s = w.find('[data-test="drafts-shortcut"]');
  expect(s.text()).toContain("(1)");
  await s.trigger("click");
  await flushPromises();
  expect(calls.at(-1)!.url).toBe("/api/v1/kb/docs?source=factory&status=draft");
  expect(w.findAll('[data-test="kb-doc"]')[0].text()).toContain("Learned rule");
  w.unmount();
});

it("reports list failures inline", async () => {
  fetchMock.mockResolvedValueOnce(
    new Response(JSON.stringify({ error: { code: "X", message: "boom" } }), {
      status: 500,
    }),
  );
  const w = await render();
  expect(w.find('[role="alert"]').text()).toContain("boom");
  w.unmount();
});

it("shows body, frontmatter, links, backlinks and commit in the detail", async () => {
  const w = await render();
  await openDoc(w, 0);
  expect(calls.at(-1)!.url).toBe(
    `/api/v1/kb/doc?id=${encodeURIComponent(factoryDoc.id)}`,
  );
  expect(body()).toContain("Heading");
  expect(body()).toContain("Some paragraph.");
  expect(body()).toContain("code <b>x</b>");
  expect(document.body.querySelector("[data-test=kb-rendered] b")).toBeNull();
  expect(btn("kb-frontmatter")!.textContent).toContain("foreman");
  expect(btn("kb-links")!.textContent).toContain("Money");
  expect(btn("kb-backlinks")!.textContent).toContain("Linked from");
  expect(btn("kb-sha")!.textContent).toBe("0123456789");
  w.unmount();
});

it("edits a factory doc and saves parsed frontmatter via PUT", async () => {
  const w = await render();
  await openDoc(w, 0);
  await click("kb-edit");
  const fm = document.body.querySelector<HTMLTextAreaElement>("#kb-fm")!;
  const bd = document.body.querySelector<HTMLTextAreaElement>("#kb-body")!;
  fm.value = '{"type":"Playbook","role":"inspector"}';
  fm.dispatchEvent(new Event("input"));
  bd.value = "New body";
  bd.dispatchEvent(new Event("input"));
  await click("kb-save");
  const put = calls.find((c) => c.method === "PUT")!;
  expect(put.url).toBe(
    `/api/v1/kb/doc?id=${encodeURIComponent(factoryDoc.id)}`,
  );
  expect(put.body).toEqual({
    frontmatter: { type: "Playbook", role: "inspector" },
    body: "New body",
  });
  w.unmount();
});

it("rejects invalid frontmatter JSON without calling the API", async () => {
  const w = await render();
  await openDoc(w, 0);
  await click("kb-edit");
  const fm = document.body.querySelector<HTMLTextAreaElement>("#kb-fm")!;
  fm.value = "{nope";
  fm.dispatchEvent(new Event("input"));
  await click("kb-save");
  expect(body()).toContain("not valid JSON");
  expect(calls.some((c) => c.method === "PUT")).toBe(false);
  w.unmount();
});

it("approves a draft with an optional note", async () => {
  const w = await render();
  await w.find("#kb-status").setValue("draft");
  await flushPromises();
  await openDoc(w, 0);
  const note = document.body.querySelector<HTMLInputElement>("#kb-note")!;
  note.value = "looks right";
  note.dispatchEvent(new Event("input"));
  await click("kb-approve");
  const post = calls.find((c) => c.method === "POST")!;
  expect(post.url).toBe(
    `/api/v1/kb/doc/approve?id=${encodeURIComponent(draftDoc.id)}`,
  );
  expect(post.body).toEqual({ note: "looks right" });
  w.unmount();
});

it("discards a draft only after confirmation", async () => {
  const w = await render();
  await w.find("#kb-status").setValue("draft");
  await flushPromises();
  await openDoc(w, 0);
  await click("kb-discard");
  expect(calls.some((c) => c.url.includes("/discard"))).toBe(false);
  await click("kb-confirm-yes");
  const post = calls.find((c) => c.url.includes("/discard"))!;
  expect(post.method).toBe("POST");
  expect(post.url).toContain(encodeURIComponent(draftDoc.id));
  w.unmount();
});

it("resets a default playbook after confirmation", async () => {
  details[factoryDoc.id] = detailOf(factoryDoc, { default_available: true });
  const w = await render();
  await openDoc(w, 0);
  await click("kb-reset");
  await click("kb-confirm-yes");
  const post = calls.find((c) => c.url.includes("/reset"))!;
  expect(post.method).toBe("POST");
  expect(post.url).toBe(
    `/api/v1/kb/doc/reset?id=${encodeURIComponent(factoryDoc.id)}`,
  );
  w.unmount();
});

it("shows repo docs read-only with no edit controls", async () => {
  const w = await render();
  await openDoc(w, 1);
  expect(btn("repo-readonly")!.textContent).toContain(
    "Changes to repository documents go through the repository.",
  );
  for (const t of ["kb-edit", "kb-approve", "kb-discard", "kb-reset"])
    expect(btn(t)).toBeNull();
  w.unmount();
});

it("hides edit controls from viewers", async () => {
  useSession().user = { id: "u", name: "U", email: "u@x", role: "VIEWER" };
  const w = await render();
  await openDoc(w, 0);
  expect(body()).toContain("Heading");
  for (const t of ["kb-edit", "kb-approve", "kb-discard", "kb-reset"])
    expect(btn(t)).toBeNull();
  w.unmount();
});
