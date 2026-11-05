import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  createFrameParser,
  streamChat,
  type ChatStreamEvent,
} from "../../src/services/api/chatStream";
import {
  addFiles,
  groupByRecency,
  renderMarkdown,
} from "../../src/features/chats/chatUtils";
import ChatComposer from "../../src/features/chats/ChatComposer.vue";
import { useChats, type Chat } from "../../src/stores/chats";
import { createRouter, createMemoryHistory } from "vue-router";
import AppShell from "../../src/app/layouts/AppShell.vue";
import { useSession } from "../../src/stores/session";

const frames = (...chunks: string[]) => {
  const out: ChatStreamEvent[] = [];
  const p = createFrameParser((e) => out.push(e));
  chunks.forEach((c) => p.push(c));
  p.flush();
  return out;
};

describe("chat stream parser", () => {
  it("handles chunk boundaries splitting a frame mid-line", () => {
    const all = frames("event: del", 'ta\ndata: {"text":"He', 'llo"}\n', "\n");
    expect(all).toEqual([{ event: "delta", data: { text: "Hello" } }]);
  });
  it("handles several frames in one chunk and CRLF", () => {
    const all = frames(
      'event: delta\r\ndata: {"text":"a"}\r\n\r\nevent: delta\r\ndata: {"text":"b"}\r\n\r\n: ping\n\n',
    );
    expect(all.map((e) => (e.data as { text: string }).text)).toEqual([
      "a",
      "b",
    ]);
  });
  it("keeps a trailing CR across chunks so CRLF-CRLF still ends the frame", () => {
    const all = frames('event: delta\r\ndata: {"text":"a"}\r\n\r', "\n");
    expect(all).toEqual([{ event: "delta", data: { text: "a" } }]);
  });
  it("delivers the error frame and flushes a trailing unterminated frame", () => {
    const all = frames(
      'event: error\ndata: {"message":{"id":2},"error":"boom"}\n\nevent: done\ndata: {"message":{"id":3}}',
    );
    expect(all.map((e) => e.event)).toEqual(["error", "done"]);
    expect((all[0].data as { error: string }).error).toBe("boom");
  });
});

describe("streamChat", () => {
  it("streams events and throws ApiError for pre-stream JSON errors", async () => {
    const enc = new TextEncoder();
    const body = new ReadableStream({
      start(c) {
        c.enqueue(enc.encode('event: delta\ndata: {"text":"x"}\n'));
        c.enqueue(enc.encode("\nevent: done\ndata: {}\n\n"));
        c.close();
      },
    });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce({ ok: true, body }));
    const seen: string[] = [];
    await streamChat("c1", { content: "hi" }, (e) => seen.push(e.event));
    expect(seen).toEqual(["delta", "done"]);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValueOnce({
        ok: false,
        status: 409,
        text: async () => '{"error":{"code":"CHAT_BUSY","message":"busy"}}',
      }),
    );
    await expect(
      streamChat("c1", { content: "hi" }, () => {}),
    ).rejects.toMatchObject({ status: 409, code: "CHAT_BUSY" });
  });
});

describe("recency grouping", () => {
  const now = new Date(2026, 10, 6, 15, 0);
  const at = (days: number): Chat => ({
    id: String(days),
    user_id: "u",
    title: "t",
    model: "",
    created_at: "",
    updated_at: new Date(2026, 10, 6 - days, 9, 0).toISOString(),
  });
  it("buckets by calendar day and omits empty groups", () => {
    const g = groupByRecency([at(0), at(1), at(3), at(7), at(8), at(40)], now);
    expect(g.map((x) => [x.label, x.chats.length])).toEqual([
      ["Today", 1],
      ["Yesterday", 1],
      ["Previous 7 days", 2],
      ["Older", 2],
    ]);
    expect(groupByRecency([at(1)], now).map((x) => x.label)).toEqual([
      "Yesterday",
    ]);
  });
});

describe("attachment validation", () => {
  const file = (name: string, bytes: number | number[]) =>
    new File(
      [
        typeof bytes === "number"
          ? "a".repeat(bytes)
          : new Uint8Array(bytes).buffer,
      ],
      name,
    );
  it("accepts UTF-8 text and rejects oversize, binary and extra files", async () => {
    let r = await addFiles(
      [],
      [file("a.txt", 10), file("big.txt", 101 * 1024)],
    );
    expect(r.files.map((f) => f.name)).toEqual(["a.txt"]);
    expect(r.errors.join()).toContain("big.txt");
    r = await addFiles(r.files, [file("bin", [0xff, 0xfe, 0x00])]);
    expect(r.errors.join()).toContain("UTF-8");
    const six = Array.from({ length: 6 }, (_, i) => file(`f${i}`, 5));
    r = await addFiles([], six);
    expect(r.files).toHaveLength(5);
    expect(r.errors.join()).toContain("5 files");
  });
  it("enforces the 300KB total", async () => {
    const r = await addFiles(
      [],
      [
        file("a", 100 * 1024),
        file("b", 100 * 1024),
        file("c", 100 * 1024),
        file("d", 1024),
      ],
    );
    expect(r.files.map((f) => f.name)).toEqual(["a", "b", "c"]);
    expect(r.errors.join()).toContain("300 KB");
  });
});

