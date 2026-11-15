import { mount, flushPromises } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import ChatContextPanel from "../../src/features/chats/ChatContextPanel.vue";
import {
  cacheState,
  callsLine,
  fmtTokens,
  loadOpen,
  partBars,
  usedPct,
  type SessionInfo,
} from "../../src/features/chats/sessionRail";
import type { Chat } from "../../src/stores/chats";
import { useSession } from "../../src/stores/session";

describe("partBars", () => {
  it("scales widths to the window", () => {
    const b = partBars(
      [
        { key: "agent", tokens: 20000 },
        { key: "messages", tokens: 10000 },
      ],
      30000,
      200000,
    );
    expect(b.map((x) => [x.pct, x.width])).toEqual([
      [10, 10],
      [5, 5],
    ]);
  });
  it("scales to used when the window is unknown", () => {
    const b = partBars(
      [
        { key: "agent", tokens: 750 },
        { key: "messages", tokens: 250 },
      ],
      1000,
      0,
    );
    expect(b.map((x) => x.width)).toEqual([75, 25]);
  });
  it("never overflows the bar and drops empty parts", () => {
    const b = partBars(
      [
        { key: "agent", tokens: 300 },
        { key: "mcp", tokens: 0 },
        { key: "messages", tokens: 300 },
      ],
      400,
      400,
    );
    expect(b).toHaveLength(2);
    expect(b.reduce((t, x) => t + x.width, 0)).toBeCloseTo(100);
  });
  it("is empty without parts or tokens", () => {
    expect(partBars([], 5000, 200000)).toEqual([]);
    expect(partBars([{ key: "agent", tokens: 1 }], 0, 0)).toEqual([]);
  });
});

describe("usedPct and fmtTokens", () => {
  it("returns null for window 0", () => {
    expect(usedPct(31200, 0)).toBeNull();
    expect(usedPct(31200, 200000)).toBe(16);
  });
  it("formats tokens", () => {
    expect([fmtTokens(950), fmtTokens(64100), fmtTokens(1_200_000)]).toEqual([
      "950",
      "64.1k",
      "1.2M",
    ]);
  });
});

describe("cacheState", () => {
  const now = Date.parse("2026-11-16T10:00:00Z");
  const c = (warm_until: string) => ({
    read: 0,
    write: 0,
    uncached: 0,
    hit_pct: 0,
    last_turn_at: "",
    warm_until,
  });
  it("is warm with minutes left, rounded up", () => {
    expect(cacheState(c("2026-11-16T10:03:10Z"), now)).toEqual({
      warm: true,
      minutesLeft: 4,
    });
  });
  it("is cold once warm_until has passed or is invalid", () => {
    expect(cacheState(c("2026-11-16T09:59:00Z"), now)?.warm).toBe(false);
    expect(cacheState(c("nope"), now)?.warm).toBe(false);
  });
  it("is null without a cache", () => {
    expect(cacheState(null, now)).toBeNull();
  });
});

describe("callsLine and loadOpen", () => {
  it("lists calls sorted", () => {
    expect(callsLine({ whoami: 1, execute_tools: 2 })).toBe(
      "execute_tools ×2 · whoami ×1",
    );
    expect(callsLine({})).toBe("");
  });
  it("falls back to defaults when storage throws", () => {
    vi.stubGlobal("localStorage", {
      getItem: () => {
        throw new Error("blocked");
      },
    });
    expect(loadOpen().project).toBe(true);
    vi.unstubAllGlobals();
  });
});

const chat: Chat = {
  id: "c1",
  user_id: "u",
  title: "T",
  model: "m",
  project_id: "p1",
  created_at: "",
  updated_at: "",
};
const info = (over: Partial<SessionInfo["context"]> = {}): SessionInfo => ({
  project: {
    id: "p1",
    name: "P",
    local: { path: "/w/p", branch: "main", head: "abc", dirty: 1 },
    worktrees: [
      { path: "/w/p", branch: "main", head: "abc", main: true },
      { path: "/w/p-x", branch: "feat/x", head: "def", main: false },
    ],
    checkout: { sha: "98b4039aaaa", behind_local: 2 },
    running: [
      {
        work_order_id: "w7",
        title: "Retry",
        state: "IMPLEMENTING",
        role: "builder",
        task_id: "t1",
        since: new Date().toISOString(),
        workspace: "/ws/w7",
      },
    ],
  },
  context: {
    model: "claude/sonnet",
    window: 200000,
    used: 64100,
    parts: [{ key: "agent", tokens: 23000 }],
    cache: null,
    ...over,
  },
  totals: {
    turns: 1,
    tokens_in: 10,
    tokens_out: 5,
    cache_read: 0,
    cache_write: 0,
    cost_micros: 0,
  },
  mcp: [
    {
      id: "m1",
      name: "wb",
      transport: "http",
      auth: "oauth",
      status: "active",
      offered: true,
      tools_enabled: 9,
      tools_total: 9,
      asks: true,
      calls: { whoami: 1 },
    },
    {
      id: "m2",
      name: "gh",
      transport: "http",
      auth: "none",
      status: "active",
      offered: false,
      tools_enabled: 0,
      tools_total: 3,
      asks: false,
      calls: {},
    },
  ],
});

