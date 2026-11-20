import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, expect, it, vi } from "vitest";
import ChatsView from "../../src/features/chats/ChatsView.vue";
import { useSession } from "../../src/stores/session";

afterEach(() => vi.unstubAllGlobals());

const json = (data: unknown) => ({
  ok: true,
  status: 200,
  headers: new Headers({ "content-type": "application/json" }),
  json: async () => data,
  text: async () => JSON.stringify(data),
});
const reply = (id: number, usage: object) => ({
  id,
  chat_id: "c",
  role: "assistant",
  content: "ok",
  attachments: [],
  created_at: "",
  usage: { tokens_in: 10, tokens_out: 5, cost_micros: 0, ...usage },
});

it("the message footer shows the served model when present, else the model", async () => {
  setActivePinia(createPinia());
  useSession().user = { role: "OWNER" } as never;
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/chats/:id?", component: ChatsView }],
  });
  await router.push("/chats/c");
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      if (url.endsWith("/api/v1/chats"))
        return json({
          chats: [{ id: "c", title: "t", model: "", updated_at: "" }],
        });
      if (url.endsWith("/api/v1/chat/models")) return json({ models: [] });
      if (url.includes("/messages"))
        return json({
          messages: [
            reply(1, { model: "claude/opus", served_model: "deepseek-flash" }),
            reply(2, { model: "claude/sonnet" }),
          ],
        });
      return json({ docs: [] });
    }),
  );
  const w = mount(ChatsView, { global: { plugins: [router] } });
  await flushPromises();
  const metas = w.findAll(".ch-meta").map((m) => m.text());
  expect(metas).toHaveLength(2);
  expect(metas[0]).toContain("deepseek-flash");
  expect(metas[0]).not.toContain("claude/opus");
  expect(metas[1]).toContain("claude/sonnet");
  w.unmount();
});

it("the footer shows no dollar cost on an API-key provider", async () => {
  setActivePinia(createPinia());
  useSession().user = { role: "OWNER" } as never;
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/chats/:id?", component: ChatsView }],
  });
  await router.push("/chats/c");
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      if (url.endsWith("/api/v1/chats"))
        return json({
          chats: [{ id: "c", title: "t", model: "", updated_at: "" }],
        });
      if (url.endsWith("/api/v1/chat/models")) return json({ models: [] });
      if (url.includes("/messages"))
        return json({
          messages: [
            reply(1, {
              model: "claude/opus",
              served_model: "deepseek-flash",
              provider_host: "api.deepseek.com",
              cost_micros: 5000,
            }),
            reply(2, { model: "claude/sonnet", cost_micros: 5000 }),
          ],
        });
      return json({ docs: [] });
    }),
  );
  const w = mount(ChatsView, { global: { plugins: [router] } });
  await flushPromises();
  const metas = w.findAll(".ch-meta").map((m) => m.text());
  expect(metas[0]).toContain("15 tokens");
  expect(metas[0]).not.toContain("$");
  expect(metas[1]).toContain("$0.01");
  w.unmount();
});
