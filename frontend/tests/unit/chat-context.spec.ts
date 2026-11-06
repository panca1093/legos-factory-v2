import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import { readFileSync } from "node:fs";
import NewWorkOrderDrawer from "../../src/features/factory/work-orders/NewWorkOrderDrawer.vue";
import { useFactory } from "../../src/stores/factory";
import ChatContextPanel from "../../src/features/chats/ChatContextPanel.vue";
import { useChats, type Chat, type ChatMessage } from "../../src/stores/chats";
import { useSession } from "../../src/stores/session";
import { createFrameParser } from "../../src/services/api/chatStream";

const chat: Chat = {
  id: "c1",
  user_id: "u",
  title: "Retry plan",
  model: "m",
  project_id: "p1",
  created_at: "",
  updated_at: "",
};
const msg = (
  id: number,
  role: "user" | "assistant",
  over = {},
): ChatMessage => ({
  id,
  chat_id: "c1",
  role,
  content: role === "user" ? "q" : "a",
  attachments: [],
  created_at: "",
  ...over,
});
const replied = [msg(1, "user"), msg(2, "assistant")];
const projects = [
  { id: "p1", name: "Payments", github_repo: "acme/pay", publish_via: "gh" },
  { id: "p2", name: "Scratch", publish_via: "none" },
];
const workOrders = [
  {
    id: "w1",
    title: "Make retries safe",
    state: "CREATED",
    source_chat_id: "c1",
  },
  { id: "w2", title: "Other", state: "CREATED" },
];
const draft = { title: "Drafted", objective: "Do it", acceptance: "Works" };

type Handler = (body: unknown) => unknown;
let calls: { method: string; url: string; body: unknown }[];
let handlers: Record<string, Handler>;
function stubFetch() {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      const body = init?.body ? JSON.parse(init.body as string) : undefined;
      calls.push({ method, url, body });
      const h = handlers[`${method} ${url}`];
      const out = h ? h(body) : {};
      const status = (out as { __status?: number }).__status;
      return {
        ok: !status,
        status: status ?? 200,
        text: async () => JSON.stringify(out),
      };
    }),
  );
}
const router = () =>
  createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/:pathMatch(.*)*", component: { template: "<div />" } }],
  });

async function mountPanel(
  props: Partial<{
    chat: Chat;
    messages: ChatMessage[];
    busy: boolean;
    active: boolean;
  }> = {},
) {
  const r = router();
  await r.push("/chats/c1");
  const w = mount(ChatContextPanel, {
    props: { chat, messages: replied, busy: false, active: true, ...props },
    global: { plugins: [r] },
    attachTo: document.body,
  });
  await flushPromises();
  return { w, r };
}

beforeEach(() => {
  setActivePinia(createPinia());
  useSession().user = { id: "u", name: "P", email: "p@x", role: "OWNER" };
  handlers = {
    "GET /api/v1/projects": () => ({ projects }),
    "GET /api/v1/work-orders": () => ({ work_orders: workOrders }),
    "GET /api/v1/chats/c1/related-knowledge": () => ({ docs: [] }),
  };
  stubFetch();
  useChats().chats = [chat];
});
afterEach(() => {
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
});

