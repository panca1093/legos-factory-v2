import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useFactory } from "../../src/stores/factory";
import { useSession } from "../../src/stores/session";
const fetchMock = vi.fn();
const sources: FakeSource[] = [];
class FakeSource {
  onmessage: ((e: { data: string }) => void) | null = null;
  onerror = null;
  listeners = new Map<string, (e: { data: string }) => void>();
  close = vi.fn();
  constructor(public url: string) {
    sources.push(this);
  }
  addEventListener(kind: string, cb: (e: { data: string }) => void) {
    this.listeners.set(kind, cb);
  }
}
const response = (value: unknown) => ({
  ok: true,
  text: async () => JSON.stringify(value),
});
beforeEach(() => {
  setActivePinia(createPinia());
  vi.stubGlobal("fetch", fetchMock);
  vi.stubGlobal("EventSource", FakeSource);
  fetchMock.mockReset();
  sources.length = 0;
});
describe("request and stream ownership", () => {
  it("does not allow an old order response to overwrite the selected order", async () => {
    let resolveA!: (v: unknown) => void;
    fetchMock.mockImplementation((url: string) => {
      if (url === "/api/v1/work-orders/a")
        return new Promise((resolve) => {
          resolveA = resolve;
        });
      if (url === "/api/v1/work-orders/b")
        return Promise.resolve(
          response({ work_order: { id: "b" }, plan: [], runs: [] }),
        );
      return Promise.resolve(
        response({ events: [], checks: [], findings: [] }),
      );
    });
    const f = useFactory();
    const pending = f.loadDetail("a");
    await f.loadDetail("b");
    resolveA(response({ work_order: { id: "a" }, plan: [], runs: [] }));
    await pending;
    expect(f.detail?.work_order.id).toBe("b");
  });
  it("owns only one global stream and closes it explicitly", () => {
    const f = useFactory();
    f.startLive();
    f.startLive();
    expect(sources).toHaveLength(1);
    f.stopLive();
    expect(sources[0].close).toHaveBeenCalledOnce();
  });
  it("refreshes data from named live events and ignores malformed frames", async () => {
    const f = useFactory();
    const refresh = vi.spyOn(f, "refreshOverview").mockResolvedValue();
    vi.spyOn(f, "refreshWorkOrders").mockResolvedValue();
    f.startLive();
    sources[0].listeners.get("state_changed")?.({ data: "broken" });
    expect(refresh).not.toHaveBeenCalled();
    sources[0].listeners.get("state_changed")?.({
      data: JSON.stringify({
        id: 1,
        actor: "builder",
        message: "Started",
        kind: "state_changed",
      }),
    });
    await Promise.resolve();
    expect(refresh).toHaveBeenCalledOnce();
    f.stopLive();
  });
  it("closes the stream on logout and clears session data", async () => {
    const f = useFactory();
    f.startLive();
    const session = useSession();
    session.user = { id: "u", name: "User", email: "u@x.test", role: "OWNER" };
    fetchMock.mockResolvedValue(response({}));
    await session.logout();
    expect(sources[0].close).toHaveBeenCalledOnce();
    expect(session.user).toBeNull();
    expect(f.workOrders).toEqual([]);
  });
});
