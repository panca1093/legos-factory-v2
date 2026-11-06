import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import KnowledgeView from "../../src/features/knowledge/KnowledgeView.vue";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import { useToast } from "../../src/composables/useToast";
import type { KBDoc, KBDocDetail } from "../../src/types/api";

const push = vi.fn();
vi.mock("vue-router", () => ({
  useRouter: () => ({ push }),
  useRoute: () => ({ query: {} }),
}));

const mk = (over: Partial<KBDoc>): KBDoc => ({
  id: "factory:prj_1:playbooks/escalation.md",
  source: "factory",
  project_id: "prj_1",
  path: "playbooks/escalation.md",
  commit_sha: "0123456789abcdef0123456789abcdef01234567",
  content_hash: "h",
  type: "Playbook",
  status: "stable",
  trust: "human",
  title: "Escalation",
  description: "When the Foreman escalates a rounding problem",
  meta: {},
  indexed_at: 300,
  ...over,
});
const factoryDoc = mk({});
const repoDoc = mk({
  id: "repo:prj_1:docs/money.md",
  source: "repo",
  path: "docs/money.md",
  type: "Spec",
  trust: "unverified",
  title: "Money",
  indexed_at: 200,
});
const draftDoc = mk({
  id: "factory:prj_1:drafts/d1.md",
  status: "draft",
  trust: "machine",
  title: "Learned rule",
  indexed_at: 100,
});
const backlink = mk({ id: "factory::b.md", title: "Linked from" });

function detailOf(doc: KBDoc, over: Partial<KBDocDetail> = {}): KBDocDetail {
  return {
    doc,
    frontmatter: { type: doc.type, role: "foreman" },
    body: "# Heading\n\nSome paragraph.\n\n## Second\n\n```\ncode <b>x</b>\n```",
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

const sseBody = () =>
  new Response(
    [
      'event: user_message\ndata: {"message":{"id":"u1","chat_id":"c1","role":"user","content":"q","created_at":""}}\n\n',
      'event: delta\ndata: {"text":"Hel"}\n\n',
      'event: delta\ndata: {"text":"lo **there**"}\n\n',
      'event: done\ndata: {"message":{"id":"a1","chat_id":"c1","role":"assistant","content":"Hello **there**","created_at":""}}\n\n',
    ].join(""),
    { status: 200, headers: { "Content-Type": "text/event-stream" } },
  );

beforeEach(() => {
  setActivePinia(createPinia());
  sessionStorage.clear();
  push.mockClear();
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
      const st = u.searchParams.get("status");
      const docs =
        st === "draft"
          ? [draftDoc]
          : st === "all"
            ? [factoryDoc, repoDoc, draftDoc]
            : [factoryDoc, repoDoc];
      return new Response(JSON.stringify({ docs }));
    }
    if (u.pathname === "/api/v1/kb/doc" && method === "GET")
      return new Response(JSON.stringify(details[u.searchParams.get("id")!]));
    if (u.pathname === "/api/v1/chats" && method === "POST")
      return new Response(JSON.stringify({ chat: { id: "c1" } }), {
        status: 201,
      });
    if (u.pathname === "/api/v1/chats/c1/messages" && method === "POST")
      return sseBody();
    if (method === "DELETE" || u.pathname.endsWith("/discard"))
      return new Response(null, { status: 204 });
    return new Response(JSON.stringify({ doc: factoryDoc }), { status: 200 });
  });
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

async function render() {
  const w = mount(KnowledgeView, { attachTo: document.body });
  await flushPromises();
  return w;
}
const body = () => document.body.textContent ?? "";
const btn = (t: string) => document.body.querySelector(`[data-test="${t}"]`);
const click = async (t: string) => {
  (btn(t) as HTMLElement).click();
  await flushPromises();
};
const items = (w: Awaited<ReturnType<typeof render>>) =>
  w.findAll('[data-test="kb-doc"]');

it("lists notebooks with counts and opens the newest note in the reader", async () => {
  const w = await render();
  expect(calls[0].url).toBe("/api/v1/kb/docs?status=stable");
  expect(w.find(".nb-books").text()).toContain("All notes2");
  expect(w.find(".nb-books").text()).toContain("Drafts to review1");
  expect(w.find(".nb-books").text()).toContain("Greeter2");
  const rows = items(w);
  expect(rows).toHaveLength(2);
  expect(rows[0].text()).toContain("Escalation");
  expect(rows[0].text()).toContain("Playbook");
  expect(rows[1].text()).toContain("repo");
  expect(calls.at(-1)!.url).toBe(
    `/api/v1/kb/doc?id=${encodeURIComponent(factoryDoc.id)}`,
  );
  expect(body()).toContain("Some paragraph.");
  expect(body()).toContain("code <b>x</b>");
  expect(document.body.querySelector("[data-test=kb-rendered] b")).toBeNull();
  expect(btn("kb-frontmatter")!.textContent).toContain("foreman");
  expect(btn("kb-links")!.textContent).toContain("Money");
  expect(btn("kb-links")!.textContent).toContain("Linked from");
  expect(btn("kb-sha")!.textContent).toBe("0123456789");
  expect(document.body.querySelectorAll(".toc a")).toHaveLength(2);
  w.unmount();
});

it("searches server-side after a pause and highlights the term", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const w = await render();
  await w.find("input[type=search]").setValue(" rounding ");
  expect(calls.some((c) => c.url.includes("q="))).toBe(false);
  await vi.advanceTimersByTimeAsync(300);
  await flushPromises();
  expect(
    calls.some((c) => c.url === "/api/v1/kb/docs?status=stable&q=rounding"),
  ).toBe(true);
  expect(items(w)[0].html()).toContain("<mark>rounding</mark>");
  w.unmount();
});