describe("markdown", () => {
  it("escapes raw HTML and only links http(s)", () => {
    const html = renderMarkdown(
      "<img src=x onerror=alert(1)> [ok](https://x.dev/a?b=1&c=2) [bad](javascript:alert(1)) **b**",
    );
    expect(html).not.toContain("<img");
    expect(html).toContain("&lt;img");
    expect(html).toContain('<a href="https://x.dev/a?b=1&amp;c=2"');
    expect(html).not.toContain('href="javascript');
    expect(html).toContain("<strong>b</strong>");
  });
  it("renders code blocks verbatim, including unterminated ones while streaming", () => {
    expect(renderMarkdown("```go\nx := <-ch\n```")).toBe(
      '<pre data-lang="go"><code>x := &lt;-ch</code></pre>',
    );
    expect(renderMarkdown("hi\n```\n**not bold**")).toContain(
      "<code>**not bold**</code>",
    );
  });
  it("keeps emphasis markers out of hrefs and allows balanced parentheses", () => {
    const html = renderMarkdown(
      "[**w**](https://en.wikipedia.org/wiki/Go_(game)) and https://x.dev/a*b*c",
    );
    expect(html).toContain(
      '<a href="https://en.wikipedia.org/wiki/Go_(game)" target="_blank" rel="noopener noreferrer"><strong>w</strong></a>',
    );
    const html2 = renderMarkdown("[x](https://x.dev/a**b**c)");
    expect(html2).toContain('href="https://x.dev/a**b**c"');
  });
  it("renders lists", () => {
    expect(renderMarkdown("- a\n- b")).toBe("<ul><li>a</li><li>b</li></ul>");
  });
});

describe("composer", () => {
  const models = [{ id: "", name: "Auto (default)" }];
  it("is disabled when empty, sends on Enter, keeps Shift+Enter as newline", async () => {
    const w = mount(ChatComposer, { props: { models } });
    const ta = w.find("textarea");
    const send = w.find("button.ch-send");
    expect(send.attributes("disabled")).toBeDefined();
    await ta.setValue("hello");
    expect(send.attributes("disabled")).toBeUndefined();
    await ta.trigger("keydown", { key: "Enter", shiftKey: true });
    expect(w.emitted("send")).toBeUndefined();
    await ta.trigger("keydown", { key: "Enter" });
    expect(w.emitted("send")![0][0]).toEqual({ content: "hello", files: [] });
    expect((ta.element as HTMLTextAreaElement).value).toBe("");
  });
  it("locks input while a turn runs or for viewers", async () => {
    for (const props of [{ busy: true }, { readonly: true }]) {
      const w = mount(ChatComposer, { props: { models, ...props } });
      expect(w.find("textarea").attributes("disabled")).toBeDefined();
      expect(w.find("button.ch-send").attributes("disabled")).toBeDefined();
      expect(
        w.find('[aria-label="Attach files"]').attributes("disabled"),
      ).toBeDefined();
    }
  });
  it("blocks messages over 32KB", async () => {
    const w = mount(ChatComposer, { props: { models } });
    await w.find("textarea").setValue("a".repeat(33 * 1024));
    expect(w.find("button.ch-send").attributes("disabled")).toBeDefined();
    expect(w.text()).toContain("too long");
  });
});

describe("chats store send", () => {
  beforeEach(() => setActivePinia(createPinia()));
  it("streams a reply into the thread and keeps the partial on error", async () => {
    const enc = new TextEncoder();
    const sse = (s: string) =>
      new ReadableStream({
        start(c) {
          c.enqueue(enc.encode(s));
          c.close();
        },
      });
    const user = {
      id: 1,
      chat_id: "c",
      role: "user",
      content: "hi",
      attachments: [],
      created_at: "",
    };
    const reply = { ...user, id: 2, role: "assistant", content: "Hello" };
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce({
          ok: true,
          body: sse(
            `event: user_message\ndata: ${JSON.stringify({ message: user })}\n\nevent: delta\ndata: {"text":"Hel"}\n\nevent: delta\ndata: {"text":"lo"}\n\nevent: done\ndata: ${JSON.stringify({ message: reply })}\n\n`,
          ),
        })
        .mockResolvedValueOnce({ ok: true, text: async () => '{"chats":[]}' })
        .mockResolvedValueOnce({
          ok: true,
          body: sse(
            'event: delta\ndata: {"text":"part"}\n\nevent: error\ndata: {"error":"upstream died"}\n\n',
          ),
        })
        .mockResolvedValue({ ok: true, text: async () => '{"chats":[]}' }),
    );
    const s = useChats();
    await s.send("c", { content: "hi" });
    expect(s.messages.c.map((m) => [m.id, m.content])).toEqual([
      [1, "hi"],
      [2, "Hello"],
    ]);
    await s.send("c", { content: "again" });
    await flushPromises();
    const last = s.messages.c.at(-1)!;
    expect(last.content).toBe("part");
    expect(last.error).toBe("upstream died");
    expect(s.busy.c).toBe(false);
  });
});