let body: unknown;
beforeEach(() => {
  setActivePinia(createPinia());
  useSession().user = { id: "u", name: "P", email: "p@x", role: "OWNER" };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      const bad = url.endsWith("/session") && body === "fail";
      return {
        ok: !bad,
        status: bad ? 500 : 200,
        text: async () =>
          JSON.stringify(
            url.endsWith("/session")
              ? bad
                ? { error: { code: "X", message: "boom" } }
                : body
              : {},
          ),
      };
    }),
  );
});
afterEach(() => {
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
});

async function mountPanel() {
  const r = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/:p(.*)*", component: { template: "<div />" } }],
  });
  await r.push("/chats/c1");
  const w = mount(ChatContextPanel, {
    props: { chat, messages: [], busy: false, active: true },
    global: { plugins: [r] },
    attachTo: document.body,
  });
  await flushPromises();
  return w;
}

describe("session rail panel", () => {
  it("renders project, context numbers and running agent links", async () => {
    body = info();
    const w = await mountPanel();
    expect(w.text()).toContain("Your folder");
    expect(w.text()).toContain("factory checkout @ 98b4039");
    expect(w.text()).toContain("2 commits");
    expect(w.find('[data-test="ctx-pct"]').text()).toBe("32% used");
    expect(w.text()).toContain("Agent system prompt + tools");
    expect(w.text()).toContain("estimated");
    expect(w.text()).toContain("workspace /ws/w7");
    expect(w.find('ul[aria-label="Running agents"] a').attributes("href")).toBe(
      "/factory/w7",
    );
    expect(w.find('[data-test="cache-card"]').text()).toContain("Cold");
    w.unmount();
  });

  it("shows tokens without a percentage when the window is 0", async () => {
    body = info({ window: 0, parts: [] });
    const w = await mountPanel();
    expect(w.find('[data-test="ctx-pct"]').text()).toBe("64.1k tokens");
    expect(w.find('[role="img"]').exists()).toBe(false);
    expect(w.text()).toContain("No breakdown");
    w.unmount();
  });

  it("shows warm cache minutes and MCP offered state", async () => {
    body = info({
      cache: {
        read: 60200,
        write: 3900,
        uncached: 0,
        hit_pct: 94,
        last_turn_at: "",
        warm_until: new Date(Date.now() + 4 * 60000 - 1000).toISOString(),
      },
    });
    const w = await mountPanel();
    expect(w.find('[data-test="cache-card"]').text()).toContain(
      "Warm · 4 min left",
    );
    await w.find("#rail-mcp-h").trigger("click");
    expect(w.find("#rail-mcp-h").attributes("aria-expanded")).toBe("true");
    expect(w.text()).toContain("Offered to chat");
    expect(w.text()).toContain("Not offered to chat");
    expect(w.text()).toContain("asks before each call");
    expect(w.text()).toContain("whoami ×1");
    w.unmount();
  });

  it("keeps the panel usable when the session fetch fails", async () => {
    body = "fail";
    const w = await mountPanel();
    expect(w.find('[role="alert"]').text()).toContain("boom");
    expect(w.find('[data-test="create-wo"]').exists()).toBe(true);
    w.unmount();
  });

  it("collapses a section and remembers it", async () => {
    const mem = new Map<string, string>();
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => mem.get(k) ?? null,
      setItem: (k: string, v: string) => mem.set(k, v),
    });
    body = info();
    const w = await mountPanel();
    await w.find("#rail-context-h").trigger("click");
    expect(w.find("#rail-context-h").attributes("aria-expanded")).toBe("false");
    expect(loadOpen().context).toBe(false);
    w.unmount();
  });

  it("renders a payload with null arrays and maps", async () => {
    const base = info();
    body = {
      ...base,
      project: {
        ...base.project,
        worktrees: null,
        running: null,
      },
      context: { ...base.context, parts: null },
      mcp: [{ ...base.mcp[0], calls: null }, ...base.mcp.slice(1)],
    };
    const w = await mountPanel();
    expect(w.find('[data-test="ctx-pct"]').text()).toBe("32% used");
    expect(w.text()).toContain("No agents running");
    w.unmount();
  });

  it("drops a response for the previous chat after switching", async () => {
    body = info();
    let release: () => void = () => {};
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) => {
        if (url === "/api/v1/chats/c1/session")
          await new Promise<void>((r) => (release = r));
        return {
          ok: true,
          status: 200,
          text: async () =>
            JSON.stringify(url.endsWith("/session") ? body : {}),
        };
      }),
    );
    const r = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/:p(.*)*", component: { template: "<div />" } }],
    });
    await r.push("/chats/c1");
    const w = mount(ChatContextPanel, {
      props: { chat, messages: [], busy: false, active: false },
      global: { plugins: [r] },
    });
    await w.setProps({ active: true });
    await w.setProps({ active: false });
    await w.setProps({ chat: { ...chat, id: "c2" } });
    release();
    await flushPromises();
    await w.setProps({ active: true });
    expect(w.text()).not.toContain("32% used");
    w.unmount();
  });

  it("clears the old project details when the project changes", async () => {
    body = info();
    const w = await mountPanel();
    expect(w.text()).toContain("factory checkout");
    body = "fail";
    await w.setProps({ chat: { ...chat, project_id: "" } });
    await flushPromises();
    expect(w.text()).not.toContain("factory checkout");
    w.unmount();
  });
});