it("moves between notes with j and k but not while typing", async () => {
  const w = await render();
  const sel = () => w.find(".nb-item.on").text();
  expect(sel()).toContain("Escalation");
  document.body.dispatchEvent(
    new KeyboardEvent("keydown", { key: "j", bubbles: true }),
  );
  await flushPromises();
  expect(sel()).toContain("Money");
  const input = w.find("input[type=search]").element;
  input.dispatchEvent(
    new KeyboardEvent("keydown", { key: "k", bubbles: true }),
  );
  await flushPromises();
  expect(sel()).toContain("Money");
  document.body.dispatchEvent(
    new KeyboardEvent("keydown", { key: "k", bubbles: true }),
  );
  await flushPromises();
  expect(sel()).toContain("Escalation");
  w.unmount();
});

it("edits a factory note and saves parsed frontmatter via PUT", async () => {
  const w = await render();
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
  await click("kb-edit");
  const fm = document.body.querySelector<HTMLTextAreaElement>("#kb-fm")!;
  fm.value = "{nope";
  fm.dispatchEvent(new Event("input"));
  await click("kb-save");
  expect(body()).toContain("not valid JSON");
  expect(calls.some((c) => c.method === "PUT")).toBe(false);
  w.unmount();
});

async function openDrafts(w: Awaited<ReturnType<typeof render>>) {
  await w.find('[data-test="drafts-shortcut"]').trigger("click");
  await flushPromises();
  expect(
    calls.filter((c) => c.url.endsWith("source=factory&status=draft")).length,
  ).toBeGreaterThan(1);
}

it("keeps drafts in their own notebook and approves with a note", async () => {
  const w = await render();
  await openDrafts(w);
  expect(items(w)).toHaveLength(1);
  expect(items(w)[0].text()).toContain("Learned rule");
  const note = document.body.querySelector<HTMLInputElement>("#kb-note")!;
  note.value = "looks right";
  note.dispatchEvent(new Event("input"));
  await click("kb-approve");
  const post = calls.find((c) => c.method === "POST")!;
  expect(post.url).toBe(
    `/api/v1/kb/doc/approve?id=${encodeURIComponent(draftDoc.id)}`,
  );
  expect(post.body).toEqual({ note: "looks right" });
  expect(btn("kb-delete")).toBeNull();
  w.unmount();
});

it("discards a draft only after confirmation", async () => {
  const w = await render();
  await openDrafts(w);
  await click("kb-discard");
  expect(calls.some((c) => c.url.includes("/discard"))).toBe(false);
  await click("kb-confirm-yes");
  const post = calls.find((c) => c.url.includes("/discard"))!;
  expect(post.method).toBe("POST");
  expect(post.url).toContain(encodeURIComponent(draftDoc.id));
  w.unmount();
});

it("resets a default playbook after confirmation and hides Delete for it", async () => {
  details[factoryDoc.id] = detailOf(factoryDoc, { default_available: true });
  const w = await render();
  expect(btn("kb-delete")).toBeNull();
  await click("kb-reset");
  await click("kb-confirm-yes");
  const post = calls.find((c) => c.url.includes("/reset"))!;
  expect(post.method).toBe("POST");
  expect(post.url).toBe(
    `/api/v1/kb/doc/reset?id=${encodeURIComponent(factoryDoc.id)}`,
  );
  w.unmount();
});

it("shows repo notes read-only with no edit or delete controls", async () => {
  const w = await render();
  await items(w)[1].trigger("click");
  await flushPromises();
  expect(btn("repo-readonly")!.textContent).toContain(
    "Changes to repository documents go through the repository.",
  );
  for (const t of [
    "kb-edit",
    "kb-approve",
    "kb-discard",
    "kb-reset",
    "kb-delete",
  ])
    expect(btn(t)).toBeNull();
  w.unmount();
});