describe("chat context panel", () => {
  it("drops the redundant title and model fields", async () => {
    const { w } = await mountPanel();
    expect(w.find('[aria-label="Chat title"]').exists()).toBe(false);
    expect(w.find('[aria-label="Model"]').exists()).toBe(false);
    expect(w.text()).toContain("Usage");
    w.unmount();
  });

  it("saves the project via PATCH and shows publish info", async () => {
    handlers["PATCH /api/v1/chats/c1"] = (b) => ({
      chat: { ...chat, project_id: (b as { project_id: string }).project_id },
    });
    const { w } = await mountPanel();
    expect(w.find('[data-test="publish-info"]').text()).toContain("acme/pay");
    await w.find("select").setValue("p2");
    await flushPromises();
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({
      project_id: "p2",
    });
    await w.setProps({ chat: useChats().chats[0] });
    expect(w.find('[data-test="publish-info"]').text()).toContain("Local only");
    w.unmount();
  });

  it("clears the project with an empty id", async () => {
    handlers["PATCH /api/v1/chats/c1"] = () => ({ chat });
    const { w } = await mountPanel();
    await w.find("select").setValue("");
    await flushPromises();
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({
      project_id: "",
    });
    w.unmount();
  });

  it.each([
    ["no project", { chat: { ...chat, project_id: "" } }, "Choose a project"],
    ["streaming", { busy: true }, "Wait for the reply"],
    ["no reply yet", { messages: [msg(1, "user")] }, "first reply"],
  ])(
    "disables Create work order while %s and says why",
    async (_n, props, why) => {
      const { w } = await mountPanel(props);
      const btn = w.find('[data-test="create-wo"]');
      expect(btn.attributes("disabled")).toBeDefined();
      expect(w.text()).toContain(why);
      await btn.trigger("click");
      expect(calls.some((c) => c.url.endsWith("work-order-draft"))).toBe(false);
      w.unmount();
    },
  );

  it("drafts, opens the prefilled drawer, and sends source_chat_id on submit", async () => {
    handlers["POST /api/v1/chats/c1/work-order-draft"] = () => draft;
    handlers["POST /api/v1/work-orders"] = () => ({ id: "w9", ...draft });
    const { w, r } = await mountPanel();
    await w.find('[data-test="create-wo"]').trigger("click");
    await flushPromises();
    const title = document.body.querySelector<HTMLInputElement>("#wo-title")!;
    expect(title.value).toBe("Drafted");
    expect(
      document.body.querySelector<HTMLTextAreaElement>("#wo-objective")!.value,
    ).toBe("Do it");
    expect(
      document.body.querySelector<HTMLSelectElement>("#wo-project")!.value,
    ).toBe("p1");
    const created = () =>
      calls.find((c) => c.url === "/api/v1/work-orders" && c.method === "POST");
    expect(created()).toBeUndefined();
    for (const t of ["drawer-next", "drawer-next", "drawer-create"]) {
      document.body.querySelector<HTMLElement>(`[data-test="${t}"]`)!.click();
      await flushPromises();
    }
    expect(created()?.body).toMatchObject({
      project_id: "p1",
      title: "Drafted",
      source_chat_id: "c1",
    });
    expect(r.currentRoute.value.path).toBe("/factory/w9");
    w.unmount();
  });

  it("shows a busy state, and Cancel aborts the draft request", async () => {
    let signal: AbortSignal | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        if (url.endsWith("work-order-draft")) {
          signal = init?.signal ?? undefined;
          return new Promise((_, rej) =>
            signal?.addEventListener("abort", () =>
              rej(new DOMException("aborted", "AbortError")),
            ),
          );
        }
        return { ok: true, status: 200, text: async () => "{}" };
      }),
    );
    const { w } = await mountPanel();
    await w.find('[data-test="create-wo"]').trigger("click");
    expect(w.find('[role="status"]').text()).toContain("Drafting");
    await w.find('[data-test="cancel-draft"]').trigger("click");
    await flushPromises();
    expect(signal?.aborted).toBe(true);
    expect(w.find('[data-test="create-wo"]').exists()).toBe(true);
    expect(w.find('[role="alert"]').exists()).toBe(false);
    expect(document.body.querySelector("#wo-title")).toBeNull();
    w.unmount();
  });

  it("shows the draft error and opens nothing", async () => {
    handlers["POST /api/v1/chats/c1/work-order-draft"] = () => ({
      __status: 502,
      error: { code: "DRAFT_FAILED", message: "Could not draft" },
    });
    const { w } = await mountPanel();
    await w.find('[data-test="create-wo"]').trigger("click");
    await flushPromises();
    expect(w.find('[role="alert"]').text()).toBe("Could not draft");
    expect(document.body.querySelector("#wo-title")).toBeNull();
    w.unmount();
  });

  it("lists only work orders created from this chat, with status", async () => {
    const { w } = await mountPanel();
    const links = w.findAll('ul[aria-label="Work orders from this chat"] a');
    expect(links.map((a) => a.attributes("href"))).toEqual(["/factory/w1"]);
    expect(links[0].text()).toContain("Make retries safe");
    expect(links[0].text()).toContain("Draft");
    w.unmount();
  });

  it("hides related knowledge when empty and renders rows when present", async () => {
    let { w } = await mountPanel();
    expect(w.text()).not.toContain("Related knowledge");
    w.unmount();
    handlers["GET /api/v1/chats/c1/related-knowledge"] = () => ({
      docs: [
        {
          id: "factory:p1:a/b.md",
          title: "Retry playbook",
          kind: "Playbook",
          snippet: "s",
        },
      ],
    });
    ({ w } = await mountPanel());
    expect(w.text()).toContain("Related knowledge");
    const a = w.find('a[aria-label="Open Retry playbook in knowledge"]');
    expect(a.text()).toContain("Playbook");
    expect(decodeURIComponent(a.attributes("href")!)).toBe(
      "/knowledge?doc=factory:p1:a/b.md",
    );
    w.unmount();
  });

  it("refreshes related knowledge after a turn completes", async () => {
    const { w } = await mountPanel();
    const count = () =>
      calls.filter((c) => c.url.endsWith("related-knowledge")).length;
    const before = count();
    await w.setProps({ busy: true });
    await w.setProps({ busy: false });
    await flushPromises();
    expect(count()).toBe(before + 1);
    w.unmount();
  });
});

