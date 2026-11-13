import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ChatsView from "../../src/features/chats/ChatsView.vue";
import ChatPermissionCard from "../../src/features/chats/ChatPermissionCard.vue";
import { useChats } from "../../src/stores/chats";
import { useSession } from "../../src/stores/session";
import type { ChatPermission } from "../../src/types/api";

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
const perm = (over: Partial<ChatPermission> = {}): ChatPermission => ({
  id: "perm_1",
  server: "docs",
  tool: "write_page",
  input: '{"title":"Hi"}',
  state: "pending",
  ...over,
});

afterEach(() => vi.unstubAllGlobals());

describe("permission events in the store", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("shows a pending card, updates it on resolution and keeps it after done", async () => {
    const enc = new TextEncoder();
    let ctl!: ReadableStreamDefaultController<Uint8Array>;
    const body = new ReadableStream<Uint8Array>({
      start: (c) => void (ctl = c),
    });
    const calls: { url: string; init?: { method?: string; body?: string } }[] =
      [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: { method?: string; body?: string }): Promise<unknown> => {
        calls.push({ url, init });
        if (url.endsWith("/messages")) return { ok: true, body };
        if (url.includes("/permissions/"))
          return {
            ok: true,
            status: 204,
            headers: new Headers(),
            text: async () => "",
          };
        return { ok: true, text: async () => '{"chats":[]}' };
      }),
    );
    const s = useChats();
    const sent = s.send("c", { content: "edit it" });
    ctl.enqueue(
      enc.encode(
        frame("user_message", { message: msg(1, "user", "edit it") }) +
          frame("permission", {
            id: "perm_1",
            server: "docs",
            tool: "write_page",
            input: '{"title":"Hi"}',
          }),
      ),
    );
    const reply = () => s.messages.c.at(-1)!;
    await vi.waitFor(() => expect(reply().permissions).toHaveLength(1));
    expect(reply().permissions![0]).toEqual(perm());

    const answered = s.answerPermission("c", "perm_1", "allow_all");
    expect(reply().permissions![0].state).toBe("answering");
    await answered;
    const post = calls.find((c) => c.url.includes("/permissions/"))!;
    expect(post.url).toBe("/api/v1/chats/c/permissions/perm_1");
    expect(JSON.parse(post.init!.body!)).toEqual({ decision: "allow_all" });

    ctl.enqueue(
      enc.encode(
        frame("permission_resolved", { id: "perm_1", outcome: "allowed" }) +
          frame("delta", { text: "Done." }) +
          frame("done", { message: msg(2, "assistant", "Done.") }),
      ),
    );
    ctl.close();
    await sent;
    expect(reply().id).toBe(2);
    expect(reply().permissions![0].state).toBe("allowed");
  });

  it("marks an expired request and ignores answers once it is no longer pending", async () => {
    const s = useChats();
    s.messages.c = [
      { ...msg(-2, "assistant", ""), role: "assistant", permissions: [perm()] },
    ] as never;
    const fetchMock = vi.fn(async () => {
      throw new Error("must not be called");
    });
    vi.stubGlobal("fetch", fetchMock);
    s.messages.c[0].permissions![0].state = "expired";
    await s.answerPermission("c", "perm_1", "allow");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("falls back to expired on a 404 and to pending on another failure", async () => {
    const s = useChats();
    const seed = () =>
      (s.messages.c = [
        {
          ...msg(-2, "assistant", ""),
          role: "assistant",
          permissions: [perm()],
        },
      ] as never);
    const reply = (status: number) => ({
      ok: false,
      status,
      headers: new Headers(),
      text: async () => JSON.stringify({ error: { code: "X", message: "no" } }),
    });
    seed();
    vi.stubGlobal("fetch", vi.fn(async () => reply(404)));
    await expect(s.answerPermission("c", "perm_1", "deny")).rejects.toThrow();
    expect(s.messages.c[0].permissions![0].state).toBe("expired");
    seed();
    vi.stubGlobal("fetch", vi.fn(async () => reply(500)));
    await expect(s.answerPermission("c", "perm_1", "deny")).rejects.toThrow();
    expect(s.messages.c[0].permissions![0].state).toBe("pending");
  });
});

describe("permission card", () => {
  it("offers the three answers and pretty-prints the input", async () => {
    const w = mount(ChatPermissionCard, {
      props: { permission: perm({ input: '{"a":{"b":1}}' }) },
    });
    expect(w.text()).toContain("docs · write_page");
    expect(w.find('[data-test="permission-input"]').text()).toBe(
      JSON.stringify({ a: { b: 1 } }, null, 2),
    );
    expect(w.find('[data-test="permission-allow"]').text()).toBe("Izinkan");
    expect(w.find('[data-test="permission-allow-all"]').text()).toBe(
      "Izinkan semua di chat ini",
    );
    expect(w.find('[data-test="permission-deny"]').text()).toBe("Tolak");
    await w.find('[data-test="permission-allow"]').trigger("click");
    await w.find('[data-test="permission-allow-all"]').trigger("click");
    await w.find('[data-test="permission-deny"]').trigger("click");
    expect(w.emitted("answer")).toEqual([["allow"], ["allow_all"], ["deny"]]);
  });

  it("falls back to the raw text for input that is not JSON", () => {
    const w = mount(ChatPermissionCard, {
      props: { permission: perm({ input: '{"cut":"of…' }) },
    });
    expect(w.find('[data-test="permission-input"]').text()).toBe('{"cut":"of…');
  });

  it("disables the buttons while answering and shows the outcome after", async () => {
    const w = mount(ChatPermissionCard, {
      props: { permission: perm({ state: "answering" }) },
    });
    for (const b of ["allow", "allow-all", "deny"])
      expect(
        w.find(`[data-test="permission-${b}"]`).attributes("disabled"),
      ).toBeDefined();
    await w.setProps({ permission: perm({ state: "denied" }) });
    expect(w.find('[data-test="permission-outcome"]').text()).toBe("Denied");
    expect(w.find('[data-test="permission-allow"]').exists()).toBe(false);
    await w.setProps({ permission: perm({ state: "expired" }) });
    expect(w.find('[data-test="permission-outcome"]').text()).toContain(
      "Expired",
    );
  });
});

describe("permission card in the thread", () => {
  it("answers through the chat's permission route when a button is clicked", async () => {
    setActivePinia(createPinia());
    useSession().user = { role: "OWNER" } as never;
    const chats = useChats();
    chats.messages.c = [
      {
        ...msg(-2, "assistant", ""),
        role: "assistant",
        pending: true,
        permissions: [perm()],
      },
    ] as never;
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
    const fetchMock = vi.fn(async (url: string) => {
      if (url.endsWith("/api/v1/chats"))
        return json({
          chats: [{ id: "c", title: "t", model: "", updated_at: "" }],
        });
      if (url.endsWith("/api/v1/chat/models")) return json({ models: [] });
      return json({ messages: [], docs: [] });
    });
    vi.stubGlobal("fetch", fetchMock);
    const w = mount(ChatsView, { global: { plugins: [router] } });
    await flushPromises();
    await w.find('[data-test="permission-deny"]').trigger("click");
    await flushPromises();
    const call = fetchMock.mock.calls.find(([u]) =>
      u.includes("/permissions/"),
    ) as unknown as [string, { body: string }];
    expect(call[0]).toBe("/api/v1/chats/c/permissions/perm_1");
    expect(JSON.parse(call[1].body)).toEqual({ decision: "deny" });
    expect(
      w.find('[data-test="permission-deny"]').attributes("disabled"),
    ).toBeDefined();
  });
});