it("hides edit controls from viewers and factory-wide delete from non-admins", async () => {
  useSession().user = { id: "u", name: "U", email: "u@x", role: "VIEWER" };
  let w = await render();
  expect(body()).toContain("Some paragraph.");
  for (const t of ["kb-edit", "kb-delete"]) expect(btn(t)).toBeNull();
  w.unmount();
  useSession().user = { id: "u", name: "U", email: "u@x", role: "ENGINEER" };
  details[factoryDoc.id] = detailOf(mk({ project_id: undefined }));
  w = await render();
  expect(btn("kb-delete")).toBeNull();
  w.unmount();
});

it("delays DELETE until the undo toast expires", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const w = await render();
  await click("kb-delete");
  expect(items(w)).toHaveLength(1);
  expect(useToast().toasts.value.at(-1)!.message).toContain("Escalation");
  expect(calls.some((c) => c.method === "DELETE")).toBe(false);
  await vi.advanceTimersByTimeAsync(5900);
  expect(calls.some((c) => c.method === "DELETE")).toBe(false);
  await vi.advanceTimersByTimeAsync(200);
  await flushPromises();
  const del = calls.find((c) => c.method === "DELETE")!;
  expect(del.url).toBe(
    `/api/v1/kb/doc?id=${encodeURIComponent(factoryDoc.id)}`,
  );
  w.unmount();
});

it("undo restores the note and never calls DELETE", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const w = await render();
  await click("kb-delete");
  const t = useToast().toasts.value.at(-1)!;
  t.undo!();
  useToast().dismiss(t.id);
  await flushPromises();
  expect(items(w)).toHaveLength(2);
  expect(w.find(".nb-item.on").text()).toContain("Escalation");
  await vi.advanceTimersByTimeAsync(10000);
  expect(calls.some((c) => c.method === "DELETE")).toBe(false);
  w.unmount();
});

it("keeps the discussion closed until asked, then chats with the note as context", async () => {
  const w = await render();
  expect(w.find(".nb-talk").exists()).toBe(false);
  await click("kb-ask");
  expect(w.find(".nb-talk").exists()).toBe(true);
  expect(w.find(".nb").classes()).toContain("talk");
  const ta =
    document.body.querySelector<HTMLTextAreaElement>(".composer textarea")!;
  ta.value = "What about rounding?";
  ta.dispatchEvent(new Event("input"));
  await flushPromises();
  await click("kb-send");
  await flushPromises();
  const create = calls.find((c) => c.url === "/api/v1/chats")!;
  expect(create.body).toEqual({ title: "Escalation" });
  const msg = calls.find((c) => c.url === "/api/v1/chats/c1/messages")!;
  expect(msg.body).toEqual({
    content: "What about rounding?",
    kb: { id: factoryDoc.id },
  });
  const turns = w.findAll('[data-test="kb-turn"]');
  expect(turns).toHaveLength(2);
  expect(turns[1].html()).toContain("<strong>there</strong>");
  expect(JSON.parse(sessionStorage.getItem("legos-kb-chats")!)).toEqual({
    [factoryDoc.id]: "c1",
  });
  await click("kb-to-chat");
  expect(push).toHaveBeenCalledWith("/chats/c1");
  w.unmount();
});

it("sends a selected passage as the quote", async () => {
  const w = await render();
  const p = document.body.querySelector("[data-test=kb-rendered] p")!;
  const range = document.createRange();
  range.selectNodeContents(p);
  const s = window.getSelection()!;
  s.removeAllRanges();
  s.addRange(range);
  range.getBoundingClientRect = () => new DOMRect(10, 100, 50, 20);
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  document.dispatchEvent(new Event("selectionchange"));
  await vi.advanceTimersByTimeAsync(100);
  vi.useRealTimers();
  await flushPromises();
  await click("kb-ask-selection");
  expect(w.find(".quote").text()).toContain("Some paragraph.");
  const ta =
    document.body.querySelector<HTMLTextAreaElement>(".composer textarea")!;
  ta.value = "Explain";
  ta.dispatchEvent(new Event("input"));
  await flushPromises();
  await click("kb-send");
  const msg = calls.find((c) => c.url === "/api/v1/chats/c1/messages")!;
  expect(msg.body).toEqual({
    content: "Explain",
    kb: { id: factoryDoc.id, quote: "Some paragraph." },
  });
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

it("restores the note and reports it when the delayed DELETE fails", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const base = fetchMock.getMockImplementation()!;
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) =>
    init?.method === "DELETE"
      ? new Response(
          JSON.stringify({ error: { code: "FORBIDDEN", message: "nope" } }),
          { status: 403 },
        )
      : base(url, init),
  );
  const w = await render();
  await click("kb-delete");
  expect(items(w)).toHaveLength(1);
  await vi.advanceTimersByTimeAsync(6100);
  await flushPromises();
  expect(items(w)).toHaveLength(2);
  expect(useToast().toasts.value.some((t) => t.message.includes("nope"))).toBe(
    true,
  );
  w.unmount();
});