describe("title frame", () => {
  it("is parsed from the stream", () => {
    const out: unknown[] = [];
    const p = createFrameParser((e) => out.push(e));
    p.push('event: title\ndata: {"title":"Short"}\n\n');
    expect(out).toEqual([{ event: "title", data: { title: "Short" } }]);
  });

  it("updates the chat title before the done frame is processed", async () => {
    const enc = new TextEncoder();
    const f = (e: string, d: unknown) =>
      `event: ${e}\ndata: ${JSON.stringify(d)}\n\n`;
    let ctl!: ReadableStreamDefaultController<Uint8Array>;
    const stream = new ReadableStream<Uint8Array>({
      start(c) {
        ctl = c;
      },
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, init?: RequestInit) =>
        init?.method === "POST"
          ? { ok: true, body: stream }
          : new Promise(() => {}),
      ),
    );
    const s = useChats();
    s.chats = [{ ...chat, title: "" }];
    const sending = s.send("c1", { content: "q" });
    ctl.enqueue(
      enc.encode(
        f("user_message", { message: msg(1, "user") }) +
          f("title", { title: "Short title" }),
      ),
    );
    await vi.waitFor(() => expect(s.chats[0].title).toBe("Short title"));
    expect(s.busy.c1).toBe(true);
    expect(s.messages.c1.at(-1)?.pending).toBe(true);
    ctl.enqueue(enc.encode(f("done", { message: msg(2, "assistant") })));
    ctl.close();
    await sending;
    expect(s.messages.c1.at(-1)?.id).toBe(2);
  });
});

