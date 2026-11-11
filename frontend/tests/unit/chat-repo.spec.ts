import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ChatsView from "../../src/features/chats/ChatsView.vue";
import { stepLine } from "../../src/features/chats/chatUtils";
import { useChats, type ChatMessage } from "../../src/stores/chats";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
import type { Project } from "../../src/types/api";

const SHA = "0123456789abcdef0123456789abcdef01234567";
const frame = (event: string, data: unknown) =>
  `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
const msg = (id: number, role: string, content: string) => ({
  id,
  chat_id: "c",
  role,
  content,
  attachments: [],
  created_at: "",
});

afterEach(() => vi.unstubAllGlobals());

describe("repo chat stream in the store", () => {
  beforeEach(() => setActivePinia(createPinia()));
  it("keeps the latest step while streaming and the commit after done", async () => {
    const enc = new TextEncoder();
    let ctl!: ReadableStreamDefaultController<Uint8Array>;
    const body = new ReadableStream<Uint8Array>({ start: (c) => void (ctl = c) });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, init?: { method?: string }) =>
        init?.method === "POST"
          ? { ok: true, body }
          : { ok: true, text: async () => '{"chats":[]}' },
      ),
    );
    const s = useChats();
    const sent = s.send("c", { content: "where?" });
    ctl.enqueue(
      enc.encode(
        frame("user_message", { message: msg(1, "user", "where?") }) +
          frame("repo", { project_id: "p1", sha: SHA }) +
          frame("step", {
            tools: [{ name: "Read File", target: "a.go", failed: false }],
          }) +
          frame("step", {
            tools: [{ name: "grep", target: "decidePlanGate", failed: false }],
          }) +
          frame("delta", { text: "See a.go:1" }),
      ),
    );
    const reply = () => s.messages.c.at(-1)!;
    await vi.waitFor(() => expect(reply().content).toBe("See a.go:1"));
    expect(reply().pending).toBe(true);
    expect(reply().repo).toEqual({ project_id: "p1", sha: SHA });
    expect(reply().step).toEqual({
      name: "grep",
      target: "decidePlanGate",
      failed: false,
    });
    ctl.enqueue(
      enc.encode(frame("done", { message: msg(2, "assistant", "See a.go:1") })),
    );
    ctl.close();
    await sent;
    expect(reply().id).toBe(2);
    expect(reply().repo).toEqual({ project_id: "p1", sha: SHA });
    expect(reply().step).toBeUndefined();
    expect(s.messages.c[0].repo).toBeUndefined();
  });

  it("leaves a plain chat reply without a commit or step", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, init?: { method?: string }) =>
        init?.method === "POST"
          ? {
              ok: true,
              body: new ReadableStream({
                start(c) {
                  c.enqueue(
                    new TextEncoder().encode(
                      frame("user_message", { message: msg(1, "user", "hi") }) +
                        frame("delta", { text: "yo" }) +
                        frame("done", { message: msg(2, "assistant", "yo") }),
                    ),
                  );
                  c.close();
                },
              }),
            }
          : { ok: true, text: async () => '{"chats":[]}' },
      ),
    );
    const s = useChats();
    await s.send("c", { content: "hi" });
    expect(s.messages.c.at(-1)).toEqual(msg(2, "assistant", "yo"));
  });
});

describe("step line", () => {
  it("names what the agent reads or searches", () => {
    expect(
      stepLine({
        name: "Read File",
        target: "backend/internal/usecase/chat.go",
        failed: false,
      }),
    ).toBe("Reading backend/internal/usecase/chat.go");
    expect(
      stepLine({ name: "grep", target: "decidePlanGate", failed: false }),
    ).toBe("Searching decidePlanGate");
    expect(stepLine({ name: "Glob", target: "**/*.vue", failed: false })).toBe(
      "Searching **/*.vue",
    );
    expect(stepLine({ name: "Read File", target: "", failed: false })).toBe(
      "Reading",
    );
    expect(stepLine({ name: "think", target: "", failed: false })).toBe(
      "think",
    );
  });
});

describe("repo chat in the thread", () => {
  async function render(messages: ChatMessage[]) {
    setActivePinia(createPinia());
    useSession().user = { role: "OWNER" } as never;
    useFactory().projects = [{ id: "p1", name: "Greeter" } as Project];
    const chats = useChats();
    chats.messages.c = messages;
    chats.busy.c = true;
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/chats/:id?", component: ChatsView }],
    });
    await router.push("/chats/c");
    const json = (data: unknown) => ({
      ok: true,
      status: 200,
      headers: new Headers({ "content-type": "application/json" }),
      json: async () => data,
      text: async () => JSON.stringify(data),
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        if (url.endsWith("/api/v1/chats"))
          return json({
            chats: [
              { id: "c", title: "t", model: "", project_id: "p1", updated_at: "" },
            ],
          });
        if (url.endsWith("/api/v1/chat/models")) return json({ models: [] });
        return json({ messages: [], docs: [] });
      }),
    );
    const w = mount(ChatsView, { global: { plugins: [router] } });
    await flushPromises();
    return w;
  }

  it("shows the latest step under a streaming reply and the commit chip", async () => {
    const w = await render([
      { ...msg(1, "user", "where?"), role: "user" },
      {
        ...msg(-2, "assistant", ""),
        role: "assistant",
        pending: true,
        repo: { project_id: "p1", sha: SHA },
        step: { name: "Read File", target: "a/b.go", failed: false },
      },
    ]);
    expect(w.find('[data-test="repo-chip"]').text()).toBe(
      "Reading Greeter @ 0123456",
    );
    expect(w.find('[data-test="step-line"]').text()).toBe("Reading a/b.go");
  });

  it("drops the step line once the reply is done but keeps the chip", async () => {
    const w = await render([
      {
        ...msg(2, "assistant", "See a.go:1"),
        role: "assistant",
        repo: { project_id: "p1", sha: SHA },
        step: { name: "grep", target: "x", failed: false },
      },
    ]);
    expect(w.find('[data-test="repo-chip"]').exists()).toBe(true);
    expect(w.find('[data-test="step-line"]').exists()).toBe(false);
  });

  it("shows neither in a plain chat", async () => {
    const w = await render([
      { ...msg(1, "user", "hi"), role: "user" },
      { ...msg(-2, "assistant", "y"), role: "assistant", pending: true },
    ]);
    expect(w.find('[data-test="repo-chip"]').exists()).toBe(false);
    expect(w.find('[data-test="step-line"]').exists()).toBe(false);
  });
});