describe("chats store races and interrupted turns", () => {
  beforeEach(() => setActivePinia(createPinia()));
  const enc = new TextEncoder();
  const sse = (s: string) =>
    new ReadableStream({
      start(c) {
        c.enqueue(enc.encode(s));
        c.close();
      },
    });
  const msg = (id: number, role: string, content: string) => ({
    id,
    chat_id: "c",
    role,
    content,
    attachments: [],
    created_at: "",
  });
  const frame = (event: string, data: unknown) =>
    `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;

  it("keeps the first exchange when the stream finishes before a stale GET resolves", async () => {
    let release: (v: unknown) => void = () => {};
    const stale = new Promise((r) => (release = r));
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: { method?: string }) => {
        if (init?.method === "POST")
          return {
            ok: true,
            body: sse(
              frame("user_message", { message: msg(1, "user", "hi") }) +
                frame("done", { message: msg(2, "assistant", "yo") }),
            ),
          };
        if (url.endsWith("/messages")) {
          await stale;
          return { ok: true, text: async () => '{"messages":[]}' };
        }
        return { ok: true, text: async () => '{"chats":[]}' };
      }),
    );
    const s = useChats();
    const load = s.loadMessages("c");
    await s.send("c", { content: "hi" });
    release(null);
    await load;
    expect(s.messages.c.map((m) => m.id)).toEqual([1, 2]);
  });

  it("marks a stream that ends without done/error as failed and keeps turn ids distinct", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: { method?: string }) => {
        if (init?.method === "POST")
          return {
            ok: true,
            body: sse(frame("delta", { text: "half" })),
          };
        if (url.endsWith("/messages"))
          return {
            ok: true,
            text: async () =>
              JSON.stringify({ messages: [msg(1, "user", "q1")] }),
          };
        return { ok: true, text: async () => '{"chats":[]}' };
      }),
    );
    const s = useChats();
    await s.send("c", { content: "q1" });
    expect(s.busy.c).toBe(false);
    let last = s.messages.c.at(-1)!;
    expect(last.role).toBe("assistant");
    expect(last.content).toBe("half");
    expect(last.error).toContain("closed");
    expect(last.pending).toBe(false);
    await s.send("c", { content: "q2" });
    const ids = s.messages.c.map((m) => m.id);
    expect(new Set(ids).size).toBe(ids.length);
    last = s.messages.c.at(-1)!;
    expect(last.error).toBeTruthy();
  });

  it("does not swap in a message with id 0", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: true,
        body: sse(frame("done", { message: msg(0, "assistant", "bad") })),
        text: async () => '{"chats":[]}',
      })),
    );
    const s = useChats();
    await s.send("c", { content: "q" }).catch(() => undefined);
    expect(s.messages.c.some((m) => m.id === 0)).toBe(false);
  });
});

describe("rail recent chats", () => {
  it("lists the latest chats and opens a draft without creating one", async () => {
    setActivePinia(createPinia());
    const calls: string[] = [];
    const list = Array.from({ length: 7 }, (_, i) => ({
      id: `c${i}`,
      user_id: "u",
      title: `Chat ${i}`,
      model: "",
      created_at: "",
      updated_at: "",
    }));
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: { method?: string }) => ({
        ok: (calls.push(init?.method ?? "GET"), true),
        text: async () =>
          JSON.stringify(
            init?.method === "POST"
              ? { chat: { ...list[0], id: "new" } }
              : url.endsWith("/chats")
                ? { chats: list }
                : { projects: [], work_orders: [], metrics: {}, budget: {} },
          ),
      })),
    );
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/:pathMatch(.*)*", component: { template: "<div />" } },
      ],
    });
    await router.push("/chats");
    await router.isReady();
    useSession().user = { id: "u", name: "P", email: "p@x", role: "OWNER" };
    const w = mount(AppShell, { global: { plugins: [router] } });
    await flushPromises();
    const links = w.findAll('nav[aria-label="Recent chats"] a');
    expect(links.map((a) => a.attributes("href"))).toEqual(
      list.slice(0, 5).map((c) => `/chats/${c.id}`),
    );
    await w.find(".rail .lg-btn.primary").trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.fullPath).toBe("/chats?new=1");
    expect(calls).not.toContain("POST");
    w.unmount();
  });
});