describe("chat context panel races and roles", () => {
  const chatB: Chat = { ...chat, id: "c2", title: "Other", project_id: "p1" };

  it("loads related knowledge and work orders only while open", async () => {
    const { w } = await mountPanel({ active: false });
    expect(calls.map((c) => c.url)).toEqual([]);
    await w.setProps({ active: true });
    await flushPromises();
    expect(calls.map((c) => c.url)).toEqual(
      expect.arrayContaining([
        "/api/v1/chats/c1/related-knowledge",
        "/api/v1/work-orders",
      ]),
    );
    w.unmount();
  });

  it("fetches once when switching chats from a busy chat", async () => {
    const { w } = await mountPanel({ busy: true });
    calls.length = 0;
    await w.setProps({ chat: chatB, busy: false });
    await flushPromises();
    expect(
      calls.filter((c) => c.url.endsWith("/related-knowledge")),
    ).toHaveLength(1);
    w.unmount();
  });

  it("ignores a stale related-knowledge result after switching chats", async () => {
    let release: (v: unknown) => void = () => {};
    handlers["GET /api/v1/chats/c1/related-knowledge"] = () => ({ docs: [] });
    const { w } = await mountPanel();
    const real = globalThis.fetch;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        if (url === "/api/v1/chats/c1/related-knowledge")
          await new Promise((r) => (release = r));
        if (url === "/api/v1/chats/c1/related-knowledge")
          return {
            ok: true,
            status: 200,
            text: async () =>
              JSON.stringify({
                docs: [{ id: "old", title: "Stale doc", kind: "Note" }],
              }),
          };
        return real(url, init);
      }),
    );
    await w.setProps({ busy: true });
    await w.setProps({ busy: false });
    await w.setProps({ chat: chatB });
    await flushPromises();
    release(null);
    await flushPromises();
    expect(w.text()).not.toContain("Stale doc");
    w.unmount();
  });

  it("does not open the drawer for chat A's draft after switching to chat B", async () => {
    let resolve: (v: unknown) => void = () => {};
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        if (url.endsWith("work-order-draft"))
          await new Promise((r) => (resolve = r));
        return {
          ok: true,
          status: 200,
          text: async () => JSON.stringify(draft),
        };
      }),
    );
    const { w } = await mountPanel();
    await w.find('[data-test="create-wo"]').trigger("click");
    await w.setProps({ chat: chatB });
    resolve(null);
    await flushPromises();
    expect(document.body.querySelector("#wo-title")).toBeNull();
    expect(w.find('[data-test="cancel-draft"]').exists()).toBe(false);
    w.unmount();
  });

  it("locks the project select while drafting", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise(() => {})),
    );
    const { w } = await mountPanel();
    await w.find('[data-test="create-wo"]').trigger("click");
    expect(w.find("select").attributes("disabled")).toBeDefined();
    w.unmount();
  });

  it("keeps the draft and edits when the drawer is closed and reopened", async () => {
    handlers["POST /api/v1/chats/c1/work-order-draft"] = () => draft;
    const { w } = await mountPanel();
    await w.find('[data-test="create-wo"]').trigger("click");
    await flushPromises();
    const title = () =>
      document.body.querySelector<HTMLInputElement>("#wo-title");
    title()!.value = "My edit";
    title()!.dispatchEvent(new Event("input"));
    await flushPromises();
    [...document.body.querySelectorAll("button")]
      .find((b) => b.textContent?.trim() === "Cancel")!
      .click();
    await flushPromises();
    expect(title()).toBeNull();
    await w.find('[data-test="create-wo"]').trigger("click");
    await flushPromises();
    expect(title()!.value).toBe("My edit");
    expect(
      calls.filter((c) => c.url.endsWith("work-order-draft")),
    ).toHaveLength(1);
    w.unmount();
  });

  it("gives viewers no create button or project change", async () => {
    useSession().user = { id: "u", name: "P", email: "p@x", role: "VIEWER" };
    const { w } = await mountPanel();
    expect(w.find("select").attributes("disabled")).toBeDefined();
    expect(w.find('[data-test="create-wo"]').attributes("disabled")).toBe("");
    expect(w.text()).toContain("read-only");
    expect(w.text()).not.toContain("Delete chat");
    w.unmount();
  });
});

describe("rail recent chats sizing", () => {
  it("applies align-content: start to the rail's growing nav", () => {
    const style = document.createElement("style");
    style.textContent = readFileSync("src/styles/shell.css", "utf8");
    document.head.append(style);
    document.body.innerHTML =
      '<aside class="rail"><nav class="lg-nav grow"></nav></aside>';
    const nav = document.querySelector(".lg-nav")!;
    const align = [...(style.sheet?.cssRules ?? [])]
      .map((r) => r as CSSStyleRule)
      .filter((r) => r.selectorText && nav.matches(r.selectorText))
      .map((r) => r.cssText)
      .join(" ");
    style.remove();
    expect(align).toMatch(/align-content:\s*start/);
  });
});

describe("new work order drawer from a chat", () => {
  it("leaves the project empty instead of defaulting to the first one", async () => {
    const f = useFactory();
    f.projects = projects as never;
    const w = mount(NewWorkOrderDrawer, {
      props: {
        open: true,
        sourceChatID: "c1",
        prefill: draft,
      },
      attachTo: document.body,
    });
    await flushPromises();
    expect(
      document.body.querySelector<HTMLSelectElement>("#wo-project")!.value,
    ).toBe("");
    w.unmount();
  });
});