it("drops stale search responses", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  const base = fetchMock.getMockImplementation()!;
  let release!: () => void;
  const gate = new Promise<void>((r) => (release = r));
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.includes("q=slow")) {
      await gate;
      return new Response(JSON.stringify({ docs: [repoDoc] }));
    }
    return base(url, init);
  });
  const w = await render();
  const box = w.find("input[type=search]");
  await box.setValue("slow");
  await vi.advanceTimersByTimeAsync(300);
  await box.setValue("rounding");
  await vi.advanceTimersByTimeAsync(300);
  await flushPromises();
  release();
  await flushPromises();
  expect(items(w)[0].text()).toContain("Escalation");
  expect(items(w)).toHaveLength(2);
  w.unmount();
});

it("recovers when the remembered chat was deleted", async () => {
  sessionStorage.setItem(
    "legos-kb-chats",
    JSON.stringify({ [factoryDoc.id]: "gone" }),
  );
  const base = fetchMock.getMockImplementation()!;
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) =>
    url.includes("/chats/gone/")
      ? new Response(
          JSON.stringify({ error: { code: "NOT_FOUND", message: "no chat" } }),
          { status: 404 },
        )
      : base(url, init),
  );
  const w = await render();
  await click("kb-ask");
  const ta =
    document.body.querySelector<HTMLTextAreaElement>(".composer textarea")!;
  ta.value = "hi";
  ta.dispatchEvent(new Event("input"));
  await flushPromises();
  await click("kb-send");
  expect(calls.some((c) => c.url === "/api/v1/chats")).toBe(true);
  expect(calls.some((c) => c.url === "/api/v1/chats/c1/messages")).toBe(true);
  expect(JSON.parse(sessionStorage.getItem("legos-kb-chats")!)).toEqual({
    [factoryDoc.id]: "c1",
  });
  expect(w.findAll('[data-test="kb-turn"]')).toHaveLength(2);
  w.unmount();
});

it("opens a linked note that is outside the current search", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  details[backlink.id] = detailOf(backlink);
  const base = fetchMock.getMockImplementation()!;
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) =>
    url.includes("q=money")
      ? new Response(JSON.stringify({ docs: [repoDoc] }))
      : base(url, init),
  );
  const w = await render();
  await w.find("input[type=search]").setValue("money");
  await vi.advanceTimersByTimeAsync(300);
  await flushPromises();
  expect(items(w)).toHaveLength(1);
  const ref = [...document.body.querySelectorAll<HTMLElement>(".ref")].find(
    (r) => r.textContent?.includes("Linked from"),
  )!;
  ref.click();
  await flushPromises();
  expect(items(w)).toHaveLength(2);
  expect(document.body.querySelector(".nb-h")!.textContent).toBe("Linked from");
  w.unmount();
});

it("ignores j/k while editing and confirms before leaving unsaved edits", async () => {
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  const w = await render();
  await click("kb-edit");
  document.body.dispatchEvent(
    new KeyboardEvent("keydown", { key: "j", bubbles: true }),
  );
  await flushPromises();
  expect(w.find(".nb-item.on").text()).toContain("Escalation");
  const bd = document.body.querySelector<HTMLTextAreaElement>("#kb-body")!;
  bd.value = "changed";
  bd.dispatchEvent(new Event("input"));
  await flushPromises();
  await items(w)[1].trigger("click");
  expect(confirm).toHaveBeenCalledTimes(1);
  expect(w.find(".nb-item.on").text()).toContain("Escalation");
  confirm.mockReturnValue(true);
  await items(w)[1].trigger("click");
  await flushPromises();
  expect(w.find(".nb-item.on").text()).toContain("Money");
  confirm.mockRestore();
  w.unmount();
});

it("filters by status and badges deprecated notes", async () => {
  const dep = mk({
    id: "d",
    title: "Old",
    status: "deprecated",
    indexed_at: 5,
  });
  const base = fetchMock.getMockImplementation()!;
  fetchMock.mockImplementation(async (url: string, init?: RequestInit) =>
    url.includes("status=deprecated") && url.includes("/kb/docs")
      ? new Response(JSON.stringify({ docs: [dep] }))
      : base(url, init),
  );
  details[dep.id] = detailOf(dep);
  const w = await render();
  await w.find('[data-test="kb-status"]').setValue("deprecated");
  await flushPromises();
  expect(items(w)[0].text()).toContain("Old");
  expect(items(w)[0].text()).toContain("deprecated");
  w.unmount();
});
